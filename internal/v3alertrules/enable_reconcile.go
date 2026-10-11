package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// A reconciler may additionally identify an already merged MR. Prepare's
// Lookup contract still accepts only an opened MR, so recovery cannot loosen
// the checks that precede a new remote write.
type alertRuleRecoveryLookup interface {
	LookupForReconciliation(context.Context, AlertRuleEnableIntent, string) (AlertRuleDeploymentReceipt, bool, error)
}

func validRecoveryReceipt(plan AlertRuleDeploymentPlan, executionID string, receipt AlertRuleDeploymentReceipt) bool {
	if receipt.State != "opened" && receipt.State != "merged" {
		return false
	}
	receipt.State = "opened"
	return validDeploymentReceipt(plan, executionID, receipt)
}

// reconcileEnableExecution never allocates an execution or calls Plan,
// Prepare or Observe. The immutable action and original approval identity are
// sufficient to inspect an old side effect; current authorization is required
// only for a new side effect. Unknown lookup results remain retryable, while
// a matching result resumes read-only observation. Lookup alone cannot promote
// a rule to enabled; missing or untrusted results require human review.
func (s *WriteService) reconcileEnableExecution(ctx context.Context, proposalID string) (AlertRuleExecutionResult, bool, error) {
	var zero AlertRuleExecutionResult
	if s == nil || s.DB == nil || s.EnableApplier == nil {
		return zero, false, nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return zero, true, err
	}
	defer tx.Rollback(ctx)
	intent, err := loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
	if err != nil {
		return zero, true, err
	}
	var record enableExecutionRecord
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT proposal_id,execution_id,action_digest,approval_id,approval_digest,
 COALESCE(first_approver_id,''),approver_id,state,generation,lease_until,deployment,observed_rule_id
 FROM v3_alert_rule_enable_executions WHERE proposal_id=$1 FOR UPDATE`, proposalID).
		Scan(&record.ProposalID, &record.ID, &record.ActionDigest, &record.ApprovalID, &record.ApprovalDigest,
			&record.FirstApproverID, &record.ApproverID, &record.State, &record.Generation,
			&record.LeaseUntil, &raw, &record.ObservedRuleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, true, err
	}
	var digest, creator, executionID, approvalDigest, firstApprover, secondApprover string
	err = tx.QueryRow(ctx, `SELECT a.action_digest,a.creator_id,d.execution_id,d.approval_digest,
 v1.approver_id,v2.approver_id
 FROM v3_proposal_actions a JOIN v3_proposal_approval_decisions d ON d.proposal_id=a.proposal_id
 JOIN v3_proposal_approval_votes v1 ON v1.id=d.first_vote_id
 JOIN v3_proposal_approval_votes v2 ON v2.id=d.second_vote_id
 WHERE a.proposal_id=$1 AND d.action_digest=a.action_digest AND d.action_kind=a.action_kind
 AND d.review_content_digest=a.review_content_digest AND d.creator_id=a.creator_id
 AND d.environment_id=a.environment_id AND d.application_id=a.application_id
 AND v1.proposal_id=a.proposal_id AND v2.proposal_id=a.proposal_id
 AND v1.action_digest=a.action_digest AND v2.action_digest=a.action_digest
 AND v1.review_content_digest=a.review_content_digest AND v2.review_content_digest=a.review_content_digest`, proposalID).
		Scan(&digest, &creator, &executionID, &approvalDigest, &firstApprover, &secondApprover)
	if err != nil {
		return zero, true, err
	}
	if !hexID(record.ID) || len(record.ID) != 32 || record.ID != executionID || record.ApprovalID != record.ID ||
		record.ActionDigest != digest || record.ApprovalDigest != approvalDigest ||
		!trialFingerprintRE.MatchString(record.ApprovalDigest) || record.FirstApproverID == "" || record.ApproverID == "" ||
		record.FirstApproverID != firstApprover || record.ApproverID != secondApprover ||
		record.FirstApproverID == record.ApproverID || record.FirstApproverID == creator || record.ApproverID == creator ||
		json.Unmarshal(raw, &record.Deployment) != nil {
		return zero, true, problem(503, "execution_invalid", "Original deployment identity is invalid")
	}
	if record.State == "pending_review" || record.State == "awaiting_provider" || record.State == "enabled" {
		if !validRecoveryReceipt(intent.Deployment, record.ID, record.Deployment) {
			return zero, true, problem(503, "execution_invalid", "Deployment MR record is invalid")
		}
		return executionResult(record), true, tx.Commit(ctx)
	}
	if record.State == "stale_remote" {
		return zero, true, problem(409, "deployment_requires_review", "Remote deployment result requires human review")
	}
	if record.State != "reserved" && record.State != "uncertain" && record.State != "exporting" {
		return zero, true, problem(503, "execution_invalid", "Deployment state is invalid")
	}
	if record.State == "exporting" && record.LeaseUntil != nil && record.LeaseUntil.After(time.Now().UTC()) {
		return zero, true, problem(409, "execution_busy", "Deployment preparation is in progress")
	}
	err = tx.QueryRow(ctx, `UPDATE v3_alert_rule_enable_executions
 SET state='exporting',generation=generation+1,lease_until=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp()
 WHERE proposal_id=$1 RETURNING generation`, proposalID).Scan(&record.Generation)
	if err != nil {
		return zero, true, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule.enable.reconcile',$3,'pending',jsonb_build_object(
 'execution_id',$4::text,'generation',$5::bigint,'approval_id',$6::text,
 'first_approver_id',$7::text,'reason','approval_or_access_unavailable'))`,
		record.ApproverID, intent.ApplicationID, proposalID, record.ID, record.Generation,
		record.ApprovalID, record.FirstApproverID)
	if err != nil {
		return zero, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, true, err
	}
	lookup := s.EnableApplier.Lookup
	if recovery, ok := s.EnableApplier.(alertRuleRecoveryLookup); ok {
		lookup = recovery.LookupForReconciliation
	}
	receipt, found, err := lookup(ctx, intent, record.ID)
	if err != nil {
		if markErr := s.markEnableExecution(ctx, record, "uncertain", nil, "reconciliation_unavailable"); markErr != nil {
			return zero, true, markErr
		}
		return zero, true, problem(503, "deployment_reconciliation_unavailable", "Existing deployment result is uncertain")
	}
	reason := "approval_unavailable_mr_not_found"
	state := "stale_remote"
	var retained *AlertRuleDeploymentReceipt
	if found {
		if !validRecoveryReceipt(intent.Deployment, record.ID, receipt) {
			if markErr := s.markEnableExecution(ctx, record, "stale_remote", nil, "untrusted_remote_receipt"); markErr != nil {
				return zero, true, markErr
			}
			return zero, true, problem(409, "deployment_requires_review", "Remote deployment identity requires human review")
		}
		retained, reason, state = &receipt, "approval_unavailable_remote_found", "pending_review"
	}
	if err := s.markEnableExecution(ctx, record, state, retained, reason); err != nil {
		return zero, true, err
	}
	record.State = state
	record.Deployment = AlertRuleDeploymentReceipt{}
	if retained != nil {
		record.Deployment = *retained
	}
	return executionResult(record), true, nil
}

