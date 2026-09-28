package v3agents

import (
	"net/http"
	"strconv"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

const unknown = "未接入"

func agentStatus(a agent) string {
	if a.Paused {
		return "paused"
	}
	if a.Canary != "" {
		return "canary"
	}
	return "stable"
}

func summary(a agent) map[string]any {
	return map[string]any{
		"id": a.ID, "icon": "bot", "name": a.Name, "desc": a.Summary,
		"version": a.Stable, "level": a.Level, "status": agentStatus(a),
		"score": nil, "runs_7d": unknown, "success": unknown, "avg_tokens": unknown,
		"available":     map[string]bool{"evaluation": false, "usage": false},
		"status_source": "configured_release", "routing_connected": false,
	}
}

func (s *Service) visibleAgents(w http.ResponseWriter, r *http.Request) ([]agent, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return nil, false
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return nil, false
	}
	rows, err := s.DB.Query(r.Context(), `SELECT `+agentColumns+` FROM v3_agents a
 WHERE a.environment_id=ANY($1) AND `+visibleAgentGrantSQL+`
 ORDER BY a.name,a.id LIMIT 201`, envs, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return nil, false
	}
	items := make([]agent, 0)
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			rows.Close()
			unavailable(w)
			return nil, false
		}
		items = append(items, a)
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return nil, false
	}
	rows.Close()
	return items, true
}

func (s *Service) Lifecycle(w http.ResponseWriter, r *http.Request) {
	items, ok := s.visibleAgents(w, r)
	if !ok {
		return
	}
	agents := make([]any, 0, len(items))
	for i, a := range items {
		if i == 200 {
			break
		}
		agents = append(agents, summary(a))
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"stages": []any{}, "agents": agents, "todos": []any{},
		"missing": []string{"evaluation_scores", "run_usage", "lifecycle_stage_aggregate"},
		"partial": true, "truncated": len(items) > 200,
	})
}

func (s *Service) AgentDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := s.objectAgent(w, r, r.PathValue("id"), "view")
	if !ok {
		return
	}
	rows, err := s.DB.Query(r.Context(), `SELECT v.version,v.created_at,EXISTS(
 SELECT 1 FROM v3_agent_release_events e WHERE e.agent_id=v.agent_id
 AND e.to_version=v.version AND e.percent=100) FROM v3_agent_versions v
 WHERE v.agent_id=$1 ORDER BY v.created_at DESC,v.version DESC LIMIT 100`, a.ID)
	if err != nil {
		unavailable(w)
		return
	}
	defer rows.Close()
	versions := make([]any, 0)
	for rows.Next() {
		var v string
		var created time.Time
		var everStable bool
		if err = rows.Scan(&v, &created, &everStable); err != nil {
			unavailable(w)
			return
		}
		status := "draft"
		if everStable {
			status = "retired"
		}
		configuredTraffic := "0%"
		if v == a.Stable {
			status = "stable"
			configuredTraffic = "100%"
			if a.Canary != "" {
				configuredTraffic = percent(100 - a.Percent)
			}
		}
		if v == a.Canary {
			status = "canary"
			configuredTraffic = percent(a.Percent)
		}
		versions = append(versions, map[string]any{"version": v, "status": status,
			"traffic": unknown, "configured_traffic": configuredTraffic, "routing_connected": false,
			"score": unknown, "success": unknown, "tokens": unknown, "created": timeString(created)})
	}
	if rows.Err() != nil {
		unavailable(w)
		return
	}
	out := summary(a)
	out["owner"] = a.Owner
	out["account"] = unknown
	out["summary"] = a.Summary
	out["versions"] = versions
	out["release"] = map[string]any{"from": a.Stable, "to": a.Canary, "current": nil,
		"target": a.Percent, "configured_canary_percent": a.Percent, "observed_traffic": nil,
		"routing_connected": false, "gates": []any{}, "tokens": nil,
		"missing": []string{"observed_traffic", "eval_gate_detail", "token_comparison"}}
	out["diff"] = []any{}
	out["config"] = []any{}
	out["scope"] = map[string]string{"application_id": a.App, "environment_id": a.Env}
	httpapi.WriteJSON(w, 200, out)
}

func percent(v int) string { return strconv.Itoa(v) + "%" }

type rolloutBody struct {
	To             string `json:"to"`
	Percent        int    `json:"percent"`
	Override       bool   `json:"override"`
	OverrideReason string `json:"override_reason"`
}

