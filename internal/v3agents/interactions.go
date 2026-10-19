package v3agents

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// These console records do not grant runtime execution or manufacture eval results.
func (s *Service) interactionTx(w http.ResponseWriter, r *http.Request, write bool) (pgx.Tx, []string, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return nil, nil, false
	}
	envs, ok := requestScope(w, r, write)
	if !ok {
		return nil, nil, false
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return nil, nil, false
	}
	if err = lockEvalHumanSession(r.Context(), tx, r); err == nil {
		err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		tx.Rollback(r.Context())
		lifecycleAuthorityError(w, err)
		return nil, nil, false
	}
	return tx, envs, true
}
func (s *Service) interactionPermission(r *http.Request, tx pgx.Tx, env, module, action string) error {
	return s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
}
func (s *Service) CreateTool(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Connector string `json:"connector_id"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !bounded(in.Name, 160) || !bounded(in.Connector, 128) || (in.Kind != "read" && in.Kind != "write") {
		invalid(w, "Tool name, kind and connector required")
		return
	}
	tx, envs, ok := s.interactionTx(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	var env string
	err := tx.QueryRow(r.Context(), `SELECT environment_id FROM v3_connectors WHERE id=$1 FOR SHARE`, in.Connector).Scan(&env)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !contains(envs, env) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	for _, p := range []struct{ module, action string }{{"agents", "manage"}, {"connectors", "view"}} {
		if err = s.interactionPermission(r, tx, env, p.module, p.action); err != nil {
			lifecycleAuthorityError(w, err)
			return
		}
	}
	id := store.ID()
	err = tx.QueryRow(r.Context(), `INSERT INTO v3_agent_tool_catalog(id,environment_id,connector_id,name,kind,created_by) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(environment_id,connector_id,name,kind) DO UPDATE SET name=EXCLUDED.name RETURNING id`, id, env, in.Connector, in.Name, in.Kind, httpapi.CurrentUser(r).ID).Scan(&id)
	if err == nil {
		err = audit(r.Context(), tx, agent{Env: env}, httpapi.CurrentUser(r).ID, "v3.agent.tool.catalog.registered", id, map[string]any{"environment_id": env, "kind": in.Kind, "connector_id": in.Connector, "agent_grants": 0})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"id": id})
}
func (s *Service) CreateDataset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !bounded(in.Name, 160) || (in.Source != "incidents" && in.Source != "runs" && in.Source != "manual") {
		invalid(w, "Dataset name and source required")
		return
	}
	tx, envs, ok := s.interactionTx(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if err := s.interactionPermission(r, tx, envs[0], "agents", "change"); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	id := store.ID()
	_, err := tx.Exec(r.Context(), `INSERT INTO v3_eval_datasets(id,environment_id,name,source,created_by) VALUES($1,$2,$3,$4,$5)`, id, envs[0], in.Name, in.Source, httpapi.CurrentUser(r).ID)
	if err == nil {
		err = audit(r.Context(), tx, agent{Env: envs[0]}, httpapi.CurrentUser(r).ID, "v3.agent.dataset.created", id, map[string]any{"environment_id": envs[0], "source": in.Source})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"id": id})
}

// Caller pins the run before Agent/catalog and authority, matching feedback writes.
func (s *Service) interactionRun(ctx context.Context, tx pgx.Tx, r *http.Request, id string, envs []string, action string) (runRecord, error) {
	v, err := lockRunRecord(ctx, tx, id, false)
	if err != nil {
		return runRecord{}, err
	}
	if !contains(envs, v.Env) {
		return runRecord{}, store.ErrNotFound
	}
	a, err := lockRunAgent(ctx, tx, v)
	if err != nil {
		return runRecord{}, err
	}
	team, err := lockRunActiveScope(ctx, tx, v.App, v.Env)
	if err != nil {
		return runRecord{}, err
	}
	rec := runRecord{Value: v, Agent: a, Team: team}
	return rec, s.authorizeRunTx(ctx, tx, r, rec, action)
}
func (s *Service) DatasetItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RunID string `json:"run_id"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !bounded(in.RunID, 128) || !bounded(r.PathValue("id"), 128) {
		invalid(w, "Dataset and run required")
		return
	}
	tx, envs, ok := s.interactionTx(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	// Dataset lock serializes deduplicated insertion/count. It is never a grant to the run.
	var env string
	err := tx.QueryRow(r.Context(), `SELECT environment_id FROM v3_eval_datasets WHERE id=$1 FOR UPDATE`, r.PathValue("id")).Scan(&env)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !contains(envs, env) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = s.interactionPermission(r, tx, env, "agents", "change"); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	rec, err := s.interactionRun(r.Context(), tx, r, in.RunID, envs, "view")
	if err != nil {
		runReadFailure(w, err)
		return
	}
	if rec.Value.Env != env {
		notFound(w)
		return
	}
	if rec.Value.Status == "running" {
		httpapi.Error(w, 409, "run_active", "Dataset requires a completed run")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_eval_dataset_items(dataset_id,run_id,created_by) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, r.PathValue("id"), in.RunID, httpapi.CurrentUser(r).ID)
	if err == nil {
		err = audit(r.Context(), tx, rec.Agent, httpapi.CurrentUser(r).ID, "v3.agent.dataset.item.added", r.PathValue("id"), map[string]any{"environment_id": env, "run_id": in.RunID})
	}
	if err != nil {
		unavailable(w)
		return
	}
	count, err := s.datasetCount(r, tx, r.PathValue("id"), envs, "")
	if err != nil {
		runReadFailure(w, err)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"count": count})
}
func (s *Service) datasetCount(r *http.Request, tx pgx.Tx, id string, envs []string, agentID string) (int, error) {
	rows, err := tx.Query(r.Context(), `SELECT run_id FROM v3_eval_dataset_items WHERE dataset_id=$1 ORDER BY run_id LIMIT 2001`, id)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			break
		}
		ids = append(ids, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(ids) > 2000 {
		return 0, errRunScopeLimit
	}
	count := 0
	for _, runID := range ids {
		rec, e := s.interactionRun(r.Context(), tx, r, runID, envs, "view")
		if errors.Is(e, store.ErrForbidden) || errors.Is(e, store.ErrNotFound) || errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return 0, e
		}
		if agentID == "" || rec.Value.AgentID == agentID {
			count++
		}
	}
	return count, nil
}
func (s *Service) Datasets(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if len(r.URL.Query()["agent_id"]) > 1 || agentID != "" && !bounded(agentID, 128) {
		invalid(w, "Select one Agent ID")
		return
	}
	tx, envs, ok := s.interactionTx(w, r, false)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), `SELECT id,environment_id,name,source FROM v3_eval_datasets WHERE environment_id=ANY($1) AND EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$2 AND g.module='agents' AND g.action='view' AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=environment_id))) ORDER BY created_at,id LIMIT 201 FOR SHARE`, envs, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	type record struct{ id, env, name, source string }
	records := []record{}
	for rows.Next() {
		var v record
		if err = rows.Scan(&v.id, &v.env, &v.name, &v.source); err != nil {
			break
		}
		records = append(records, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if len(records) > 200 {
		httpapi.Error(w, 413, "dataset_scope_too_large", "Too many datasets")
		return
	}
	items := []any{}
	for _, v := range records {
		if err = s.interactionPermission(r, tx, v.env, "agents", "view"); errors.Is(err, store.ErrForbidden) {
			continue
		} else if err != nil {
			unavailable(w)
			return
		}
		count, e := s.datasetCount(r, tx, v.id, envs, agentID)
		if e != nil {
			runReadFailure(w, e)
			return
		}
		if agentID != "" && count == 0 {
			continue
		}
		items = append(items, map[string]any{"id": v.id, "name": v.name, "source": v.source, "count": count})
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}
func (s *Service) AnnotateRun(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Score int    `json:"score"`
		Note  string `json:"note"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.Score < 1 || in.Score > 5 || len([]rune(in.Note)) > 2000 {
		invalid(w, "Score must be 1..5 and note bounded")
		return
	}
	tx, envs, ok := s.interactionTx(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	rec, err := s.interactionRun(r.Context(), tx, r, r.PathValue("id"), envs, "change")
	if err != nil {
		runReadFailure(w, err)
		return
	}
	if rec.Value.Status == "running" {
		httpapi.Error(w, 409, "run_active", "Annotation requires a completed run")
		return
	}
	id := store.ID()
	var created time.Time
	err = tx.QueryRow(r.Context(), `INSERT INTO v3_agent_run_annotations(id,run_id,actor_id,score,note) VALUES($1,$2,$3,$4,$5) RETURNING created_at`, id, rec.Value.ID, httpapi.CurrentUser(r).ID, in.Score, in.Note).Scan(&created)
	if err == nil {
		err = audit(r.Context(), tx, rec.Agent, httpapi.CurrentUser(r).ID, "v3.agent.run.annotated", id, map[string]any{"environment_id": rec.Value.Env, "run_id": rec.Value.ID, "score": in.Score})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"id": id, "run_id": rec.Value.ID, "score": in.Score, "note": in.Note, "created_at": created})
}
func (s *Service) PromptVersions(w http.ResponseWriter, r *http.Request) {
	tx, envs, ok := s.interactionTx(w, r, false)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	var p prompt
	err := tx.QueryRow(r.Context(), `SELECT id,agent_id,name,current_version FROM v3_agent_prompts WHERE id=$1 FOR SHARE`, r.PathValue("id")).Scan(&p.ID, &p.AgentID, &p.Name, &p.Version)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	a, err := scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, p.AgentID))
	if err != nil || !contains(envs, a.Env) {
		notFound(w)
		return
	}
	team, err := lockRunActiveScope(r.Context(), tx, a.App, a.Env)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	if err = s.authorizeEvalTx(r.Context(), tx, r, a, team, "view"); err != nil {
		runReadFailure(w, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT version,created_at,created_by FROM v3_agent_prompt_versions WHERE prompt_id=$1 ORDER BY version DESC LIMIT 1001 FOR SHARE`, p.ID)
	if err != nil {
		unavailable(w)
		return
	}
	items := []any{}
	for rows.Next() {
		var v int
		var at time.Time
		var author string
		if err = rows.Scan(&v, &at, &author); err != nil {
			break
		}
		items = append(items, map[string]any{"version": promptDisplayVersion(v), "created_at": at, "author": author, "current": v == p.Version})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if len(items) > 1000 {
		httpapi.Error(w, 413, "prompt_history_too_large", "Too many Prompt versions")
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (s *Service) readToolCatalog(r *http.Request, tx pgx.Tx, envs []string, kind string, facets map[string][]string) ([]any, error) {
	items := []any{}
	if len(facets["agent"]) > 0 {
		return items, nil
	}
	rows, err := tx.Query(r.Context(), `SELECT t.id,t.environment_id,t.connector_id,t.name,t.kind,c.provider FROM v3_agent_tool_catalog t JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id WHERE t.environment_id=ANY($1) AND ($2='all' OR t.kind=$2) AND (cardinality($3::text[])=0 OR t.connector_id=ANY($3)) AND EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$4 AND g.module='agents' AND g.action='view' AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=t.environment_id))) AND EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$4 AND g.module='connectors' AND g.action='view' AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=t.environment_id))) ORDER BY t.name,t.id LIMIT 1001 FOR SHARE OF t,c`, envs, kind, facets["connector"], httpapi.CurrentUser(r).ID)
	if err != nil {
		return nil, err
	}
	type item struct{ id, env, connector, name, kind, brand string }
	records := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.env, &v.connector, &v.name, &v.kind, &v.brand); err != nil {
			break
		}
		records = append(records, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(records) > 1000 {
		return nil, errRunScopeLimit
	}
	for _, v := range records {
		visible := true
		for _, module := range []string{"agents", "connectors"} {
			err = s.interactionPermission(r, tx, v.env, module, "view")
			if errors.Is(err, store.ErrForbidden) {
				visible = false
				break
			}
			if err != nil {
				return nil, err
			}
		}
		if !visible {
			continue
		}
		approval := promptLocalized(r, "无审批要求", "No approval configured")
		risk := "low"
		if v.kind == "write" {
			approval = promptLocalized(r, "需人工批准", "Human approval required")
			risk = "high"
		}
		items = append(items, map[string]any{"id": v.id, "name": v.name, "kind": v.kind, "risk": risk, "approval": approval, "agents": "", "envs": v.env, "calls": "—", "error": "—", "agent_id": "", "application_id": "", "environment_id": v.env, "brand": v.brand, "connector_id": v.connector, "registered_enabled": false, "executable": false, "execution_status": "not_connected", "metrics_basis": "recorded_run_spans_only", "metrics_complete": false, "catalog_only": true})
	}
	return items, nil
}
