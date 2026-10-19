package v3passkey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
)

// BeginProposalAssertion intentionally cannot issue even a challenge. The V3
// proposal row currently has mutable content and no durable immutable digest.
// A future approval integration must bind a challenge to the locked proposal
// ID and digest, then consume its proof in the same approval transaction.
func (s *Service) BeginProposalAssertion(_ context.Context, _ Actor, _ string) (Ceremony, error) {
	return Ceremony{}, ErrProposalDigestUnavailable
}

// FinishProposalAssertion cannot exchange an account assertion for proposal
// assurance while the exact proposal digest cannot be bound and rechecked.
func (s *Service) FinishProposalAssertion(_ context.Context, _ Actor, _, _ string, _ []byte) error {
	return ErrProposalDigestUnavailable
}

func (s *Service) BeginRegistration(ctx context.Context, a Actor) (Ceremony, error) {
	var out Ceremony
	tx, err := s.beginTx(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	email, sessionHash, now, err := s.lockedActor(ctx, tx, a, true)
	if err != nil {
		return out, err
	}
	u, err := s.account(ctx, tx, a, email, true)
	if err != nil {
		return out, err
	}
	options, session, err := s.wa.BeginRegistration(u,
		webauthn.WithCredentialParameters(webauthn.CredentialParametersRecommendedL3()),
		webauthn.WithExclusions(webauthn.Credentials(u.credentials).CredentialDescriptors()),
		webauthn.WithRegistrationOrigin(s.origin))
	if err != nil {
		return out, err
	}
	out, err = s.saveChallenge(ctx, tx, a, sessionHash, "register", nil, nil, options, session, now)
	if err != nil {
		return Ceremony{}, err
	}
	if err = s.audit(ctx, tx, a, sessionHash, "passkey.registration.begin", "success", ""); err != nil {
		return Ceremony{}, err
	}
	return out, tx.Commit(ctx)
}

func (s *Service) BeginAssertion(ctx context.Context, a Actor) (Ceremony, error) {
	return s.beginAssertion(ctx, a, nil, "verify", false)
}

// BeginDeletion asks a currently active credential to authorize revocation of
// the selected credential. A fresh login is required at both ceremony ends.
func (s *Service) BeginDeletion(ctx context.Context, a Actor, targetID []byte) (Ceremony, error) {
	if len(targetID) == 0 || len(targetID) > 1023 {
		return Ceremony{}, ErrNoCredential
	}
	return s.beginAssertion(ctx, a, targetID, "delete", true)
}

func (s *Service) beginAssertion(ctx context.Context, a Actor, targetID []byte, kind string, fresh bool) (Ceremony, error) {
	var out Ceremony
	tx, err := s.beginTx(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	email, sessionHash, now, err := s.lockedActor(ctx, tx, a, fresh)
	if err != nil {
		return out, err
	}
	u, err := s.account(ctx, tx, a, email, false)
	if err != nil {
		return out, err
	}
	if len(u.credentials) == 0 {
		return out, ErrNoCredential
	}
	if kind == "delete" {
		found := false
		for _, c := range u.credentials {
			if bytes.Equal(c.ID, targetID) {
				found = true
				break
			}
		}
		if !found {
			return out, ErrNoCredential
		}
	}
	options, session, err := s.wa.BeginLogin(u,
		webauthn.WithUserVerification(protocol.VerificationRequired),
		webauthn.WithLoginOrigin(s.origin))
	if err != nil {
		return out, err
	}
	out, err = s.saveChallenge(ctx, tx, a, sessionHash, kind, targetID, nil, options, session, now)
	if err != nil {
		return Ceremony{}, err
	}
	if err = s.audit(ctx, tx, a, sessionHash, "passkey."+kind+".begin", "success", credentialFingerprint(targetID)); err != nil {
		return Ceremony{}, err
	}
	return out, tx.Commit(ctx)
}

func (s *Service) saveChallenge(ctx context.Context, tx pgx.Tx, a Actor, sessionHash, kind string, targetID []byte, binding *ProposalBinding, options any, session *webauthn.SessionData, now time.Time) (Ceremony, error) {
	var out Ceremony
	if (kind == "proposal_approve") != (binding != nil) {
		return out, ErrUnavailable
	}
	if session == nil || session.RelyingPartyID != s.rpID || session.Origin != s.origin ||
		session.UserVerification != protocol.VerificationRequired || session.Challenge == "" {
		return out, ErrUnavailable
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return out, err
	}
	// Bound challenge storage and per-account ceremony issuance. Existing
	// challenge rows are retained for at least 24 hours to support investigation.
	// Approval votes retain their referenced challenge rows; the hash-chain
	// audit remains the durable operation history.
	_, err = tx.Exec(ctx, `WITH stale AS (
	 SELECT c.id FROM v3_passkey_challenges c WHERE c.expires_at<$1
	 AND NOT EXISTS (SELECT 1 FROM v3_proposal_approval_votes v WHERE v.challenge_id=c.id)
	 ORDER BY c.expires_at LIMIT 100 FOR UPDATE OF c SKIP LOCKED
	) DELETE FROM v3_passkey_challenges c USING stale WHERE c.id=stale.id`, now.Add(-24*time.Hour))
	if err != nil {
		return out, err
	}
	var recent int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_passkey_challenges
	 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND created_at>$4`,
		s.instanceID, s.rpID, a.UserID, now.Add(-time.Minute)).Scan(&recent)
	if err != nil {
		return out, err
	}
	if recent >= 10 {
		return out, ErrRateLimited
	}
	// Keep at most one outstanding challenge of each kind per login session.
	_, err = tx.Exec(ctx, `UPDATE v3_passkey_challenges SET consumed_at=$6
	 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND session_hash=$4
	 AND kind=$5 AND consumed_at IS NULL`, s.instanceID, s.rpID, a.UserID, sessionHash, kind, now)
	if err != nil {
		return out, err
	}
	out = Ceremony{ID: store.ID(), Options: options, ExpiresAt: now.Add(challengeLifetime)}
	var proposalID, actionKind, actionDigest, reviewDigest any
	if binding != nil {
		proposalID, actionKind = binding.ProposalID, binding.ActionKind
		actionDigest, reviewDigest = binding.ActionDigest, binding.ReviewContentDigest
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_passkey_challenges
	 (id,instance_id,rp_id,user_id,session_hash,tier,environment_id,kind,target_credential_id,
	  proposal_id,action_kind,action_digest,review_content_digest,session_data,created_at,expires_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, out.ID, s.instanceID, s.rpID, a.UserID,
		sessionHash, a.Tier, a.EnvironmentID, kind, targetID, proposalID, actionKind, actionDigest, reviewDigest,
		raw, now, out.ExpiresAt)
	return out, err
}

type lockedChallenge struct {
	session    webauthn.SessionData
	targetID   []byte
	expiresAt  time.Time
	consumedAt *time.Time
}

func (s *Service) challenge(ctx context.Context, tx pgx.Tx, a Actor, sessionHash, id, kind string) (lockedChallenge, error) {
	var c lockedChallenge
	if len(id) != 32 {
		return c, ErrChallengeInvalid
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT session_data,target_credential_id,expires_at,consumed_at
	 FROM v3_passkey_challenges WHERE id=$1 AND instance_id=$2 AND rp_id=$3
	 AND user_id=$4 AND session_hash=$5 AND tier=$6 AND environment_id=$7 AND kind=$8
	 FOR UPDATE`, id, s.instanceID, s.rpID, a.UserID, sessionHash, a.Tier, a.EnvironmentID, kind).
		Scan(&raw, &c.targetID, &c.expiresAt, &c.consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrChallengeInvalid
	}
	if err != nil {
		return c, err
	}
	if err = json.Unmarshal(raw, &c.session); err != nil {
		return c, err
	}
	if c.session.RelyingPartyID != s.rpID || c.session.Origin != s.origin ||
		c.session.UserVerification != protocol.VerificationRequired || c.session.Challenge == "" {
		return c, ErrChallengeInvalid
	}
	return c, nil
}

// consume records a one-use attempt and audit in the same transaction. A bad
// response burns its challenge; callers commit that denial rather than retry.
func (s *Service) consume(ctx context.Context, tx pgx.Tx, a Actor, sessionHash, id, action, outcome string, credentialID []byte, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE v3_passkey_challenges SET consumed_at=$2 WHERE id=$1 AND consumed_at IS NULL`, id, now)
	if err != nil {
		return err
	}
	return s.audit(ctx, tx, a, sessionHash, action, outcome, credentialFingerprint(credentialID))
}

func (s *Service) FinishRegistration(ctx context.Context, a Actor, id string, response []byte, label string) (CredentialInfo, error) {
	var info CredentialInfo
	if strings.TrimSpace(label) != label || len(label) > 100 {
		return info, ErrInvalidResponse
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return info, err
	}
	defer tx.Rollback(ctx)
	email, sessionHash, now, err := s.lockedActor(ctx, tx, a, true)
	if err != nil {
		return info, err
	}
	u, err := s.account(ctx, tx, a, email, false)
	if err != nil {
		return info, err
	}
	c, err := s.challenge(ctx, tx, a, sessionHash, id, "register")
	if err != nil {
		return info, err
	}
	if c.consumedAt != nil {
		return info, ErrChallengeInvalid
	}
	if !now.Before(c.expiresAt) {
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey.registration.finish", "expired", nil, now); err != nil {
			return info, err
		}
		if err = tx.Commit(ctx); err != nil {
			return info, err
		}
		return info, ErrChallengeInvalid
	}
	var credential *webauthn.Credential
	if len(response) > 0 && len(response) <= maxResponseBytes {
		parsed, parseErr := protocol.ParseCredentialCreationResponseBytes(response)
		if parseErr == nil {
			credential, err = s.wa.CreateCredential(u, c.session, parsed)
		} else {
			err = parseErr
		}
	} else {
		err = ErrInvalidResponse
	}
	if err != nil || credential == nil || len(credential.ID) == 0 || len(credential.ID) > 1023 ||
		!credential.Flags.UserPresent || !credential.Flags.UserVerified ||
		credential.Flags.BackupState && !credential.Flags.BackupEligible {
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey.registration.finish", "denied", nil, now); err != nil {
			return info, err
		}
		if err = tx.Commit(ctx); err != nil {
			return info, err
		}
		return info, ErrInvalidResponse
	}
	now, err = s.acceptanceTime(ctx, tx, a, sessionHash, c.expiresAt, true)
	if errors.Is(err, ErrChallengeInvalid) {
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey.registration.finish", "expired", credential.ID, now); err != nil {
			return info, err
		}
		if err = tx.Commit(ctx); err != nil {
			return info, err
		}
		return info, ErrChallengeInvalid
	}
	if err != nil {
		return info, err
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return info, err
	}
	var inserted []byte
	err = tx.QueryRow(ctx, `INSERT INTO v3_passkey_credentials
	 (instance_id,rp_id,user_id,credential_id,credential_json,sign_count,backup_eligible,backup_state,label,created_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),$10)
	 ON CONFLICT DO NOTHING RETURNING credential_id`, s.instanceID, s.rpID, a.UserID,
		credential.ID, raw, credential.Authenticator.SignCount, credential.Flags.BackupEligible,
		credential.Flags.BackupState, label, now).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey.registration.finish", "conflict", credential.ID, now); err != nil {
			return info, err
		}
		if err = tx.Commit(ctx); err != nil {
			return info, err
		}
		return info, ErrConflict
	}
	if err != nil {
		return info, err
	}
	if err = s.consume(ctx, tx, a, sessionHash, id, "passkey.registration.finish", "success", credential.ID, now); err != nil {
		return info, err
	}
	if err = tx.Commit(ctx); err != nil {
		return info, err
	}
	return CredentialInfo{ID: credential.ID, Label: label, CreatedAt: now,
		BackupEligible: credential.Flags.BackupEligible, BackupState: credential.Flags.BackupState}, nil
}

func (s *Service) FinishAssertion(ctx context.Context, a Actor, id string, response []byte) (AssertionResult, error) {
	return s.finishAssertion(ctx, a, id, response, "verify")
}

func (s *Service) FinishDeletion(ctx context.Context, a Actor, id string, response []byte) error {
	_, err := s.finishAssertion(ctx, a, id, response, "delete")
	return err
}

func (s *Service) finishAssertion(ctx context.Context, a Actor, id string, response []byte, kind string) (AssertionResult, error) {
	var out AssertionResult
	tx, err := s.beginTx(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	email, sessionHash, now, err := s.lockedActor(ctx, tx, a, kind == "delete")
	if err != nil {
		return out, err
	}
	u, err := s.account(ctx, tx, a, email, false)
	if err != nil {
		return out, err
	}
	c, err := s.challenge(ctx, tx, a, sessionHash, id, kind)
	if err != nil {
		return out, err
	}
	if c.consumedAt != nil {
		return out, ErrChallengeInvalid
	}
	if !now.Before(c.expiresAt) {
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey."+kind+".finish", "expired", nil, now); err != nil {
			return out, err
		}
		if err = tx.Commit(ctx); err != nil {
			return out, err
		}
		return out, ErrChallengeInvalid
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
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey."+kind+".finish", "denied", nil, now); err != nil {
			return out, err
		}
		if err = tx.Commit(ctx); err != nil {
			return out, err
		}
		return out, ErrInvalidResponse
	}
	now, err = s.acceptanceTime(ctx, tx, a, sessionHash, c.expiresAt, kind == "delete")
	if errors.Is(err, ErrChallengeInvalid) {
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey."+kind+".finish", "expired", credential.ID, now); err != nil {
			return out, err
		}
		if err = tx.Commit(ctx); err != nil {
			return out, err
		}
		return out, ErrChallengeInvalid
	}
	if err != nil {
		return out, err
	}
	var previousCount int64
	var previousBE, previousBS, previousClone bool
	err = tx.QueryRow(ctx, `SELECT sign_count,backup_eligible,backup_state,clone_warning
	 FROM v3_passkey_credentials WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3
	 AND credential_id=$4 AND revoked_at IS NULL FOR UPDATE`, s.instanceID, s.rpID,
		a.UserID, credential.ID).Scan(&previousCount, &previousBE, &previousBS, &previousClone)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey."+kind+".finish", "revoked", credential.ID, now); err != nil {
			return out, err
		}
		if err = tx.Commit(ctx); err != nil {
			return out, err
		}
		return out, ErrNoCredential
	}
	if err != nil {
		return out, err
	}
	if previousBE != credential.Flags.BackupEligible || previousClone || credential.Authenticator.CloneWarning ||
		int64(credential.Authenticator.SignCount) < previousCount ||
		!previousBE && previousBS {
		_, err = tx.Exec(ctx, `UPDATE v3_passkey_credentials SET clone_warning=true,revoked_at=$5
		 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND credential_id=$4 AND revoked_at IS NULL`,
			s.instanceID, s.rpID, a.UserID, credential.ID, now)
		if err != nil {
			return out, err
		}
		if err = s.consume(ctx, tx, a, sessionHash, id, "passkey."+kind+".finish", "clone_warning", credential.ID, now); err != nil {
			return out, err
		}
		if err = tx.Commit(ctx); err != nil {
			return out, err
		}
		return out, ErrInvalidResponse
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_passkey_credentials SET credential_json=$5,sign_count=$6,
	 backup_state=$7,last_used_at=$8 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3
	 AND credential_id=$4 AND revoked_at IS NULL`, s.instanceID, s.rpID, a.UserID, credential.ID,
		raw, credential.Authenticator.SignCount, credential.Flags.BackupState, now)
	if err != nil {
		return out, err
	}
	if kind == "delete" {
		command, updateErr := tx.Exec(ctx, `UPDATE v3_passkey_credentials SET revoked_at=$5
		 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND credential_id=$4 AND revoked_at IS NULL`,
			s.instanceID, s.rpID, a.UserID, c.targetID, now)
		if updateErr != nil {
			return out, updateErr
		}
		if command.RowsAffected() != 1 {
			if err = s.consume(ctx, tx, a, sessionHash, id, "passkey.delete.finish", "target_revoked", c.targetID, now); err != nil {
				return out, err
			}
			if err = tx.Commit(ctx); err != nil {
				return out, err
			}
			return out, ErrNoCredential
		}
	}
	if err = s.consume(ctx, tx, a, sessionHash, id, "passkey."+kind+".finish", "success", credential.ID, now); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return AssertionResult{CredentialID: credential.ID, Verified: true}, nil
}

// List returns only public metadata for the authenticated account and RP.
func (s *Service) List(ctx context.Context, a Actor) ([]CredentialInfo, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, _, _, err = s.lockedActor(ctx, tx, a, false)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT credential_id,COALESCE(label,''),created_at,last_used_at,
	 backup_eligible,backup_state FROM v3_passkey_credentials
	 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND revoked_at IS NULL
	 ORDER BY created_at,credential_id`, s.instanceID, s.rpID, a.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	infos := make([]CredentialInfo, 0)
	for rows.Next() {
		var c CredentialInfo
		if err = rows.Scan(&c.ID, &c.Label, &c.CreatedAt, &c.LastUsedAt, &c.BackupEligible, &c.BackupState); err != nil {
			return nil, err
		}
		infos = append(infos, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return infos, nil
}
