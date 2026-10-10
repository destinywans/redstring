package v3rollouts

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3proposals"
)

type approverJSON struct {
	Name       string `json:"name"`
	VerifiedAt string `json:"verified_at"`
}

// Execution answers GET /executions/{id} for a rollback execution (PA-08).
// It reports false for any other ID so the legacy projection can answer.
// Missing proposals.view hides the execution (404).
func (s *Service) Execution(w http.ResponseWriter, r *http.Request, id string) bool {
	if s == nil || s.DB == nil || id == "" || len(id) > 128 {
		return false
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)
	var proposalID, env, app, status string
	var reason *string
	var timeline, rollout, verification []byte
	var created, updated time.Time
	err = tx.QueryRow(ctx, `SELECT proposal_id,environment_id,application_id,status,status_reason,timeline,rollout,verification,created_at,updated_at
 FROM v3_rollout_executions WHERE execution_id=$1`, id).Scan(&proposalID, &env, &app, &status, &reason, &timeline,
		&rollout, &verification, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		httpapi.Error(w, 503, "execution_unavailable", "Execution unavailable")
		return true
	}
	scope, ok := httpapi.V3ScopeFromContext(ctx)
	inScope := false
	for _, e := range scope.EnvironmentIDs {
		inScope = inScope || e == env
	}
	if !ok || !inScope {
		httpapi.Error(w, 404, "not_found", "Execution not found")
		return true
	}
	team, err := ownerTeamReadTx(ctx, tx, app, env)
	if err == nil {
		err = s.authorizeTx(ctx, tx, httpapi.CurrentUser(r), app, env, team, [2]string{"proposals", "view"})
	}
	if errors.Is(err, errNotFound) || errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 404, "not_found", "Execution not found")
		return true
	}
	if err != nil {
		httpapi.Error(w, 503, "execution_unavailable", "Execution unavailable")
		return true
	}
	action, _, _, err := v3proposals.LoadRollbackActionTx(ctx, tx, proposalID)
	if err != nil {
		httpapi.Error(w, 503, "execution_unavailable", "Execution unavailable")
		return true
	}
	var approvedAt time.Time
	var firstVote, secondVote string
	if err = tx.QueryRow(ctx, `SELECT approved_at,first_vote_id,second_vote_id FROM v3_proposal_approval_decisions WHERE proposal_id=$1`,
		proposalID).Scan(&approvedAt, &firstVote, &secondVote); err != nil {
		httpapi.Error(w, 503, "execution_unavailable", "Execution unavailable")
		return true
	}
	approvers := []approverJSON{}
	for _, vote := range []string{firstVote, secondVote} {
		var name string
		var at time.Time
		if err = tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(u.name,''),u.id),v.verified_at FROM v3_proposal_approval_votes v
 JOIN users u ON u.id=v.approver_id WHERE v.id=$1`, vote).Scan(&name, &at); err == nil {
			approvers = append(approvers, approverJSON{Name: name, VerifiedAt: at.UTC().Format(time.RFC3339)})
		}
	}
	var entries []timelineEntry
	_ = json.Unmarshal(timeline, &entries)
	timelineOut := make([]map[string]string, 0, len(entries))
	for _, e := range entries {
		timelineOut = append(timelineOut, map[string]string{"state": e.State, "at": e.At.UTC().Format(time.RFC3339)})
	}
	var rolloutOut any
	if len(rollout) > 0 {
		var snap rolloutSnapshot
		if json.Unmarshal(rollout, &snap) == nil {
			rolloutOut = map[string]any{"desired": snap.Desired, "updated": snap.Updated, "available": snap.Available,
				"current_revision": snap.CurrentRevision, "observed_at": snap.ObservedAt.UTC().Format(time.RFC3339)}
		}
	}
	var verificationOut any
	if len(verification) > 0 {
		var v verificationState
		if json.Unmarshal(verification, &v) == nil {
			samples := make([]map[string]any, 0, len(v.Samples))
			for _, sample := range v.Samples {
				samples = append(samples, map[string]any{"at": sample.At.UTC().Format(time.RFC3339),
					"request_rate": sample.RequestRate, "error_ratio": sample.ErrorRatio,
					"sample_age_seconds": sample.SampleAgeSeconds, "ok": sample.OK})
			}
			verificationOut = map[string]any{"plan": v.Plan, "started_at": formatTime(v.StartedAt),
				"finished_at": formatTime(v.FinishedAt), "result": v.Result, "samples": samples}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{
		"id": id, "kind": v3proposals.RollbackActionKind, "proposal_id": proposalID,
		"investigation_id": action.InvestigationID, "application_id": app, "environment_id": env,
		"status": status, "status_reason": reason, "approved_at": approvedAt.UTC().Format(time.RFC3339),
		"approvers": approvers, "created_at": created.UTC().Format(time.RFC3339), "updated_at": updated.UTC().Format(time.RFC3339),
		"target": map[string]string{"namespace": action.Namespace, "deployment": action.Deployment,
			"from_revision": action.FromRevision, "target_revision": action.TargetRevision},
		"timeline": timelineOut, "rollout": rolloutOut, "verification": verificationOut,
	})
	return true
}

func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
