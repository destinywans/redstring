// Package v3agents owns the persisted V3 Agent console. It records only
// catalog metadata and sanitized execution facts. Explicit governed runs use
// only the composition root's bounded read-only tool factory.
package v3agents

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3interactionaudit"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

// Register is called by the V3 composition root after V3 environment policy
// has been configured. Object handlers recheck persisted scope and app grants.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"POST /api/v1/agents", "manage", s.CreateAgent},
		{"POST /api/v1/agents/{id}/versions", "manage", s.CreateAgentVersion},
		{"GET /api/v1/agents/lifecycle", "view", s.Lifecycle},
		{"GET /api/v1/agents/{id}", "view", s.AgentDetail},
		{"POST /api/v1/agents/{id}/rollout", "manage", s.Rollout},
		{"POST /api/v1/agents/{id}/rollback", "manage", s.Rollback},
		{"PATCH /api/v1/agents/{id}/level", "manage", s.Level},
		{"GET /api/v1/prompts", "view", s.Prompts},
		{"POST /api/v1/prompts", "change", s.CreatePrompt},
		{"GET /api/v1/prompts/{id}", "view", s.PromptDetail},
		{"POST /api/v1/prompts/{id}/versions", "change", s.PromptVersion},
		{"POST /api/v1/prompts/{id}/trial", "change", s.PromptTrial},
		{"GET /api/v1/agent-tools", "view", s.Tools},
		{"POST /api/v1/agent-tools", "manage", s.CreateTool},
		{"GET /api/v1/eval-datasets", "view", s.Datasets},
		{"POST /api/v1/eval-datasets", "change", s.CreateDataset},
		{"POST /api/v1/eval-datasets/{id}/items", "change", s.DatasetItem},
		{"POST /api/v1/agent-runs/{id}/annotations", "change", s.AnnotateRun},
		{"GET /api/v1/prompts/{id}/versions", "view", s.PromptVersions},
		{"GET /api/v1/evals", "view", s.Evals},
		{"GET /api/v1/evals/{id}", "view", s.EvalDetail},
		{"POST /api/v1/evals", "change", s.CreateEval},
		{"POST /api/v1/eval-cases", "change", s.CreateEvalCase},
		{"POST /api/v1/evals/{id}/execute", "change", s.ExecuteEval},
		{"GET /api/v1/evals/{id}/execution", "view", s.EvalExecution},
		{"POST /api/v1/evals/{id}/cancel", "change", s.CancelEval},
		{"GET /api/v1/eval-cases/{id}", "view", s.EvalCase},
		{"POST /api/v1/eval-cases/{id}/labels", "change", s.LabelCase},
		{"GET /api/v1/agent-runs", "view", s.Runs},
		{"GET /api/v1/agent-runs/{id}", "view", s.RunDetail},
		{"GET /api/v1/agent-runs/{id}/trace", "view", s.RunTrace},
		{"POST /api/v1/agent-runs/{id}/feedback", "change", s.RunFeedback},
		{"GET /api/v1/guardrails", "view", s.Guardrails},
		{"PATCH /api/v1/guardrails/{id}", "manage", s.Guardrail},
	} {
		handler := route.handler
		if action, ok := map[string]string{"POST /api/v1/agent-tools": "v3.agent.tool.catalog.registered", "POST /api/v1/eval-datasets": "v3.agent.dataset.created", "POST /api/v1/eval-datasets/{id}/items": "v3.agent.dataset.item.added", "POST /api/v1/agent-runs/{id}/annotations": "v3.agent.run.annotated"}[route.pattern]; ok {
			v3interactionaudit.Mark(api, s.DB, route.pattern, action)
		}
		api.Register(route.pattern, "viewer", handler)
		api.MarkV3ObjectRoute(route.pattern, "agents", route.action)
	}
	s.RegisterExecution(api)
}

type agent struct {
	ID, App, Env, Name, Summary, Owner, Level string
	Stable, Previous, Canary                  string
	Percent                                   int
	Paused                                    bool
	Created, Updated                          time.Time
}

func scanAgent(row pgx.Row) (agent, error) {
	var a agent
	err := row.Scan(&a.ID, &a.App, &a.Env, &a.Name, &a.Summary, &a.Owner, &a.Level,
		&a.Stable, &a.Previous, &a.Canary, &a.Percent, &a.Paused, &a.Created, &a.Updated)
	return a, err
}

const agentColumns = `id,application_id,environment_id,name,summary,
 COALESCE(owner_id,''),level,COALESCE(stable_version,''),
 COALESCE(previous_stable_version,''),COALESCE(canary_version,''),
 canary_percent,paused,created_at,updated_at`

// List queries apply the same persisted environment + module grant conditions
// as rbac.V3Policy before LIMIT. Per-row authorization after LIMIT can hide a
// later visible row behind many inaccessible rows. Object writes still use
// Access.AuthorizeWithTx after locking the target.
const visibleAgentGrantSQL = `EXISTS (SELECT 1 FROM v3_effective_role_grants e
 WHERE e.user_id=$2 AND e.module='environment' AND e.action='view'
 AND ((e.scope_kind='global' AND e.scope_id='*') OR
      (e.scope_kind='environment' AND e.scope_id=a.environment_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$2 AND g.module='agents' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR
      (g.scope_kind='environment' AND g.scope_id=a.environment_id) OR
      (g.scope_kind='application' AND g.scope_id=a.application_id)))
 AND EXISTS (SELECT 1 FROM v3_application_environments ae
 WHERE ae.application_id=a.application_id AND ae.environment_id=a.environment_id)`

