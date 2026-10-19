package v3agents

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type registeredTool struct {
	ID, Name, Kind, Risk, AgentID, AgentName, ApplicationID, EnvironmentID, Brand string
	ApprovalRequired, Enabled                                                     bool
}

// readTools separates registered governance metadata from runtime evidence.
// RegisterTool and connector preferences alone never prove that an Agent can
// execute a tool. Only spans reported by the trusted in-process RecordSpan
// producer contribute to the observed seven-day usage columns.
func (s *Service) readTools(w http.ResponseWriter, r *http.Request) {
	facets, valid := interactionFacets(r.URL.Query(), "connector", "agent")
	if !valid {
		invalid(w, "Invalid tool facets")
		return
	}
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
	// READ COMMITTED plus locks on the physical session/grant sources makes a
	// revoke that committed before this read visible, while a later revoke
	// waits for the response's final authorization transaction to finish.
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Agent permission required")
		return
	} else if err != nil {
		unavailable(w)
		return
	}

	rows, err := tx.Query(r.Context(), `SELECT t.id,t.name,t.kind,t.risk,t.approval_required,t.enabled,
 a.id,a.name,a.application_id,a.environment_id,COALESCE(c.provider,'')
 FROM v3_agent_tools t JOIN v3_agents a ON a.id=t.agent_id
 LEFT JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=a.environment_id
 WHERE a.environment_id=ANY($1) AND ($3='all' OR t.kind=$3)
 AND (cardinality($4::text[])=0 OR t.connector_id=ANY($4))
 AND (cardinality($5::text[])=0 OR a.id=ANY($5)) AND `+visibleAgentGrantSQL+`
 ORDER BY t.name,t.id LIMIT 1001 FOR SHARE OF t,a`, envs, httpapi.CurrentUser(r).ID, kind, facets["connector"], facets["agent"])
	if err != nil {
		unavailable(w)
		return
	}
	tools := make([]registeredTool, 0, 1001)
	for rows.Next() {
		var tool registeredTool
		if err = rows.Scan(&tool.ID, &tool.Name, &tool.Kind, &tool.Risk,
			&tool.ApprovalRequired, &tool.Enabled, &tool.AgentID, &tool.AgentName,
			&tool.ApplicationID, &tool.EnvironmentID, &tool.Brand); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		tools = append(tools, tool)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	if len(tools) > 1000 {
		httpapi.Error(w, http.StatusRequestEntityTooLarge, "agent_tool_scope_too_large", "Selected scope contains too many registered tools")
		return
	}

	// The SQL guard prunes candidates before the bounded scan. Count after
	// authoritative policy checks so hidden candidates cannot obscure later
	// visible tools or falsely set truncated.
	visible := make([]registeredTool, 0, len(tools))
	activeScopes := make(map[string]bool)
	inactiveScopes := make(map[string]bool)
	for _, tool := range tools {
		scopeKey := tool.ApplicationID + "\x00" + tool.EnvironmentID
		if inactiveScopes[scopeKey] {
			continue
		}
		if !activeScopes[scopeKey] {
			err = lockActiveEvalScope(r.Context(), tx, agent{App: tool.ApplicationID, Env: tool.EnvironmentID})
			if errors.Is(err, pgx.ErrNoRows) {
				inactiveScopes[scopeKey] = true
				continue
			}
			if err != nil {
				unavailable(w)
				return
			}
			activeScopes[scopeKey] = true
		}
		err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
			Module: "agents", Action: "view", ApplicationID: tool.ApplicationID,
			EnvironmentID: tool.EnvironmentID})
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			unavailable(w)
			return
		}
		visible = append(visible, tool)
	}
	truncated := len(visible) > 200
	if truncated {
		visible = visible[:200]
	}
	toolIDs := make([]string, 0, len(visible))
	for _, tool := range visible {
		toolIDs = append(toolIDs, tool.ID)
	}

	// A span is only an observed invocation. It does not establish complete
	// instrumentation or a present executable binding. Blocked/unknown spans
	// are excluded from the executed-call denominator.
	type usage struct{ calls, failed int64 }
	observed := make(map[string]usage, len(toolIDs))
	if len(toolIDs) != 0 {
		usageRows, queryErr := tx.Query(r.Context(), `SELECT t.id,
 COUNT(*) FILTER (WHERE sp.status IN ('ok','failed')),
 COUNT(*) FILTER (WHERE sp.status='failed')
 FROM v3_agent_run_spans sp JOIN v3_agent_runs gr ON gr.id=sp.run_id
 JOIN v3_agent_tools t ON t.id=sp.tool_id AND t.agent_id=gr.agent_id
 JOIN v3_agents a ON a.id=t.agent_id
 AND a.application_id=gr.application_id AND a.environment_id=gr.environment_id
 WHERE gr.started_at>=t.created_at AND sp.started_at>=gr.started_at
 AND sp.kind='tool' AND t.id=ANY($1)
 AND sp.started_at>=now()-interval '7 days' AND sp.started_at<=now()
 GROUP BY t.id`, toolIDs)
		if queryErr != nil {
			unavailable(w)
			return
		}
		for usageRows.Next() {
			var toolID string
			var calls, failed int64
			if err = usageRows.Scan(&toolID, &calls, &failed); err != nil {
				usageRows.Close()
				unavailable(w)
				return
			}
			observed[toolID] = usage{calls: calls, failed: failed}
		}
		if err = usageRows.Err(); err != nil {
			usageRows.Close()
			unavailable(w)
			return
		}
		usageRows.Close()
	}

	// An environment with a visible Agent but no registered tool still has a
	// meaningful persisted cap. Check every candidate application before
	// deduplicating: one denied application must not hide another visible one.
	envRows, err := tx.Query(r.Context(), `SELECT DISTINCT
 a.application_id,a.environment_id FROM v3_agents a
 WHERE a.environment_id=ANY($1) AND `+visibleAgentGrantSQL+`
 ORDER BY a.environment_id,a.application_id LIMIT 1001`, envs, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	type envOwner struct{ app, env string }
	owners := make([]envOwner, 0, len(envs))
	for envRows.Next() {
		var owner envOwner
		if err = envRows.Scan(&owner.app, &owner.env); err != nil {
			envRows.Close()
			unavailable(w)
			return
		}
		owners = append(owners, owner)
	}
	if err = envRows.Err(); err != nil {
		envRows.Close()
		unavailable(w)
		return
	}
	envRows.Close()
	if len(owners) > 1000 {
		httpapi.Error(w, http.StatusRequestEntityTooLarge, "agent_tool_scope_too_large", "Selected scope contains too many Agent applications")
		return
	}
	limits := make([]any, 0, len(owners))
	visibleEnvs := make(map[string]bool, len(envs))
	for _, owner := range owners {
		if visibleEnvs[owner.env] {
			continue
		}
		var agentID string
		err = tx.QueryRow(r.Context(), `SELECT id FROM v3_agents
 WHERE application_id=$1 AND environment_id=$2 ORDER BY id LIMIT 1 FOR SHARE`,
			owner.app, owner.env).Scan(&agentID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			unavailable(w)
			return
		}
		scopeKey := owner.app + "\x00" + owner.env
		if inactiveScopes[scopeKey] {
			continue
		}
		if !activeScopes[scopeKey] {
			err = lockActiveEvalScope(r.Context(), tx, agent{App: owner.app, Env: owner.env})
			if errors.Is(err, pgx.ErrNoRows) {
				inactiveScopes[scopeKey] = true
				continue
			}
			if err != nil {
				unavailable(w)
				return
			}
			activeScopes[scopeKey] = true
		}
		err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
			Module: "agents", Action: "view", ApplicationID: owner.app,
			EnvironmentID: owner.env})
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			unavailable(w)
			return
		}
		visibleEnvs[owner.env] = true
		var max string
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT max_level
 FROM v3_agent_environment_limits WHERE environment_id=$1),'L0')`, owner.env).Scan(&max); err != nil {
			unavailable(w)
			return
		}
		limits = append(limits, map[string]string{
			"env": owner.env, "max": max,
			"note": promptLocalized(r, "已配置的环境上限；不代表执行器已接入", "Configured cap; executor availability is separate"),
		})
	}
	catalogItems, err := s.readToolCatalog(r, tx, envs, kind, facets)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	// Session expiry uses wall-clock time; a long bounded read may outlive it.
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}

	items := append(make([]any, 0, len(visible)+len(catalogItems)), catalogItems...)
	for _, tool := range visible {
		calls, errorRate := "—", "—"
		if u, found := observed[tool.ID]; found && u.calls > 0 {
			calls = strconv.FormatInt(u.calls, 10)
			errorRate = strconv.FormatFloat(100*float64(u.failed)/float64(u.calls), 'f', 1, 64) + "%"
		}
		approval := promptLocalized(r, "无审批要求", "No approval configured")
		if tool.ApprovalRequired {
			approval = promptLocalized(r, "需人工批准", "Human approval required")
		}
		item := map[string]any{
			"id": tool.ID, "name": tool.Name, "kind": tool.Kind, "risk": tool.Risk,
			"approval": approval, "agents": tool.AgentName, "envs": tool.EnvironmentID,
			"calls": calls, "error": errorRate,
			"agent_id": tool.AgentID, "application_id": tool.ApplicationID,
			"environment_id": tool.EnvironmentID, "registered_enabled": tool.Enabled,
			"executable": false, "execution_status": "not_connected",
			"metrics_basis": "recorded_run_spans_only", "metrics_complete": false,
		}
		if tool.Brand != "" {
			item["brand"] = tool.Brand
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].(map[string]any), items[j].(map[string]any)
		if a["name"].(string) != b["name"].(string) {
			return a["name"].(string) < b["name"].(string)
		}
		return a["id"].(string) < b["id"].(string)
	})
	if len(items) > 200 {
		items = items[:200]
		truncated = true
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "levels": toolLevelDescriptions(r), "env_limits": limits,
		"rules": []string{
			promptLocalized(r, "写工具只登记提案权限；动作须经人工批准并由独立执行器实施", "Write tools only describe proposal permissions; actions require human approval and an independent executor"),
			promptLocalized(r, "工具输出视为不可信数据", "Tool output is untrusted data"),
		},
		"partial": true, "truncated": truncated, "execution_available": false,
		"missing": []string{"runtime_tool_binding", "tool_call_coverage", "automatic_execution_policy"},
	})
}

func toolLevelDescriptions(r *http.Request) []any {
	return []any{
		map[string]string{"code": "L0", "name": promptLocalized(r, "仅建议", "Advice only"),
			"desc": promptLocalized(r, "不授予工具执行能力", "No tool execution is granted")},
		map[string]string{"code": "L1", "name": promptLocalized(r, "只读取证", "Read evidence"),
			"desc": promptLocalized(r, "只读工具仍需受审核运行时接线", "Read tools still require a reviewed runtime binding")},
		map[string]string{"code": "L2", "name": promptLocalized(r, "可提案", "Can propose"),
			"desc": promptLocalized(r, "提案执行须经人工批准并使用独立执行器", "Executing a write proposal requires human approval and an independent executor")},
		map[string]string{"code": "L3", "name": promptLocalized(r, "受限自动化上限", "Limited automation cap"),
			"desc": promptLocalized(r, "仅配置上限；自动执行当前未接入", "Configuration cap only; automatic execution is not connected")},
	}
}
