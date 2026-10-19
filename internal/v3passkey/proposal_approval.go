package v3passkey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
)

const proposalChallengeKind = "proposal_approve"

var proposalDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ProposalBinding is supplied by the proposal service after verifying the
// immutable action and review snapshots. It is never taken from browser JSON.
type ProposalBinding struct {
	ProposalID          string
	ActionKind          string
	ActionDigest        string
	ReviewContentDigest string
	EnvironmentID       string
}

// ProposalProof is returned only after this transaction has verified and
// consumed the exact proposal challenge. It must be used before that same
// transaction commits; its exported fields are audit data, not a bearer token.
type ProposalProof struct {
	ChallengeID           string
	CredentialID          []byte
	CredentialFingerprint string
	VerifiedAt            time.Time
	UserID                string
	InstanceID            string
	RPID                  string
}

func validProposalBinding(b ProposalBinding) bool {
	return b.ProposalID != "" && len(b.ProposalID) <= 128 &&
		b.ActionKind != "" && len(b.ActionKind) <= 128 &&
		proposalDigestRE.MatchString(b.ActionDigest) &&
		proposalDigestRE.MatchString(b.ReviewContentDigest) &&
		b.EnvironmentID != "" && b.EnvironmentID != "all" && len(b.EnvironmentID) <= 128
}

// CheckProposalWriteOrigin applies the same trusted transport and browser
// origin boundary as the self-account passkey write endpoints.
func (s *Service) CheckProposalWriteOrigin(r *http.Request, allowedOrigin string, secureCookies bool) error {
	if s == nil || r == nil || s.origin == "" {
		return ErrForbidden
	}
	proxyTLS := secureCookies && allowedOrigin == s.origin && strings.HasPrefix(s.origin, "https://")
	localFixture := s.tier == "nonprod" && (s.origin == "http://localhost" || strings.HasPrefix(s.origin, "http://localhost:"))
	if r.TLS == nil && !proxyTLS && !localFixture {
		return ErrForbidden
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin == s.origin {
			return nil
		}
	} else if r.Header.Get("Sec-Fetch-Site") == "same-origin" && allowedOrigin == s.origin {
		return nil
	}
	return ErrForbidden
}

