package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

var (
	deploymentRevisionRE = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	deploymentBranchRE   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$`)
)

// A plan is the exact deployable YAML target and content approved by a human.
// The adapter must derive ContentDigest from complete bytes, including all
// existing rules retained in the target file, at BaseRevision. No caller input
// may choose a project, target path or branch.
type AlertRuleDeploymentPlan struct {
	ProjectID        int64  `json:"project_id"`
	TargetBranch     string `json:"target_branch"`
	Path             string `json:"path"`
	BaseRevision     string `json:"base_revision"`
	ContentDigest    string `json:"content_digest"`
	ScopeFingerprint string `json:"scope_fingerprint"`
	// Load and Merge are file_pull and executor for a D11 binding: the
	// approval covers the executor merging the MR. Both are empty for the
	// WAN-118 Argo CD flow, whose MR a person merges.
	Load  string `json:"load,omitempty"`
	Merge string `json:"merge,omitempty"`
	// ReadbackTimeoutSeconds is how long a file_pull deployment waits for
	// Prometheus to load the merged file (D11).
	ReadbackTimeoutSeconds int `json:"readback_timeout_seconds,omitempty"`
}

// MergeByExecutor is the plan's Merge when the executor merges (D11).
const MergeByExecutor = "executor"

// ExecutorMerges reports a D11 plan.
func (p AlertRuleDeploymentPlan) ExecutorMerges() bool { return p.Merge == MergeByExecutor }

func validateDeploymentPlan(p AlertRuleDeploymentPlan) error {
	if p.ProjectID <= 0 || !deploymentBranchRE.MatchString(p.TargetBranch) ||
		!deploymentRevisionRE.MatchString(p.BaseRevision) || !trialFingerprintRE.MatchString(p.ContentDigest) ||
		!trialFingerprintRE.MatchString(p.ScopeFingerprint) || len(p.Path) == 0 || len(p.Path) > 1000 ||
		strings.HasPrefix(p.Path, "/") || strings.Contains(p.Path, "\\") || strings.Contains(p.Path, "//") ||
		path.Clean(p.Path) != p.Path || strings.HasPrefix(p.Path, "../") || strings.Contains(p.Path, "/../") ||
		strings.HasPrefix(p.Path, "ops-config/alert-rule-drafts/") ||
		!(strings.HasSuffix(p.Path, ".yml") || strings.HasSuffix(p.Path, ".yaml")) ||
		!((p.Load == "" && p.Merge == "" && p.ReadbackTimeoutSeconds == 0) ||
			(p.Load == LoadFilePull && p.Merge == MergeByExecutor && p.ReadbackTimeoutSeconds >= 60 && p.ReadbackTimeoutSeconds <= 3600)) {
		return problem(503, "rule_deployment_plan_invalid", "Trusted alert rule target plan is invalid")
	}
	return nil
}

// WAN-117 supplies this from a durable proposal-bound passkey approval. The
// verifier must lock current approval, approver/session/grants, action digest,
// challenge consumption and target scope in the SAME transaction. Account
// passkey verification alone is not proposal approval.
type AlertRuleApprovalSource interface {
	VerifyWithTx(context.Context, pgx.Tx, string, string) (AlertRuleApproval, error)
}

type AlertRuleApproval struct {
	ID           string
	Digest       string // SHA-256 of immutable, proposal-bound proof record
	ActionDigest string
	ApproverID   string   // Second approver, retained for the original audit actor column.
	ApproverIDs  []string // Both independent human approvers in vote order.
	ApprovedAt   time.Time
	ExpiresAt    time.Time
}

// WAN-117 wires a server-configured, exact GitOps Prometheus YAML adapter.
// Prepare must reconcile by executionID, create only a protected pending MR,
// and never merge or notify. Observe verifies independent human MR review,
// exact committed bytes, and Argo CD sync to that merged revision. It must
// never treat a draft JSON MR as deployable content.
type AlertRuleApplier interface {
	Plan(context.Context, AlertRuleEnableIntent) (AlertRuleDeploymentPlan, error)
	Prepare(context.Context, AlertRuleEnableIntent, AlertRuleApproval, string) (AlertRuleDeploymentReceipt, error)
	Lookup(context.Context, AlertRuleEnableIntent, string) (AlertRuleDeploymentReceipt, bool, error)
	Observe(context.Context, AlertRuleEnableIntent, AlertRuleDeploymentReceipt) (AlertRuleDeploymentObservation, error)
}

type AlertRuleDeploymentReceipt struct {
	ProjectID     int64  `json:"project_id"`
	MR            int64  `json:"mr_iid"`
	URL           string `json:"url"`
	SourceBranch  string `json:"source_branch"`
	TargetBranch  string `json:"target_branch"`
	Path          string `json:"path"`
	ContentDigest string `json:"content_digest"`
	State         string `json:"state"` // Prepare returns opened; read-only recovery may record merged.
	// SourceRevision is the MR head; MergeRevision and MergedAt are the
	// revision a merged MR put on the target branch (D11).
	SourceRevision string     `json:"source_revision,omitempty"`
	MergeRevision  string     `json:"merge_revision,omitempty"`
	MergedAt       *time.Time `json:"merged_at,omitempty"`
}

type AlertRuleDeploymentObservation struct {
	State          string `json:"state"` // pending_review, waiting_sync, synced
	MergeRevision  string `json:"merge_revision,omitempty"`
	SyncedRevision string `json:"synced_revision,omitempty"`
	ContentDigest  string `json:"content_digest,omitempty"`
	// GroupRules are the alert rules of the bound group in the merged file,
	// for the whole-group readback of a file_pull deployment (D11).
	GroupRules []GroupRule `json:"group_rules,omitempty"`
}

type AlertRuleExecutionResult struct {
	ProposalID     string                      `json:"proposal_id"`
	ExecutionID    string                      `json:"execution_id"`
	Status         string                      `json:"status"`
	Deployment     *AlertRuleDeploymentReceipt `json:"deployment,omitempty"`
	ObservedRuleID string                      `json:"observed_rule_id,omitempty"`
}

type enableExecutionRecord struct {
	ProposalID, ID, ActionDigest, ApprovalID, ApprovalDigest, ApproverID, FirstApproverID, State, ObservedRuleID string
	Generation                                                                                                   int64
	LeaseUntil                                                                                                   *time.Time
	Deployment                                                                                                   AlertRuleDeploymentReceipt
}

func validApproval(approval AlertRuleApproval, actionDigest, creator string) bool {
	now := time.Now().UTC()
	return hexID(approval.ID) && len(approval.ID) == 32 && trialFingerprintRE.MatchString(approval.Digest) &&
		approval.ActionDigest == actionDigest && len(approval.ApproverIDs) == 2 &&
		approval.ApproverIDs[0] != "" && approval.ApproverIDs[1] != "" &&
		approval.ApproverIDs[0] != approval.ApproverIDs[1] &&
		approval.ApproverIDs[0] != creator && approval.ApproverIDs[1] != creator &&
		approval.ApproverID == approval.ApproverIDs[1] &&
		!approval.ApprovedAt.IsZero() && !approval.ApprovedAt.After(now) && approval.ExpiresAt.After(now)
}

func (s *WriteService) approvedIntentTx(ctx context.Context, tx pgx.Tx, proposalID string) (AlertRuleEnableIntent, AlertRuleApproval, string, error) {
	var zero AlertRuleEnableIntent
	if s == nil || s.DB == nil || s.Access == nil || s.EnableApproval == nil || s.EnableApplier == nil {
		return zero, AlertRuleApproval{}, "", problem(503, "enable_not_connected", "Alert rule approval or executor is not connected")
	}
	intent, err := LoadAlertRuleEnableIntentWithTx(ctx, tx, proposalID)
	if err != nil {
		return zero, AlertRuleApproval{}, "", err
	}
	var creator, actionDigest, status string
	err = tx.QueryRow(ctx, `SELECT a.creator_id,a.action_digest,p.status FROM v3_proposal_actions a
 JOIN v3_proposals p ON p.id=a.proposal_id
 WHERE a.proposal_id=$1 AND a.action_kind=$2 FOR SHARE OF a,p`, proposalID, alertRuleEnableKind).
		Scan(&creator, &actionDigest, &status)
	if err != nil {
		return zero, AlertRuleApproval{}, "", err
	}
	if status != "approved" {
		return zero, AlertRuleApproval{}, "", problem(409, "approval_required", "A current independent approval is required")
	}
	if _, err := s.enableScopeTx(ctx, tx, core.User{ID: creator}, intent.EnvironmentID, intent.ApplicationID, intent.InvestigationID); err != nil {
		return zero, AlertRuleApproval{}, "", err
	}
	approval, err := s.EnableApproval.VerifyWithTx(ctx, tx, proposalID, actionDigest)
	if err != nil {
		return zero, AlertRuleApproval{}, "", err
	}
	if !validApproval(approval, actionDigest, creator) {
		return zero, AlertRuleApproval{}, "", problem(409, "approval_invalid", "Current independent human approval is unavailable")
	}
	if intent.SchemaVersion == alertRuleEnableIntentDirect {
		return intent, approval, actionDigest, nil
	}
	// Recheck the exact immutable draft and its current version. A later draft
	// edit invalidates the old action rather than silently changing its target.
	var opState, branch, artifactPath, manifestDigest, specHash string
	var payload []byte
	var projectID, mr int64
	err = tx.QueryRow(ctx, `SELECT o.state,o.branch,o.path,o.project_id,COALESCE(o.mr_iid,0),o.payload,r.spec_digest
 FROM v3_alert_rule_draft_operations o
 JOIN v3_alert_rule_draft_revisions r ON r.operation_id=o.id
 JOIN v3_alert_rule_drafts d ON d.id=r.draft_id AND d.current_version=r.version
 WHERE o.id=$1 AND o.operation='enable' AND r.draft_id=$2 AND r.version=$3
 FOR SHARE OF o,r,d`, intent.DraftOperationID, intent.DraftID, intent.DraftVersion).
		Scan(&opState, &branch, &artifactPath, &projectID, &mr, &payload, &specHash)
	if err != nil {
		return zero, AlertRuleApproval{}, "", err
	}
	manifestDigest = enableDigest("aiops.v3.alert_rule.manifest.v1", payload)
	if opState != "pending_review" || branch != intent.Branch || artifactPath != intent.Path ||
		projectID != intent.ProjectID || mr != intent.MR || specHash != intent.SpecDigest || manifestDigest != intent.ManifestDigest {
		return zero, AlertRuleApproval{}, "", problem(409, "enable_draft_changed", "Approved alert rule draft is no longer current")
	}
	return intent, approval, actionDigest, nil
}

func (s *WriteService) approvedIntent(ctx context.Context, proposalID string) (AlertRuleEnableIntent, AlertRuleApproval, string, error) {
	if s == nil || s.DB == nil || s.Access == nil || s.EnableApproval == nil || s.EnableApplier == nil {
		return AlertRuleEnableIntent{}, AlertRuleApproval{}, "", problem(503, "enable_not_connected", "Alert rule approval or executor is not connected")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return AlertRuleEnableIntent{}, AlertRuleApproval{}, "", err
	}
	defer tx.Rollback(ctx)
	intent, approval, digest, err := s.approvedIntentTx(ctx, tx, proposalID)
	if err != nil {
		return AlertRuleEnableIntent{}, AlertRuleApproval{}, "", err
	}
	return intent, approval, digest, tx.Commit(ctx)
}

func (s *WriteService) recheckApprovedSource(ctx context.Context, intent AlertRuleEnableIntent) error {
	if intent.SchemaVersion == alertRuleEnableIntentDirect {
		// Version 2 has no draft: the bound group and the observed rule
		// identity must still be what the request was checked against.
		groups, observed, err := s.inspectCandidates(ctx, intent.EnvironmentID, intent.ApplicationID, []DraftRuleSpec{intent.Spec})
		if err != nil {
			return err
		}
		if fileIdentityDigest(groups[0].file) != intent.ProviderFileIdentityDigest ||
			observed[0].id != intent.ObservedRuleID || observed[0].digest != intent.ObservedDigest {
			return problem(409, "source_changed", "Bound rule source changed since the request")
		}
		return nil
	}
	var op operationRecord
	err := s.DB.QueryRow(ctx, `SELECT id,environment_id,application_id,payload FROM v3_alert_rule_draft_operations
 WHERE id=$1 AND state='pending_review'`, intent.DraftOperationID).
		Scan(&op.ID, &op.EnvironmentID, &op.ApplicationID, &op.Payload)
	if err != nil {
		return err
	}
	return s.recheckManifestSources(ctx, op)
}

func (s *WriteService) sameApprovedPlan(ctx context.Context, intent AlertRuleEnableIntent) error {
	current, err := s.EnableApplier.Plan(ctx, intent)
	if err != nil {
		return problem(503, "rule_applier_unavailable", "Deployment target could not be verified")
	}
	if validateDeploymentPlan(current) != nil || current != intent.Deployment {
		return problem(409, "deployment_plan_changed", "Approved deployment bytes or target changed")
	}
	return nil
}

// validDeploymentReceipt checks an opened MR receipt, or a merged one of an
// executor-merged plan (D11). The MR web URL is GitLab's external URL, which
// an operator's TLS entry may front, so the adapter checks its exact base.
func validDeploymentReceipt(plan AlertRuleDeploymentPlan, executionID string, receipt AlertRuleDeploymentReceipt) bool {
	u, err := url.Parse(receipt.URL)
	merged := plan.ExecutorMerges() && receipt.State == "merged" && deploymentRevisionRE.MatchString(receipt.MergeRevision) &&
		receipt.MergedAt != nil
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.Fragment == "" &&
		(receipt.State == "opened" || merged) && receipt.ProjectID == plan.ProjectID && receipt.MR > 0 &&
		receipt.TargetBranch == plan.TargetBranch && receipt.Path == plan.Path &&
		receipt.ContentDigest == plan.ContentDigest &&
		receipt.SourceBranch == "aiops-alert-rule-enable/"+executionID
}

// ExecuteApproved is called only by a separate worker after WAN-117 approval.
// It creates a pending deployable MR through the trusted adapter; it does not
// merge it and never reports the rule enabled here.
func (s *WriteService) ExecuteApproved(ctx context.Context, proposalID string) (AlertRuleExecutionResult, error) {
	var zero AlertRuleExecutionResult
	if !hexID(proposalID) {
		return zero, problem(404, "not_found", "Proposal not found")
	}
	intent, approval, digest, err := s.approvedIntent(ctx, proposalID)
	if err != nil {
		if result, handled, recoveryErr := s.reconcileEnableExecution(ctx, proposalID); handled {
			return result, recoveryErr
		}
		return zero, err
	}
	record, ready, err := s.claimEnableExecution(ctx, proposalID, intent, approval, digest)
	if err != nil {
		// Approval can expire or be revoked between the first verification and
		// the transactional claim. Recover an existing identity without writing
		// remotely, while leaving ordinary live-approval claim errors unchanged.
		if _, _, _, approvalErr := s.approvedIntent(ctx, proposalID); approvalErr != nil {
			if result, handled, recoveryErr := s.reconcileEnableExecution(ctx, proposalID); handled {
				return result, recoveryErr
			}
		}
		return zero, err
	}
	if ready {
		return executionResult(record), nil
	}
	var merger alertRuleMerger
	if intent.Deployment.ExecutorMerges() {
		var ok bool
		if merger, ok = s.EnableApplier.(alertRuleMerger); !ok {
			if markErr := s.markEnableExecution(ctx, record, "uncertain", nil, "merge_unavailable"); markErr != nil {
				return zero, markErr
			}
			return zero, problem(503, "enable_not_connected", "Alert rule executor cannot merge")
		}
		// D11: a merge this execution already made is its result. The target
		// has moved past the approved base, so the plan checks below no longer
		// hold; the lookup verifies the merged bytes instead. Any other lookup
		// result goes through those checks, Prepare and Merge as usual.
		receipt, found, err := s.EnableApplier.Lookup(ctx, intent, record.ID)
		if err == nil && found && receipt.State == "merged" {
			if !validDeploymentReceipt(intent.Deployment, record.ID, receipt) {
				if markErr := s.markEnableExecution(ctx, record, "stale_remote", nil, "untrusted_remote_receipt"); markErr != nil {
					return zero, markErr
				}
				return zero, problem(409, "deployment_requires_review", "Remote deployment identity requires human review")
			}
			if err := s.markEnableExecution(ctx, record, "merged", &receipt, "deployment_merged"); err != nil {
				return zero, err
			}
			record.State, record.Deployment = "merged", receipt
			return executionResult(record), nil
		}
	}
	// Claim the durable remote identity first. After a failed state write, the
	// old MR may already exist; mutable source drift must still reach same-ID
	// read-only reconciliation and an explicit human-review terminal state.
	if err := s.recheckApprovedSource(ctx, intent); err != nil {
		return zero, s.staleBeforePrepare(ctx, record, intent, "source_changed_before_prepare", err)
	}
	if err := s.sameApprovedPlan(ctx, intent); err != nil {
		return zero, s.staleBeforePrepare(ctx, record, intent, "plan_changed_before_prepare", err)
	}
	// The adapter must reconcile an uncertain prior result under this exact
	// execution ID; a timeout must never allocate another remote identity.
	receipt, err := s.EnableApplier.Prepare(ctx, intent, approval, record.ID)
	if err != nil || !validDeploymentReceipt(intent.Deployment, record.ID, receipt) {
		if markErr := s.markEnableExecution(ctx, record, "uncertain", nil, "prepare_unknown"); markErr != nil {
			return zero, markErr
		}
		return zero, problem(503, "rule_prepare_unknown", "Deployment MR result is uncertain; retry the same execution")
	}
	if err := s.recheckApprovedSource(ctx, intent); err != nil {
		if markErr := s.markEnableExecution(ctx, record, "stale_remote", &receipt, "source_changed_after_prepare"); markErr != nil {
			return zero, markErr
		}
		return zero, err
	}
	if err := s.sameApprovedPlan(ctx, intent); err != nil {
		if markErr := s.markEnableExecution(ctx, record, "stale_remote", &receipt, "plan_changed_after_prepare"); markErr != nil {
			return zero, markErr
		}
		return zero, err
	}
	if _, currentApproval, currentDigest, err := s.approvedIntent(ctx, proposalID); err != nil ||
		currentDigest != digest || currentApproval.ID != approval.ID || currentApproval.Digest != approval.Digest ||
		!reflect.DeepEqual(currentApproval.ApproverIDs, approval.ApproverIDs) {
		if markErr := s.markEnableExecution(ctx, record, "stale_remote", &receipt, "approval_or_access_changed_after_prepare"); markErr != nil {
			return zero, markErr
		}
		if err != nil {
			return zero, err
		}
		return zero, problem(409, "approval_changed", "Approval evidence changed after deployment preparation")
	}
	if merger == nil {
		if err := s.markEnableExecution(ctx, record, "pending_review", &receipt, "deployment_mr_opened"); err != nil {
			return zero, err
		}
		record.State, record.Deployment = "pending_review", receipt
		return executionResult(record), nil
	}
	// D11: the approval covers the executor merging exactly this head.
	if err := s.noteEnableMerging(ctx, record, receipt); err != nil {
		return zero, err
	}
	merged, err := merger.Merge(ctx, intent, receipt, record.ID)
	if code, refused := MergeRefusal(err); refused {
		if markErr := s.markEnableExecution(ctx, record, "stale_remote", &receipt, code); markErr != nil {
			return zero, markErr
		}
		return zero, problem(409, "deployment_requires_review", "The deployment MR was not merged: "+code)
	}
	if err != nil || merged.State != "merged" || !validDeploymentReceipt(intent.Deployment, record.ID, merged) {
		if markErr := s.markEnableExecution(ctx, record, "uncertain", &receipt, "merge_unknown"); markErr != nil {
			return zero, markErr
		}
		return zero, problem(503, "rule_merge_unknown", "Deployment merge result is uncertain; retry the same execution")
	}
	if err := s.markEnableExecution(ctx, record, "merged", &merged, "deployment_merged"); err != nil {
		return zero, err
	}
	record.State, record.Deployment = "merged", merged
	return executionResult(record), nil
}

func (s *WriteService) staleBeforePrepare(ctx context.Context, record enableExecutionRecord,
	intent AlertRuleEnableIntent, reason string, cause error) error {
	// Lookup must inspect only the deterministic branch for executionID and
	// never create another MR. A lookup outage keeps the ledger uncertain.
	receipt, found, err := s.EnableApplier.Lookup(ctx, intent, record.ID)
	if err != nil {
		if markErr := s.markEnableExecution(ctx, record, "uncertain", nil, "reconciliation_unavailable"); markErr != nil {
			return markErr
		}
		return problem(503, "deployment_reconciliation_unavailable", "Existing deployment result is uncertain")
	}
	if found {
		if !validDeploymentReceipt(intent.Deployment, record.ID, receipt) {
			if markErr := s.markEnableExecution(ctx, record, "stale_remote", nil, "untrusted_remote_receipt"); markErr != nil {
				return markErr
			}
			return problem(409, "deployment_requires_review", "Remote deployment identity requires human review")
		}
		if markErr := s.markEnableExecution(ctx, record, "stale_remote", &receipt, reason); markErr != nil {
			return markErr
		}
	} else if markErr := s.markEnableExecution(ctx, record, "stale_remote", nil, reason); markErr != nil {
		return markErr
	}
	return cause
}

func (s *WriteService) claimEnableExecution(ctx context.Context, proposalID string, intent AlertRuleEnableIntent,
	approval AlertRuleApproval, digest string) (enableExecutionRecord, bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return enableExecutionRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	_, currentApproval, currentDigest, err := s.approvedIntentTx(ctx, tx, proposalID)
	if err != nil {
		return enableExecutionRecord{}, false, err
	}
	if currentDigest != digest || currentApproval.ID != approval.ID || currentApproval.Digest != approval.Digest ||
		!reflect.DeepEqual(currentApproval.ApproverIDs, approval.ApproverIDs) {
		return enableExecutionRecord{}, false, problem(409, "approval_changed", "Approval evidence changed before execution")
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_alert_rule_enable_executions
 (proposal_id,execution_id,action_digest,approval_id,approval_digest,first_approver_id,approver_id,state,steps)
 VALUES($1,$2,$3,$4,$5,$6,$7,'reserved',jsonb_build_array(jsonb_build_object('state','reserved','at',clock_timestamp())))
 ON CONFLICT(proposal_id) DO NOTHING`,
		proposalID, approval.ID, digest, approval.ID, approval.Digest,
		approval.ApproverIDs[0], approval.ApproverIDs[1])
	if err != nil {
		return enableExecutionRecord{}, false, err
	}
	var record enableExecutionRecord
	var deploymentRaw []byte
	err = tx.QueryRow(ctx, `SELECT proposal_id,execution_id,action_digest,approval_id,approval_digest,
 COALESCE(first_approver_id,''),approver_id,
 state,generation,lease_until,deployment,observed_rule_id FROM v3_alert_rule_enable_executions
 WHERE proposal_id=$1 FOR UPDATE`, proposalID).
		Scan(&record.ProposalID, &record.ID, &record.ActionDigest, &record.ApprovalID, &record.ApprovalDigest,
			&record.FirstApproverID, &record.ApproverID, &record.State, &record.Generation,
			&record.LeaseUntil, &deploymentRaw, &record.ObservedRuleID)
	if err != nil {
		return record, false, err
	}
	if record.ActionDigest != digest || record.ApprovalID != approval.ID || record.ApprovalDigest != approval.Digest ||
		record.ID != approval.ID || record.FirstApproverID != approval.ApproverIDs[0] ||
		record.ApproverID != approval.ApproverIDs[1] {
		return record, false, problem(409, "approval_changed", "Approval evidence changed")
	}
	if err := json.Unmarshal(deploymentRaw, &record.Deployment); err != nil {
		return record, false, problem(503, "execution_invalid", "Execution record is invalid")
	}
	if enableExecutionSettled(record.State) {
		if !validRecoveryReceipt(intent.Deployment, record.ID, record.Deployment) {
			return record, false, problem(503, "execution_invalid", "Deployment MR record is invalid")
		}
		return record, true, tx.Commit(ctx)
	}
	if record.State == "stale_remote" {
		return record, false, problem(409, "deployment_requires_review", "Remote deployment MR requires human review")
	}
	if record.State == "exporting" && record.LeaseUntil != nil && record.LeaseUntil.After(time.Now().UTC()) {
		return record, false, problem(409, "execution_busy", "Deployment preparation is in progress")
	}
	err = tx.QueryRow(ctx, `UPDATE v3_alert_rule_enable_executions
 SET state='exporting',generation=generation+1,lease_until=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp(),
 steps=`+enableStepSQL("'committing'")+`
 WHERE proposal_id=$1 RETURNING generation`, proposalID).Scan(&record.Generation)
	if err != nil {
		return record, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule.enable.prepare',$3,'pending',jsonb_build_object(
 'execution_id',$4::text,'generation',$5::bigint,'approval_id',$6::text,
 'first_approver_id',$7::text))`,
		approval.ApproverID, intent.ApplicationID, proposalID, record.ID,
		record.Generation, approval.ID, approval.ApproverIDs[0])
	if err != nil {
		return record, false, err
	}
	return record, false, tx.Commit(ctx)
}

// enableExecutionSettled names states whose remote result is recorded: the
// claim returns them as they are and only observation moves them on.
func enableExecutionSettled(state string) bool {
	return state == "pending_review" || state == "merged" || state == "awaiting_provider" || state == "enabled" || state == "failed"
}

// enableStepSQL appends a timeline step (D11) unless the last step already
// names it; the timeline keeps at most 64 steps. step is a SQL text value.
func enableStepSQL(step string) string {
	return `CASE WHEN jsonb_array_length(steps)>=64 OR steps->-1->>'state'=` + step + ` THEN steps
 ELSE steps||jsonb_build_array(jsonb_build_object('state',` + step + `::text,'at',clock_timestamp())) END`
}

// enableStep is the timeline name of an execution state (D11 §11.5).
func enableStep(state string) string {
	switch state {
	case "exporting":
		return "committing"
	case "awaiting_provider":
		return "loading"
	case "stale_remote":
		return "needs_attention"
	}
	return state
}

// enableFailure is the failure code a transition reason records; an empty
// code clears it. Merge refusals use their code as the reason.
func enableFailure(reason string) string {
	switch reason {
	case "source_changed_before_prepare", "source_changed_after_prepare":
		return "source_changed"
	case "plan_changed_before_prepare", "plan_changed_after_prepare":
		return "plan_changed"
	case "approval_or_access_changed_after_prepare", "approval_unavailable_mr_not_found", "approval_unavailable_mr_opened":
		return "approval_invalid"
	case "untrusted_remote_receipt":
		return "branch_conflict"
	case "stale_base", "branch_conflict", "mr_closed", "protection_changed", "merge_refused", "credential_rejected":
		return reason
	}
	return ""
}

// alertRuleMerger merges the MR of an executor-merged plan (D11).
type alertRuleMerger interface {
	Merge(context.Context, AlertRuleEnableIntent, AlertRuleDeploymentReceipt, string) (AlertRuleDeploymentReceipt, error)
}

func (s *WriteService) markEnableExecution(ctx context.Context, record enableExecutionRecord,
	state string, receipt *AlertRuleDeploymentReceipt, reason string) error {
	if state != "uncertain" && state != "pending_review" && state != "stale_remote" && state != "merged" {
		return errors.New("invalid execution transition")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var payload []byte
	var err error
	if receipt != nil {
		payload, err = json.Marshal(receipt)
		if err != nil {
			return err
		}
	} else {
		payload = []byte(`{}`)
	}
	tx, err := s.DB.Begin(cleanup)
	if err != nil {
		return err
	}
	defer tx.Rollback(cleanup)
	tag, err := tx.Exec(cleanup, `UPDATE v3_alert_rule_enable_executions SET state=$3,
 deployment=$4::jsonb,lease_until=NULL,updated_at=clock_timestamp(),failure_code=$5,steps=`+enableStepSQL("$6")+`
 WHERE proposal_id=$1 AND generation=$2 AND state='exporting'`,
		record.ProposalID, record.Generation, state, payload, enableFailure(reason), enableStep(state))
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return err
		}
		return problem(409, "execution_conflict", "Execution lease changed")
	}
	_, err = tx.Exec(cleanup, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 SELECT approver_id,p.application_id,'v3.alert_rule.enable.execution_state',proposal_id,$2,
 jsonb_build_object('reason',$3::text,'generation',$4::bigint,'execution_id',execution_id)
 FROM v3_alert_rule_enable_executions x JOIN v3_proposals p ON p.id=x.proposal_id
 WHERE x.proposal_id=$1`, record.ProposalID, state, reason, record.Generation)
	if err != nil {
		return err
	}
	return tx.Commit(cleanup)
}

