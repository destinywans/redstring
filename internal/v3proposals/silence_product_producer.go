package v3proposals

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

var silenceProductSessionHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
var silenceProductDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SilenceProductProducer bridges the product request to the durable proposal
// producer. It does not approve, reserve an Alertmanager write, or execute one.
// The same instance can be used as the cancellation review writer.
type SilenceProductProducer struct {
	Proposals *Service
	Ledger    *v3alertmanager.DatabaseSilenceLedger
	Client    *v3alertmanager.Client
}

func (p *SilenceProductProducer) SubmitSilenceCreate(ctx context.Context, actor core.User,
	sessionTokenHash, investigationID string, in v3alertmanager.SilenceCreateInput) (v3alertmanager.SilenceProductProposal, error) {
	var zero v3alertmanager.SilenceProductProposal
	if p == nil || p.Proposals == nil || p.Ledger == nil || p.Client == nil ||
		p.Proposals.DB == nil || p.Proposals.DB != p.Ledger.DB {
		return zero, v3alertmanager.ErrNotConfigured
	}
	result, err := p.Proposals.CreateSilenceProposalForSession(ctx, actor, sessionTokenHash,
		SilenceProposalRequest{InvestigationID: investigationID, Silence: in}, p.Ledger, p.Client)
	if err != nil {
		return zero, err
	}
	return v3alertmanager.SilenceProductProposal{ProposalID: result.ProposalID,
		Status: result.Status, ActionDigest: result.ActionDigest}, nil
}

func lockSilenceProductSession(ctx context.Context, tx pgx.Tx, actorID, tokenHash string) error {
	var expiresAt time.Time
	err := tx.QueryRow(ctx, `SELECT expires_at FROM sessions
 WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`,
		tokenHash, actorID).Scan(&expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrSessionRevoked
	}
	return err
}

func recheckSilenceProductSession(ctx context.Context, tx pgx.Tx, actorID, tokenHash string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, tokenHash, actorID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return store.ErrSessionRevoked
	}
	return err
}

