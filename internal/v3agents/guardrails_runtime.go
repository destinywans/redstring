package v3agents

import (
	"context"
	"errors"
)

// GuardDecisionHooks is the required pull/report boundary for a trusted Agent
// executor. It reads the policy bound to its persisted run, evaluates the
// guard, then reports a blocked/masked decision synchronously. Reporting
// captures database time after evaluation and rechecks the revision. A stale
// result must be reevaluated or fail closed. A settings PATCH only changes the
// stored policy. No executor is wired by this package's HTTP registration.
type GuardDecisionHooks interface {
	PolicySnapshotForRun(context.Context, string, string) (GuardPolicySnapshot, error)
	ValidateGuardDecision(context.Context, string, GuardPolicySnapshot) error
	ReportGuardDecision(context.Context, GuardEvent) error
}

var _ GuardDecisionHooks = (*Service)(nil)

// PolicySnapshotForRun derives the environment from the trusted persisted
// run, rather than accepting a caller-supplied environment for enforcement.
// The row locks end before any external Agent/model/tool work can begin.
func (s *Service) PolicySnapshotForRun(ctx context.Context, runID, policyID string) (GuardPolicySnapshot, error) {
	if s == nil || s.DB == nil || !bounded(runID, 128) || !bounded(policyID, 128) {
		return GuardPolicySnapshot{}, errors.New("invalid guard decision scope")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return GuardPolicySnapshot{}, err
	}
	defer tx.Rollback(ctx)
	var env string
	if err = tx.QueryRow(ctx, `SELECT environment_id FROM v3_agent_runs
 WHERE id=$1 AND status='running' FOR SHARE`, runID).Scan(&env); err != nil {
		return GuardPolicySnapshot{}, err
	}
	var snapshot GuardPolicySnapshot
	if err = tx.QueryRow(ctx, `SELECT environment_id,revision,enabled
 FROM v3_agent_guard_policies WHERE id=$1 AND environment_id=$2 FOR SHARE`,
		policyID, env).Scan(&snapshot.EnvironmentID, &snapshot.Revision, &snapshot.Enabled); err != nil {
		return GuardPolicySnapshot{}, err
	}
	snapshot.PolicyID = policyID
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&snapshot.ObservedAt); err != nil {
		return GuardPolicySnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return GuardPolicySnapshot{}, err
	}
	return snapshot, nil
}

// ValidateGuardDecision is the final handoff for a guard that allowed an
// action or was skipped because it was disabled. If the run or policy changed
// since the snapshot, the executor must reevaluate or fail closed. A later
// PATCH does not retroactively cancel a decision already handed off here.
// Blocked/masked results use ReportGuardDecision as their final handoff.
func (s *Service) ValidateGuardDecision(ctx context.Context, runID string, snapshot GuardPolicySnapshot) error {
	if s == nil || s.DB == nil || !bounded(runID, 128) ||
		!bounded(snapshot.PolicyID, 128) || !bounded(snapshot.EnvironmentID, 128) ||
		snapshot.Revision < 1 || snapshot.ObservedAt.IsZero() {
		return errors.New("invalid guard decision snapshot")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var runEnv string
	if err = tx.QueryRow(ctx, `SELECT environment_id FROM v3_agent_runs
 WHERE id=$1 AND status='running' FOR SHARE`, runID).Scan(&runEnv); err != nil {
		return err
	}
	var policyEnv string
	var revision int64
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT environment_id,revision,enabled
 FROM v3_agent_guard_policies WHERE id=$1 FOR SHARE`, snapshot.PolicyID).
		Scan(&policyEnv, &revision, &enabled); err != nil {
		return err
	}
	if runEnv != snapshot.EnvironmentID || policyEnv != runEnv ||
		revision != snapshot.Revision || enabled != snapshot.Enabled {
		return errors.New("guard decision used a stale policy snapshot")
	}
	return tx.Commit(ctx)
}
