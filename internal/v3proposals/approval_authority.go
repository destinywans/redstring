package v3proposals

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3alertmanager"
	"github.com/jackc/pgx/v5"
)

// SilenceApprovalAuthority verifies the persisted approval generation inside
// the executor's proposal/action/claim transaction. No HTTP input or cached
// authorization result is accepted as execution authority.
type SilenceApprovalAuthority struct {
	Proposals *Service
}

var _ v3alertmanager.SilenceApprovalAuthority = (*SilenceApprovalAuthority)(nil)

func (a *SilenceApprovalAuthority) Ready(ctx context.Context, environmentID, kind string) error {
	if a == nil || a.Proposals == nil || a.Proposals.DB == nil ||
		a.Proposals.ApprovalPasskeys == nil || a.Proposals.Access == nil ||
		environmentID == "" || environmentID == "all" ||
		(kind != v3alertmanager.ProposedSilenceCreateKind && kind != v3alertmanager.ProposedSilenceCancelKind) {
		return v3alertmanager.ErrSilenceApprovalUnavailable
	}
	// Table probes only establish that the durable verification path is
	// installed. A particular approval is checked again under the claim tx.
	for _, table := range []string{
		"v3_proposal_approval_decisions", "v3_proposal_approval_votes",
		"v3_proposal_approval_revocations", "v3_passkey_challenges",
		"v3_passkey_credentials", "v3_effective_role_grants",
	} {
		if _, err := a.Proposals.DB.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 0"); err != nil {
			return v3alertmanager.ErrSilenceApprovalUnavailable
		}
	}
	return nil
}

