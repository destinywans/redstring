package v3agents

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// Other Agent projections still use this explicit unknown marker while their
// own producer contracts are being completed.
const unknown = "未接入"

// Lifecycle state is persisted release configuration. There is no traffic
// router or usage producer attached to this service.
type lifecycleAgent struct {
	agent
	Revision int64
	Team     string
}

const lifecycleColumns = `a.id,a.application_id,a.environment_id,a.name,a.summary,
 COALESCE(a.owner_id,''),a.level,COALESCE(a.stable_version,''),
 COALESCE(a.previous_stable_version,''),COALESCE(a.canary_version,''),
 a.canary_percent,a.paused,a.created_at,a.updated_at,a.lifecycle_revision,
 COALESCE(app.owner_team_id,'')`

const lifecycleScopeJoin = ` FROM v3_agents a
 JOIN applications app ON app.id=a.application_id AND NOT app.archived
 JOIN v3_application_environments mapped ON mapped.application_id=a.application_id
   AND mapped.environment_id=a.environment_id
 JOIN application_environments env ON env.application_id=a.application_id
   AND env.key=a.environment_id AND NOT env.archived`

// Prefilter before LIMIT so hidden Agents cannot displace visible ones. The
// authoritative per-row policy check still runs after physical grant locks.
const visibleLifecycleGrants = `EXISTS (SELECT 1 FROM v3_effective_role_grants e
 WHERE e.user_id=$2 AND e.module='environment' AND e.action='view'
 AND ((e.scope_kind='global' AND e.scope_id='*') OR
      (e.scope_kind='environment' AND e.scope_id=a.environment_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$2 AND g.module='agents' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR
      (g.scope_kind='environment' AND g.scope_id=a.environment_id) OR
      (g.scope_kind='application' AND g.scope_id=a.application_id) OR
      (app.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=app.owner_team_id)))`

func scanLifecycleAgent(row pgx.Row) (lifecycleAgent, error) {
	var a lifecycleAgent
	err := row.Scan(&a.ID, &a.App, &a.Env, &a.Name, &a.Summary, &a.Owner, &a.Level,
		&a.Stable, &a.Previous, &a.Canary, &a.Percent, &a.Paused, &a.Created, &a.Updated,
		&a.Revision, &a.Team)
	return a, err
}

func loadLifecycleAgent(ctx context.Context, tx pgx.Tx, id string, write bool) (lifecycleAgent, error) {
	lock := ` FOR SHARE OF a,app,mapped,env`
	if write {
		lock = ` FOR UPDATE OF a FOR SHARE OF app,mapped,env`
	}
	return scanLifecycleAgent(tx.QueryRow(ctx, `SELECT `+lifecycleColumns+lifecycleScopeJoin+
		` WHERE a.id=$1`+lock, id))
}

func lifecycleSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	return store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID)
}

func lifecycleAuthorityError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	} else if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Agent permission required")
	} else {
		unavailable(w)
	}
}

func (s *Service) authorizeLifecycle(ctx context.Context, tx pgx.Tx, r *http.Request, a lifecycleAgent, action string) error {
	decision := rbac.V3Decision{Module: "agents", Action: action,
		EnvironmentID: a.Env, ApplicationID: a.App}
	err := s.Access.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(r), decision)
	if errors.Is(err, store.ErrForbidden) && a.Team != "" {
		decision.ApplicationID = ""
		decision.TeamID = a.Team
		err = s.Access.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(r), decision)
	}
	return err
}

func (s *Service) beginLifecycleRead(w http.ResponseWriter, r *http.Request) (pgx.Tx, []string, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return nil, nil, false
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return nil, nil, false
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return nil, nil, false
	}
	if err = lifecycleSession(r.Context(), tx, r); err == nil {
		err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		_ = tx.Rollback(r.Context())
		lifecycleAuthorityError(w, err)
		return nil, nil, false
	}
	return tx, envs, true
}

func (s *Service) readLifecycleObject(w http.ResponseWriter, r *http.Request, tx pgx.Tx, envs []string) (lifecycleAgent, bool) {
	a, err := loadLifecycleAgent(r.Context(), tx, r.PathValue("id"), false)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !contains(envs, a.Env)) {
		notFound(w)
		return lifecycleAgent{}, false
	}
	if err != nil {
		unavailable(w)
		return lifecycleAgent{}, false
	}
	if err = s.authorizeLifecycle(r.Context(), tx, r, a, "view"); errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return lifecycleAgent{}, false
	} else if err != nil {
		unavailable(w)
		return lifecycleAgent{}, false
	}
	return a, true
}

func agentStatus(a agent) string {
	if a.Paused {
		return "paused"
	}
	if a.Canary != "" {
		return "canary"
	}
	if a.Stable == "" {
		return "unconfigured"
	}
	return "stable"
}

