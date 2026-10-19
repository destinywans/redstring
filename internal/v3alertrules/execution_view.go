package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// failureMessages explain a failure code; the page has its own text per
// code and shows this only for a code it does not know.
var failureMessages = map[string]string{
	"stale_base":          "The target branch moved after the approval; nothing was merged.",
	"branch_conflict":     "The execution branch or MR does not hold the approved change.",
	"mr_closed":           "The MR was closed before the executor merged it.",
	"protection_changed":  "The target branch protection differs from the approval.",
	"merge_refused":       "GitLab refused the merge.",
	"credential_rejected": "GitLab rejected the executor's write credential.",
	"approval_invalid":    "The two approvals no longer hold (revoked or expired).",
	"plan_changed":        "The target file or branch changed before the change was written.",
	"source_changed":      "The rule group or the observed rule changed before the change was written.",
	"load_timeout":        "The change is merged, but Prometheus did not load it within the readback timeout.",
	"rule_health_error":   "Prometheus loaded the rule, and its evaluation fails.",
}

// failureMessage explains a failure; a load timeout also says what
// Prometheus reported about its last configuration reload (PA-90).
func failureMessage(code string, reload *enableReload) string {
	message := failureMessages[code]
	if code != "load_timeout" || reload == nil {
		return message
	}
	switch {
	case reload.LastSuccessful != nil && !*reload.LastSuccessful:
		message += " Prometheus's last configuration reload failed"
	case reload.LastSuccessful != nil:
		message += " Prometheus's last configuration reload succeeded, so the merged file has not reached it (check the rule sync)"
	default:
		return message + " Prometheus's reload status could not be read (" + reload.Unavailable + ")."
	}
	if reload.LastSuccessAt != nil {
		message += "; the last successful reload was at " + reload.LastSuccessAt.UTC().Format(time.RFC3339)
	}
	return message + "."
}

// executionViewState is the page's state (D11 §11.5) for a ledger state.
func executionViewState(state, failure string, receipt AlertRuleDeploymentReceipt) string {
	switch state {
	case "exporting":
		if receipt.MR > 0 {
			return "merging"
		}
		return "committing"
	case "pending_review":
		return "merging"
	case "awaiting_provider":
		if failure == "load_timeout" {
			return "needs_attention"
		}
		return "loading"
	case "stale_remote":
		return "needs_attention"
	}
	return state
}

// ExecutionView answers GET /executions/{id} for an alert rule enable or
// disable execution (D11 §11.5) and reports false for any other ID. A
// missing proposals.view grant hides the execution (404).
func (s *WriteService) ExecutionView(w http.ResponseWriter, r *http.Request, id string) bool {
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	status, body, handled := s.executionView(r.Context(), scope.EnvironmentIDs, httpapi.CurrentUser(r), id)
	if !handled {
		return false
	}
	if status != http.StatusOK {
		code, message := "execution_unavailable", "Execution unavailable"
		if status == http.StatusNotFound {
			code, message = "not_found", "Execution not found"
		}
		httpapi.Error(w, status, code, message)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, body)
	return true
}