func (s *Service) Rollout(w http.ResponseWriter, r *http.Request) {
	a, ok := s.objectAgent(w, r, r.PathValue("id"), "manage")
	if !ok {
		return
	}
	var body rolloutBody
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if !bounded(body.To, 80) || body.Percent < 1 || body.Percent > 100 || body.To == a.Stable ||
		(body.Override && !bounded(body.OverrideReason, 500)) || (!body.Override && body.OverrideReason != "") {
		invalid(w, "Select another known version, a percentage from 1 to 100, and a reason for override")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	a, err = scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR UPDATE`, a.ID))
	if err != nil {
		unavailable(w)
		return
	}
	if !s.authorizedTx(w, r, tx, a, "manage") {
		return
	}
	if body.Override {
		var admin bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(
	 SELECT 1 FROM users u WHERE u.id=$1 AND u.v3_identity_kind='human'
	 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo'
	 AND (EXISTS (SELECT 1 FROM rbac_user_roles ur
	   WHERE ur.user_id=u.id AND ur.role_id='admin') OR
	      EXISTS (SELECT 1 FROM rbac_department_roles dr
	   WHERE dr.department_id=u.department_id AND dr.role_id='admin'))
	)`, httpapi.CurrentUser(r).ID).Scan(&admin)
		if err != nil {
			unavailable(w)
			return
		}
		if !admin {
			httpapi.Error(w, 403, "admin_override_required", "Human administrator required for gate override")
			return
		}
	}
	if body.To == a.Stable {
		httpapi.Error(w, 409, "already_stable", "Version is already stable")
		return
	}
	var exists bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_agent_versions WHERE agent_id=$1 AND version=$2)`, a.ID, body.To).Scan(&exists)
	if err != nil {
		unavailable(w)
		return
	}
	if !exists {
		notFound(w)
		return
	}
	var gateID string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT CASE WHEN status='passed' THEN id ELSE '' END
 FROM v3_agent_eval_experiments WHERE agent_id=$1 AND candidate_version=$2
 AND baseline_version=$3 ORDER BY created_at DESC,id DESC LIMIT 1),'')`,
		a.ID, body.To, a.Stable).Scan(&gateID)
	if err != nil {
		unavailable(w)
		return
	}
	if gateID == "" && !body.Override {
		httpapi.Error(w, 409, "eval_gate_unverified", "A persisted passing evaluation is required")
		return
	}
	from := a.Stable
	if body.Percent == 100 {
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET previous_stable_version=stable_version,
 stable_version=$2,canary_version=NULL,canary_percent=0,paused=false,updated_at=now() WHERE id=$1`, a.ID, body.To)
	} else {
		if a.Stable == "" {
			httpapi.Error(w, 409, "stable_version_required", "Set a stable version before canary rollout")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET canary_version=$2,canary_percent=$3,
 paused=false,updated_at=now() WHERE id=$1`, a.ID, body.To, body.Percent)
	}
	if err != nil {
		unavailable(w)
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_release_events
 (id,agent_id,from_version,to_version,percent,kind,gate_experiment_id,override_reason,actor_id)
 VALUES($1,$2,$3,$4,$5,'rollout',NULLIF($6,''),NULLIF($7,''),$8)`, id, a.ID, from, body.To, body.Percent, gateID, body.OverrideReason, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.rollout", a.ID,
		map[string]any{"environment_id": a.Env, "from": from, "to": body.To, "percent": body.Percent,
			"gate_experiment_id": gateID, "override": body.Override})
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": id, "to": body.To,
		"configured_percent": body.Percent, "observed_traffic": nil, "routing_connected": false,
		"gate_experiment_id": gateID, "override": body.Override})
}

func (s *Service) Rollback(w http.ResponseWriter, r *http.Request) {
	a, ok := s.objectAgent(w, r, r.PathValue("id"), "manage")
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	a, err = scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR UPDATE`, a.ID))
	if err != nil {
		unavailable(w)
		return
	}
	if !s.authorizedTx(w, r, tx, a, "manage") {
		return
	}
	from := a.Stable
	to := a.Previous
	if a.Canary != "" {
		from = a.Canary
		to = a.Stable
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET canary_version=NULL,canary_percent=0,updated_at=now() WHERE id=$1`, a.ID)
	} else if to != "" {
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET stable_version=$2,previous_stable_version=$3,
 updated_at=now() WHERE id=$1`, a.ID, to, a.Stable)
	} else {
		httpapi.Error(w, 409, "rollback_unavailable", "No prior persisted version exists")
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_release_events
 (id,agent_id,from_version,to_version,percent,kind,actor_id)
 VALUES($1,$2,$3,$4,0,'rollback',$5)`, id, a.ID, from, to, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.rollback", a.ID,
		map[string]any{"environment_id": a.Env, "from": from, "to": to})
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": id, "to": to})
}

func levelNumber(level string) int {
	switch level {
	case "L0":
		return 0
	case "L1":
		return 1
	case "L2":
		return 2
	case "L3":
		return 3
	}
	return -1
}

func (s *Service) Level(w http.ResponseWriter, r *http.Request) {
	a, ok := s.objectAgent(w, r, r.PathValue("id"), "manage")
	if !ok {
		return
	}
	var body struct {
		Level string `json:"level"`
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if levelNumber(body.Level) < 0 {
		invalid(w, "Unknown Agent level")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	a, err = scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR UPDATE`, a.ID))
	if err != nil {
		unavailable(w)
		return
	}
	if !s.authorizedTx(w, r, tx, a, "manage") {
		return
	}
	var max string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT max_level FROM v3_agent_environment_limits
 WHERE environment_id=$1),'L0')`, a.Env).Scan(&max)
	if err != nil {
		unavailable(w)
		return
	}
	if levelNumber(body.Level) > levelNumber(max) {
		httpapi.Error(w, 409, "agent_level_cap", "Environment level cap would be exceeded")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET level=$2,updated_at=now() WHERE id=$1`, a.ID, body.Level)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.level", a.ID,
		map[string]any{"environment_id": a.Env, "from": a.Level, "to": body.Level, "cap": max})
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"level": body.Level, "max_level": max})
}
