package v3agents

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

type prompt struct {
	ID, AgentID, Name string
	Version           int
}

func (s *Service) loadPrompt(r *http.Request, id string) (prompt, error) {
	var p prompt
	err := s.DB.QueryRow(r.Context(), `SELECT id,agent_id,name,current_version FROM v3_agent_prompts WHERE id=$1`, id).
		Scan(&p.ID, &p.AgentID, &p.Name, &p.Version)
	return p, err
}

func (s *Service) promptAgent(w http.ResponseWriter, r *http.Request, id, action string) (prompt, agent, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return prompt{}, agent{}, false
	}
	p, err := s.loadPrompt(r, id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return prompt{}, agent{}, false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, false
	}
	a, ok := s.objectAgent(w, r, p.AgentID, action)
	return p, a, ok
}

func (s *Service) Prompts(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return
	}
	items := make([]any, 0)
	rows, err := s.DB.Query(r.Context(), `SELECT p.id,p.name,p.current_version FROM v3_agent_prompts p
 JOIN v3_agents a ON a.id=p.agent_id WHERE a.environment_id=ANY($1) AND `+visibleAgentGrantSQL+`
 ORDER BY p.name,p.id LIMIT 201`, envs, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	for rows.Next() {
		var id, name string
		var version int
		if err = rows.Scan(&id, &name, &version); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		items = append(items, map[string]any{"id": id, "kind": "prompt", "name": name,
			"version": strconv.Itoa(version), "status": "stored"})
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	truncated := len(items) > 200
	if truncated {
		items = items[:200]
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "truncated": truncated})
}

var promptVariable = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z_0-9]*)\s*\}\}`)

func (s *Service) PromptDetail(w http.ResponseWriter, r *http.Request) {
	p, a, ok := s.promptAgent(w, r, r.PathValue("id"), "view")
	if !ok {
		return
	}
	if p.Version == 0 {
		httpapi.Error(w, 409, "prompt_empty", "Prompt has no persisted version")
		return
	}
	var body string
	err := s.DB.QueryRow(r.Context(), `SELECT body FROM v3_agent_prompt_versions
 WHERE prompt_id=$1 AND version=$2`, p.ID, p.Version).Scan(&body)
	if err != nil {
		unavailable(w)
		return
	}
	variables := make([]string, 0)
	seen := map[string]bool{}
	for _, match := range promptVariable.FindAllStringSubmatch(body, -1) {
		if !seen[match[1]] {
			variables = append(variables, match[1])
			seen[match[1]] = true
		}
	}
	lines := make([]any, 0)
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, map[string]any{"text": line})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": p.ID, "name": p.Name, "version": strconv.Itoa(p.Version),
		"status": "stored", "variables": variables, "used_by": a.Name, "lines": lines, "notes": "",
		"scope": map[string]string{"application_id": a.App, "environment_id": a.Env}})
}

func (s *Service) PromptVersion(w http.ResponseWriter, r *http.Request) {
	p, a, ok := s.promptAgent(w, r, r.PathValue("id"), "change")
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if !bounded(body.Text, 20000) {
		invalid(w, "Prompt must contain 1 to 20000 characters")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	err = tx.QueryRow(r.Context(), `SELECT current_version FROM v3_agent_prompts WHERE id=$1 AND agent_id=$2 FOR UPDATE`, p.ID, a.ID).Scan(&p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.authorizedTx(w, r, tx, a, "change") {
		return
	}
	next := p.Version + 1
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_prompt_versions(prompt_id,version,body,created_by)
 VALUES($1,$2,$3,$4)`, p.ID, next, body.Text, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE v3_agent_prompts SET current_version=$2 WHERE id=$1`, p.ID, next)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.prompt.version", p.ID,
		map[string]any{"environment_id": a.Env, "version": next})
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"version": strconv.Itoa(next)})
}

func (s *Service) PromptTrial(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.promptAgent(w, r, r.PathValue("id"), "change")
	if !ok {
		return
	}
	var body struct {
		Case string `json:"case"`
		A    string `json:"a"`
		B    string `json:"b"`
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if !bounded(body.Case, 128) || !bounded(body.A, 80) || !bounded(body.B, 80) {
		invalid(w, "Case and versions are required")
		return
	}
	httpapi.Error(w, 503, "prompt_trial_unavailable", "A scoped evaluation runner is not connected")
}

func (s *Service) Tools(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "read" && kind != "write" {
		invalid(w, "Unknown tool kind")
		return
	}
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return
	}
	items := make([]any, 0)
	rows, err := s.DB.Query(r.Context(), `SELECT t.id,t.name,t.kind,t.risk,t.approval_required,t.enabled,
 a.name,a.environment_id FROM v3_agent_tools t JOIN v3_agents a ON a.id=t.agent_id
 WHERE a.environment_id=ANY($1) AND ($3='all' OR t.kind=$3) AND `+visibleAgentGrantSQL+`
 ORDER BY t.name,t.id LIMIT 201`, envs, httpapi.CurrentUser(r).ID, kind)
	if err != nil {
		unavailable(w)
		return
	}
	for rows.Next() {
		var id, name, k, risk, agentName, agentEnv string
		var approval, enabled bool
		if err = rows.Scan(&id, &name, &k, &risk, &approval, &enabled, &agentName, &agentEnv); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		approvalText := "read only"
		if approval {
			approvalText = "manual approval required"
		}
		items = append(items, map[string]any{"id": id, "name": name, "kind": k, "risk": risk,
			"approval": approvalText, "agents": agentName, "envs": agentEnv, "calls": unknown, "error": unknown,
			"enabled": enabled, "executable": false})
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	truncated := len(items) > 200
	if truncated {
		items = items[:200]
	}
	limits := make([]any, 0, len(envs))
	visibleEnvs := map[string]bool{}
	envRows, err := s.DB.Query(r.Context(), `SELECT DISTINCT a.environment_id FROM v3_agents a
 WHERE a.environment_id=ANY($1) AND `+visibleAgentGrantSQL, envs, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	for envRows.Next() {
		var env string
		if err = envRows.Scan(&env); err != nil {
			envRows.Close()
			unavailable(w)
			return
		}
		visibleEnvs[env] = true
	}
	if envRows.Err() != nil {
		envRows.Close()
		unavailable(w)
		return
	}
	envRows.Close()
	for _, env := range envs {
		if !visibleEnvs[env] {
			continue
		}
		var max string
		err := s.DB.QueryRow(r.Context(), `SELECT COALESCE((SELECT max_level FROM v3_agent_environment_limits
 WHERE environment_id=$1),'L0')`, env).Scan(&max)
		if err != nil {
			unavailable(w)
			return
		}
		limits = append(limits, map[string]string{"env": env, "max": max, "note": "configured environment cap"})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "levels": []any{}, "env_limits": limits,
		"rules":   []string{"Write tools require a separate proposal, human approval, and executor"},
		"missing": []string{"tool_call_usage", "tool_error_rate"}, "truncated": truncated})
}
