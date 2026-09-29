package v3proposals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3passkey"
)

const approvalVoteLifetime = 15 * time.Minute

// ApprovalResult is a durable decision snapshot. A pending result never has
// execution authority; execution_id is allocated only with the second vote.
type ApprovalResult struct {
	ProposalID        string `json:"proposal_id"`
	Status            string `json:"status"`
	RequiredApprovals int    `json:"required_approvals"`
	ReceivedApprovals int    `json:"received_approvals"`
	ExecutionID       string `json:"execution_id,omitempty"`
	ApprovalDigest    string `json:"approval_digest,omitempty"`
}

type approvalVote struct {
	ID, ProposalID, ApproverID, ChallengeID, ActionKind, ActionDigest, ReviewContentDigest string
	InstanceID, RPID, CredentialFingerprint, RequestHash, Comment                          string
	CredentialID                                                                           []byte
	VerifiedAt, ExpiresAt                                                                  time.Time
}

type approvalDecision struct {
	ProposalID, ExecutionID, ActionKind, ActionDigest, ReviewContentDigest, ApprovalDigest string
	CreatorID, EnvironmentID, ApplicationID, Risk, FirstVoteID, SecondVoteID               string
	RequiredCount                                                                          int
	ApprovedAt, ExpiresAt                                                                  time.Time
}

func approvalDecisionDigest(d approvalDecision, first, second approvalVote) string {
	type voteDigest struct {
		ID, UserID, ChallengeID, InstanceID, RPID, CredentialFingerprint, RequestHash string
		CredentialID                                                                  string
		VerifiedAt, ExpiresAt                                                         string
	}
	encodeVote := func(v approvalVote) voteDigest {
		return voteDigest{v.ID, v.ApproverID, v.ChallengeID, v.InstanceID, v.RPID,
			v.CredentialFingerprint, v.RequestHash, hex.EncodeToString(v.CredentialID),
			v.VerifiedAt.UTC().Format(time.RFC3339Nano), v.ExpiresAt.UTC().Format(time.RFC3339Nano)}
	}
	raw, err := json.Marshal(struct {
		ProposalID, ExecutionID, ActionKind, ActionDigest, ReviewContentDigest string
		CreatorID, EnvironmentID, ApplicationID, Risk                          string
		RequiredCount                                                          int
		ApprovedAt, ExpiresAt                                                  string
		First, Second                                                          voteDigest
	}{d.ProposalID, d.ExecutionID, d.ActionKind, d.ActionDigest, d.ReviewContentDigest,
		d.CreatorID, d.EnvironmentID, d.ApplicationID, d.Risk, d.RequiredCount,
		d.ApprovedAt.UTC().Format(time.RFC3339Nano), d.ExpiresAt.UTC().Format(time.RFC3339Nano),
		encodeVote(first), encodeVote(second)})
	if err != nil {
		return ""
	}
	return proposalDigest("aiops.v3.proposal.approval.v1", raw)
}

func approvalRequestHash(proposalID, challengeID string, response []byte, comment string) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(proposalID), []byte(challengeID), response, []byte(comment)} {
		var length [8]byte
		n := uint64(len(part))
		for i := 7; i >= 0; i-- {
			length[i] = byte(n)
			n >>= 8
		}
		_, _ = h.Write(length[:])
		_, _ = h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func approvalBinding(a v3alertmanager.SilenceReviewedAction) v3passkey.ProposalBinding {
	return v3passkey.ProposalBinding{ProposalID: a.ProposalID, ActionKind: a.Kind,
		ActionDigest: a.ActionDigest, ReviewContentDigest: a.ReviewContentDigest,
		EnvironmentID: a.EnvironmentID}
}

// loadApprovalActionTx takes the proposal write lock before the immutable
// action reader's SHARE locks, so two approvals cannot deadlock on promotion.
func (s *Service) loadApprovalActionTx(ctx context.Context, tx pgx.Tx, id string) (record,
	v3alertmanager.SilenceReviewedAction, error) {
	var zero v3alertmanager.SilenceReviewedAction
	if s == nil || s.DB == nil || tx == nil || !bounded(id, 128) {
		return record{}, zero, ErrInvalidProposal
	}
	v, err := loadReviewTx(ctx, tx, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return record{}, zero, store.ErrNotFound
	}
	if err != nil {
		return record{}, zero, err
	}
	a, err := (&SilenceExecutionActionSource{Proposals: s}).LoadReviewedActionWithTx(ctx, tx, id)
	if err != nil {
		return record{}, zero, err
	}
	if a.ProposalID != v.ID || a.Status != v.Status || a.EnvironmentID != v.EnvironmentID ||
		a.ApplicationID != v.ApplicationID || a.InvestigationID != v.InvestigationID ||
		a.CreatorID == "" || a.ActionDigest == "" || a.ReviewContentDigest == "" ||
		(a.Kind != v3alertmanager.ProposedSilenceCreateKind && a.Kind != v3alertmanager.ProposedSilenceCancelKind) {
		return record{}, zero, ErrInvalidProposal
	}
	return v, a, nil
}

func lockApprovalActorTx(ctx context.Context, tx pgx.Tx, actor v3passkey.Actor) error {
	if actor.UserID == "" || actor.SessionToken == "" || actor.EnvironmentID == "" ||
		actor.EnvironmentID == "all" {
		return store.ErrForbidden
	}
	var userID string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled
 AND deleted_at IS NULL AND source<>'settings_demo' AND v3_identity_kind='human' FOR SHARE`,
		actor.UserID).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrForbidden
		}
		return err
	}
	var expiry time.Time
	if err := tx.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE token_hash=$1
 AND user_id=$2 FOR SHARE`, store.Hash(actor.SessionToken), actor.UserID).Scan(&expiry); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrForbidden
		}
		return err
	}
	return approvalSessionFreshTx(ctx, tx, actor)
}