func summary(a lifecycleAgent) map[string]any {
	return map[string]any{
		"id": a.ID, "icon": "bot", "name": a.Name, "desc": a.Summary,
		"version": a.Stable, "level": a.Level, "status": agentStatus(a.agent),
		"revision": a.Revision, "score": nil, "runs_7d": nil, "success": nil, "avg_tokens": nil,
		"available":     map[string]bool{"evaluation": false, "usage": false},
		"status_source": "configured_release", "routing_connected": false,
	}
}

func (s *Service) Lifecycle(w http.ResponseWriter, r *http.Request) {
	tx, envs, ok := s.beginLifecycleRead(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), `SELECT `+lifecycleColumns+lifecycleScopeJoin+
		` WHERE a.environment_id=ANY($1) AND `+visibleLifecycleGrants+
		` ORDER BY a.name,a.id LIMIT 201 FOR SHARE OF a,app,mapped,env`,
		envs, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	items := make([]lifecycleAgent, 0, 201)
	for rows.Next() {
		a, scanErr := scanLifecycleAgent(rows)
		if scanErr != nil {
			rows.Close()
			unavailable(w)
			return
		}
		items = append(items, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	agents := make([]any, 0, len(items))
	for _, a := range items {
		err = s.authorizeLifecycle(r.Context(), tx, r, a, "view")
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			unavailable(w)
			return
		}
		if len(agents) == 200 {
			break
		}
		agents = append(agents, summary(a))
	}
	if err = lifecycleSession(r.Context(), tx, r); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"stages": nil, "agents": agents, "todos": nil,
		"missing": []string{"evaluation_scores", "run_usage", "lifecycle_stage_aggregate", "lifecycle_todos"},
		"partial": true, "truncated": len(items) > 200,
	})
}

func (s *Service) AgentDetail(w http.ResponseWriter, r *http.Request) {
	tx, envs, ok := s.beginLifecycleRead(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	a, ok := s.readLifecycleObject(w, r, tx, envs)
	if !ok {
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT v.version,v.created_at,EXISTS(
 SELECT 1 FROM v3_agent_release_events e WHERE e.agent_id=v.agent_id
 AND e.to_version=v.version AND e.percent=100) FROM v3_agent_versions v
 WHERE v.agent_id=$1 ORDER BY v.created_at DESC,v.version DESC LIMIT 101`, a.ID)
	if err != nil {
		unavailable(w)
		return
	}
	versions := make([]any, 0)
	truncated := false
	for rows.Next() {
		var v string
		var created time.Time
		var everStable bool
		if err = rows.Scan(&v, &created, &everStable); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		if len(versions) == 100 {
			truncated = true
			continue
		}
		status := "draft"
		if everStable {
			status = "retired"
		}
		configuredTraffic := "0%"
		if v == a.Stable {
			status = "stable"
			configuredTraffic = percent(100 - a.Percent)
		}
		if v == a.Canary {
			status = "canary"
			configuredTraffic = percent(a.Percent)
		}
		versions = append(versions, map[string]any{"version": v, "status": status,
			"traffic": nil, "configured_traffic": configuredTraffic, "routing_connected": false,
			"score": nil, "success": nil, "tokens": nil, "created": timeString(created)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	out := summary(a)
	out["owner"] = a.Owner
	out["account"] = nil
	out["summary"] = a.Summary
	out["versions"] = versions
	out["truncated"] = truncated
	out["release"] = map[string]any{"from": a.Stable, "to": a.Canary, "current": nil,
		"target": a.Percent, "configured_canary_percent": a.Percent, "observed_traffic": nil,
		"routing_connected": false, "gates": nil, "tokens": nil,
		"missing": []string{"observed_traffic", "eval_gate_detail", "token_comparison"}}
	out["diff"] = nil
	out["config"] = nil
	out["scope"] = map[string]string{"application_id": a.App, "environment_id": a.Env,
		"owner_team_id": a.Team}
	if err = lifecycleSession(r.Context(), tx, r); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func percent(v int) string { return strconv.Itoa(v) + "%" }

// Management writes serialize on the Agent row, then recheck the selected
// environment, active application mapping, session, grant sources and human
// identity under the same transaction before saving a configuration intent.
func (s *Service) beginLifecycleWrite(w http.ResponseWriter, r *http.Request) (pgx.Tx, lifecycleAgent, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return nil, lifecycleAgent{}, false
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return nil, lifecycleAgent{}, false
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return nil, lifecycleAgent{}, false
	}
	a, err := loadLifecycleAgent(r.Context(), tx, r.PathValue("id"), true)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.Env != envs[0]) {
		_ = tx.Rollback(r.Context())
		notFound(w)
		return nil, lifecycleAgent{}, false
	}
	if err == nil {
		err = lifecycleSession(r.Context(), tx, r)
	}
	if err == nil {
		err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err == nil {
		var kind string
		err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind FROM users WHERE id=$1`,
			httpapi.CurrentUser(r).ID).Scan(&kind)
		if err == nil && kind != "human" {
			err = store.ErrForbidden
		}
	}
	if err == nil {
		err = s.authorizeLifecycle(r.Context(), tx, r, a, "manage")
	}
	if err != nil {
		// A caller without even agents.view must not learn whether an Agent ID
		// exists by trying a management route. A visible Agent may return 403
		// when the caller lacks human identity or manage authority.
		if errors.Is(err, store.ErrForbidden) {
			viewErr := s.authorizeLifecycle(r.Context(), tx, r, a, "view")
			if errors.Is(viewErr, store.ErrForbidden) {
				_ = tx.Rollback(r.Context())
				notFound(w)
				return nil, lifecycleAgent{}, false
			}
			if viewErr != nil {
				err = viewErr
			}
		}
		_ = tx.Rollback(r.Context())
		lifecycleAuthorityError(w, err)
		return nil, lifecycleAgent{}, false
	}
	return tx, a, true
}