// executionView builds the response, or the status of a handled failure;
// handled is false for an ID that is not an alert rule execution.
func (s *WriteService) executionView(ctx context.Context, envs []string, user core.User, id string) (int, map[string]any, bool) {
	if s == nil || s.DB == nil || !hexID(id) {
		return 0, nil, false
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, nil, false
	}
	defer tx.Rollback(ctx)
	var proposalID, state, failure, env, app, kind string
	var deploymentRaw, readbackRaw, stepsRaw []byte
	var created, updated time.Time
	err = tx.QueryRow(ctx, `SELECT x.proposal_id,x.state,x.failure_code,x.deployment,x.readback,x.steps,x.created_at,x.updated_at,
 p.environment_id,p.application_id,a.action_kind
 FROM v3_alert_rule_enable_executions x JOIN v3_proposals p ON p.id=x.proposal_id
 JOIN v3_proposal_actions a ON a.proposal_id=x.proposal_id WHERE x.execution_id=$1`, id).
		Scan(&proposalID, &state, &failure, &deploymentRaw, &readbackRaw, &stepsRaw, &created, &updated, &env, &app, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false
	}
	if err != nil {
		return http.StatusServiceUnavailable, nil, true
	}
	inScope := false
	for _, e := range envs {
		inScope = inScope || e == env
	}
	if !inScope {
		return http.StatusNotFound, nil, true
	}
	if err := s.authorizeProposalsViewTx(ctx, tx, user, env, app); err != nil {
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) {
			return http.StatusNotFound, nil, true
		}
		return http.StatusServiceUnavailable, nil, true
	}
	intent, err := loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
	var receipt AlertRuleDeploymentReceipt
	var readback enableReadback
	var steps []struct {
		State  string    `json:"state"`
		At     time.Time `json:"at"`
		Reason string    `json:"reason"`
	}
	if err != nil || json.Unmarshal(deploymentRaw, &receipt) != nil || json.Unmarshal(readbackRaw, &readback) != nil ||
		json.Unmarshal(stepsRaw, &steps) != nil {
		return http.StatusServiceUnavailable, nil, true
	}
	var approvedAt time.Time
	var firstVote, secondVote string
	if err := tx.QueryRow(ctx, `SELECT approved_at,first_vote_id,second_vote_id FROM v3_proposal_approval_decisions WHERE proposal_id=$1`,
		proposalID).Scan(&approvedAt, &firstVote, &secondVote); err != nil {
		return http.StatusServiceUnavailable, nil, true
	}
	approvers := []map[string]string{}
	for _, vote := range []string{firstVote, secondVote} {
		var name string
		var at time.Time
		if tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(u.name,''),u.id),v.verified_at FROM v3_proposal_approval_votes v
 JOIN users u ON u.id=v.approver_id WHERE v.id=$1`, vote).Scan(&name, &at) == nil {
			approvers = append(approvers, map[string]string{"name": name, "verified_at": at.UTC().Format(time.RFC3339)})
		}
	}
	operation := "enable"
	if kind == alertRuleDisableKind {
		operation = "disable"
	}
	stepsOut := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		stepsOut = append(stepsOut, map[string]any{"state": step.State, "at": step.At.UTC().Format(time.RFC3339),
			"reason": textOrNil(step.Reason)})
	}
	var projectPath any
	if display, ok := s.targetDisplay(intent); ok && display.ProjectPath != "" {
		projectPath = display.ProjectPath
	}
	repository := map[string]any{"project_id": strconv.FormatInt(intent.Deployment.ProjectID, 10), "project_path": projectPath,
		"source_branch": "aiops-alert-rule-enable/" + id, "target_branch": intent.Deployment.TargetBranch, "path": intent.Deployment.Path,
		"commit_sha": textOrNil(receipt.SourceRevision), "mr_iid": nil, "mr_url": textOrNil(receipt.URL),
		"merged_sha": textOrNil(receipt.MergeRevision), "merged_at": timeOrNil(receipt.MergedAt)}
	if receipt.MR > 0 {
		repository["mr_iid"] = receipt.MR
	}
	readbackState := readback.State
	if readbackState == "" {
		readbackState = "pending"
	}
	var failureOut any
	if failure != "" {
		failureOut = map[string]string{"code": failure, "message": failureMessage(failure, readback.Reload)}
	}
	var reloadOut any
	if r := readback.Reload; r != nil {
		reloadOut = map[string]any{"last_successful": r.LastSuccessful, "last_success_at": timeOrNil(r.LastSuccessAt),
			"unavailable": textOrNil(r.Unavailable)}
	}
	return http.StatusOK, map[string]any{
		"id": id, "kind": kind, "proposal_id": proposalID, "investigation_id": intent.InvestigationID,
		"application_id": app, "environment_id": env, "approved_at": approvedAt.UTC().Format(time.RFC3339), "approvers": approvers,
		"created_at": created.UTC().Format(time.RFC3339), "updated_at": updated.UTC().Format(time.RFC3339),
		"alert_rule": map[string]any{
			"proposal_id": proposalID, "operation": operation, "state": executionViewState(state, failure, receipt),
			"steps": stepsOut, "repository": repository,
			"readback": map[string]any{"state": readbackState, "checked_at": timeOrNil(readback.CheckedAt),
				"first_seen_at": timeOrNil(readback.FirstSeenAt), "health": textOrNil(readback.Health),
				"last_evaluation_at": timeOrNil(readback.LastEvaluationAt), "last_error": textOrNil(readback.LastError), "reload": reloadOut},
			"failure": failureOut,
		},
	}, true
}

// authorizeProposalsViewTx checks proposals.view on the application, or on
// its owner team.
func (s *WriteService) authorizeProposalsViewTx(ctx context.Context, tx pgx.Tx, user core.User, env, app string) error {
	if s.Access == nil || user.ID == "" {
		return store.ErrForbidden
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(owner_team_id,'') FROM applications WHERE id=$1 AND NOT archived`, app).Scan(&owner); err != nil {
		return err
	}
	decision := rbac.V3Decision{Module: "proposals", Action: "view", EnvironmentID: env, ApplicationID: app}
	err := s.Access.AuthorizeWithTx(ctx, tx, user, decision)
	if errors.Is(err, store.ErrForbidden) && owner != "" {
		decision.ApplicationID, decision.TeamID = "", owner
		err = s.Access.AuthorizeWithTx(ctx, tx, user, decision)
	}
	return err
}

func textOrNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func timeOrNil(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339)
}
