package v3agents

import (
	"errors"
	"net/http"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

// ExecutionResult is a private delivered snapshot. Shared run metadata and
// trace endpoints never return this body. The original actor must retain
// current application/environment view authority to read it again.
func (s *Service) ExecutionResult(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	v, err := lockRunRecord(r.Context(), tx, r.PathValue("id"), false)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	if !contains(envs, v.Env) {
		notFound(w)
		return
	}
	a, err := lockRunAgent(r.Context(), tx, v)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	team, err := lockRunActiveScope(r.Context(), tx, v.App, v.Env)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	var owner, model, prompt, hash string
	var promptVersion int
	var revision int64
	err = tx.QueryRow(r.Context(), `SELECT actor_id,model_id,prompt_id,prompt_version,config_sha256,lifecycle_revision FROM v3_agent_execution_snapshots WHERE run_id=$1 FOR SHARE`, v.ID).Scan(&owner, &model, &prompt, &promptVersion, &hash, &revision)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != httpapi.CurrentUser(r).ID) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = lockRunAuthority(r.Context(), tx, r); err == nil {
		err = s.authorizeRunTx(r.Context(), tx, r, runRecord{Value: v, Agent: a, Team: team}, "view")
	}
	if err != nil {
		runReadFailure(w, err)
		return
	}
	var output *string
	if v.Status == "success" {
		var body string
		err = tx.QueryRow(r.Context(), `SELECT output_text FROM v3_agent_execution_results WHERE run_id=$1 FOR SHARE`, v.ID).Scan(&body)
		if err != nil {
			unavailable(w)
			return
		}
		output = &body
	}
	if err = currentRunSession(r.Context(), tx, r); err != nil {
		runReadFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": v.ID, "agent_id": a.ID, "version": v.Version, "model_id": model, "prompt_id": prompt, "prompt_version": promptVersion, "config_sha256": hash, "lifecycle_revision": revision, "status": v.Status, "error_code": v.ErrorCode, "output": output, "read_only": true})
}
