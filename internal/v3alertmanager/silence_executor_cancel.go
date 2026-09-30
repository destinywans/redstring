package v3alertmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// ExecuteApprovedCancel consumes one durable dispatch right. A committed claim
// blocks every later invocation, including after a timeout, process crash, or
// an ambiguous upstream 404. The network call is never made in the claim tx.
func (e *SilenceExecutor) ExecuteApprovedCancel(ctx context.Context, proposalID string) (SilenceExecutionResult, error) {
	var zero SilenceExecutionResult
	if e == nil || e.Ledger == nil || e.Ledger.DB == nil || e.Client == nil || e.Actions == nil || e.Approvals == nil {
		return zero, ErrNotConfigured
	}
	// Readiness borrows the pool, so check it before opening the claim tx. The
	// reviewed action and approval are reloaded under locks in that tx.
	var environmentID string
	if err := e.Ledger.DB.QueryRow(ctx, `SELECT environment_id FROM v3_proposals WHERE id=$1`,
		proposalID).Scan(&environmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, store.ErrNotFound
		}
		return zero, err
	}
	if err := e.ready(ctx, environmentID, ProposedSilenceCancelKind); err != nil {
		return zero, err
	}
	tx, err := e.Ledger.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	action, proof, err := e.loadApprovedWithTx(ctx, tx, proposalID, ProposedSilenceCancelKind)
	if err != nil {
		return zero, err
	}
	if action.EnvironmentID != environmentID {
		return zero, store.ErrConflict
	}
	intent := action.Cancel
	if intent == nil || intent.ActorID != action.CreatorID || intent.EnvironmentID != action.EnvironmentID ||
		intent.ApplicationID != action.ApplicationID || intent.InvestigationID != action.InvestigationID ||
		!intent.InvestigationUpdatedAt.Equal(action.InvestigationUpdatedAt) || !validSilenceID(intent.UpstreamID) ||
		!silenceDigestPattern.MatchString(intent.ObservedDigest) || !silenceDigestPattern.MatchString(intent.CreatePayloadHash) {
		return zero, store.ErrConflict
	}
	actor := core.User{ID: action.CreatorID}
	if err = e.Ledger.activeHuman(ctx, tx, actor); err != nil {
		return zero, err
	}
	appID, teamID, err := e.Ledger.lockApplicationScope(ctx, tx, action.EnvironmentID,
		intent.ApplicationLabel, action.ApplicationID)
	if err != nil {
		return zero, err
	}
	if appID != action.ApplicationID || teamID != intent.OwnerTeamID ||
		intent.EnvironmentLabel != e.Ledger.EnvironmentLabel(action.EnvironmentID) {
		return zero, store.ErrConflict
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return zero, err
	}
	for _, decision := range []rbac.V3Decision{
		{Module: "silences", Action: "view", EnvironmentID: action.EnvironmentID, ApplicationID: appID},
		{Module: "silences", Action: "manage", EnvironmentID: action.EnvironmentID, ApplicationID: appID},
		{Module: "proposals", Action: "change", EnvironmentID: action.EnvironmentID, ApplicationID: appID},
	} {
		if err = authorizeSilenceCancelScope(ctx, tx, e.Ledger.Access, actor, decision, teamID); err != nil {
			return zero, err
		}
	}
	var investigationUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments ae ON ae.id=i.environment_id AND ae.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
   AND ae.key=$3 AND NOT ae.archived FOR SHARE OF i`, action.InvestigationID,
		appID, action.EnvironmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if !investigationUpdatedAt.Equal(action.InvestigationUpdatedAt) {
		return zero, store.ErrConflict
	}
	op, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE id=$1 FOR SHARE`, intent.CreateOperationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if op.State != SilenceCreateConfirmed || op.ID != intent.CreateOperationID || op.UpstreamID != intent.UpstreamID ||
		op.PayloadHash != intent.CreatePayloadHash || op.EnvironmentID != action.EnvironmentID ||
		op.EnvironmentLabel != intent.EnvironmentLabel || op.ApplicationID != appID ||
		op.ApplicationLabel != intent.ApplicationLabel || op.OwnerTeamID != teamID ||
		op.ActorID == "" || op.Request.CreatedBy != op.ActorID ||
		op.Request.Annotations[silenceOperationAnnotation] != op.ID {
		return zero, store.ErrConflict
	}
	binding, err := e.Client.CancelBindingFingerprint(action.EnvironmentID, intent.EnvironmentLabel,
		intent.ApplicationLabel, appID)
	if err != nil || binding != intent.BindingFingerprint {
		return zero, store.ErrConflict
	}
	// A complete fresh read must still be the exact object and exact provider
	// evidence presented for approval. No stale proposal snapshot authorizes a
	// later, modified upstream object.
	target, snapshot, err := e.Client.observeCancelSilence(ctx, action.EnvironmentID, intent.UpstreamID, op.ID)
	if err != nil {
		return zero, err
	}
	sum := sha256.Sum256(snapshot)
	if hex.EncodeToString(sum[:]) != intent.ObservedDigest ||
		!bytes.Equal(snapshot, action.CancelObservedSnapshot) ||
		(target.Status.State != "active" && target.Status.State != "pending") ||
		!target.EndsAt.After(e.Ledger.currentTime()) ||
		!matchesCreateSnapshot(op.SilenceCreateOperation, target) ||
		classifySilence(target, op.EnvironmentLabel, action.EnvironmentID, e.Ledger.environmentLabels) != scopeOwn {
		return zero, store.ErrConflict
	}
	appLabel, exact, ambiguous := exactSilenceApplication(target)
	if !exact || ambiguous || appLabel != intent.ApplicationLabel {
		return zero, store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_silence_execution_claims
 (proposal_id,action_kind,action_digest,review_content_digest,approval_digest,
  create_operation_id,cancel_intent_id,upstream_id,state,dispatch_started_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,'dispatching',clock_timestamp())
	`, proposalID, ProposedSilenceCancelKind, action.ActionDigest,
		action.ReviewContentDigest, proof.ApprovalDigest, op.ID, intent.IntentID, intent.UpstreamID)
	if silenceClaimConflict(err) {
		return zero, ErrSilenceExecutionClaimed
	}
	if err != nil {
		return zero, err
	}
	if err = auditSilenceCreate(ctx, tx, action.CreatorID, appID, "v3.silence_cancel.dispatching",
		proposalID, "pending", action.EnvironmentID, "dispatching"); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	result := SilenceExecutionResult{ProposalID: proposalID, Kind: ProposedSilenceCancelKind,
		State: "dispatching", OperationID: op.ID, UpstreamID: intent.UpstreamID}
	writeErr := e.Client.ExpireSilence(ctx, action.EnvironmentID, intent.UpstreamID)
	if writeErr != nil {
		// A proxy 404, broken connection or timeout cannot prove that the write
		// did not reach Alertmanager. Preserve the one-shot claim for readback.
		if markErr := e.setCancelExecutionState(ctx, proposalID, "unknown"); markErr != nil {
			return result, errors.Join(ErrSilenceExecutionUnknown, writeErr, markErr)
		}
		result.State = "unknown"
		return result, errors.Join(ErrSilenceExecutionUnknown, writeErr)
	}
	reconciled, reconcileErr := e.ReconcileCancel(ctx, proposalID)
	if reconcileErr == nil || reconciled.State == "unknown" || reconciled.State == "manual_review" ||
		reconciled.State == "confirmed" {
		return reconciled, reconcileErr
	}
	// Even a failed local readback must leave an explicit durable unknown
	// outcome. If this update also fails, dispatching still bars another write.
	if markErr := e.setCancelExecutionState(ctx, proposalID, "unknown"); markErr != nil {
		return result, errors.Join(ErrSilenceExecutionUnknown, reconcileErr, markErr)
	}
	result.State = "unknown"
	return result, errors.Join(ErrSilenceExecutionUnknown, reconcileErr)
}

// ReconcileCancel is read only with respect to Alertmanager. It never calls
// ExpireSilence, including if the provider omitted the record or returned 404.
func (e *SilenceExecutor) ReconcileCancel(ctx context.Context, proposalID string) (SilenceExecutionResult, error) {
	var zero SilenceExecutionResult
	if e == nil || e.Ledger == nil || e.Ledger.DB == nil || e.Client == nil || e.Actions == nil {
		return zero, ErrNotConfigured
	}
	tx, err := e.Ledger.DB.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var result SilenceExecutionResult
	var actionDigest, reviewDigest, approvalDigest, intentID string
	var dispatchStarted time.Time
	err = tx.QueryRow(ctx, `SELECT action_kind,state,create_operation_id,upstream_id,
 action_digest,review_content_digest,approval_digest,cancel_intent_id,dispatch_started_at
 FROM v3_silence_execution_claims WHERE proposal_id=$1 AND action_kind=$2 FOR SHARE`,
		proposalID, ProposedSilenceCancelKind).Scan(&result.Kind, &result.State, &result.OperationID,
		&result.UpstreamID, &actionDigest, &reviewDigest, &approvalDigest, &intentID, &dispatchStarted)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	result.ProposalID = proposalID
	if result.State == "confirmed" {
		return result, tx.Commit(ctx)
	}
	if result.State == "manual_review" {
		return result, errors.Join(ErrSilenceExecutionUnknown, store.ErrConflict)
	}
	if result.State != "dispatching" && result.State != "unknown" {
		return result, store.ErrConflict
	}
	action, err := e.Actions.LoadReviewedActionWithTx(ctx, tx, proposalID)
	if err != nil {
		return result, err
	}
	if action.Status != "approved" || action.Kind != ProposedSilenceCancelKind || action.Cancel == nil ||
		action.ActionDigest != actionDigest || action.ReviewContentDigest != reviewDigest ||
		!silenceDigestPattern.MatchString(approvalDigest) || action.Cancel.IntentID != intentID ||
		action.Cancel.CreateOperationID != result.OperationID || action.Cancel.UpstreamID != result.UpstreamID ||
		action.EnvironmentID != action.Cancel.EnvironmentID {
		return result, store.ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	observed, snapshot, observeErr := e.Client.observeCancelSilence(ctx, action.EnvironmentID,
		result.UpstreamID, result.OperationID)
	next := "unknown"
	if observeErr == nil {
		var original Silence
		if json.Unmarshal(action.CancelObservedSnapshot, &original) != nil ||
			!stableCancelSnapshot(action.CancelObservedSnapshot, snapshot) ||
			observed.ID != original.ID || observed.Annotations[silenceOperationAnnotation] != result.OperationID ||
			!sameSilenceMatchers(observed.Matchers, original.Matchers) ||
			observed.CreatedBy != original.CreatedBy || observed.Comment != original.Comment {
			next = "manual_review"
		} else if observed.Status.State == "expired" &&
			observed.EndsAt.Before(original.EndsAt) && !observed.UpdatedAt.Before(dispatchStarted.Add(-30*time.Second)) {
			next = "confirmed"
		}
	} else if !errors.Is(observeErr, store.ErrNotFound) {
		// An invalid or ambiguous full snapshot needs human attention. A read
		// outage or a missing record stays unknown; neither allows retry.
		if errors.Is(observeErr, store.ErrConflict) {
			next = "manual_review"
		}
	}
	if err = e.setCancelExecutionState(ctx, proposalID, next); err != nil {
		return result, err
	}
	result.State = next
	if next == "confirmed" {
		return result, nil
	}
	if observeErr != nil {
		return result, errors.Join(ErrSilenceExecutionUnknown, observeErr)
	}
	return result, ErrSilenceExecutionUnknown
}

func stableCancelSnapshot(before, after []byte) bool {
	var a, b map[string]any
	if json.Unmarshal(before, &a) != nil || json.Unmarshal(after, &b) != nil || a == nil || b == nil {
		return false
	}
	for _, field := range []string{"status", "startsAt", "endsAt", "updatedAt"} {
		delete(a, field)
		delete(b, field)
	}
	aRaw, aErr := json.Marshal(a)
	bRaw, bErr := json.Marshal(b)
	return aErr == nil && bErr == nil && bytes.Equal(aRaw, bRaw)
}

func (e *SilenceExecutor) setCancelExecutionState(ctx context.Context, proposalID, state string) error {
	if state != "unknown" && state != "manual_review" && state != "confirmed" {
		return store.ErrConflict
	}
	// A canceled caller context must not prevent recording the outcome. The
	// committed dispatching claim still prevents another DELETE if this fails.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	tx, err := e.Ledger.DB.Begin(persistCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(persistCtx)
	var actorID, appID, env, oldState string
	err = tx.QueryRow(persistCtx, `SELECT c.state,a.creator_id,a.application_id,a.environment_id
 FROM v3_silence_execution_claims c JOIN v3_proposal_actions a ON a.proposal_id=c.proposal_id
 WHERE c.proposal_id=$1 AND c.action_kind=$2 FOR UPDATE OF c`, proposalID,
		ProposedSilenceCancelKind).Scan(&oldState, &actorID, &appID, &env)
	if err != nil {
		return err
	}
	if oldState == state || oldState == "confirmed" || oldState == "manual_review" {
		return tx.Commit(persistCtx)
	}
	if oldState != "dispatching" && !(oldState == "unknown" && (state == "confirmed" || state == "manual_review")) {
		return store.ErrConflict
	}
	if _, err = tx.Exec(persistCtx, `UPDATE v3_silence_execution_claims
 SET state=$2,updated_at=clock_timestamp() WHERE proposal_id=$1`, proposalID, state); err != nil {
		return err
	}
	outcome := "unknown"
	if state == "confirmed" {
		outcome = "success"
	}
	if err = auditSilenceCreate(persistCtx, tx, actorID, appID,
		"v3.silence_cancel."+state, proposalID, outcome, env, state); err != nil {
		return err
	}
	return tx.Commit(persistCtx)
}

func silenceClaimConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
