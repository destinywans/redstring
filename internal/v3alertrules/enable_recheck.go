package v3alertrules

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

// Recheck reads an alert rule execution back now (D11 §5.4): the same
// read-only observation the worker runs every 15 seconds, also after its
// 24-hour window, so a late Prometheus load is recorded as enabled. It
// writes nothing to GitLab or Prometheus and never merges or retries a
// write; only the local ledger records what the readback found.
func (s *WriteService) Recheck(w http.ResponseWriter, r *http.Request) {
	env, actor, err := s.prerequisites(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.recheckExecution(r.Context(), env, actor, r.PathValue("id"))
	if err != nil {
		var p *writeProblem
		if !errors.As(err, &p) {
			err = problem(503, "recheck_unavailable", "The execution could not be read back")
		}
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (s *WriteService) recheckExecution(ctx context.Context, env string, actor core.User, executionID string) (AlertRuleExecutionResult, error) {
	var zero AlertRuleExecutionResult
	if s.EnableApplier == nil {
		return zero, problem(503, "enable_not_connected", "Alert rule executor is not connected")
	}
	if !hexID(executionID) {
		return zero, problem(404, "not_found", "Execution not found")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var proposalID, state, app string
	err = tx.QueryRow(ctx, `SELECT x.proposal_id,x.state,p.application_id
 FROM v3_alert_rule_enable_executions x JOIN v3_proposals p ON p.id=x.proposal_id
 WHERE x.execution_id=$1 AND p.environment_id=$2`, executionID, env).Scan(&proposalID, &state, &app)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, problem(404, "not_found", "Execution not found")
	}
	if err != nil {
		return zero, err
	}
	if err := s.authorizeTx(ctx, tx, actor, env, app); err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, err
	}
	if state != "pending_review" && state != "merged" && state != "awaiting_provider" && state != "enabled" && state != "disabled" {
		return zero, problem(409, "execution_not_observable", "Only a merged or loading execution can be rechecked")
	}
	return s.ObserveEnabled(ctx, proposalID)
}
