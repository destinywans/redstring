package v3alertrules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// AlertRuleApprovalAuthority reads the durable WAN-117 decision in the same
// transaction that claims an enable execution. A prior HTTP response, account
// passkey assertion, or caller-supplied approver never grants authority here.
type AlertRuleApprovalAuthority struct{}

var _ AlertRuleApprovalSource = (*AlertRuleApprovalAuthority)(nil)

type alertRuleApprovalDecision struct {
	ProposalID, ExecutionID, ActionKind, ActionDigest, ReviewContentDigest, ApprovalDigest string
	CreatorID, EnvironmentID, ApplicationID, Risk, FirstVoteID, SecondVoteID               string
	RequiredCount                                                                          int
	ApprovedAt, ExpiresAt                                                                  time.Time
}

type alertRuleApprovalVote struct {
	ID, ProposalID, ApproverID, ChallengeID, ActionKind, ActionDigest, ReviewContentDigest string
	InstanceID, RPID, CredentialFingerprint, RequestHash, Comment                          string
	CredentialID                                                                           []byte
	VerifiedAt, ExpiresAt                                                                  time.Time
}

func alertRuleApprovalUnavailable() error {
	return problem(409, "approval_unavailable", "Current independent human approval is unavailable")
}

func alertRuleApprovalReadError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return alertRuleApprovalUnavailable()
	}
	return err
}