// WritePendingSilenceCancelWithTx writes the review and action in the caller's
// transaction. The caller owns target/authority locks and writes the matching
// immutable cancel intent before commit. Neither component can commit alone.
func (p *SilenceProductProducer) WritePendingSilenceCancelWithTx(ctx context.Context, tx pgx.Tx,
	actor core.User, action v3alertmanager.ProposedSilenceCancel) (v3alertmanager.SilenceProductProposal, error) {
	var zero v3alertmanager.SilenceProductProposal
	if p == nil || p.Proposals == nil || p.Proposals.DB == nil || p.Proposals.Access == nil ||
		p.Ledger == nil || p.Proposals.DB != p.Ledger.DB || p.Client == nil || tx == nil {
		return zero, v3alertmanager.ErrNotConfigured
	}
	if actor.ID == "" || actor.Disabled || actor.ID != action.ActorID ||
		action.Kind != v3alertmanager.ProposedSilenceCancelKind ||
		action.SchemaVersion != v3alertmanager.ProposedSilenceCancelVersion ||
		!bounded(action.IntentID, 128) || !bounded(action.CreateOperationID, 128) ||
		!bounded(action.EnvironmentID, 128) || !bounded(action.ApplicationID, 128) ||
		!bounded(action.InvestigationID, 128) || !bounded(action.UpstreamID, 128) ||
		!bounded(action.Reason, 1000) || !silenceProposalKey.MatchString(action.IdempotencyKey) ||
		!silenceProductDigest.MatchString(action.CreatePayloadHash) ||
		!silenceProductDigest.MatchString(action.RequestHash) ||
		!silenceProductDigest.MatchString(action.ObservedDigest) ||
		!silenceProductDigest.MatchString(action.BindingFingerprint) {
		return zero, ErrInvalidProposal
	}
	// The cancellation domain has already locked and authorized the current
	// investigation. Read its version again for the relational action record.
	var investigationUpdatedAt time.Time
	err := tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
   AND e.key=$3 AND NOT e.archived FOR SHARE OF i`, action.InvestigationID,
		action.ApplicationID, action.EnvironmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if !investigationUpdatedAt.Equal(action.InvestigationUpdatedAt) {
		return zero, store.ErrConflict
	}
	var existingID string
	err = tx.QueryRow(ctx, `SELECT proposal_id FROM v3_proposal_actions
 WHERE creator_id=$1 AND idempotency_key=$2 FOR SHARE`, actor.ID, action.IdempotencyKey).Scan(&existingID)
	if err == nil {
		return zero, store.ErrConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	content := silenceCancelReviewContent(action)
	if !validContent(content) {
		return zero, ErrInvalidProposal
	}
	actionRaw, err := canonicalProposalJSON(action)
	if err != nil {
		return zero, err
	}
	contentRaw, err := canonicalProposalJSON(content)
	if err != nil {
		return zero, err
	}
	actionDigest := proposalDigest("aiops.v3.proposal.action.v1", actionRaw)
	contentDigest := proposalDigest("aiops.v3.proposal.review.v1", contentRaw)
	proposalID := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposals
 (id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 VALUES($1,$2,$3,$4,$5::jsonb,$6,'user')`, proposalID, action.InvestigationID,
		action.ApplicationID, action.EnvironmentID, contentRaw, actor.ID)
	if err != nil {
		return zero, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO v3_proposal_actions
 (proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,request_hash,
  binding_fingerprint,creator_id,environment_id,application_id,investigation_id,
  investigation_updated_at,idempotency_key,source_kind)
 VALUES($1,$2,$3,$4::jsonb,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
 ON CONFLICT DO NOTHING`, proposalID, action.Kind, action.SchemaVersion, actionRaw, actionDigest,
		contentDigest, action.RequestHash, action.BindingFingerprint, actor.ID, action.EnvironmentID,
		action.ApplicationID, action.InvestigationID, investigationUpdatedAt,
		action.IdempotencyKey, silenceProposalSource)
	if err != nil {
		return zero, err
	}
	if tag.RowsAffected() != 1 {
		return zero, store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.proposal.silence_cancel_requested',$3,'success',
 jsonb_build_object('environment_id',$4::text,'investigation_id',$5::text,
 'action_digest',$6::text,'review_content_digest',$7::text,'create_operation_id',$8::text))`,
		actor.ID, action.ApplicationID, proposalID, action.EnvironmentID, action.InvestigationID,
		actionDigest, contentDigest, action.CreateOperationID)
	if err != nil {
		return zero, err
	}
	return v3alertmanager.SilenceProductProposal{ProposalID: proposalID,
		Status: "pending", ActionDigest: actionDigest}, nil
}

func silenceCancelReviewContent(action v3alertmanager.ProposedSilenceCancel) Content {
	return Content{
		Title:           fmt.Sprintf("Cancel application silence %q", action.ApplicationLabel),
		Risk:            "high",
		Impact:          "The identified application silence would be expired in Alertmanager only after separate human approval and execution.",
		Reversible:      false,
		ProposedBy:      action.ActorID,
		InvestigationID: action.InvestigationID,
		Target: fmt.Sprintf("Alertmanager silence %s, application=%q, %s=%q",
			action.UpstreamID, action.ApplicationLabel, action.EnvironmentLabel, action.EnvironmentID),
		Diff: Diff{Path: "alertmanager/silences/" + action.UpstreamID,
			Lines: []string{fmt.Sprintf("Create operation: %s", action.CreateOperationID),
				fmt.Sprintf("Observed silence digest: %s", action.ObservedDigest),
				"Reason: " + action.Reason}},
		Method: "Human-requested cancellation of one AIOps-created, confirmed silence. No upstream silence is expired by this proposal.",
		Evidence: []Evidence{{N: 1, Icon: "activity", Title: "Exact ownership observation",
			Desc: "The local confirmed create operation, upstream operation marker, complete payload and current application/environment scope matched at request time."}},
		Verification:     []string{"Recheck the immutable action digest, approval, current grants, connector binding, create operation and a fresh complete Alertmanager observation before expiry."},
		VerificationNote: "The observed digest is a proposal-time fact. Later upstream changes require a new decision.",
		Checklist:        []string{"Independent human approval with fresh passkey proof is required before execution."},
		Requirement:      "This pending review grants no permission to delete or expire an upstream silence.",
	}
}