const visiblePolicyGrantSQL = `EXISTS (SELECT 1 FROM v3_effective_role_grants e
 WHERE e.user_id=$2 AND e.module='environment' AND e.action='view'
 AND ((e.scope_kind='global' AND e.scope_id='*') OR
      (e.scope_kind='environment' AND e.scope_id=p.environment_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$2 AND g.module='agents' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR
      (g.scope_kind='environment' AND g.scope_id=p.environment_id)))`

func (s *Service) loadAgent(ctx context.Context, id string) (agent, error) {
	return scanAgent(s.DB.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, id))
}

func requestScope(w http.ResponseWriter, r *http.Request, write bool) ([]string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return nil, false
	}
	if write && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment before writing")
		return nil, false
	}
	return scope.EnvironmentIDs, true
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Service) authorized(w http.ResponseWriter, r *http.Request, a agent, action string) bool {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return false
	}
	envs, ok := requestScope(w, r, action != "view")
	if !ok {
		return false
	}
	if !contains(envs, a.Env) {
		httpapi.Error(w, 404, "not_found", "Agent resource not found")
		return false
	}
	err := s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: action, EnvironmentID: a.Env, ApplicationID: a.App})
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Agent permission required")
		return false
	}
	if err != nil {
		unavailable(w)
		return false
	}
	return true
}

func (s *Service) authorizedTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx, a agent, action string) bool {
	err := s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: action, EnvironmentID: a.Env, ApplicationID: a.App})
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Agent permission required")
		return false
	}
	if err != nil {
		unavailable(w)
		return false
	}
	return true
}

func (s *Service) objectAgent(w http.ResponseWriter, r *http.Request, id, action string) (agent, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return agent{}, false
	}
	a, err := s.loadAgent(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Agent resource not found")
		return agent{}, false
	}
	if err != nil {
		unavailable(w)
		return agent{}, false
	}
	if !s.authorized(w, r, a, action) {
		return agent{}, false
	}
	return a, true
}

func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "agent_console_unavailable", "Agent console storage or policy unavailable")
}
func invalid(w http.ResponseWriter, msg string) { httpapi.Error(w, 422, "invalid_agent_request", msg) }
func notFound(w http.ResponseWriter)            { httpapi.Error(w, 404, "not_found", "Agent resource not found") }
func bounded(v string, n int) bool {
	return v != "" && strings.TrimSpace(v) == v && len([]rune(v)) <= n
}

var metadataCode = regexp.MustCompile(`^[A-Za-z0-9._:/@#-]+$`)
var auditCode = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func safeCode(v string, n int) bool      { return bounded(v, n) && metadataCode.MatchString(v) }
func safeAuditCode(v string, n int) bool { return bounded(v, n) && auditCode.MatchString(v) }

func audit(ctx context.Context, tx pgx.Tx, a agent, actor, action, target string, changes any) error {
	kind := "user"
	if actor == "system" {
		kind = "system"
	}
	_, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,$5,'recorded',$6)`, actor, kind, a.App, action, target, changes)
	return err
}

func timeString(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// RegisterCatalogAgent is a trusted in-process catalog import. It validates
// a real application/environment mapping and records an artifact reference,
// never an executable tool grant or a model credential.
func (s *Service) RegisterCatalogAgent(ctx context.Context, id, app, env, name, version, artifactRef, actor string) error {
	if s == nil || s.DB == nil || !bounded(id, 128) || !bounded(app, 128) || !bounded(env, 128) ||
		!bounded(name, 160) || !safeCode(version, 80) || !safeCode(artifactRef, 500) || !bounded(actor, 128) {
		return errors.New("invalid agent catalog record")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var appID string
	err = tx.QueryRow(ctx, `SELECT a.id FROM applications a JOIN v3_application_environments e ON e.application_id=a.id
 WHERE a.id=$1 AND e.environment_id=$2 AND NOT a.archived`, app, env).Scan(&appID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agents(id,application_id,environment_id,name) VALUES($1,$2,$3,$4)
 ON CONFLICT (id) DO NOTHING`, id, app, env, name)
	if err != nil {
		return err
	}
	var agentApp, agentEnv, agentName string
	err = tx.QueryRow(ctx, `SELECT application_id,environment_id,name FROM v3_agents WHERE id=$1`, id).Scan(&agentApp, &agentEnv, &agentName)
	if err != nil {
		return err
	}
	if agentApp != app || agentEnv != env || agentName != name {
		return errors.New("agent catalog scope conflict")
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_versions(agent_id,version,artifact_ref,created_by)
 SELECT $1,$2,$3,$4 FROM v3_agents WHERE id=$1 AND application_id=$5 AND environment_id=$6
 ON CONFLICT (agent_id,version) DO NOTHING`, id, version, artifactRef, actor, app, env)
	if err != nil {
		return err
	}
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_agent_versions WHERE agent_id=$1 AND version=$2 AND artifact_ref=$3)`, id, version, artifactRef).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("agent version conflicts with existing catalog record")
	}
	if err = audit(ctx, tx, agent{App: app, Env: env}, actor, "v3.agent.catalog.registered", id,
		map[string]any{"environment_id": env, "version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
