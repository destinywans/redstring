package v3alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// ExecuteApprovedCreate consumes the approved proposal's only dispatch right.
// The claim and fixed Alertmanager payload commit before the one network POST.
// A repeated call never sends again, even when the first outcome is unknown.
func (e *SilenceExecutor) ExecuteApprovedCreate(ctx context.Context, proposalID string) (SilenceExecutionResult, error) {
	var zero SilenceExecutionResult
	if e == nil || e.Ledger == nil || e.Ledger.DB == nil || e.Client == nil || e.Actions == nil || e.Approvals == nil {
		return zero, ErrNotConfigured
	}
	var environmentID string
	if err := e.Ledger.DB.QueryRow(ctx, `SELECT environment_id FROM v3_proposals WHERE id=$1`, proposalID).Scan(&environmentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, store.ErrNotFound
		}
		return zero, err
	}
	if err := e.ready(ctx, environmentID, ProposedSilenceCreateKind); err != nil {
		return zero, err
	}
	tx, err := e.Ledger.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)

	// The preview is untrusted. Lock the bulk group first, then the catalog
	// binding, proposal and grant rows; the canonical source must match below.
	preview, err := previewSilenceCreate(ctx, tx, proposalID)
	if err != nil {
		return zero, err
	}
	if e.GroupEvidence == nil {
		if strings.HasPrefix(preview.IdempotencyKey, "bulk-") {
			return zero, ErrNotConfigured
		}
	} else if err := e.GroupEvidence.PrelockBulkSilenceWithTx(ctx, tx, proposalID); err != nil {
		return zero, err
	}
	previewInput := silenceCreateInput(preview)
	target, err := e.Ledger.LockCreateProposalTargetWithTx(ctx, tx, previewInput, e.Client)
	if err != nil {
		return zero, err
	}
	action, proof, err := e.loadApprovedWithTx(ctx, tx, proposalID, ProposedSilenceCreateKind)
	if err != nil {
		return zero, err
	}
	if action.EnvironmentID != environmentID || action.Create == nil {
		return zero, store.ErrConflict
	}
	in := silenceCreateInput(*action.Create)
	if !sameProposedSilenceCreate(preview, *action.Create) ||
		target.EnvironmentID != action.EnvironmentID || target.ApplicationID != action.ApplicationID ||
		target.OwnerTeamID != action.Create.OwnerTeamID || target.BindingFingerprint != action.Create.BindingFingerprint ||
		action.CreatorID != action.Create.ActorID {
		return zero, store.ErrConflict
	}
	actor := core.User{ID: action.CreatorID}
	if err = e.Ledger.activeHuman(ctx, tx, actor); err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return zero, err
	}
	if err = e.Ledger.RevalidateCreateProposalWithTx(ctx, tx, actor, *action.Create, e.Client); err != nil {
		return zero, err
	}
	decision := rbac.V3Decision{Module: "proposals", Action: "change", EnvironmentID: action.EnvironmentID,
		ApplicationID: action.ApplicationID}
	if err = authorizeSilenceCancelScope(ctx, tx, e.Ledger.Access, actor, decision, target.OwnerTeamID); err != nil {
		return zero, err
	}
	var investigationUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments ae ON ae.id=i.environment_id AND ae.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
   AND ae.key=$3 AND NOT ae.archived FOR SHARE OF i`, action.InvestigationID,
		action.ApplicationID, action.EnvironmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	if !investigationUpdatedAt.Equal(action.InvestigationUpdatedAt) ||
		action.InvestigationUpdatedAt.IsZero() || action.Create.EnvironmentID != in.EnvironmentID {
		return zero, store.ErrConflict
	}
	if e.GroupEvidence != nil {
		if err = e.GroupEvidence.RevalidateBulkSilenceWithTx(ctx, tx, action); err != nil {
			return zero, err
		}
	}
	matchers, appLabel, envLabel, err := e.Ledger.prepare(in)
	if err != nil {
		return zero, err
	}
	logicalHash, err := createExecutionLogicalHash(in, matchers, appLabel, envLabel)
	if err != nil {
		return zero, err
	}
	op, err := e.Ledger.reserveTx(ctx, tx, actor, in, matchers, appLabel, envLabel, logicalHash)
	if err != nil {
		return zero, err
	}
	if op.State != SilenceCreateReserved {
		return zero, ErrSilenceExecutionClaimed
	}
	if err = validCreateExecutionOperation(op, action, matchers, logicalHash, in.Duration); err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_silence_execution_claims
 (proposal_id,action_kind,action_digest,review_content_digest,approval_digest,create_operation_id,state)
 VALUES($1,$2,$3,$4,$5,$6,'reserved')`, proposalID, ProposedSilenceCreateKind,
		action.ActionDigest, action.ReviewContentDigest, proof.ApprovalDigest, op.ID)
	if silenceClaimConflict(err) {
		return zero, ErrSilenceExecutionClaimed
	}
	if err != nil {
		return zero, err
	}
	result := SilenceExecutionResult{ProposalID: proposalID, Kind: ProposedSilenceCreateKind,
		State: "dispatching", OperationID: op.ID}
	now := e.Ledger.currentTime()
	if now.After(op.Request.StartsAt.Add(maxAlertmanagerStartShift)) || !now.Before(op.Request.EndsAt) {
		if _, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations SET state='expired',updated_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
			return zero, err
		}
		if _, err = tx.Exec(ctx, `UPDATE v3_silence_execution_claims SET state='expired',updated_at=clock_timestamp() WHERE proposal_id=$1`, proposalID); err != nil {
			return zero, err
		}
		if err = auditSilenceCreate(ctx, tx, actor.ID, action.ApplicationID,
			"v3.silence_create.expired", proposalID, "expired", action.EnvironmentID, "expired"); err != nil {
			return zero, err
		}
		if err = tx.Commit(ctx); err != nil {
			return zero, err
		}
		result.State = "expired"
		return result, ErrSilenceCreateExpired
	}
	if _, err = tx.Exec(ctx, `UPDATE v3_silence_create_operations SET state='dispatching',updated_at=clock_timestamp() WHERE id=$1 AND state='reserved'`, op.ID); err != nil {
		return zero, err
	}
	if _, err = tx.Exec(ctx, `UPDATE v3_silence_execution_claims
 SET state='dispatching',dispatch_started_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE proposal_id=$1 AND state='reserved'`, proposalID); err != nil {
		return zero, err
	}
	if err = auditSilenceCreate(ctx, tx, actor.ID, action.ApplicationID,
		"v3.silence_create.dispatching", proposalID, "pending", action.EnvironmentID, "dispatching"); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	upstreamID, writeErr := e.Client.CreateSilence(ctx, action.EnvironmentID, op.Request)
	// Persist the result even if the worker's original context was canceled.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if errors.Is(writeErr, ErrSilenceCreateExpired) {
		ledgerErr := e.Ledger.markCreateNotSentExpired(persistCtx, op.ID)
		claimErr := e.setCreateExecutionState(persistCtx, proposalID, "expired", "")
		result.State = "expired"
		return result, errors.Join(writeErr, ledgerErr, claimErr)
	}
	if writeErr != nil {
		ledgerErr := e.Ledger.markCreateUnknown(persistCtx, op.ID)
		claimErr := e.setCreateExecutionState(persistCtx, proposalID, "unknown", "")
		result.State = "unknown"
		return result, errors.Join(ErrSilenceExecutionUnknown, writeErr, ledgerErr, claimErr)
	}
	if err = e.Ledger.acknowledgeCreate(persistCtx, op.ID, upstreamID); err != nil {
		ledgerErr := e.Ledger.markCreateUnknown(persistCtx, op.ID)
		claimErr := e.setCreateExecutionState(persistCtx, proposalID, "unknown", "")
		result.State = "unknown"
		return result, errors.Join(ErrSilenceExecutionUnknown, err, ledgerErr, claimErr)
	}
	reconciled, reconcileErr := e.ReconcileCreate(persistCtx, proposalID)
	if reconcileErr != nil && reconciled.State != "unknown" && reconciled.State != "manual_review" && reconciled.State != "confirmed" {
		ledgerErr := e.Ledger.markCreateUnknown(persistCtx, op.ID)
		claimErr := e.setCreateExecutionState(persistCtx, proposalID, "unknown", "")
		result.State = "unknown"
		return result, errors.Join(ErrSilenceExecutionUnknown, reconcileErr, ledgerErr, claimErr)
	}
	return reconciled, reconcileErr
}