// LookupForReconciliation performs only GETs against the original deterministic
// branch. An already merged MR is evidence of an old effect, not permission to
// create another MR or proof of an active Prometheus rule.
func (a *GitOpsAlertRuleApplier) LookupForReconciliation(ctx context.Context, in AlertRuleEnableIntent, executionID string) (AlertRuleDeploymentReceipt, bool, error) {
	var zero AlertRuleDeploymentReceipt
	b, err := a.binding(in)
	if err != nil {
		return zero, false, err
	}
	if validateDeploymentPlan(in.Deployment) != nil || in.Deployment.ProjectID != b.ProjectID ||
		in.Deployment.TargetBranch != b.TargetBranch || in.Deployment.Path != b.YAMLPath ||
		in.Deployment.ScopeFingerprint != deploymentScope(b) || !hexID(executionID) || len(executionID) != 32 {
		return zero, false, errors.New("untrusted GitOps recovery identity")
	}
	branch := "aiops-alert-rule-enable/" + executionID
	m, found, err := a.mrByBranch(ctx, b, branch)
	if err != nil || !found {
		return zero, found, err
	}
	// The executor's own merge (D11) is the result of this execution.
	if m.State == "merged" && in.Deployment.ExecutorMerges() {
		receipt, err := a.mergedReceipt(ctx, b, in, branch, m)
		return receipt, err == nil, err
	}
	state := m.State
	// Reuse the exact source-parent, one-file diff and complete-byte checks.
	// This copy affects only receipt validation, never the remote MR state.
	m.State = "opened"
	receipt, err := a.remoteReceipt(ctx, b, in, branch, m)
	if err != nil {
		return zero, false, err
	}
	receipt.State = state
	return receipt, true, nil
}
