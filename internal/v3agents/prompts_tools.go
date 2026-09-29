package v3agents

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type prompt struct {
	ID, AgentID, Name string
	Version           int
}

func promptDisplayVersion(version int) string { return "v" + strconv.Itoa(version) }

func promptLocalized(r *http.Request, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		return en
	}
	return zh
}

func promptRequestError(w http.ResponseWriter, r *http.Request, code, zh, en string) {
	httpapi.Error(w, 422, code, promptLocalized(r, zh, en))
}

// The shared decoder has English-only errors. Keep the same strict JSON and
// size rules here so Prompt validation can respond in the selected language.
func decodePromptVersion(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		promptRequestError(w, r, "invalid_body", "请求正文必须是有效 JSON", "Invalid JSON body")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		promptRequestError(w, r, "invalid_body", "请求只能包含一个 JSON 对象", "Expected one JSON object")
		return false
	}
	return true
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
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) > 160 {
		promptRequestError(w, r, "invalid_agent_request", "搜索词不能超过 160 个字符", "Prompt search must be at most 160 characters")
		return
	}
	items := make([]any, 0)
	rows, err := s.DB.Query(r.Context(), `SELECT p.id,p.name,p.current_version FROM v3_agent_prompts p
	 JOIN v3_agents a ON a.id=p.agent_id WHERE p.current_version>0
	 AND a.environment_id=ANY($1) AND `+visibleAgentGrantSQL+`
	 AND ($3='' OR strpos(lower(p.name),lower($3))>0 OR strpos(lower(p.id),lower($3))>0)
 ORDER BY p.name,p.id LIMIT 201`, envs, httpapi.CurrentUser(r).ID, query)
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
			"version": promptDisplayVersion(version)})
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	if len(items) > 200 {
		httpapi.Error(w, 413, "prompt_list_too_large", "Filter Prompts with q to return at most 200 items")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "truncated": false,
		"partial": true, "unavailable_fields": []string{"skills", "publication_status"}})
}

var promptVariable = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z_0-9]*)\s*\}\}|\{\s*([a-zA-Z_][a-zA-Z_0-9]*)\s*\}`)

func (s *Service) PromptDetail(w http.ResponseWriter, r *http.Request) {
	p, a, body, ok := s.readPromptVersion(w, r)
	if !ok {
		return
	}
	variables := make([]string, 0)
	seen := map[string]bool{}
	for _, match := range promptVariable.FindAllStringSubmatch(body, -1) {
		name := match[1]
		if name == "" {
			name = match[2]
		}
		if !seen[name] {
			variables = append(variables, name)
			seen[name] = true
		}
	}
	lines := make([]any, 0)
	for _, line := range strings.Split(body, "\n") {
		lines = append(lines, map[string]any{"text": line})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": p.ID, "name": p.Name, "version": promptDisplayVersion(p.Version),
		"variables": variables, "used_by": promptLocalized(r, "运行时使用关系未接入", "Runtime usage unavailable"), "lines": lines,
		"notes": promptLocalized(r, "发布说明未记录", "Release notes unavailable"), "registered_agent": map[string]string{"id": a.ID, "name": a.Name},
		"scope":   map[string]string{"application_id": a.App, "environment_id": a.Env},
		"partial": true, "unavailable_fields": []string{"publication_status", "release_notes", "runtime_usage"}})
}

// readPromptVersion keeps scope, authorization, current-version metadata and
// content in one locked read. A prompt that exists outside the current user's
// scope has the same response as an unknown prompt ID.
func (s *Service) readPromptVersion(w http.ResponseWriter, r *http.Request) (prompt, agent, string, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return prompt{}, agent{}, "", false
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	defer tx.Rollback(r.Context())
	var agentID string
	err = tx.QueryRow(r.Context(), `SELECT agent_id FROM v3_agent_prompts WHERE id=$1`, r.PathValue("id")).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return prompt{}, agent{}, "", false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	a, err := scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, agentID))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return prompt{}, agent{}, "", false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	var p prompt
	err = tx.QueryRow(r.Context(), `SELECT id,agent_id,name,current_version FROM v3_agent_prompts
	 WHERE id=$1 FOR SHARE`, r.PathValue("id")).Scan(&p.ID, &p.AgentID, &p.Name, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (p.AgentID != a.ID || !contains(envs, a.Env))) {
		notFound(w)
		return prompt{}, agent{}, "", false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: "view", EnvironmentID: a.Env, ApplicationID: a.App})
	if errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return prompt{}, agent{}, "", false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	if p.Version == 0 {
		httpapi.Error(w, 409, "prompt_empty", "Prompt has no persisted version")
		return prompt{}, agent{}, "", false
	}
	var body string
	err = tx.QueryRow(r.Context(), `SELECT body FROM v3_agent_prompt_versions
	 WHERE prompt_id=$1 AND version=$2`, p.ID, p.Version).Scan(&body)
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	return p, a, body, true
}

func (s *Service) PromptVersion(w http.ResponseWriter, r *http.Request) {
	p, a, ok := s.promptAgent(w, r, r.PathValue("id"), "change")
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodePromptVersion(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" || len([]rune(body.Text)) > 20000 {
		promptRequestError(w, r, "invalid_agent_request", "提示词必须包含 1 到 20000 个字符", "Prompt must contain 1 to 20000 characters")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	var agentApp, agentEnv string
	err = tx.QueryRow(r.Context(), `SELECT application_id,environment_id FROM v3_agents
 WHERE id=$1 FOR SHARE`, a.ID).Scan(&agentApp, &agentEnv)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (agentApp != a.App || agentEnv != a.Env)) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
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
	httpapi.WriteJSON(w, 201, map[string]any{"version": promptDisplayVersion(next)})
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
	s.readTools(w, r)
}