// BeginBoundProposalAssertion issues a new five-minute challenge for one
// immutable proposal tuple. Lock the proposal before the actor: approval
// takes its proposal lock before its user lock, and challenge insertion also
// takes a proposal key-share lock through its foreign key.
func (s *Service) BeginBoundProposalAssertion(ctx context.Context, a Actor, b ProposalBinding) (Ceremony, error) {
	if !validProposalBinding(b) || a.EnvironmentID != b.EnvironmentID {
		return Ceremony{}, ErrChallengeInvalid
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return Ceremony{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockProposalTupleForBegin(ctx, tx, b); err != nil {
		return Ceremony{}, err
	}
	email, sessionHash, now, err := s.lockedActor(ctx, tx, a, false)
	if err != nil {
		return Ceremony{}, err
	}
	u, err := s.account(ctx, tx, a, email, false)
	if err != nil {
		return Ceremony{}, err
	}
	if len(u.credentials) == 0 {
		return Ceremony{}, ErrNoCredential
	}
	match, err := proposalTupleMatches(ctx, tx, b)
	if err != nil {
		return Ceremony{}, err
	}
	if !match {
		return Ceremony{}, ErrProposalDigestUnavailable
	}
	options, session, err := s.wa.BeginLogin(u,
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithLoginOrigin(s.origin))
	if err != nil {
		return Ceremony{}, err
	}
	ceremony, err := s.saveChallenge(ctx, tx, a, sessionHash, proposalChallengeKind, nil, &b, options, session, now)
	if err != nil {
		return Ceremony{}, err
	}
	if err = s.auditProposal(ctx, tx, a, sessionHash, b, ceremony.ID, "begin", "success", ""); err != nil {
		return Ceremony{}, err
	}
	return ceremony, tx.Commit(ctx)
}

func lockProposalTupleForBegin(ctx context.Context, tx pgx.Tx, b ProposalBinding) error {
	var found int
	err := tx.QueryRow(ctx, `SELECT 1
	 FROM v3_proposals p JOIN v3_proposal_actions a ON a.proposal_id=p.id
	 WHERE p.id=$1 AND p.status='pending' AND p.environment_id=$2
	 AND a.action_kind=$3 AND a.action_digest=$4 AND a.review_content_digest=$5
	 FOR KEY SHARE OF p,a`, b.ProposalID, b.EnvironmentID, b.ActionKind,
		b.ActionDigest, b.ReviewContentDigest).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProposalDigestUnavailable
	}
	return err
}

func proposalTupleMatches(ctx context.Context, tx pgx.Tx, b ProposalBinding) (bool, error) {
	var match bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
	 SELECT 1 FROM v3_proposals p JOIN v3_proposal_actions a ON a.proposal_id=p.id
	 WHERE p.id=$1 AND p.status='pending' AND p.environment_id=$2
	 AND a.action_kind=$3 AND a.action_digest=$4 AND a.review_content_digest=$5
	)`, b.ProposalID, b.EnvironmentID, b.ActionKind, b.ActionDigest, b.ReviewContentDigest).Scan(&match)
	return match, err
}

// proposalActorWithTx avoids advisory locks and user-row lock upgrades after
// the caller has locked the proposal. The caller remains responsible for the
// final scoped approval grant and the proposal's current status.
func (s *Service) proposalActorWithTx(ctx context.Context, tx pgx.Tx, a Actor, b ProposalBinding) (string, string, time.Time, error) {
	if s == nil || s.wa == nil || tx == nil || a.UserID == "" || a.SessionToken == "" ||
		a.Tier != s.tier || a.EnvironmentID != b.EnvironmentID {
		return "", "", time.Time{}, ErrForbidden
	}
	var email string
	err := tx.QueryRow(ctx, `SELECT email FROM users WHERE id=$1 AND NOT disabled
	 AND deleted_at IS NULL AND v3_identity_kind='human' AND source<>'settings_demo' FOR SHARE`, a.UserID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", time.Time{}, ErrForbidden
	}
	if err != nil {
		return "", "", time.Time{}, err
	}
	sessionHash := store.Hash(a.SessionToken)
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE token_hash=$1 AND user_id=$2 FOR SHARE`,
		sessionHash, a.UserID).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", time.Time{}, ErrForbidden
	}
	if err != nil {
		return "", "", time.Time{}, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return "", "", time.Time{}, err
	}
	if !now.Before(expiresAt) {
		return "", "", time.Time{}, ErrForbidden
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
	 WHERE g.user_id=$1 AND g.module='environment' AND g.action='view'
	 AND ((g.scope_kind='global' AND g.scope_id='*') OR
	      (g.scope_kind='environment' AND g.scope_id=$2)))`, a.UserID, b.EnvironmentID).Scan(&allowed)
	if err != nil {
		return "", "", time.Time{}, err
	}
	if !allowed {
		return "", "", time.Time{}, ErrForbidden
	}
	return email, sessionHash, now.UTC(), nil
}

type boundChallenge struct {
	binding    ProposalBinding
	session    webauthn.SessionData
	expiresAt  time.Time
	consumedAt *time.Time
}

func (s *Service) proposalChallenge(ctx context.Context, tx pgx.Tx, a Actor, sessionHash, id string) (boundChallenge, error) {
	var c boundChallenge
	if len(id) != 32 {
		return c, ErrChallengeInvalid
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT proposal_id,action_kind,action_digest,review_content_digest,
	 environment_id,session_data,expires_at,consumed_at
	 FROM v3_passkey_challenges WHERE id=$1 AND instance_id=$2 AND rp_id=$3
	 AND user_id=$4 AND session_hash=$5 AND tier=$6 AND environment_id=$7 AND kind=$8
	 FOR UPDATE`, id, s.instanceID, s.rpID, a.UserID, sessionHash, a.Tier, a.EnvironmentID, proposalChallengeKind).
		Scan(&c.binding.ProposalID, &c.binding.ActionKind, &c.binding.ActionDigest,
			&c.binding.ReviewContentDigest, &c.binding.EnvironmentID, &raw, &c.expiresAt, &c.consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrChallengeInvalid
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(raw, &c.session); err != nil {
		return c, err
	}
	if !validProposalBinding(c.binding) || c.session.RelyingPartyID != s.rpID || c.session.Origin != s.origin ||
		c.session.UserVerification != protocol.VerificationRequired || c.session.Challenge == "" {
		return c, ErrChallengeInvalid
	}
	return c, nil
}

func (s *Service) consumeProposal(ctx context.Context, tx pgx.Tx, a Actor, sessionHash string,
	b ProposalBinding, id, outcome, fingerprint string, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE v3_passkey_challenges SET consumed_at=$2
	 WHERE id=$1 AND consumed_at IS NULL`, id, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrChallengeInvalid
	}
	return s.auditProposal(ctx, tx, a, sessionHash, b, id, "finish", outcome, fingerprint)
}

func (s *Service) auditProposal(ctx context.Context, tx pgx.Tx, a Actor, sessionHash string,
	b ProposalBinding, id, stage, outcome, fingerprint string) error {
	sessionFingerprint := sha256.Sum256([]byte("v3passkey-audit\x00" + sessionHash))
	changes, err := json.Marshal(map[string]string{
		"challenge_id": id, "instance_id": s.instanceID, "rp_id": s.rpID,
		"tier": a.Tier, "environment_id": b.EnvironmentID, "action_kind": b.ActionKind,
		"action_digest": b.ActionDigest, "review_content_digest": b.ReviewContentDigest,
		"session_fingerprint":    hex.EncodeToString(sessionFingerprint[:8]),
		"credential_fingerprint": fingerprint,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
	 VALUES($1,'user',$2,$3,$4,$5)`, a.UserID, "passkey.proposal_approve."+stage,
		b.ProposalID, outcome, changes)
	return err
}