// noteEnableMerging records the opened MR of an executor-merged plan before
// the merge, renewing the lease; the execution stays exporting (D11).
func (s *WriteService) noteEnableMerging(ctx context.Context, record enableExecutionRecord, receipt AlertRuleDeploymentReceipt) error {
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE v3_alert_rule_enable_executions SET deployment=$3::jsonb,
 lease_until=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp(),steps=`+enableStepSQL("'merging'")+`
 WHERE proposal_id=$1 AND generation=$2 AND state='exporting'`, record.ProposalID, record.Generation, payload)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return problem(409, "execution_conflict", "Execution lease changed")
	}
	return nil
}

func executionResult(record enableExecutionRecord) AlertRuleExecutionResult {
	result := AlertRuleExecutionResult{ProposalID: record.ProposalID, ExecutionID: record.ID,
		Status: record.State, ObservedRuleID: record.ObservedRuleID}
	if record.Deployment.MR > 0 {
		result.Deployment = &record.Deployment
	}
	return result
}

// ObserveEnabled is the independent readback phase. A merged MR or GitOps
// sync alone does not establish an active rule: only an exact, unique rule in
// the bound Prometheus group can transition the ledger to enabled.
func (s *WriteService) ObserveEnabled(ctx context.Context, proposalID string) (AlertRuleExecutionResult, error) {
	var zero AlertRuleExecutionResult
	// Observation is read-only reconciliation of a previously authorized
	// deployment. Revocation or investigation closure after deployment cannot
	// make the running Prometheus rule disappear from our state ledger.
	intent, actionDigest, err := s.observationIntent(ctx, proposalID)
	if err != nil {
		return zero, err
	}
	var record enableExecutionRecord
	var raw, readbackRaw []byte
	err = s.DB.QueryRow(ctx, `SELECT execution_id,action_digest,COALESCE(first_approver_id,''),approver_id,
 state,deployment,observed_rule_id,readback
 FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, proposalID).
		Scan(&record.ID, &record.ActionDigest, &record.FirstApproverID, &record.ApproverID,
			&record.State, &raw, &record.ObservedRuleID, &readbackRaw)
	if err != nil {
		return zero, err
	}
	record.ProposalID = proposalID
	var previous enableReadback
	if record.ActionDigest != actionDigest || json.Unmarshal(raw, &record.Deployment) != nil ||
		!validRecoveryReceipt(intent.Deployment, record.ID, record.Deployment) ||
		record.FirstApproverID == "" || record.FirstApproverID == record.ApproverID ||
		json.Unmarshal(readbackRaw, &previous) != nil {
		return zero, problem(503, "execution_invalid", "Deployment receipt is invalid")
	}
	if intent.Deployment.ExecutorMerges() {
		return s.readBackMerged(ctx, intent, actionDigest, record, raw, previous)
	}
	if record.State != "pending_review" && record.State != "awaiting_provider" && record.State != "enabled" {
		return zero, problem(409, "execution_not_observable", "Deployment is not ready for observation")
	}
	// Adapters historically accept the original opened creation receipt and
	// independently re-read the current MR state. Recovery may retain merged
	// as factual evidence, so normalize only this read-only adapter argument.
	// The adapter still GETs the real MR and verifies review, merge and sync;
	// this does not change the remote state or skip factual observation.
	observationReceipt := record.Deployment
	observationReceipt.State = "opened"
	observation, err := s.EnableApplier.Observe(ctx, intent, observationReceipt)
	if err != nil {
		return zero, problem(503, "deployment_observation_unavailable", "Deployment state is unavailable")
	}
	if observation.State == "pending_review" || observation.State == "waiting_sync" {
		if record.State == "enabled" {
			return zero, problem(409, "deployment_drift", "Previously enabled deployment no longer has confirmed sync")
		}
		record.State = observation.State
		return executionResult(record), nil
	}
	if observation.State != "synced" || !deploymentRevisionRE.MatchString(observation.MergeRevision) ||
		observation.MergeRevision != observation.SyncedRevision || observation.ContentDigest != intent.Deployment.ContentDigest {
		return zero, problem(503, "deployment_observation_invalid", "Exact reviewed deployment and sync could not be verified")
	}
	observedID, matched, err := s.observeEnabledRule(ctx, intent)
	if err != nil {
		return zero, err
	}
	state := "awaiting_provider"
	if matched {
		state = "enabled"
	}
	if record.State == "enabled" {
		if !matched || record.ObservedRuleID != observedID {
			return zero, problem(409, "rule_content_changed", "Previously enabled rule is no longer active with approved content")
		}
		return executionResult(record), nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	storedIntent, loadErr := loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
	if loadErr != nil || !reflect.DeepEqual(storedIntent, intent) {
		return zero, problem(409, "enable_intent_changed", "Deployment action changed during observation")
	}
	var lockedActionDigest string
	var lockedDeployment []byte
	if err := tx.QueryRow(ctx, `SELECT action_digest,deployment FROM v3_alert_rule_enable_executions
 WHERE proposal_id=$1 AND execution_id=$2 FOR UPDATE`, proposalID, record.ID).
		Scan(&lockedActionDigest, &lockedDeployment); err != nil {
		return zero, err
	}
	if lockedActionDigest != actionDigest || string(lockedDeployment) != string(raw) {
		return zero, problem(409, "execution_conflict", "Deployment identity changed during observation")
	}
	tag, err := tx.Exec(ctx, `UPDATE v3_alert_rule_enable_executions SET state=$2,observed_rule_id=$3,
 updated_at=clock_timestamp() WHERE proposal_id=$1 AND state IN ('pending_review','awaiting_provider')`,
		proposalID, state, observedID)
	if err != nil || tag.RowsAffected() != 1 {
		if err != nil {
			return zero, err
		}
		return zero, problem(409, "execution_conflict", "Deployment state changed")
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 SELECT approver_id,p.application_id,'v3.alert_rule.enable.observed',proposal_id,$2,
 jsonb_build_object('observed_rule_id',$3::text,'merge_revision',$4::text,'content_digest',$5::text)
 FROM v3_alert_rule_enable_executions x JOIN v3_proposals p ON p.id=x.proposal_id
 WHERE x.proposal_id=$1`, proposalID, state, observedID, observation.MergeRevision, observation.ContentDigest)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	record.State, record.ObservedRuleID = state, observedID
	return executionResult(record), nil
}

func (s *WriteService) observationIntent(ctx context.Context, proposalID string) (AlertRuleEnableIntent, string, error) {
	var zero AlertRuleEnableIntent
	if s == nil || s.DB == nil || s.Reader == nil || s.Reader.Source == nil || s.EnableApplier == nil {
		return zero, "", problem(503, "enable_not_connected", "Alert rule observation is unavailable")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return zero, "", err
	}
	defer tx.Rollback(ctx)
	intent, err := loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
	if err != nil {
		return zero, "", err
	}
	var digest string
	if err := tx.QueryRow(ctx, `SELECT action_digest FROM v3_proposal_actions
 WHERE proposal_id=$1 AND action_kind=$2 FOR SHARE`, proposalID, alertRuleEnableKind).Scan(&digest); err != nil {
		return zero, "", err
	}
	return intent, digest, tx.Commit(ctx)
}

// enableReadback is the last Prometheus readback of a file_pull deployment
// (D11 §11.5): pending, loaded, mismatch, error or timeout.
type enableReadback struct {
	State            string     `json:"state,omitempty"`
	CheckedAt        *time.Time `json:"checked_at,omitempty"`
	FirstSeenAt      *time.Time `json:"first_seen_at,omitempty"`
	Health           string     `json:"health,omitempty"`
	LastEvaluationAt *time.Time `json:"last_evaluation_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

// readBackMerged observes an executor-merged deployment (D11). The target
// must still hold the merged bytes; Prometheus has no revision, so the whole
// bound group must equal the merged file's group, and the new rule must be
// exact, evaluated after the merge and healthy. A rule that evaluates with an
// error is failed: it runs, and only a disable removes it. A load slower than
// the approved timeout needs attention, and later readbacks may still find it.
func (s *WriteService) readBackMerged(ctx context.Context, intent AlertRuleEnableIntent, actionDigest string,
	record enableExecutionRecord, raw []byte, previous enableReadback) (AlertRuleExecutionResult, error) {
	var zero AlertRuleExecutionResult
	if record.State != "merged" && record.State != "awaiting_provider" && record.State != "enabled" {
		return zero, problem(409, "execution_not_observable", "Deployment is not ready for observation")
	}
	merged := record.Deployment
	if merged.State != "merged" || merged.MergedAt == nil || !validDeploymentReceipt(intent.Deployment, record.ID, merged) {
		return zero, problem(503, "execution_invalid", "Deployment receipt is invalid")
	}
	observation, err := s.EnableApplier.Observe(ctx, intent, merged)
	if err != nil {
		return zero, problem(503, "deployment_observation_unavailable", "Deployment state is unavailable")
	}
	if observation.State != "synced" || observation.MergeRevision != merged.MergeRevision ||
		observation.ContentDigest != intent.Deployment.ContentDigest || len(observation.GroupRules) == 0 {
		return zero, problem(503, "deployment_observation_invalid", "Merged deployment could not be verified")
	}
	readback, observedID, err := s.readBackGroup(ctx, intent, observation.GroupRules, *merged.MergedAt)
	if err != nil {
		return zero, err
	}
	now := time.Now().UTC()
	readback.CheckedAt, readback.FirstSeenAt = &now, previous.FirstSeenAt
	if observedID != "" && readback.FirstSeenAt == nil {
		readback.FirstSeenAt = &now
	}
	state, failure := "awaiting_provider", ""
	switch readback.State {
	case "loaded":
		state = "enabled"
	case "error":
		state, failure = "failed", "rule_health_error"
	default:
		if now.After(merged.MergedAt.Add(time.Duration(intent.Deployment.ReadbackTimeoutSeconds) * time.Second)) {
			readback.State, failure = "timeout", "load_timeout"
		}
	}
	if record.State == "enabled" {
		if state != "enabled" || record.ObservedRuleID != observedID {
			return zero, problem(409, "rule_content_changed", "Previously enabled rule is no longer active with approved content")
		}
		return executionResult(record), nil
	}
	readbackJSON, err := json.Marshal(readback)
	if err != nil {
		return zero, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	storedIntent, loadErr := loadAlertRuleEnableIntentWithTx(ctx, tx, record.ProposalID, false)
	if loadErr != nil || !reflect.DeepEqual(storedIntent, intent) {
		return zero, problem(409, "enable_intent_changed", "Deployment action changed during observation")
	}
	var lockedActionDigest, lockedState string
	var lockedDeployment []byte
	if err := tx.QueryRow(ctx, `SELECT action_digest,deployment,state FROM v3_alert_rule_enable_executions
 WHERE proposal_id=$1 AND execution_id=$2 FOR UPDATE`, record.ProposalID, record.ID).
		Scan(&lockedActionDigest, &lockedDeployment, &lockedState); err != nil {
		return zero, err
	}
	if lockedActionDigest != actionDigest || string(lockedDeployment) != string(raw) || lockedState != record.State {
		return zero, problem(409, "execution_conflict", "Deployment identity changed during observation")
	}
	if _, err := tx.Exec(ctx, `UPDATE v3_alert_rule_enable_executions SET state=$2,observed_rule_id=$3,readback=$4::jsonb,
 failure_code=$5,steps=`+enableStepSQL("$6")+`,updated_at=clock_timestamp()
 WHERE proposal_id=$1`, record.ProposalID, state, observedID, readbackJSON, failure, enableStep(state)); err != nil {
		return zero, err
	}
	if state != record.State {
		_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 SELECT approver_id,p.application_id,'v3.alert_rule.enable.observed',proposal_id,$2,
 jsonb_build_object('observed_rule_id',$3::text,'merge_revision',$4::text,'content_digest',$5::text,'readback',$6::text)
 FROM v3_alert_rule_enable_executions x JOIN v3_proposals p ON p.id=x.proposal_id
 WHERE x.proposal_id=$1`, record.ProposalID, state, observedID, observation.MergeRevision, observation.ContentDigest, readback.State)
		if err != nil {
			return zero, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	record.State, record.ObservedRuleID = state, observedID
	return executionResult(record), nil
}

// readBackGroup compares the bound Prometheus group with the merged file's
// group. Expressions are compared for the new rule only: Prometheus reports
// a normalized expression, which a hand-written rule's text may not equal.
func (s *WriteService) readBackGroup(ctx context.Context, intent AlertRuleEnableIntent, rules []GroupRule,
	mergedAt time.Time) (enableReadback, string, error) {
	group, err := s.resolveGroup(intent.EnvironmentID, intent.ApplicationID, intent.Spec.Group)
	if err != nil || fileIdentityDigest(group.file) != intent.ProviderFileIdentityDigest {
		return enableReadback{}, "", problem(409, "rule_binding_changed", "Rule group binding changed")
	}
	items, err := s.Reader.Source.List(ctx, intent.EnvironmentID)
	if err != nil {
		return enableReadback{}, "", problem(503, "rule_source_unavailable", "Prometheus rule source is unavailable")
	}
	identity := group.file + "\x00" + intent.Spec.Group
	var loaded []ObservedRule
	var found *ObservedRule
	for i := range items {
		if items[i].GroupIdentity != identity {
			continue
		}
		loaded = append(loaded, items[i])
		if items[i].Name == intent.Spec.Name {
			if found != nil {
				return enableReadback{}, "", problem(409, "rule_identity_conflict", "Observed rule identity is ambiguous")
			}
			found = &items[i]
		}
	}
	if found == nil {
		return enableReadback{State: "pending"}, "", nil
	}
	if err := approvedRuleMatches(*found, intent); err != nil {
		return enableReadback{}, "", err
	}
	readback := enableReadback{Health: found.Health, LastError: found.LastError}
	evaluated := found.LastEvaluation.After(mergedAt)
	if !found.LastEvaluation.IsZero() {
		at := found.LastEvaluation.UTC()
		readback.LastEvaluationAt = &at
	}
	switch {
	case found.Health == "err" && evaluated:
		readback.State = "error"
	case !sameLoadedGroup(loaded, rules):
		readback.State = "mismatch"
	case found.Health == "ok" && evaluated:
		readback.State = "loaded"
	default:
		readback.State = "pending"
	}
	return readback, toDetail(intent.EnvironmentID, intent.ApplicationID, *found).ID, nil
}

func sameLoadedGroup(loaded []ObservedRule, rules []GroupRule) bool {
	if len(loaded) != len(rules) {
		return false
	}
	byName := make(map[string]ObservedRule, len(loaded))
	for _, rule := range loaded {
		byName[rule.Name] = rule
	}
	for _, rule := range rules {
		got, ok := byName[rule.Name]
		if !ok || got.Duration != rule.ForSeconds || !maps.Equal(got.Labels, rule.Labels) || !maps.Equal(got.Annotations, rule.Annotations) {
			return false
		}
	}
	return len(byName) == len(rules)
}

func approvedRuleMatches(found ObservedRule, intent AlertRuleEnableIntent) error {
	forDuration, err := time.ParseDuration(intent.Spec.For)
	if err != nil {
		return problem(503, "execution_invalid", "Approved rule duration is invalid")
	}
	if found.Expression != intent.Spec.Expr || found.Duration != forDuration.Seconds() ||
		!maps.Equal(found.Labels, intent.Spec.Labels) || !maps.Equal(found.Annotations, intent.Spec.Annotations) ||
		found.Labels["environment"] != intent.EnvironmentID {
		return problem(409, "rule_content_changed", "Observed rule differs from approved bytes")
	}
	return nil
}

func (s *WriteService) observeEnabledRule(ctx context.Context, intent AlertRuleEnableIntent) (string, bool, error) {
	group, err := s.resolveGroup(intent.EnvironmentID, intent.ApplicationID, intent.Spec.Group)
	if err != nil || fileIdentityDigest(group.file) != intent.ProviderFileIdentityDigest {
		return "", false, problem(409, "rule_binding_changed", "Rule group binding changed")
	}
	items, err := s.Reader.Source.List(ctx, intent.EnvironmentID)
	if err != nil {
		return "", false, problem(503, "rule_source_unavailable", "Prometheus rule source is unavailable")
	}
	var found *ObservedRule
	for i := range items {
		item := &items[i]
		if item.GroupIdentity != group.file+"\x00"+intent.Spec.Group || item.Name != intent.Spec.Name {
			continue
		}
		if found != nil {
			return "", false, problem(409, "rule_identity_conflict", "Observed rule identity is ambiguous")
		}
		found = item
	}
	if found == nil {
		return "", false, nil
	}
	if err := approvedRuleMatches(*found, intent); err != nil {
		return "", false, err
	}
	return toDetail(intent.EnvironmentID, intent.ApplicationID, *found).ID, true, nil
}

func (p AlertRuleDeploymentPlan) String() string {
	return fmt.Sprintf("project %d %s@%s", p.ProjectID, p.Path, p.BaseRevision)
}