func createExecutionLogicalHash(in SilenceCreateInput, matchers []Matcher, appLabel, envLabel string) (string, error) {
	hash, _, err := digestSilence(struct {
		EnvironmentID    string
		EnvironmentLabel string
		ApplicationLabel string
		Matchers         []Matcher
		Duration         int64
		Comment          string
	}{in.EnvironmentID, envLabel, appLabel, matchers, int64(in.Duration), in.Comment})
	return hash, err
}

func previewSilenceCreate(ctx context.Context, tx pgx.Tx, proposalID string) (ProposedSilenceCreate, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT action FROM v3_proposal_actions WHERE proposal_id=$1`, proposalID).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ProposedSilenceCreate{}, store.ErrNotFound
		}
		return ProposedSilenceCreate{}, err
	}
	var preview struct {
		Silence ProposedSilenceCreate `json:"silence"`
	}
	if json.Unmarshal(raw, &preview) != nil || preview.Silence.Kind != ProposedSilenceCreateKind {
		return ProposedSilenceCreate{}, store.ErrConflict
	}
	return preview.Silence, nil
}

func silenceCreateInput(action ProposedSilenceCreate) SilenceCreateInput {
	return SilenceCreateInput{EnvironmentID: action.EnvironmentID, IdempotencyKey: action.IdempotencyKey,
		Comment: action.Comment, Matchers: action.Matchers,
		Duration: time.Duration(action.DurationSeconds) * time.Second}
}

func sameProposedSilenceCreate(a, b ProposedSilenceCreate) bool {
	if a.SchemaVersion != b.SchemaVersion || a.Kind != b.Kind || a.ActorID != b.ActorID ||
		a.EnvironmentID != b.EnvironmentID || a.EnvironmentLabel != b.EnvironmentLabel ||
		a.ApplicationID != b.ApplicationID || a.ApplicationLabel != b.ApplicationLabel ||
		a.OwnerTeamID != b.OwnerTeamID || a.IdempotencyKey != b.IdempotencyKey ||
		a.DurationSeconds != b.DurationSeconds || a.Comment != b.Comment ||
		a.BindingFingerprint != b.BindingFingerprint {
		return false
	}
	return sameSilenceMatchers(a.Matchers, b.Matchers)
}

func validCreateExecutionOperation(op SilenceCreateOperation, action SilenceReviewedAction,
	matchers []Matcher, logicalHash string, duration time.Duration) error {
	if op.ID == "" || op.ActorID != action.CreatorID ||
		op.EnvironmentID != action.EnvironmentID || op.ApplicationID != action.ApplicationID ||
		op.EnvironmentLabel != action.Create.EnvironmentLabel ||
		op.ApplicationLabel != action.Create.ApplicationLabel || op.OwnerTeamID != action.Create.OwnerTeamID ||
		op.RequestHash != logicalHash || !silenceDigestPattern.MatchString(op.PayloadHash) ||
		op.Request.CreatedBy != op.ActorID || op.Request.Comment != action.Create.Comment ||
		len(op.Request.Annotations) != 1 || op.Request.Annotations[silenceOperationAnnotation] != op.ID ||
		!sameSilenceMatchers(op.Request.Matchers, matchers) ||
		op.Request.StartsAt.IsZero() || op.Request.EndsAt.Sub(op.Request.StartsAt) != duration {
		return store.ErrConflict
	}
	computed, _, err := digestSilence(op.Request)
	if err != nil || computed != op.PayloadHash {
		return store.ErrConflict
	}
	return nil
}

// ReconcileCreate reads the complete upstream list and never sends another POST.
func (e *SilenceExecutor) ReconcileCreate(ctx context.Context, proposalID string) (SilenceExecutionResult, error) {
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
	var actionDigest, reviewDigest, approvalDigest string
	var upstreamID *string
	err = tx.QueryRow(ctx, `SELECT action_kind,state,create_operation_id,upstream_id,
 action_digest,review_content_digest,approval_digest
 FROM v3_silence_execution_claims WHERE proposal_id=$1 AND action_kind=$2 FOR SHARE`,
		proposalID, ProposedSilenceCreateKind).Scan(&result.Kind, &result.State, &result.OperationID,
		&upstreamID, &actionDigest, &reviewDigest, &approvalDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, store.ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	result.ProposalID = proposalID
	if upstreamID != nil {
		result.UpstreamID = *upstreamID
	}
	if result.State == "confirmed" {
		return result, tx.Commit(ctx)
	}
	if result.State == "manual_review" || result.State == "expired" {
		return result, ErrSilenceExecutionClaimed
	}
	if result.State != "dispatching" && result.State != "unknown" {
		return result, ErrSilenceExecutionClaimed
	}
	action, err := e.Actions.LoadReviewedActionWithTx(ctx, tx, proposalID)
	if err != nil {
		return result, err
	}
	if action.Kind != ProposedSilenceCreateKind || action.Create == nil ||
		action.ActionDigest != actionDigest || action.ReviewContentDigest != reviewDigest ||
		!silenceDigestPattern.MatchString(approvalDigest) {
		return result, store.ErrConflict
	}
	op, err := scanSilenceCreate(tx.QueryRow(ctx, `SELECT `+silenceCreateColumns+`
 FROM v3_silence_create_operations WHERE id=$1 FOR SHARE`, result.OperationID))
	if err != nil {
		return result, err
	}
	in := silenceCreateInput(*action.Create)
	logicalHash, hashErr := createExecutionLogicalHash(in, action.Create.Matchers,
		action.Create.ApplicationLabel, action.Create.EnvironmentLabel)
	if hashErr != nil || validCreateExecutionOperation(op.SilenceCreateOperation, action, action.Create.Matchers,
		logicalHash, in.Duration) != nil {
		return result, store.ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	reconciled, reconcileErr := e.Ledger.reconcileCreate(ctx, result.OperationID, e.Client)
	next := reconciled.State
	if next != SilenceCreateConfirmed && next != SilenceCreateManualReview {
		next = SilenceCreateUnknown
	}
	if stateErr := e.setCreateExecutionState(ctx, proposalID, next, reconciled.UpstreamID); stateErr != nil {
		return result, errors.Join(ErrSilenceExecutionUnknown, reconcileErr, stateErr)
	}
	result.State, result.UpstreamID = next, reconciled.UpstreamID
	if next == SilenceCreateConfirmed {
		return result, nil
	}
	return result, errors.Join(ErrSilenceExecutionUnknown, reconcileErr)
}

func (e *SilenceExecutor) setCreateExecutionState(ctx context.Context, proposalID, state, upstreamID string) error {
	if state != "unknown" && state != "manual_review" && state != "confirmed" && state != "expired" {
		return store.ErrConflict
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	tx, err := e.Ledger.DB.Begin(persistCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(persistCtx)
	var oldState, oldUpstream, actorID, appID, env string
	err = tx.QueryRow(persistCtx, `SELECT c.state,COALESCE(c.upstream_id,''),a.creator_id,a.application_id,a.environment_id
 FROM v3_silence_execution_claims c JOIN v3_proposal_actions a ON a.proposal_id=c.proposal_id
 WHERE c.proposal_id=$1 AND c.action_kind=$2 FOR UPDATE OF c`, proposalID,
		ProposedSilenceCreateKind).Scan(&oldState, &oldUpstream, &actorID, &appID, &env)
	if err != nil {
		return err
	}
	if oldState == state || oldState == "confirmed" || oldState == "manual_review" || oldState == "expired" {
		if oldState == "confirmed" && upstreamID != "" && oldUpstream != upstreamID {
			return store.ErrConflict
		}
		return tx.Commit(persistCtx)
	}
	if oldState != "dispatching" && !(oldState == "unknown" && (state == "confirmed" || state == "manual_review")) {
		return store.ErrConflict
	}
	if state == "confirmed" && !validSilenceID(upstreamID) {
		return store.ErrConflict
	}
	if _, err = tx.Exec(persistCtx, `UPDATE v3_silence_execution_claims
 SET state=$2,upstream_id=COALESCE(NULLIF($3,''),upstream_id),updated_at=clock_timestamp()
 WHERE proposal_id=$1`, proposalID, state, upstreamID); err != nil {
		return err
	}
	outcome := "unknown"
	if state == "confirmed" {
		outcome = "success"
	} else if state == "expired" {
		outcome = "expired"
	}
	if err = auditSilenceCreate(persistCtx, tx, actorID, appID,
		"v3.silence_create."+state, proposalID, outcome, env, state); err != nil {
		return err
	}
	return tx.Commit(persistCtx)
}
