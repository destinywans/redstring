// Package v3mfa manages an account TOTP authenticator and one-use recovery
// codes. It does not issue login sessions or passkey approval assurance.
package v3mfa

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

var (
	ErrUnavailable        = errors.New("MFA encryption or storage unavailable")
	ErrForbidden          = errors.New("MFA account, session or environment unavailable")
	ErrFreshLoginRequired = errors.New("recent login required for authenticator changes")
	ErrConflict           = errors.New("MFA enrollment state conflict")
	ErrNoFactor           = errors.New("no active TOTP authenticator")
	ErrInvalidCode        = errors.New("invalid or already used authenticator code")
	ErrRateLimited        = errors.New("too many authenticator attempts")
	ErrInvalidInput       = errors.New("invalid authenticator input")
	instanceIDRE          = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
)

const (
	enrollmentLifetime = 10 * time.Minute
	freshLoginWindow   = 10 * time.Minute
	lockoutDuration    = 5 * time.Minute
	maxFailures        = 5
)

// Actor is supplied by authenticated V3 HTTP middleware. Every operation
// independently checks its user, raw session cookie and selected environment
// again inside the database transaction.
type Actor struct {
	UserID        string
	SessionToken  string
	Tier          string
	EnvironmentID string
}

type Status struct {
	State             string `json:"state"` // none, pending, expired, active
	RecoveryRemaining int    `json:"recovery_remaining"`
}

type Enrollment struct {
	Secret     string    `json:"secret"`
	OTPAuthURL string    `json:"otpauth_url"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Confirmation struct {
	Enabled       bool     `json:"enabled"`
	RecoveryCodes []string `json:"recovery_codes"`
}

type Verification struct {
	Verified bool   `json:"verified"`
	Method   string `json:"method"` // totp or recovery; never passkey assurance
}

type Service struct {
	db            *pgxpool.Pool
	key           [32]byte
	keyID         string
	instanceID    string
	now           func() time.Time
	allowedOrigin string
}

// New requires a server-managed 32-byte key and an instance identifier. It
// never generates or persists a fallback key, so missing configuration fails
// before an MFA route can claim a verified state.
func New(db *pgxpool.Pool, key []byte, instanceID string, clock func() time.Time) (*Service, error) {
	if db == nil || len(key) != 32 || !instanceIDRE.MatchString(instanceID) {
		return nil, ErrUnavailable
	}
	if clock == nil {
		clock = time.Now
	}
	s := &Service{db: db, instanceID: instanceID, now: clock}
	copy(s.key[:], key)
	digest := sha256.Sum256(key)
	s.keyID = hex.EncodeToString(digest[:16])
	return s, nil
}

type factor struct {
	generation, state, keyID                   string
	boundSession, boundTier, boundEnvironment  string
	nonce, ciphertext                          []byte
	pendingExpiresAt, lockedUntil, activatedAt time.Time
	lastStep                                   int64
	failed                                     int
}

func (s *Service) loadFactor(ctx context.Context, tx pgx.Tx, userID string) (factor, error) {
	var f factor
	err := tx.QueryRow(ctx, `SELECT generation,state,key_id,seed_nonce,seed_ciphertext,
	 COALESCE(bound_session_hash,''),COALESCE(bound_tier,''),COALESCE(bound_environment_id,''),
	 COALESCE(pending_expires_at,'epoch'::timestamptz),last_accepted_step,failed_attempts,
	 COALESCE(locked_until,'epoch'::timestamptz),COALESCE(activated_at,'epoch'::timestamptz)
	 FROM v3_mfa_totp WHERE instance_id=$1 AND user_id=$2 FOR UPDATE`,
		s.instanceID, userID).Scan(&f.generation, &f.state, &f.keyID, &f.nonce,
		&f.ciphertext, &f.boundSession, &f.boundTier, &f.boundEnvironment,
		&f.pendingExpiresAt, &f.lastStep, &f.failed, &f.lockedUntil, &f.activatedAt)
	return f, err
}

func (s *Service) lockedActor(ctx context.Context, tx pgx.Tx, a Actor, now time.Time, fresh bool) (string, string, error) {
	if a.UserID == "" || a.SessionToken == "" || a.EnvironmentID == "" || a.EnvironmentID == "all" ||
		(a.Tier != "nonprod" && a.Tier != "production") {
		return "", "", ErrForbidden
	}
	// Match the lock order used by account and V3 role writers. In particular,
	// a revoked account or environment grant cannot race a factor mutation.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return "", "", err
		}
	}
	var email string
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT email,NOT disabled AND deleted_at IS NULL
	 AND v3_identity_kind='human' AND source<>'settings_demo'
	 FROM users WHERE id=$1 FOR UPDATE`, a.UserID).Scan(&email, &eligible)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !eligible {
		return "", "", ErrForbidden
	}
	if err != nil {
		return "", "", err
	}
	sessionHash := store.Hash(a.SessionToken)
	var issuedAt sql.NullTime
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT issued_at,expires_at FROM sessions WHERE token_hash=$1
	 AND user_id=$2 FOR SHARE`, sessionHash, a.UserID).Scan(&issuedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrForbidden
	}
	if err != nil {
		return "", "", err
	}
	// A session or governance lock may have waited past the time sampled by
	// the caller. Validate only after the session lock has been acquired.
	now = s.clock()
	if !now.Before(expiresAt) {
		return "", "", ErrForbidden
	}
	if fresh && (!issuedAt.Valid || now.Sub(issuedAt.Time) > freshLoginWindow ||
		issuedAt.Time.After(now.Add(2*time.Minute))) {
		return "", "", ErrFreshLoginRequired
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
	 WHERE g.user_id=$1 AND g.module='environment' AND g.action='view'
	 AND ((g.scope_kind='global' AND g.scope_id='*') OR
	      (g.scope_kind='environment' AND g.scope_id=$2)))`, a.UserID, a.EnvironmentID).Scan(&allowed)
	if err != nil {
		return "", "", err
	}
	if !allowed {
		return "", "", ErrForbidden
	}
	return email, sessionHash, nil
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, a Actor, sessionHash, action, outcome, method string) error {
	fingerprint := sha256.Sum256([]byte("v3mfa-audit\x00" + sessionHash))
	changes, err := json.Marshal(map[string]string{
		"instance_id": s.instanceID, "tier": a.Tier,
		"environment_id":      a.EnvironmentID,
		"session_fingerprint": hex.EncodeToString(fingerprint[:8]), "method": method,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
	 VALUES($1,$2,$3,$4,$5)`, a.UserID, action, a.UserID, outcome, changes)
	return err
}

func (s *Service) beginTx(ctx context.Context) (pgx.Tx, error) {
	if s == nil || s.db == nil || s.now == nil {
		return nil, ErrUnavailable
	}
	return s.db.Begin(ctx)
}

func (s *Service) clock() time.Time { return s.now().UTC() }

// All authorization locks are already held. Re-read time at the successful
// commit boundary so audit/database waits cannot extend a credential's life.
func (s *Service) commitCurrent(ctx context.Context, tx pgx.Tx, a Actor, fresh bool, expires time.Time, step *int64) error {
	now := s.clock()
	if _, _, err := s.lockedActor(ctx, tx, a, now, fresh); err != nil {
		return err
	}
	now = s.clock()
	if !expires.IsZero() && !now.Before(expires) {
		return ErrConflict
	}
	if step != nil {
		current := now.Unix() / int64(totpPeriod/time.Second)
		if *step < current-1 || *step > current+1 {
			return ErrInvalidCode
		}
	}
	return tx.Commit(ctx)
}
