package v3agents

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// evalListItem is one saved experiment. It carries metadata only; results,
// metrics and per-case outputs stay on GET /evals/{id} and its execution.
type evalListItem struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	AgentID          string  `json:"agent_id"`
	AgentName        string  `json:"agent_name"`
	ApplicationID    string  `json:"application_id"`
	EnvironmentID    string  `json:"environment_id"`
	BaselineVersion  string  `json:"baseline_version"`
	CandidateVersion string  `json:"candidate_version"`
	Status           string  `json:"status"`
	ExecutionStatus  *string `json:"execution_status"`
	CreatedAt        string  `json:"created_at"`
	FinishedAt       *string `json:"finished_at"`
	Href             string  `json:"href"`
}

const maxEvalListCandidates = 1000

// listPage reads ?page and ?page_size (default 50, at most 100).
func listPage(r *http.Request) (int, int, bool) {
	page, size := 1, 50
	for name, target := range map[string]*int{"page": &page, "page_size": &size} {
		values, present := r.URL.Query()[name]
		if !present {
			continue
		}
		n, err := strconv.Atoi(values[0])
		if len(values) != 1 || err != nil || n < 1 {
			return 0, 0, false
		}
		*target = n
	}
	return page, size, size <= 100 && page <= 1000
}

// Evals lists the saved experiments the caller may open with GET /evals/{id}:
// the same active application scope, creation-audit provenance and agents.view
// on the application or owner team, rechecked per row in one locked read.
func (s *Service) Evals(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return
	}
	page, size, valid := listPage(r)
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if !valid || len(agentID) > 128 {
		httpapi.Error(w, 422, "invalid_eval_query", "Use page>=1, page_size 1-100 and an agent_id of at most 128 characters")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if err = lockEvalSession(r.Context(), tx, r); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	user := httpapi.CurrentUser(r)
	if err = lockEvalGrantSources(r.Context(), tx, user.ID); errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Agent permission required")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	// The grant prefilter only bounds the scan; every row is authorized
	// below with the same policy and team fallback as the detail read.
	rows, err := tx.Query(r.Context(), `SELECT e.id,e.title,e.agent_id,a.name,a.application_id,a.environment_id,
 e.baseline_version,e.candidate_version,e.status,e.created_at,e.finished_at,COALESCE(app.owner_team_id,''),x.status
 FROM v3_agent_eval_experiments e
 JOIN v3_agents a ON a.id=e.agent_id
 JOIN applications app ON app.id=a.application_id AND NOT app.archived
 JOIN v3_application_environments mapped ON mapped.application_id=a.application_id AND mapped.environment_id=a.environment_id
 JOIN application_environments ae ON ae.application_id=app.id AND ae.key=a.environment_id AND NOT ae.archived
 LEFT JOIN v3_agent_eval_executions x ON x.experiment_id=e.id
 WHERE a.environment_id=ANY($1) AND ($2='' OR e.agent_id=$2)
 AND (SELECT COALESCE(m.changes->>'environment_id','') FROM management_audit m
      WHERE m.application_id=a.application_id AND m.action='v3.agent.eval.created' AND m.target=e.id
      AND m.actor_id=e.created_by AND m.outcome='recorded' ORDER BY m.id LIMIT 1)=a.environment_id
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$3 AND g.module='agents' AND g.action='view'
      AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=a.environment_id)
        OR (g.scope_kind='application' AND g.scope_id=a.application_id)
        OR (g.scope_kind='team' AND g.scope_id=app.owner_team_id)))
 ORDER BY e.created_at DESC,e.id LIMIT 1001 FOR SHARE OF e,a,app,mapped,ae`, envs, agentID, user.ID)
	if err != nil {
		unavailable(w)
		return
	}
	type candidate struct {
		item evalListItem
		team string
	}
	candidates := make([]candidate, 0, 64)
	for rows.Next() {
		var c candidate
		var created time.Time
		var finished *time.Time
		var execution *string
		if err = rows.Scan(&c.item.ID, &c.item.Title, &c.item.AgentID, &c.item.AgentName, &c.item.ApplicationID,
			&c.item.EnvironmentID, &c.item.BaselineVersion, &c.item.CandidateVersion, &c.item.Status, &created, &finished,
			&c.team, &execution); err != nil {
			break
		}
		c.item.CreatedAt, c.item.ExecutionStatus, c.item.Href = timeString(created), execution, "/evals/"+c.item.ID
		if finished != nil {
			value := timeString(*finished)
			c.item.FinishedAt = &value
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if len(candidates) > maxEvalListCandidates {
		httpapi.Error(w, 413, "eval_list_too_large", "Filter experiments by agent_id")
		return
	}
	visible := make([]evalListItem, 0, len(candidates))
	for _, c := range candidates {
		err = s.authorizeEvalTx(r.Context(), tx, r, agent{App: c.item.ApplicationID, Env: c.item.EnvironmentID}, c.team, "view")
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			unavailable(w)
			return
		}
		visible = append(visible, c.item)
	}
	emptyReason := any(nil)
	if len(visible) == 0 {
		var anyAgent bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_agents a WHERE a.environment_id=ANY($1) AND `+visibleAgentGrantSQL+`)`,
			envs, user.ID).Scan(&anyAgent)
		if err != nil {
			unavailable(w)
			return
		}
		if !anyAgent {
			emptyReason = "no_authorized_target"
		}
	}
	if err = evalSessionStillActive(r.Context(), tx, r); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	start := min((page-1)*size, len(visible))
	end := min(start+size, len(visible))
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"items": visible[start:end], "total": len(visible), "page": page, "page_size": size,
		"empty_reason": emptyReason, "unavailable": map[string]string{}})
}