func approvalSessionFreshTx(ctx context.Context, tx pgx.Tx, actor v3passkey.Actor) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(actor.SessionToken), actor.UserID).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrForbidden
		}
		return err
	}
	if !active {
		return store.ErrForbidden
	}
	return nil
}

// approvalGrantTx is the conditional approval cell. Caller holds current
// target/user rows and the grant-source SHARE locks; the generic policy
// intentionally denies action approve even when this cell exists.
func approvalGrantTx(ctx context.Context, tx pgx.Tx, userID, envID, appID, teamID string) error {
	if userID == "" || envID == "" || envID == "all" || appID == "" {
		return store.ErrForbidden
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='environment' AND g.action='view'
  AND ((g.scope_kind='global' AND g.scope_id='*') OR
       (g.scope_kind='environment' AND g.scope_id=$2)))
 AND EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
  AND g.module='proposals' AND g.action='approve' AND g.assurance='passkey'
  AND g.v3_identity_kind='human' AND g.role_kind<>'machine'
  AND ((g.scope_kind='global' AND g.scope_id='*') OR
       (g.scope_kind='environment' AND g.scope_id=$2) OR
       (g.scope_kind='application' AND g.scope_id=$3) OR
       ($4<>'' AND g.scope_kind='team' AND g.scope_id=$4)))
 AND EXISTS(SELECT 1 FROM v3_application_environments ae
  WHERE ae.application_id=$3 AND ae.environment_id=$2)`,
		userID, envID, appID, teamID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return store.ErrForbidden
	}
	return nil
}

func (s *Service) ApprovalOptions(ctx context.Context, actor v3passkey.Actor, proposalID string) (
	v3passkey.ProposalBinding, error) {
	var empty v3passkey.ProposalBinding
	if s == nil || s.DB == nil || s.ApprovalPasskeys == nil {
		return empty, v3alertmanager.ErrNotConfigured
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	v, action, err := s.loadApprovalActionTx(ctx, tx, proposalID)
	if err != nil {
		return empty, err
	}
	if v.EnvironmentID != actor.EnvironmentID {
		return empty, store.ErrNotFound
	}
	if v.Status != "pending" {
		return empty, store.ErrConflict
	}
	if action.CreatorID == actor.UserID {
		return empty, store.ErrForbidden
	}
	if err = lockApprovalActorTx(ctx, tx, actor); err != nil {
		return empty, err
	}
	if err = lockReviewAuthorityTables(ctx, tx); err != nil {
		return empty, err
	}
	if err = approvalSessionFreshTx(ctx, tx, actor); err != nil {
		return empty, err
	}
	if err = approvalGrantTx(ctx, tx, actor.UserID, v.EnvironmentID, v.ApplicationID, v.TeamID); err != nil {
		return empty, err
	}
	return approvalBinding(action), tx.Commit(ctx)
}

func approvalVoteByChallengeTx(ctx context.Context, tx pgx.Tx, proposalID, challengeID string) (approvalVote, bool, error) {
	var v approvalVote
	err := tx.QueryRow(ctx, `SELECT id,proposal_id,approver_id,challenge_id,action_kind,
 action_digest,review_content_digest,instance_id,rp_id,credential_id,
 credential_fingerprint,request_hash,comment,verified_at,expires_at
 FROM v3_proposal_approval_votes WHERE challenge_id=$1`, challengeID).Scan(
		&v.ID, &v.ProposalID, &v.ApproverID, &v.ChallengeID, &v.ActionKind,
		&v.ActionDigest, &v.ReviewContentDigest, &v.InstanceID, &v.RPID, &v.CredentialID,
		&v.CredentialFingerprint, &v.RequestHash, &v.Comment, &v.VerifiedAt, &v.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return approvalVote{}, false, nil
	}
	if err != nil {
		return approvalVote{}, false, err
	}
	if v.ProposalID != proposalID {
		return approvalVote{}, false, store.ErrConflict
	}
	return v, true, nil
}

// approvalVoteEvidenceTx validates a durable vote, after the caller has
// acquired any desired challenge and credential row locks. It never upgrades
// a row lock and does not treat a prior account assertion as proposal proof.
func approvalVoteEvidenceTx(ctx context.Context, tx pgx.Tx, v approvalVote,
	b v3passkey.ProposalBinding, at time.Time) error {
	fingerprint := sha256.Sum256(v.CredentialID)
	if v.ProposalID != b.ProposalID || v.ActionKind != b.ActionKind ||
		v.ActionDigest != b.ActionDigest || v.ReviewContentDigest != b.ReviewContentDigest ||
		v.ApproverID == "" || v.ChallengeID == "" || len(v.CredentialID) == 0 ||
		v.CredentialFingerprint != hex.EncodeToString(fingerprint[:8]) || v.RequestHash == "" ||
		!v.ExpiresAt.After(at) || !v.ExpiresAt.Equal(v.VerifiedAt.Add(approvalVoteLifetime)) {
		return v3alertmanager.ErrSilenceApprovalUnavailable
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_passkey_challenges c
 JOIN v3_passkey_credentials k ON k.instance_id=c.instance_id AND k.rp_id=c.rp_id
  AND k.user_id=c.user_id AND k.credential_id=$6
 WHERE c.id=$1 AND c.user_id=$2 AND c.proposal_id=$3 AND c.action_kind=$4
  AND c.action_digest=$5 AND c.review_content_digest=$7 AND c.environment_id=$8
  AND c.instance_id=$9 AND c.rp_id=$10 AND c.kind='proposal_approve'
  AND c.consumed_at=$11 AND c.expires_at>=$11 AND c.created_at<=$11
  AND k.revoked_at IS NULL AND NOT k.clone_warning
  AND k.last_used_at>=c.consumed_at)`,
		v.ChallengeID, v.ApproverID, b.ProposalID, b.ActionKind, b.ActionDigest,
		v.CredentialID, b.ReviewContentDigest, b.EnvironmentID, v.InstanceID,
		v.RPID, v.VerifiedAt).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return v3alertmanager.ErrSilenceApprovalUnavailable
	}
	return nil
}

