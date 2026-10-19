package v3proposals

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3alertmanager"
	"github.com/jackc/pgx/v5"
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
	if strings.HasPrefix(in.IdempotencyKey, "bulk-") {
		return zero, v3alertmanager.ErrSilenceCreateInvalid
	}
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
	content := silenceCancelReviewContent(action, httpapi.EnglishRequested(ctx))
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

// silenceCancelReviewContent is an early-end proposal's review in the
// requester's language (PA-121): after two approvals the executor rereads
// Alertmanager and expires the silence once.
func silenceCancelReviewContent(action v3alertmanager.ProposedSilenceCancel, english bool) Content {
	return Content{
		Title: pick(english, fmt.Sprintf("提前结束应用 %s 的静默", action.ApplicationLabel),
			fmt.Sprintf("Cancel application silence %q", action.ApplicationLabel)),
		Risk: "high",
		Impact: pick(english, "批准后，执行器让这条静默在 Alertmanager 中立即到期，匹配的告警恢复通知。",
			"Once approved, the executor expires this silence in Alertmanager and matching alerts notify again."),
		Reversible:      false,
		ProposedBy:      action.ActorID,
		InvestigationID: action.InvestigationID,
		Target: fmt.Sprintf("Alertmanager silence %s, application=%q, %s=%q",
			action.UpstreamID, action.ApplicationLabel, action.EnvironmentLabel, action.EnvironmentID),
		Diff: Diff{Path: "alertmanager/silences/" + action.UpstreamID,
			Lines: []string{pick(english, "创建操作：", "Create operation: ") + action.CreateOperationID,
				pick(english, "观测到的静默摘要：", "Observed silence digest: ") + action.ObservedDigest,
				pick(english, "原因：", "Reason: ") + action.Reason}},
		Method: pick(english,
			"只提前结束一条由 AIOps 创建并已确认的静默。两位审批人批准后，独立执行器重新完整读取 Alertmanager，静默仍与发起时观测到的一致才让它到期，只执行一次；不自动重试，结果不明时只读取核对。",
			"Cancels one confirmed silence that AIOps created. After two approvals, the independent executor reads Alertmanager again in full and expires the silence once, only if it still matches what was observed at request time. It never retries; an unknown outcome is only reconciled by reading."),
		Evidence: []Evidence{{N: 1, Icon: "activity", Title: pick(english, "归属核对", "Exact ownership observation"),
			Desc: pick(english, "发起时，本地已确认的创建操作、上游操作标记、完整内容和当前应用、环境范围全部一致。",
				"The local confirmed create operation, upstream operation marker, complete payload and current application/environment scope matched at request time.")}},
		Verification: []string{
			pick(english, "执行前复核提案摘要、批准、当前权限、连接器绑定和创建操作，并重新完整读取 Alertmanager；静默有任何变化都不写入。",
				"Before expiring, the executor rechecks the proposal digest, approval, current grants, connector binding and create operation, and reads Alertmanager again in full; nothing is written if the silence changed."),
			pick(english, "结果不明时只读取 Alertmanager 核对，不再次写入。",
				"An unknown outcome is only reconciled by reading Alertmanager; it is never written again."),
		},
		VerificationNote: pick(english, "观测摘要是发起时的事实；之后上游有变化需要重新发起。",
			"The observed digest is a request-time fact. Later upstream changes require a new request."),
		Checklist: []string{
			pick(english, "两位非发起人的审批人分别用通行密钥批准。", "Two approvers other than the requester each approve with a passkey."),
			pick(english, "第二位批准后由执行器自动让静默到期，不需要另行执行。",
				"After the second approval the executor expires the silence automatically; there is no separate execution step."),
		},
		Requirement: pick(english, "待审期间不会让任何静默到期；第二位审批人批准后自动执行。",
			"No silence is expired while the review is pending; it runs automatically after the second approval."),
	}
}

// legacySilenceCancelReviewContent is the English review written before
// PA-121. Proposals stored with it keep it; it is never written again.
func legacySilenceCancelReviewContent(action v3alertmanager.ProposedSilenceCancel) Content {
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

// silenceCancelReviewContents are the reviews a stored early-end proposal
// may carry.
func silenceCancelReviewContents(action v3alertmanager.ProposedSilenceCancel) []Content {
	return []Content{silenceCancelReviewContent(action, false), silenceCancelReviewContent(action, true),
		legacySilenceCancelReviewContent(action)}
}