func revisionConflict(w http.ResponseWriter) {
	httpapi.Error(w, 409, "version_conflict", "Agent configuration changed; reload before writing")
}

func (s *Service) finishLifecycleWrite(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	if err := lifecycleSession(r.Context(), tx, r); err != nil {
		lifecycleAuthorityError(w, err)
		return false
	}
	if err := tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return false
	}
	return true
}

type rolloutBody struct {
	To               string `json:"to"`
	Percent          int    `json:"percent"`
	Override         bool   `json:"override"`
	OverrideReason   string `json:"override_reason"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *Service) Rollout(w http.ResponseWriter, r *http.Request) {
	var body rolloutBody
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if !bounded(body.To, 80) || body.Percent < 1 || body.Percent > 100 ||
		body.ExpectedRevision < 1 || (body.Override && !bounded(body.OverrideReason, 500)) ||
		(!body.Override && body.OverrideReason != "") {
		invalid(w, "Select a known version, percentage, expected revision, and override reason when required")
		return
	}
	tx, a, ok := s.beginLifecycleWrite(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if body.ExpectedRevision != a.Revision {
		revisionConflict(w)
		return
	}
	if body.To == a.Stable {
		httpapi.Error(w, 409, "already_stable", "Version is already stable")
		return
	}
	if body.Percent < 100 && a.Stable == "" {
		httpapi.Error(w, 409, "stable_version_required", "Set a stable version before canary rollout")
		return
	}
	if body.Override {
		var admin bool
		err := tx.QueryRow(r.Context(), `SELECT EXISTS(
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
	var exists bool
	err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_agent_versions
 WHERE agent_id=$1 AND version=$2)`, a.ID, body.To).Scan(&exists)
	if err != nil {
		unavailable(w)
		return
	}
	if !exists {
		notFound(w)
		return
	}
	// The event pins the evaluation decision used for this intent. Later
	// evaluations do not turn this saved configuration into observed traffic.
	var gateID, gateStatus string
	err = tx.QueryRow(r.Context(), `SELECT e.id,e.status FROM v3_agent_eval_experiments e
 WHERE e.agent_id=$1 AND e.candidate_version=$2 AND e.baseline_version=$3
 AND EXISTS (SELECT 1 FROM management_audit m
   WHERE m.application_id=$4 AND m.action='v3.agent.eval.created'
   AND m.target=e.id AND m.actor_id=e.created_by AND m.outcome='recorded'
   AND m.changes->>'environment_id'=$5)
 ORDER BY e.created_at DESC,e.id DESC LIMIT 1 FOR SHARE OF e`,
		a.ID, body.To, a.Stable, a.App, a.Env).
		Scan(&gateID, &gateStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		gateID, gateStatus = "", ""
	} else if err != nil {
		unavailable(w)
		return
	}
	if gateStatus != "passed" && !body.Override {
		httpapi.Error(w, 409, "eval_gate_unverified", "A persisted passing evaluation is required")
		return
	}
	if gateStatus != "passed" {
		gateID = ""
	}
	from := a.Stable
	if body.Percent == 100 {
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET previous_stable_version=stable_version,
 stable_version=$2,canary_version=NULL,canary_percent=0,paused=false,
 lifecycle_revision=lifecycle_revision+1,updated_at=now() WHERE id=$1`, a.ID, body.To)
	} else {
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET canary_version=$2,canary_percent=$3,
 paused=false,lifecycle_revision=lifecycle_revision+1,updated_at=now() WHERE id=$1`,
			a.ID, body.To, body.Percent)
	}
	if err != nil {
		unavailable(w)
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_release_events
 (id,agent_id,from_version,to_version,percent,kind,gate_experiment_id,override_reason,actor_id)
 VALUES($1,$2,$3,$4,$5,'rollout',NULLIF($6,''),NULLIF($7,''),$8)`,
		id, a.ID, from, body.To, body.Percent, gateID, body.OverrideReason, httpapi.CurrentUser(r).ID)
	if err == nil {
		err = audit(r.Context(), tx, a.agent, httpapi.CurrentUser(r).ID, "v3.agent.rollout", a.ID,
			map[string]any{"environment_id": a.Env, "from": from, "to": body.To,
				"percent": body.Percent, "gate_experiment_id": gateID, "override": body.Override,
				"revision_from": a.Revision, "revision_to": a.Revision + 1,
				"release_effect": "configuration_saved_pending_activation"})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": id, "to": body.To,
		"configured_percent": body.Percent, "revision": a.Revision + 1,
		"observed_traffic": nil, "routing_connected": false,
		"release_effect":     "configuration_saved_pending_activation",
		"gate_experiment_id": gateID, "override": body.Override})
}