func (a *AlertRuleApprovalAuthority) VerifyWithTx(ctx context.Context, tx pgx.Tx,
	proposalID, actionDigest string) (AlertRuleApproval, error) {
	var zero AlertRuleApproval
	if a == nil || tx == nil || !hexID(proposalID) || !trialFingerprintRE.MatchString(actionDigest) {
		return zero, alertRuleApprovalUnavailable()
	}
	// The executor also loads the action, but the approval source independently
	// checks the persisted review/action identity before trusting a decision.
	var actionRaw, reviewRaw []byte
	var status, creatorKind, creator, actionCreator, env, app, risk, kind, source, storedAction, reviewDigest string
	err := tx.QueryRow(ctx, `SELECT p.status,p.created_by,p.creator_kind,p.environment_id,
 p.application_id,p.content->>'risk',p.content,a.action_kind,a.source_kind,
 a.creator_id,a.action_digest,a.review_content_digest,a.action
 FROM v3_proposals p JOIN v3_proposal_actions a ON a.proposal_id=p.id
 WHERE p.id=$1 FOR SHARE OF p,a`, proposalID).Scan(
		&status, &creator, &creatorKind, &env, &app, &risk, &reviewRaw, &kind,
		&source, &actionCreator, &storedAction, &reviewDigest, &actionRaw)
	if err != nil {
		return zero, alertRuleApprovalReadError(err)
	}
	canonicalAction, actionErr := canonicalEnableJSON(actionRaw)
	canonicalReview, reviewErr := canonicalEnableJSON(reviewRaw)
	if status != "approved" || creatorKind != "user" || source != "human_request" ||
		kind != alertRuleEnableKind || creator == "" || actionCreator != creator ||
		env == "" || env == "all" || app == "" ||
		!trialFingerprintRE.MatchString(storedAction) || storedAction != actionDigest ||
		!trialFingerprintRE.MatchString(reviewDigest) || actionErr != nil || reviewErr != nil ||
		enableDigest("aiops.v3.proposal.action.v1", canonicalAction) != actionDigest ||
		enableDigest("aiops.v3.proposal.review.v1", canonicalReview) != reviewDigest {
		return zero, alertRuleApprovalUnavailable()
	}

	var d alertRuleApprovalDecision
	err = tx.QueryRow(ctx, `SELECT proposal_id,execution_id,action_kind,action_digest,
 review_content_digest,approval_digest,creator_id,environment_id,application_id,risk,
 required_count,first_vote_id,second_vote_id,approved_at,expires_at
 FROM v3_proposal_approval_decisions WHERE proposal_id=$1 FOR SHARE`, proposalID).Scan(
		&d.ProposalID, &d.ExecutionID, &d.ActionKind, &d.ActionDigest,
		&d.ReviewContentDigest, &d.ApprovalDigest, &d.CreatorID, &d.EnvironmentID,
		&d.ApplicationID, &d.Risk, &d.RequiredCount, &d.FirstVoteID,
		&d.SecondVoteID, &d.ApprovedAt, &d.ExpiresAt)
	if err != nil {
		return zero, alertRuleApprovalReadError(err)
	}
	if d.ProposalID != proposalID || !hexID(d.ExecutionID) ||
		d.ActionKind != kind || d.ActionDigest != actionDigest ||
		d.ReviewContentDigest != reviewDigest || d.CreatorID != creator ||
		d.EnvironmentID != env || d.ApplicationID != app || d.Risk != risk ||
		d.RequiredCount != 2 || d.FirstVoteID == d.SecondVoteID ||
		!trialFingerprintRE.MatchString(d.ApprovalDigest) {
		return zero, alertRuleApprovalUnavailable()
	}
	var revoked string
	err = tx.QueryRow(ctx, `SELECT proposal_id FROM v3_proposal_approval_revocations
 WHERE proposal_id=$1 FOR SHARE`, proposalID).Scan(&revoked)
	if err == nil {
		return zero, alertRuleApprovalUnavailable()
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}

	// Preserve first/second vote order for the digest; lock rows in stable ID
	// order to avoid competing claim and revocation deadlocks.
	voteIDs := []string{d.FirstVoteID, d.SecondVoteID}
	sort.Strings(voteIDs)
	locked := make(map[string]alertRuleApprovalVote, 2)
	for _, id := range voteIDs {
		var v alertRuleApprovalVote
		err = tx.QueryRow(ctx, `SELECT id,proposal_id,approver_id,challenge_id,action_kind,
 action_digest,review_content_digest,instance_id,rp_id,credential_id,
 credential_fingerprint,request_hash,comment,verified_at,expires_at
 FROM v3_proposal_approval_votes WHERE id=$1 FOR SHARE`, id).Scan(
			&v.ID, &v.ProposalID, &v.ApproverID, &v.ChallengeID, &v.ActionKind,
			&v.ActionDigest, &v.ReviewContentDigest, &v.InstanceID, &v.RPID,
			&v.CredentialID, &v.CredentialFingerprint, &v.RequestHash,
			&v.Comment, &v.VerifiedAt, &v.ExpiresAt)
		if err != nil {
			return zero, alertRuleApprovalReadError(err)
		}
		locked[id] = v
	}
	first, second := locked[d.FirstVoteID], locked[d.SecondVoteID]
	if first.ID != d.FirstVoteID || second.ID != d.SecondVoteID ||
		first.ProposalID != proposalID || second.ProposalID != proposalID ||
		first.ApproverID == "" || second.ApproverID == "" ||
		first.ApproverID == second.ApproverID ||
		first.ApproverID == creator || second.ApproverID == creator ||
		first.ChallengeID == "" || second.ChallengeID == "" ||
		first.ChallengeID == second.ChallengeID ||
		first.ActionKind != kind || second.ActionKind != kind ||
		first.ActionDigest != actionDigest || second.ActionDigest != actionDigest ||
		first.ReviewContentDigest != reviewDigest || second.ReviewContentDigest != reviewDigest ||
		first.VerifiedAt.After(second.VerifiedAt) ||
		(first.VerifiedAt.Equal(second.VerifiedAt) && first.ID >= second.ID) ||
		second.VerifiedAt.After(d.ApprovedAt) || d.ApprovedAt.IsZero() ||
		!d.ExpiresAt.After(d.ApprovedAt) ||
		d.ExpiresAt.After(first.ExpiresAt) || d.ExpiresAt.After(second.ExpiresAt) ||
		alertRuleDecisionDigest(d, first, second) != d.ApprovalDigest {
		return zero, alertRuleApprovalUnavailable()
	}

	challengeIDs := []string{first.ChallengeID, second.ChallengeID}
	sort.Strings(challengeIDs)
	for _, id := range challengeIDs {
		var found string
		if err = tx.QueryRow(ctx, `SELECT id FROM v3_passkey_challenges WHERE id=$1 FOR SHARE`, id).Scan(&found); err != nil {
			return zero, alertRuleApprovalReadError(err)
		}
	}
	users := []string{first.ApproverID, second.ApproverID}
	sort.Strings(users)
	for _, id := range users {
		var found string
		if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled
 AND deleted_at IS NULL AND v3_identity_kind='human' AND source<>'settings_demo' FOR SHARE`, id).Scan(&found); err != nil {
			return zero, alertRuleApprovalReadError(err)
		}
	}
	// Keep the user-before-session lock order used by proposal approval.
	// Session deletion is a revocation, so hold each row through the claim.
	for _, id := range challengeIDs {
		var tokenHash string
		if err = tx.QueryRow(ctx, `SELECT s.token_hash FROM v3_passkey_challenges c
 JOIN sessions s ON s.token_hash=c.session_hash AND s.user_id=c.user_id
 WHERE c.id=$1 FOR SHARE OF s`, id).Scan(&tokenHash); err != nil {
			return zero, alertRuleApprovalReadError(err)
		}
	}
	credentials := []alertRuleApprovalVote{first, second}
	sort.Slice(credentials, func(i, j int) bool {
		l, r := credentials[i], credentials[j]
		if l.InstanceID != r.InstanceID {
			return l.InstanceID < r.InstanceID
		}
		if l.RPID != r.RPID {
			return l.RPID < r.RPID
		}
		return bytes.Compare(l.CredentialID, r.CredentialID) < 0
	})
	for _, vote := range credentials {
		var found []byte
		if err = tx.QueryRow(ctx, `SELECT credential_id FROM v3_passkey_credentials
 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND credential_id=$4 FOR SHARE`,
			vote.InstanceID, vote.RPID, vote.ApproverID, vote.CredentialID).Scan(&found); err != nil {
			return zero, alertRuleApprovalReadError(err)
		}
	}
	var teamID string
	err = tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ve,e`, app, env).Scan(&teamID)
	if err != nil {
		return zero, alertRuleApprovalReadError(err)
	}
	// The view is derived from these source tables. Lock them before the final
	// current-grant read so permission revocation cannot race the claim commit.
	if _, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles, rbac_department_roles,
 v3_role_grants IN SHARE MODE`); err != nil {
		return zero, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return zero, err
	}
	if d.ApprovedAt.After(now) || !d.ExpiresAt.After(now) ||
		!first.ExpiresAt.After(now) || !second.ExpiresAt.After(now) {
		return zero, alertRuleApprovalUnavailable()
	}
	for _, vote := range []alertRuleApprovalVote{first, second} {
		if err = alertRuleVoteEvidenceTx(ctx, tx, vote, proposalID, kind, actionDigest,
			reviewDigest, env, now); err != nil {
			return zero, err
		}
		if err = alertRuleApprovalGrantTx(ctx, tx, vote.ApproverID, env, app, teamID); err != nil {
			return zero, err
		}
	}
	return AlertRuleApproval{ID: d.ExecutionID, Digest: d.ApprovalDigest,
		ActionDigest: actionDigest, ApproverID: second.ApproverID,
		ApproverIDs: []string{first.ApproverID, second.ApproverID},
		ApprovedAt:  d.ApprovedAt, ExpiresAt: d.ExpiresAt}, nil
}

// This must mirror v3proposals.approvalDecisionDigest exactly. The schema's
// immutable decision digest is recomputed rather than trusted by its shape.
func alertRuleDecisionDigest(d alertRuleApprovalDecision, first, second alertRuleApprovalVote) string {
	type voteDigest struct {
		ID, UserID, ChallengeID, InstanceID, RPID, CredentialFingerprint, RequestHash string
		CredentialID                                                                  string
		VerifiedAt, ExpiresAt                                                         string
	}
	encodeVote := func(v alertRuleApprovalVote) voteDigest {
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
	return enableDigest("aiops.v3.proposal.approval.v1", raw)
}

func alertRuleVoteEvidenceTx(ctx context.Context, tx pgx.Tx, v alertRuleApprovalVote,
	proposalID, kind, actionDigest, reviewDigest, env string, now time.Time) error {
	fingerprint := sha256.Sum256(v.CredentialID)
	if v.ProposalID != proposalID || v.ActionKind != kind || v.ActionDigest != actionDigest ||
		v.ReviewContentDigest != reviewDigest || v.ApproverID == "" || v.ChallengeID == "" ||
		len(v.CredentialID) == 0 || v.RequestHash == "" ||
		v.CredentialFingerprint != hex.EncodeToString(fingerprint[:8]) ||
		!v.ExpiresAt.After(now) || !v.ExpiresAt.Equal(v.VerifiedAt.Add(15*time.Minute)) {
		return alertRuleApprovalUnavailable()
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_passkey_challenges c
 JOIN v3_passkey_credentials k ON k.instance_id=c.instance_id AND k.rp_id=c.rp_id
  AND k.user_id=c.user_id AND k.credential_id=$6
 JOIN sessions s ON s.token_hash=c.session_hash AND s.user_id=c.user_id
 WHERE c.id=$1 AND c.user_id=$2 AND c.proposal_id=$3 AND c.action_kind=$4
  AND c.action_digest=$5 AND c.review_content_digest=$7 AND c.environment_id=$8
  AND c.instance_id=$9 AND c.rp_id=$10 AND c.kind='proposal_approve'
  AND c.consumed_at=$11 AND c.expires_at>=$11 AND c.created_at<=$11
  AND s.expires_at>clock_timestamp() AND k.revoked_at IS NULL AND NOT k.clone_warning
  AND k.last_used_at>=c.consumed_at)`,
		v.ChallengeID, v.ApproverID, proposalID, kind, actionDigest, v.CredentialID,
		reviewDigest, env, v.InstanceID, v.RPID, v.VerifiedAt).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return alertRuleApprovalUnavailable()
	}
	return nil
}

func alertRuleApprovalGrantTx(ctx context.Context, tx pgx.Tx, userID, env, app, team string) error {
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
		userID, env, app, team).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return alertRuleApprovalUnavailable()
	}
	return nil
}
