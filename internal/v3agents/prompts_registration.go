package v3agents

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// lockPromptRequest holds the current session and the physical role sources
// until commit. A concurrent logout or grant change either completes before
// this authorization read, or waits until this request has finished.
func lockPromptRequest(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	if !recheckPromptSession(w, r, tx) {
		return false
	}
	return lockPromptGrantSources(w, r, tx)
}

func lockPromptGrantSources(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	if err := lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Agent permission required")
		return false
	} else if err != nil {
		unavailable(w)
		return false
	}
	return true
}

func recheckPromptSession(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return false
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return false
	} else if err != nil {
		unavailable(w)
		return false
	}
	return true
}

// CreatePrompt records a reviewable v1 text for an existing Agent. It does
// not publish the text to an Agent runtime or claim an evaluation result.
func (s *Service) CreatePrompt(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	var in struct {
		AgentID string `json:"agent_id"`
		Name    string `json:"name"`
		Text    string `json:"text"`
	}
	if !decodePromptVersion(w, r, &in) {
		return
	}
	if !bounded(in.AgentID, 128) || !bounded(in.Name, 160) ||
		strings.TrimSpace(in.Text) == "" || len([]rune(in.Text)) > 20000 {
		promptRequestError(w, r, "invalid_agent_request", "提示词登记字段无效", "Invalid Prompt registration fields")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if !lockPromptRequest(w, r, tx) {
		return
	}
	a, err := scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, in.AgentID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !contains(envs, a.Env)) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: "change", EnvironmentID: a.Env, ApplicationID: a.App})
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Agent permission required")
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_prompts(id,agent_id,name,current_version)
	 VALUES($1,$2,$3,0)`, id, a.ID, in.Name)
	if err != nil {
		unavailable(w)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_prompt_versions(prompt_id,version,body,created_by)
	 VALUES($1,1,$2,$3)`, id, in.Text, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE v3_agent_prompts SET current_version=1 WHERE id=$1`, id)
	if err != nil {
		unavailable(w)
		return
	}
	if err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.prompt.registered", id,
		map[string]any{"environment_id": a.Env, "version": 1}); err != nil {
		unavailable(w)
		return
	}
	if !recheckPromptSession(w, r, tx) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"id": id, "version": "v1"})
}