type rollbackBody struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

func (s *Service) Rollback(w http.ResponseWriter, r *http.Request) {
	var body rollbackBody
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if body.ExpectedRevision < 1 {
		invalid(w, "Expected Agent revision is required")
		return
	}
	tx, a, ok := s.beginLifecycleWrite(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if body.ExpectedRevision != a.Revision {
		revisionConflict(w)
		return
	}
	from := a.Stable
	to := a.Previous
	configuredPercent := 100
	var err error
	if a.Canary != "" {
		from = a.Canary
		to = a.Stable
		if to == "" {
			httpapi.Error(w, 409, "rollback_unavailable", "No prior persisted version exists")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET canary_version=NULL,canary_percent=0,
 lifecycle_revision=lifecycle_revision+1,updated_at=now() WHERE id=$1`, a.ID)
	} else if to != "" {
		_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET stable_version=$2,previous_stable_version=$3,
 lifecycle_revision=lifecycle_revision+1,updated_at=now() WHERE id=$1`, a.ID, to, a.Stable)
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
 VALUES($1,$2,$3,$4,$5,'rollback',$6)`,
		id, a.ID, from, to, configuredPercent, httpapi.CurrentUser(r).ID)
	if err == nil {
		err = audit(r.Context(), tx, a.agent, httpapi.CurrentUser(r).ID, "v3.agent.rollback", a.ID,
			map[string]any{"environment_id": a.Env, "from": from, "to": to,
				"revision_from": a.Revision, "revision_to": a.Revision + 1,
				"release_effect": "configuration_saved_pending_activation"})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": id, "to": to,
		"configured_percent": configuredPercent, "revision": a.Revision + 1,
		"configured_canary_percent": 0,
		"observed_traffic":          nil, "routing_connected": false,
		"release_effect": "configuration_saved_pending_activation"})
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

type levelBody struct {
	Level            string `json:"level"`
	ExpectedRevision int64  `json:"expected_revision"`
}

func (s *Service) Level(w http.ResponseWriter, r *http.Request) {
	var body levelBody
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if levelNumber(body.Level) < 0 || body.ExpectedRevision < 1 {
		invalid(w, "Known Agent level and expected revision are required")
		return
	}
	tx, a, ok := s.beginLifecycleWrite(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if body.ExpectedRevision != a.Revision {
		revisionConflict(w)
		return
	}
	// Lock the table as well as an existing cap row: an environment with no
	// row has an implicit L0 cap, and a concurrent insert must not race it.
	_, err := tx.Exec(r.Context(), `LOCK TABLE v3_agent_environment_limits IN SHARE MODE`)
	if err != nil {
		unavailable(w)
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
	_, err = tx.Exec(r.Context(), `UPDATE v3_agents SET level=$2,
 lifecycle_revision=lifecycle_revision+1,updated_at=now() WHERE id=$1`, a.ID, body.Level)
	if err == nil {
		err = audit(r.Context(), tx, a.agent, httpapi.CurrentUser(r).ID, "v3.agent.level", a.ID,
			map[string]any{"environment_id": a.Env, "from": a.Level, "to": body.Level,
				"cap": max, "revision_from": a.Revision, "revision_to": a.Revision + 1})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": a.ID, "level": body.Level, "max_level": max,
		"revision": a.Revision + 1, "observed_traffic": nil, "routing_connected": false,
		"release_effect": "configuration_saved_pending_activation"})
}