func (a *SilenceApprovalAuthority) VerifyApprovedWithTx(ctx context.Context, tx pgx.Tx,
	action v3alertmanager.SilenceReviewedAction) (v3alertmanager.SilenceApprovalProof, error) {
	var zero v3alertmanager.SilenceApprovalProof
	if a == nil || a.Proposals == nil || a.Proposals.DB == nil ||
		a.Proposals.ApprovalPasskeys == nil || a.Proposals.Access == nil || tx == nil ||
		action.ProposalID == "" || action.Status != "approved" ||
		!silenceProductDigest.MatchString(action.ActionDigest) ||
		!silenceProductDigest.MatchString(action.ReviewContentDigest) ||
		action.CreatorID == "" || action.ApplicationID == "" ||
		action.EnvironmentID == "" || action.EnvironmentID == "all" {
		return zero, v3alertmanager.ErrSilenceApprovalUnavailable
	}

	// The action source has already locked and reconstructed the immutable
	// proposal and action. The decision lock also serializes a later revocation
	// path, which must lock the proposal and decision before inserting a row.
	d, err := silenceApprovalDecisionTx(ctx, tx, action.ProposalID)
	if err != nil {
		return zero, approvalAuthorityError(err)
	}
	if d.ProposalID != action.ProposalID || d.ActionKind != action.Kind ||
		d.ActionDigest != action.ActionDigest || d.ReviewContentDigest != action.ReviewContentDigest ||
		d.CreatorID != action.CreatorID || d.EnvironmentID != action.EnvironmentID ||
		d.ApplicationID != action.ApplicationID || d.RequiredCount != 2 ||
		d.FirstVoteID == d.SecondVoteID || d.ExecutionID == "" ||
		!silenceProductDigest.MatchString(d.ApprovalDigest) {
		return zero, v3alertmanager.ErrSilenceApprovalUnavailable
	}
	var status, creator, creatorKind, env, app, risk string
	err = tx.QueryRow(ctx, `SELECT status,created_by,creator_kind,environment_id,application_id,
 content->>'risk' FROM v3_proposals WHERE id=$1 FOR SHARE`, action.ProposalID).
		Scan(&status, &creator, &creatorKind, &env, &app, &risk)
	if err != nil {
		return zero, approvalAuthorityError(err)
	}
	if status != "approved" || creatorKind != "user" || creator != d.CreatorID ||
		env != d.EnvironmentID || app != d.ApplicationID || risk != d.Risk {
		return zero, v3alertmanager.ErrSilenceApprovalUnavailable
	}
	var revokedID string
	err = tx.QueryRow(ctx, `SELECT proposal_id FROM v3_proposal_approval_revocations
 WHERE proposal_id=$1 FOR SHARE`, action.ProposalID).Scan(&revokedID)
	if err == nil {
		return zero, v3alertmanager.ErrSilenceApprovalUnavailable
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}

	// Acquire both vote locks in stable ID order, while preserving the stored
	// first/second order for the approval digest and the returned proof.
	voteIDs := []string{d.FirstVoteID, d.SecondVoteID}
	sort.Strings(voteIDs)
	lockedVotes := make(map[string]approvalVote, 2)
	for _, id := range voteIDs {
		v, err := silenceApprovalVoteTx(ctx, tx, id)
		if err != nil {
			return zero, approvalAuthorityError(err)
		}
		lockedVotes[id] = v
	}
	first, second := lockedVotes[d.FirstVoteID], lockedVotes[d.SecondVoteID]
	if first.ID != d.FirstVoteID || second.ID != d.SecondVoteID ||
		first.ProposalID != action.ProposalID || second.ProposalID != action.ProposalID ||
		first.ApproverID == "" || second.ApproverID == "" ||
		first.ApproverID == second.ApproverID ||
		first.ApproverID == action.CreatorID || second.ApproverID == action.CreatorID ||
		first.ChallengeID == "" || second.ChallengeID == "" ||
		first.ChallengeID == second.ChallengeID ||
		first.ActionKind != action.Kind || second.ActionKind != action.Kind ||
		first.ActionDigest != action.ActionDigest || second.ActionDigest != action.ActionDigest ||
		first.ReviewContentDigest != action.ReviewContentDigest ||
		second.ReviewContentDigest != action.ReviewContentDigest ||
		first.VerifiedAt.After(second.VerifiedAt) ||
		first.VerifiedAt.Equal(second.VerifiedAt) && first.ID >= second.ID ||
		second.VerifiedAt.After(d.ApprovedAt) ||
		!d.ExpiresAt.After(d.ApprovedAt) ||
		d.ExpiresAt.After(first.ExpiresAt) || d.ExpiresAt.After(second.ExpiresAt) ||
		approvalDecisionDigest(d, first, second) != d.ApprovalDigest {
		return zero, v3alertmanager.ErrSilenceApprovalUnavailable
	}

	// Lock both challenge rows, then both current human users in ascending ID
	// order before credentials. Passkey mutation also locks users before their
	// credentials, so the executor does not invert that shared lock order.
	challengeIDs := []string{first.ChallengeID, second.ChallengeID}
	sort.Strings(challengeIDs)
	for _, id := range challengeIDs {
		var found string
		if err := tx.QueryRow(ctx, `SELECT id FROM v3_passkey_challenges WHERE id=$1 FOR SHARE`, id).Scan(&found); err != nil {
			return zero, approvalAuthorityError(err)
		}
	}
	users := []string{first.ApproverID, second.ApproverID}
	sort.Strings(users)
	for _, id := range users {
		var found string
		if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled
 AND deleted_at IS NULL AND v3_identity_kind='human' AND source<>'settings_demo' FOR SHARE`, id).Scan(&found); err != nil {
			return zero, approvalAuthorityError(err)
		}
	}
	credentials := []approvalVote{first, second}
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
		fingerprint := sha256.Sum256(vote.CredentialID)
		if vote.CredentialFingerprint != hex.EncodeToString(fingerprint[:8]) {
			return zero, v3alertmanager.ErrSilenceApprovalUnavailable
		}
		var found []byte
		if err := tx.QueryRow(ctx, `SELECT credential_id FROM v3_passkey_credentials
 WHERE instance_id=$1 AND rp_id=$2 AND user_id=$3 AND credential_id=$4 FOR SHARE`,
			vote.InstanceID, vote.RPID, vote.ApproverID, vote.CredentialID).Scan(&found); err != nil {
			return zero, approvalAuthorityError(err)
		}
	}
	var teamID string
	err = tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ve,e`,
		action.ApplicationID, action.EnvironmentID).Scan(&teamID)
	if err != nil {
		return zero, approvalAuthorityError(err)
	}
	if err := lockReviewAuthorityTables(ctx, tx); err != nil {
		return zero, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return zero, err
	}
	if d.ApprovedAt.IsZero() || d.ApprovedAt.After(now) || !d.ExpiresAt.After(now) ||
		!first.ExpiresAt.After(now) || !second.ExpiresAt.After(now) {
		return zero, v3alertmanager.ErrSilenceApprovalUnavailable
	}
	binding := approvalBinding(action)
	for _, vote := range []approvalVote{first, second} {
		if err := approvalVoteEvidenceTx(ctx, tx, vote, binding, now); err != nil {
			return zero, approvalAuthorityError(err)
		}
		if err := approvalGrantTx(ctx, tx, vote.ApproverID,
			action.EnvironmentID, action.ApplicationID, teamID); err != nil {
			return zero, approvalAuthorityError(err)
		}
	}
	return v3alertmanager.SilenceApprovalProof{
		ProposalID: action.ProposalID, ActionKind: action.Kind,
		ActionDigest: action.ActionDigest, ReviewContentDigest: action.ReviewContentDigest,
		CreatorID: action.CreatorID, EnvironmentID: action.EnvironmentID,
		ApplicationID: action.ApplicationID, ApprovalDigest: d.ApprovalDigest,
		Status: "approved", ApprovedAt: d.ApprovedAt, ExpiresAt: d.ExpiresAt,
		Approvers: []v3alertmanager.SilenceHumanApproval{
			{UserID: first.ApproverID, PasskeyProofID: first.ChallengeID, VerifiedAt: first.VerifiedAt},
			{UserID: second.ApproverID, PasskeyProofID: second.ChallengeID, VerifiedAt: second.VerifiedAt},
		},
	}, nil
}