// VerifyProposalAssertionWithTx validates the signature and returns evidence
// only within the caller's transaction. On a semantic denial after locking the
// challenge, it consumes and audits the challenge; the caller must commit that
// transaction to preserve the denial. Database errors require rollback.
func (s *Service) VerifyProposalAssertionWithTx(ctx context.Context, tx pgx.Tx, a Actor,
	b ProposalBinding, challengeID string, response []byte) (ProposalProof, error) {
	var zero ProposalProof
	if !validProposalBinding(b) || a.EnvironmentID != b.EnvironmentID {
		return zero, ErrChallengeInvalid
	}
	email, sessionHash, now, err := s.proposalActorWithTx(ctx, tx, a, b)
	if err != nil {
		return zero, err
	}
	u, err := s.account(ctx, tx, a, email, false)
	if err != nil {
		return zero, err
	}
	c, err := s.proposalChallenge(ctx, tx, a, sessionHash, challengeID)
	if err != nil {
		return zero, err
	}
	if c.consumedAt != nil {
		return zero, ErrChallengeInvalid
	}
	deny := func(outcome string, cause error, fingerprint string) (ProposalProof, error) {
		if consumeErr := s.consumeProposal(ctx, tx, a, sessionHash, c.binding, challengeID, outcome, fingerprint, now); consumeErr != nil {
			return zero, consumeErr
		}
		return zero, cause
	}
	if c.binding != b {
		return deny("scope_mismatch", ErrChallengeInvalid, "")
	}
	if !now.Before(c.expiresAt) {
		return deny("expired", ErrChallengeInvalid, "")
	}
	match, err := proposalTupleMatches(ctx, tx, b)
	if err != nil {
		return zero, err
	}
	if !match {
		return deny("proposal_changed", ErrChallengeInvalid, "")
	}
	if len(u.credentials) == 0 {
		return deny("credential_unavailable", ErrNoCredential, "")
	}
	var credential *webauthn.Credential
	if len(response) > 0 && len(response) <= maxResponseBytes {
		parsed, parseErr := protocol.ParseCredentialRequestResponseBytes(response)
		if parseErr == nil {
			credential, err = s.wa.ValidateLogin(u, c.session, parsed)
		} else {
			err = parseErr
		}
	} else {
		err = ErrInvalidResponse
	}
	if err != nil || credential == nil || !credential.Flags.UserPresent || !credential.Flags.UserVerified ||
		credential.Flags.BackupState && !credential.Flags.BackupEligible {
		return deny("denied", ErrInvalidResponse, "")
	}
	fingerprint := credentialFingerprint(credential.ID)
	now, err = s.acceptanceTime(ctx, tx, a, sessionHash, c.expiresAt, false)
	if errors.Is(err, ErrChallengeInvalid) {
		return deny("expired", ErrChallengeInvalid, fingerprint)
	}
	if err != nil {
		return zero, err
	}
	var previousCount int64
	var previousBE, previousBS, previousClone bool
	err = tx.QueryRow(ctx, `SELECT sign_count,backup_eligible,backup_state,clone_warning
	 FROM v3_passkey_credentials WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3
	 AND credential_id=$4 AND revoked_at IS NULL FOR UPDATE`, s.instanceID, s.rpID,
		a.UserID, credential.ID).Scan(&previousCount, &previousBE, &previousBS, &previousClone)
	if errors.Is(err, pgx.ErrNoRows) {
		return deny("revoked", ErrNoCredential, fingerprint)
	}
	if err != nil {
		return zero, err
	}
	count := int64(credential.Authenticator.SignCount)
	if previousBE != credential.Flags.BackupEligible || previousClone || credential.Authenticator.CloneWarning ||
		(count != 0 || previousCount != 0) && count <= previousCount || !previousBE && previousBS {
		_, err = tx.Exec(ctx, `UPDATE v3_passkey_credentials SET clone_warning=true,revoked_at=$5
		 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND credential_id=$4 AND revoked_at IS NULL`,
			s.instanceID, s.rpID, a.UserID, credential.ID, now)
		if err != nil {
			return zero, err
		}
		return deny("clone_warning", ErrInvalidResponse, fingerprint)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_passkey_credentials SET credential_json=$5,sign_count=$6,
	 backup_state=$7,last_used_at=$8 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3
	 AND credential_id=$4 AND revoked_at IS NULL`, s.instanceID, s.rpID, a.UserID, credential.ID,
		raw, credential.Authenticator.SignCount, credential.Flags.BackupState, now)
	if err != nil {
		return zero, err
	}
	if err = s.consumeProposal(ctx, tx, a, sessionHash, b, challengeID, "success", fingerprint, now); err != nil {
		return zero, err
	}
	return ProposalProof{ChallengeID: challengeID, CredentialID: append([]byte(nil), credential.ID...), CredentialFingerprint: fingerprint, VerifiedAt: now,
		UserID: a.UserID, InstanceID: s.instanceID, RPID: s.rpID}, nil
}