func lockApprovalVoteProofTx(ctx context.Context, tx pgx.Tx, v approvalVote) error {
	var challengeID string
	if err := tx.QueryRow(ctx, `SELECT id FROM v3_passkey_challenges WHERE id=$1 FOR SHARE`,
		v.ChallengeID).Scan(&challengeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return v3alertmanager.ErrSilenceApprovalUnavailable
		}
		return err
	}
	var credentialID []byte
	if err := tx.QueryRow(ctx, `SELECT credential_id FROM v3_passkey_credentials
 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND credential_id=$4 FOR SHARE`,
		v.InstanceID, v.RPID, v.ApproverID, v.CredentialID).Scan(&credentialID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return v3alertmanager.ErrSilenceApprovalUnavailable
		}
		return err
	}
	return nil
}

func approvalCandidateVotesTx(ctx context.Context, tx pgx.Tx, proposalID, excludeUser string,
	at time.Time) ([]approvalVote, error) {
	rows, err := tx.Query(ctx, `SELECT id,proposal_id,approver_id,challenge_id,action_kind,
 action_digest,review_content_digest,instance_id,rp_id,credential_id,
 credential_fingerprint,request_hash,comment,verified_at,expires_at
 FROM v3_proposal_approval_votes
 WHERE proposal_id=$1 AND approver_id<>$2 AND expires_at>$3
 ORDER BY verified_at DESC,id`, proposalID, excludeUser, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []approvalVote
	for rows.Next() {
		var v approvalVote
		if err = rows.Scan(&v.ID, &v.ProposalID, &v.ApproverID, &v.ChallengeID,
			&v.ActionKind, &v.ActionDigest, &v.ReviewContentDigest, &v.InstanceID,
			&v.RPID, &v.CredentialID, &v.CredentialFingerprint, &v.RequestHash,
			&v.Comment, &v.VerifiedAt, &v.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func lockCandidateUsersTx(ctx context.Context, tx pgx.Tx, candidates []approvalVote, current string) error {
	ids := make([]string, 0, len(candidates))
	seen := map[string]bool{current: true}
	for _, v := range candidates {
		if !seen[v.ApproverID] {
			seen[v.ApproverID] = true
			ids = append(ids, v.ApproverID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		var ignored string
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR SHARE`, id).Scan(&ignored)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return nil
}

func semanticPasskeyError(err error) bool {
	return errors.Is(err, v3passkey.ErrForbidden) || errors.Is(err, v3passkey.ErrFreshLoginRequired) ||
		errors.Is(err, v3passkey.ErrChallengeInvalid) || errors.Is(err, v3passkey.ErrInvalidResponse) ||
		errors.Is(err, v3passkey.ErrNoCredential) || errors.Is(err, v3passkey.ErrRateLimited)
}

func (s *Service) ApproveProposal(ctx context.Context, actor v3passkey.Actor, proposalID,
	challengeID string, response []byte, comment string) (ApprovalResult, error) {
	var empty ApprovalResult
	if s == nil || s.DB == nil || s.ApprovalPasskeys == nil {
		return empty, v3alertmanager.ErrNotConfigured
	}
	if !bounded(proposalID, 128) || len(challengeID) != 32 || len(response) == 0 ||
		len(response) > 128<<10 || len(comment) > 1000 {
		return empty, ErrInvalidProposal
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	v, action, err := s.loadApprovalActionTx(ctx, tx, proposalID)
	if err != nil {
		return empty, err
	}
	if v.EnvironmentID != actor.EnvironmentID {
		return empty, store.ErrNotFound
	}
	if action.CreatorID == actor.UserID {
		return empty, store.ErrForbidden
	}
	if v.Status != "pending" && v.Status != "approved" {
		return empty, store.ErrConflict
	}
	if err = lockApprovalActorTx(ctx, tx, actor); err != nil {
		return empty, err
	}
	requestHash := approvalRequestHash(proposalID, challengeID, response, comment)
	replayed, exists, err := approvalVoteByChallengeTx(ctx, tx, proposalID, challengeID)
	if err != nil {
		return empty, err
	}
	if exists && (replayed.ApproverID != actor.UserID || replayed.RequestHash != requestHash ||
		replayed.ActionKind != action.Kind || replayed.ActionDigest != action.ActionDigest ||
		replayed.ReviewContentDigest != action.ReviewContentDigest) {
		return empty, store.ErrConflict
	}
	if !exists && v.Status != "pending" {
		return empty, store.ErrConflict
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return empty, err
	}
	excludeUser := actor.UserID
	if exists {
		excludeUser = ""
	}
	candidates, err := approvalCandidateVotesTx(ctx, tx, proposalID, excludeUser, now)
	if err != nil {
		return empty, err
	}
	if err = lockCandidateUsersTx(ctx, tx, candidates, actor.UserID); err != nil {
		return empty, err
	}
	if err = lockReviewAuthorityTables(ctx, tx); err != nil {
		return empty, err
	}
	if err = approvalSessionFreshTx(ctx, tx, actor); err != nil {
		return empty, err
	}
	if err = approvalGrantTx(ctx, tx, actor.UserID, v.EnvironmentID, v.ApplicationID, v.TeamID); err != nil {
		return empty, err
	}
	if exists {
		result, err := approvalResultTx(ctx, tx, v, action, candidates)
		if err != nil {
			return empty, err
		}
		return result, tx.Commit(ctx)
	}
	binding := approvalBinding(action)
	proof, err := s.ApprovalPasskeys.VerifyProposalAssertionWithTx(ctx, tx, actor, binding,
		challengeID, response)
	if err != nil {
		if semanticPasskeyError(err) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return empty, commitErr
			}
		}
		return empty, err
	}
	if proof.ChallengeID != challengeID || proof.UserID != actor.UserID ||
		proof.InstanceID == "" || proof.RPID == "" || len(proof.CredentialID) == 0 ||
		proof.CredentialFingerprint == "" || proof.VerifiedAt.IsZero() {
		return empty, v3alertmanager.ErrSilenceApprovalUnavailable
	}
	if err = approvalSessionFreshTx(ctx, tx, actor); err != nil {
		// Keep the consumed challenge, but never commit a vote for an expired session.
		if errors.Is(err, store.ErrForbidden) {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return empty, commitErr
			}
		}
		return empty, err
	}
	current := approvalVote{ID: store.ID(), ProposalID: proposalID, ApproverID: actor.UserID,
		ChallengeID: challengeID, ActionKind: action.Kind, ActionDigest: action.ActionDigest,
		ReviewContentDigest: action.ReviewContentDigest, InstanceID: proof.InstanceID,
		RPID: proof.RPID, CredentialID: proof.CredentialID,
		CredentialFingerprint: proof.CredentialFingerprint, RequestHash: requestHash,
		Comment: comment, VerifiedAt: proof.VerifiedAt,
		ExpiresAt: proof.VerifiedAt.Add(approvalVoteLifetime)}
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposal_approval_votes
 (id,proposal_id,approver_id,challenge_id,action_kind,action_digest,review_content_digest,
  instance_id,rp_id,credential_id,credential_fingerprint,request_hash,comment,verified_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		current.ID, current.ProposalID, current.ApproverID, current.ChallengeID,
		current.ActionKind, current.ActionDigest, current.ReviewContentDigest,
		current.InstanceID, current.RPID, current.CredentialID,
		current.CredentialFingerprint, current.RequestHash, current.Comment,
		current.VerifiedAt, current.ExpiresAt)
	if err != nil {
		return empty, err
	}
	if err = approvalAuditTx(ctx, tx, actor.UserID, v, action, current.ID, "vote", "success"); err != nil {
		return empty, err
	}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return empty, err
	}
	var prior *approvalVote
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.ApproverID == action.CreatorID {
			continue
		}
		var human bool
		if err = tx.QueryRow(ctx, `SELECT NOT disabled AND deleted_at IS NULL
 AND source<>'settings_demo' AND v3_identity_kind='human' FROM users WHERE id=$1`,
			candidate.ApproverID).Scan(&human); err != nil {
			return empty, err
		}
		if !human {
			continue
		}
		if err = approvalGrantTx(ctx, tx, candidate.ApproverID, v.EnvironmentID,
			v.ApplicationID, v.TeamID); errors.Is(err, store.ErrForbidden) {
			continue
		} else if err != nil {
			return empty, err
		}
		if err = lockApprovalVoteProofTx(ctx, tx, *candidate); err != nil {
			if errors.Is(err, v3alertmanager.ErrSilenceApprovalUnavailable) {
				continue
			}
			return empty, err
		}
		if err = approvalVoteEvidenceTx(ctx, tx, *candidate, binding, now); err != nil {
			if errors.Is(err, v3alertmanager.ErrSilenceApprovalUnavailable) {
				continue
			}
			return empty, err
		}
		prior = candidate
		break
	}
	result := ApprovalResult{ProposalID: proposalID, Status: "pending_review",
		RequiredApprovals: 2, ReceivedApprovals: 1}
	if prior != nil && current.ExpiresAt.After(now) {
		first, second := *prior, current
		if second.VerifiedAt.Before(first.VerifiedAt) ||
			(second.VerifiedAt.Equal(first.VerifiedAt) && second.ID < first.ID) {
			first, second = second, first
		}
		expiresAt := first.ExpiresAt
		if second.ExpiresAt.Before(expiresAt) {
			expiresAt = second.ExpiresAt
		}
		decision := approvalDecision{ProposalID: proposalID, ExecutionID: store.ID(),
			ActionKind: action.Kind, ActionDigest: action.ActionDigest,
			ReviewContentDigest: action.ReviewContentDigest, CreatorID: action.CreatorID,
			EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
			Risk: v.Content.Risk, RequiredCount: 2, FirstVoteID: first.ID,
			SecondVoteID: second.ID, ApprovedAt: now, ExpiresAt: expiresAt}
		decision.ApprovalDigest = approvalDecisionDigest(decision, first, second)
		if decision.ApprovalDigest == "" || !decision.ExpiresAt.After(decision.ApprovedAt) {
			return empty, v3alertmanager.ErrSilenceApprovalUnavailable
		}
		_, err = tx.Exec(ctx, `INSERT INTO v3_proposal_approval_decisions
 (proposal_id,execution_id,action_kind,action_digest,review_content_digest,
  approval_digest,creator_id,environment_id,application_id,risk,required_count,
  first_vote_id,second_vote_id,approved_at,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			decision.ProposalID, decision.ExecutionID, decision.ActionKind,
			decision.ActionDigest, decision.ReviewContentDigest,
			decision.ApprovalDigest, decision.CreatorID, decision.EnvironmentID,
			decision.ApplicationID, decision.Risk, decision.RequiredCount,
			decision.FirstVoteID, decision.SecondVoteID, decision.ApprovedAt, decision.ExpiresAt)
		if err != nil {
			return empty, err
		}
		tag, err := tx.Exec(ctx, `UPDATE v3_proposals SET status='approved'
 WHERE id=$1 AND status='pending'`, proposalID)
		if err != nil {
			return empty, err
		}
		if tag.RowsAffected() != 1 {
			return empty, store.ErrConflict
		}
		if err = approvalAuditTx(ctx, tx, actor.UserID, v, action,
			decision.ExecutionID, "approved", "success"); err != nil {
			return empty, err
		}
		result.Status, result.ReceivedApprovals = "approved", 2
		result.ExecutionID, result.ApprovalDigest = decision.ExecutionID, decision.ApprovalDigest
	}
	if err = approvalSessionFreshTx(ctx, tx, actor); err != nil {
		return empty, err
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, err
	}
	return result, nil
}

func approvalResultTx(ctx context.Context, tx pgx.Tx, v record,
	a v3alertmanager.SilenceReviewedAction, candidates []approvalVote) (ApprovalResult, error) {
	result := ApprovalResult{ProposalID: v.ID, RequiredApprovals: 2}
	if v.Status == "approved" {
		err := tx.QueryRow(ctx, `SELECT execution_id,approval_digest FROM
 v3_proposal_approval_decisions WHERE proposal_id=$1`, v.ID).Scan(
			&result.ExecutionID, &result.ApprovalDigest)
		if err != nil {
			return ApprovalResult{}, err
		}
		result.Status, result.ReceivedApprovals = "approved", 2
		return result, nil
	}
	result.Status = "pending_review"
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return ApprovalResult{}, err
	}
	for _, candidate := range candidates {
		if candidate.ApproverID == a.CreatorID {
			continue
		}
		var human bool
		if err := tx.QueryRow(ctx, `SELECT NOT disabled AND deleted_at IS NULL
 AND source<>'settings_demo' AND v3_identity_kind='human' FROM users WHERE id=$1`,
			candidate.ApproverID).Scan(&human); err != nil {
			return ApprovalResult{}, err
		}
		if !human {
			continue
		}
		if err := approvalGrantTx(ctx, tx, candidate.ApproverID, v.EnvironmentID,
			v.ApplicationID, v.TeamID); errors.Is(err, store.ErrForbidden) {
			continue
		} else if err != nil {
			return ApprovalResult{}, err
		}
		if err := lockApprovalVoteProofTx(ctx, tx, candidate); errors.Is(err, v3alertmanager.ErrSilenceApprovalUnavailable) {
			continue
		} else if err != nil {
			return ApprovalResult{}, err
		}
		if err := approvalVoteEvidenceTx(ctx, tx, candidate, approvalBinding(a), now); errors.Is(err, v3alertmanager.ErrSilenceApprovalUnavailable) {
			continue
		} else if err != nil {
			return ApprovalResult{}, err
		}
		result.ReceivedApprovals = 1 // pending state cannot claim executable quorum
		break
	}
	return result, nil
}

func approvalAuditTx(ctx context.Context, tx pgx.Tx, actorID string, v record,
	a v3alertmanager.SilenceReviewedAction, target, event, outcome string) error {
	changes, err := json.Marshal(map[string]string{
		"proposal_id": v.ID, "environment_id": v.EnvironmentID,
		"action_kind": a.Kind, "action_digest": a.ActionDigest,
		"review_content_digest": a.ReviewContentDigest,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,$3,$4,$5,$6)`, actorID, v.ApplicationID,
		"v3.proposal.approval."+event, target, outcome, changes)
	return err
}
