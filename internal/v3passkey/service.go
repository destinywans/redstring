// Package v3passkey provides account-owned WebAuthn ceremonies and a separate
// proposal-bound assertion that a caller can verify inside an approval
// transaction. An account assertion does not grant proposal approval.
package v3passkey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUnavailable               = errors.New("passkey RP, origin or storage unavailable")
	ErrProposalDigestUnavailable = errors.New("immutable V3 proposal digest unavailable")
	ErrForbidden                 = errors.New("passkey account, session or environment unavailable")
	ErrFreshLoginRequired        = errors.New("recent login required for passkey changes")
	ErrInvalidResponse           = errors.New("invalid WebAuthn response")
	ErrChallengeInvalid          = errors.New("WebAuthn challenge expired, consumed or out of scope")
	ErrNoCredential              = errors.New("no active passkey credential")
	ErrConflict                  = errors.New("passkey credential conflict")
	ErrRateLimited               = errors.New("too many passkey ceremonies")
	instanceIDRE                 = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
)

const (
	challengeLifetime = 5 * time.Minute
	freshLoginWindow  = 10 * time.Minute
	maxResponseBytes  = 128 << 10
)

// Config must be built from trusted deployment configuration, never a request
// Host or Origin header. HTTP is only permitted on explicit nonproduction
// localhost fixtures; the default is a stable HTTPS RP origin.
type Config struct {
	DB                 *pgxpool.Pool
	InstanceID         string
	RPID               string
	Origin             string
	Tier               string
	DisplayName        string
	AllowLocalhostHTTP bool
}

type Actor struct {
	UserID        string
	SessionToken  string
	Tier          string
	EnvironmentID string
}

type Ceremony struct {
	ID        string    `json:"id"`
	Options   any       `json:"options"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CredentialInfo struct {
	ID             []byte     `json:"id"`
	Label          string     `json:"label,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	BackupEligible bool       `json:"backup_eligible"`
	BackupState    bool       `json:"backup_state"`
}

// AssertionResult is deliberately account-scoped. It is never an approval
// proof and cannot satisfy assurance=passkey at the V3 proposal deny gate.
type AssertionResult struct {
	CredentialID []byte `json:"credential_id"`
	Verified     bool   `json:"verified"`
}

type Service struct {
	db                             *pgxpool.Pool
	wa                             *webauthn.WebAuthn
	instanceID, rpID, origin, tier string
}

