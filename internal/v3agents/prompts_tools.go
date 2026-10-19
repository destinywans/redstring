package v3agents

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
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

func (s *Service) promptAgent(w http.ResponseWriter, r *http.Request, id, action string) (prompt, agent, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return prompt{}, agent{}, false
	}
	var p prompt
	err := s.DB.QueryRow(r.Context(), `SELECT id,agent_id,name,current_version FROM v3_agent_prompts WHERE id=$1`, id).
		Scan(&p.ID, &p.AgentID, &p.Name, &p.Version)
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

// Discovery authorization supplies a user-facing object decision before the
// transaction starts. The transaction always reloads and reauthorizes the
// current object; this first check does not grant access to its contents.
func (s *Service) discoverPromptAgent(w http.ResponseWriter, r *http.Request, id string, envs []string, action string) (agent, bool) {
	var agentID string
	err := s.DB.QueryRow(r.Context(), `SELECT agent_id FROM v3_agent_prompts WHERE id=$1`, id).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return agent{}, false
	}
	if err != nil {
		unavailable(w)
		return agent{}, false
	}
	a, err := s.loadAgent(r.Context(), agentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !contains(envs, a.Env)) {
		notFound(w)
		return agent{}, false
	}
	if err != nil {
		unavailable(w)
		return agent{}, false
	}
	err = s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: action, EnvironmentID: a.Env, ApplicationID: a.App})
	if errors.Is(err, store.ErrForbidden) {
		if action == "view" {
			notFound(w)
		} else {
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Agent permission required")
		}
		return agent{}, false
	}
	if err != nil {
		unavailable(w)
		return agent{}, false
	}
	return a, true
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
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if !lockPromptRequest(w, r, tx) {
		return
	}
	// The joined revision is the actual current body. An unversioned catalog
	// row, or a dangling current_version, is not a readable Prompt.
	rows, err := tx.Query(r.Context(), `SELECT p.id,p.name,p.current_version,a.application_id,a.environment_id
	 FROM v3_agent_prompts p JOIN v3_agents a ON a.id=p.agent_id
	 JOIN v3_agent_prompt_versions v ON v.prompt_id=p.id AND v.version=p.current_version
	 JOIN v3_application_environments mapped ON mapped.application_id=a.application_id
	   AND mapped.environment_id=a.environment_id
	 JOIN applications app ON app.id=a.application_id AND NOT app.archived
	 JOIN application_environments ae ON ae.application_id=app.id
	   AND ae.key=a.environment_id AND NOT ae.archived
	 WHERE p.current_version>0 AND a.environment_id=ANY($1) AND `+visibleAgentGrantSQL+`
	 AND ($3='' OR strpos(lower(p.name),lower($3))>0 OR strpos(lower(p.id),lower($3))>0)
 ORDER BY p.name,p.id LIMIT 1001 FOR SHARE OF p,a,v,mapped,app,ae`, envs, httpapi.CurrentUser(r).ID, query)
	if err != nil {
		unavailable(w)
		return
	}
	type listedPrompt struct {
		id, name, app, env string
		version            int
	}
	candidates := make([]listedPrompt, 0, 201)
	for rows.Next() {
		var p listedPrompt
		if err = rows.Scan(&p.id, &p.name, &p.version, &p.app, &p.env); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		candidates = append(candidates, p)
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	if len(candidates) > 1000 {
		httpapi.Error(w, 413, "prompt_list_too_large", "Filter Prompts with q to return at most 200 items")
		return
	}
	items := make([]any, 0, len(candidates))
	activeScopes := make(map[string]bool)
	for _, p := range candidates {
		scopeKey := p.app + "\x00" + p.env
		if !activeScopes[scopeKey] {
			if err = lockActiveEvalScope(r.Context(), tx, agent{App: p.app, Env: p.env}); errors.Is(err, pgx.ErrNoRows) {
				continue
			} else if err != nil {
				unavailable(w)
				return
			}
			activeScopes[scopeKey] = true
		}
		err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
			Module: "agents", Action: "view", EnvironmentID: p.env, ApplicationID: p.app})
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			unavailable(w)
			return
		}
		items = append(items, map[string]any{"id": p.id, "kind": "prompt", "name": p.name,
			"version": promptDisplayVersion(p.version)})
		if len(items) > 200 {
			break
		}
	}
	if len(items) > 200 {
		httpapi.Error(w, 413, "prompt_list_too_large", "Filter Prompts with q to return at most 200 items")
		return
	}
	if !recheckPromptSession(w, r, tx) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
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
		"scope":              map[string]string{"application_id": a.App, "environment_id": a.Env},
		"publication_status": nil, "runtime_usage": nil, "evaluation_score": nil, "evaluation_coverage": nil,
		"partial": true, "unavailable_fields": []string{
			"publication_status", "release_notes", "runtime_usage", "evaluation_score", "evaluation_coverage"}})
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
	discovered, ok := s.discoverPromptAgent(w, r, r.PathValue("id"), envs, "view")
	if !ok {
		return prompt{}, agent{}, "", false
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	defer tx.Rollback(r.Context())
	if !recheckPromptSession(w, r, tx) {
		return prompt{}, agent{}, "", false
	}
	var agentID string
	err = tx.QueryRow(r.Context(), `SELECT agent_id FROM v3_agent_prompts WHERE id=$1`, r.PathValue("id")).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && agentID != discovered.ID) {
		notFound(w)
		return prompt{}, agent{}, "", false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	a, err := scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, agentID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (a.App != discovered.App || a.Env != discovered.Env)) {
		notFound(w)
		return prompt{}, agent{}, "", false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return prompt{}, agent{}, "", false
	} else if err != nil {
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
	if !lockPromptGrantSources(w, r, tx) || !recheckPromptSession(w, r, tx) {
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
	 WHERE prompt_id=$1 AND version=$2 FOR SHARE`, p.ID, p.Version).Scan(&body)
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	if !recheckPromptSession(w, r, tx) {
		return prompt{}, agent{}, "", false
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return prompt{}, agent{}, "", false
	}
	return p, a, body, true
}

func (s *Service) PromptVersion(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	discovered, ok := s.discoverPromptAgent(w, r, r.PathValue("id"), envs, "change")
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
	if !recheckPromptSession(w, r, tx) {
		return
	}
	var agentID string
	err = tx.QueryRow(r.Context(), `SELECT agent_id FROM v3_agent_prompts WHERE id=$1`, r.PathValue("id")).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	a, err := scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, agentID))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!contains(envs, a.Env) || a.App != discovered.App || a.Env != discovered.Env)) {
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
	var p prompt
	err = tx.QueryRow(r.Context(), `SELECT id,agent_id,name,current_version FROM v3_agent_prompts
	 WHERE id=$1 FOR UPDATE`, r.PathValue("id")).Scan(&p.ID, &p.AgentID, &p.Name, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.AgentID != a.ID) {
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
	if !lockPromptGrantSources(w, r, tx) || !recheckPromptSession(w, r, tx) || !s.authorizedTx(w, r, tx, a, "change") {
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
	if !recheckPromptSession(w, r, tx) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"version": promptDisplayVersion(next)})
}

func (s *Service) Tools(w http.ResponseWriter, r *http.Request) {
	s.readTools(w, r)
}