func silenceApprovalDecisionTx(ctx context.Context, tx pgx.Tx, id string) (approvalDecision, error) {
	var d approvalDecision
	err := tx.QueryRow(ctx, `SELECT proposal_id,execution_id,action_kind,action_digest,
 review_content_digest,approval_digest,creator_id,environment_id,application_id,risk,
 required_count,first_vote_id,second_vote_id,approved_at,expires_at
 FROM v3_proposal_approval_decisions WHERE proposal_id=$1 FOR SHARE`, id).Scan(
		&d.ProposalID, &d.ExecutionID, &d.ActionKind, &d.ActionDigest,
		&d.ReviewContentDigest, &d.ApprovalDigest, &d.CreatorID, &d.EnvironmentID,
		&d.ApplicationID, &d.Risk, &d.RequiredCount, &d.FirstVoteID,
		&d.SecondVoteID, &d.ApprovedAt, &d.ExpiresAt)
	return d, err
}

func silenceApprovalVoteTx(ctx context.Context, tx pgx.Tx, id string) (approvalVote, error) {
	var v approvalVote
	err := tx.QueryRow(ctx, `SELECT id,proposal_id,approver_id,challenge_id,action_kind,
 action_digest,review_content_digest,instance_id,rp_id,credential_id,
 credential_fingerprint,request_hash,comment,verified_at,expires_at
 FROM v3_proposal_approval_votes WHERE id=$1 FOR SHARE`, id).Scan(
		&v.ID, &v.ProposalID, &v.ApproverID, &v.ChallengeID, &v.ActionKind,
		&v.ActionDigest, &v.ReviewContentDigest, &v.InstanceID, &v.RPID,
		&v.CredentialID, &v.CredentialFingerprint, &v.RequestHash,
		&v.Comment, &v.VerifiedAt, &v.ExpiresAt)
	return v, err
}

func approvalAuthorityError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrForbidden) {
		return v3alertmanager.ErrSilenceApprovalUnavailable
	}
	return err
}