func New(c Config) (*Service, error) {
	if c.DB == nil || !instanceIDRE.MatchString(c.InstanceID) || c.Tier != "nonprod" && c.Tier != "production" ||
		c.DisplayName == "" || len(c.DisplayName) > 100 || c.RPID == "" || c.Origin == "" {
		return nil, ErrUnavailable
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Path != "" || u.Hostname() != c.RPID || u.Host == "" ||
		strings.TrimSuffix(u.String(), "/") != c.Origin || strings.ToLower(c.RPID) != c.RPID {
		return nil, ErrUnavailable
	}
	if u.Scheme != "https" {
		if !(c.AllowLocalhostHTTP && c.Tier == "nonprod" && u.Scheme == "http" && c.RPID == "localhost") {
			return nil, ErrUnavailable
		}
	}
	if ip := net.ParseIP(c.RPID); ip != nil || strings.Contains(c.RPID, ":") {
		return nil, ErrUnavailable
	}
	if c.RPID == "localhost" && !(c.AllowLocalhostHTTP && c.Tier == "nonprod" && u.Scheme == "http") {
		return nil, ErrUnavailable
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID: c.RPID, RPDisplayName: c.DisplayName, RPOrigins: []string{c.Origin},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, ErrUnavailable
	}
	return &Service{db: c.DB, wa: wa, instanceID: c.InstanceID, rpID: c.RPID,
		origin: c.Origin, tier: c.Tier}, nil
}

type accountUser struct {
	handle      []byte
	email       string
	credentials []webauthn.Credential
}

func (u accountUser) WebAuthnID() []byte                         { return u.handle }
func (u accountUser) WebAuthnName() string                       { return u.email }
func (u accountUser) WebAuthnDisplayName() string                { return u.email }
func (u accountUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Service) beginTx(ctx context.Context) (pgx.Tx, error) {
	if s == nil || s.db == nil || s.wa == nil {
		return nil, ErrUnavailable
	}
	return s.db.Begin(ctx)
}

// lockedActor rechecks every identity boundary under the same lock ordering
// as V3 account and role writers. A session from before issued_at migration
// cannot mutate authenticators until the user signs in again.
func (s *Service) lockedActor(ctx context.Context, tx pgx.Tx, a Actor, fresh bool) (string, string, time.Time, error) {
	if a.UserID == "" || a.SessionToken == "" || a.Tier != s.tier || a.EnvironmentID == "" || a.EnvironmentID == "all" {
		return "", "", time.Time{}, ErrForbidden
	}
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return "", "", time.Time{}, err
		}
	}
	var email string
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT email,NOT disabled AND deleted_at IS NULL
	 AND v3_identity_kind='human' AND source<>'settings_demo'
	 FROM users WHERE id=$1 FOR UPDATE`, a.UserID).Scan(&email, &eligible)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !eligible {
		return "", "", time.Time{}, ErrForbidden
	}
	if err != nil {
		return "", "", time.Time{}, err
	}
	sessionHash := store.Hash(a.SessionToken)
	var issuedAt sql.NullTime
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT issued_at,expires_at FROM sessions WHERE token_hash=$1
	 AND user_id=$2 FOR SHARE`, sessionHash, a.UserID).Scan(&issuedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", time.Time{}, ErrForbidden
	}
	if err != nil {
		return "", "", time.Time{}, err
	}
	// now() is fixed at transaction start. Read actual wall time after the
	// account, role and session locks so queued requests cannot extend validity.
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return "", "", time.Time{}, err
	}
	if !now.Before(expiresAt) {
		return "", "", time.Time{}, ErrForbidden
	}
	if fresh && (!issuedAt.Valid || now.Sub(issuedAt.Time) > freshLoginWindow || issuedAt.Time.After(now.Add(2*time.Minute))) {
		return "", "", time.Time{}, ErrFreshLoginRequired
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
	 WHERE g.user_id=$1 AND g.module='environment' AND g.action='view'
	 AND ((g.scope_kind='global' AND g.scope_id='*') OR
	      (g.scope_kind='environment' AND g.scope_id=$2)))`, a.UserID, a.EnvironmentID).Scan(&allowed)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if !allowed {
		return "", "", time.Time{}, ErrForbidden
	}
	return email, sessionHash, now.UTC(), nil
}

// acceptanceTime checks time-sensitive boundaries again after protocol
// verification, immediately before a successful credential mutation. The
// session row is already held FOR SHARE and the actor row FOR UPDATE.
func (s *Service) acceptanceTime(ctx context.Context, tx pgx.Tx, a Actor, sessionHash string, expiresAt time.Time, fresh bool) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, err
	}
	if !now.Before(expiresAt) {
		return now, ErrChallengeInvalid
	}
	var issuedAt sql.NullTime
	err := tx.QueryRow(ctx, `SELECT issued_at FROM sessions WHERE token_hash=$1
	 AND user_id=$2 AND expires_at>$3 FOR SHARE`, sessionHash, a.UserID, now).Scan(&issuedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return now, ErrForbidden
	}
	if err != nil {
		return now, err
	}
	if fresh && (!issuedAt.Valid || now.Sub(issuedAt.Time) > freshLoginWindow || issuedAt.Time.After(now.Add(2*time.Minute))) {
		return now, ErrFreshLoginRequired
	}
	return now.UTC(), nil
}

func (s *Service) account(ctx context.Context, tx pgx.Tx, a Actor, email string, create bool) (accountUser, error) {
	var u accountUser
	if create {
		handle := make([]byte, 32)
		if _, err := rand.Read(handle); err != nil {
			return u, err
		}
		_, err := tx.Exec(ctx, `INSERT INTO v3_passkey_accounts(instance_id,rp_id,user_id,user_handle)
		 VALUES($1,$2,$3,$4) ON CONFLICT(instance_id,rp_id,user_id) DO NOTHING`, s.instanceID, s.rpID, a.UserID, handle)
		if err != nil {
			return u, err
		}
	}
	err := tx.QueryRow(ctx, `SELECT user_handle FROM v3_passkey_accounts
	 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 FOR UPDATE`, s.instanceID, s.rpID, a.UserID).Scan(&u.handle)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNoCredential
	}
	if err != nil {
		return u, err
	}
	u.email = email
	rows, err := tx.Query(ctx, `SELECT credential_json FROM v3_passkey_credentials
	 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND revoked_at IS NULL ORDER BY created_at,credential_id`, s.instanceID, s.rpID, a.UserID)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return u, err
		}
		var c webauthn.Credential
		if err = json.Unmarshal(raw, &c); err != nil {
			return u, err
		}
		u.credentials = append(u.credentials, c)
	}
	return u, rows.Err()
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, a Actor, sessionHash, action, outcome, credentialID string) error {
	fingerprint := sha256.Sum256([]byte("v3passkey-audit\x00" + sessionHash))
	changes, err := json.Marshal(map[string]string{
		"instance_id": s.instanceID, "rp_id": s.rpID, "tier": a.Tier,
		"environment_id": a.EnvironmentID, "session_fingerprint": hex.EncodeToString(fingerprint[:8]),
		"credential_fingerprint": credentialID,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
	 VALUES($1,$2,$3,$4,$5)`, a.UserID, action, a.UserID, outcome, changes)
	return err
}

func credentialFingerprint(id []byte) string {
	if len(id) == 0 {
		return ""
	}
	h := sha256.Sum256(id)
	return hex.EncodeToString(h[:8])
}
