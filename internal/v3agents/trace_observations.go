package v3agents

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
)

var (
	ErrTraceRunScopeLimit     = errors.New("persisted Agent trace scope exceeds its bound")
	ErrTraceRunInvalidRequest = errors.New("invalid persisted Agent trace request")
)

// TraceRunObservation is a terminal producer report, not a complete execution
// graph. Span counts describe persisted reports only; absent token reports stay
// unknown. Application and Agent names are their current authorized catalog names.
type TraceRunObservation struct {
	ID, AgentID, AgentName, ApplicationID, ApplicationName, EnvironmentID string
	Version, Trigger, Status                                              string
	StartedAt, FinishedAt                                                 time.Time
	TokenInput, TokenOutput                                               *int
	SpanCount, ErrorSpanCount                                             int
	ModelIDs                                                              []string
}

type TraceRunObservations struct {
	Items                      []TraceRunObservation
	WindowStartAt, WindowEndAt time.Time
	ExcludedRunning            int
}

// ReadTraceRunObservations holds the Runs lock order: runs, sorted Agents,
// sorted active scopes, then session and physical grant sources. Both module
// policies are checked in that transaction before reading display metadata or
// span aggregates. No provider configuration, tool execution, or model call is
// involved. A bounded population is rejected instead of sampled.
func (s *Service) ReadTraceRunObservations(r *http.Request) (TraceRunObservations, error) {
	result := TraceRunObservations{Items: []TraceRunObservation{}}
	if s == nil || s.DB == nil || s.Access == nil {
		return result, errors.New("Agent trace storage or policy unavailable")
	}
	query := r.URL.Query()
	if len(query["service"]) > 1 || len(query["env"]) > 1 || len(query["scope"]) > 1 {
		return result, ErrTraceRunInvalidRequest
	}
	if scope := query.Get("scope"); scope != "" && scope != "agent" {
		return result, ErrTraceRunInvalidRequest
	}
	service := strings.TrimSpace(query.Get("service"))
	if len(service) > 200 {
		return result, ErrTraceRunInvalidRequest
	}
	// The middleware's persisted environment context is the authority; query
	// parameters cannot add another environment to it.
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		return result, store.ErrForbidden
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return result, err
	}
	defer tx.Rollback(r.Context())
	if err = tx.QueryRow(r.Context(), `SELECT clock_timestamp()`).Scan(&result.WindowEndAt); err != nil {
		return result, err
	}
	result.WindowEndAt = result.WindowEndAt.UTC()
	window, rangeErr := v3pagequery.ParseRange(r.URL.Query(), 24*time.Hour)
	if rangeErr != nil {
		return result, ErrTraceRunInvalidRequest
	}
	result.WindowStartAt = result.WindowEndAt.Add(-window)
	// Qualify both modules before LIMIT, so inaccessible runs cannot displace
	// authorized rows. Current catalog/policy checks are repeated after locks,
	// exactly as in Runs, to observe a revoke committed during this scan.
	rows, err := tx.Query(r.Context(), `SELECT `+runColumns+`
FROM v3_agent_runs v
JOIN v3_agents a ON a.id=v.agent_id AND a.application_id=v.application_id AND a.environment_id=v.environment_id
JOIN applications app ON app.id=v.application_id AND NOT app.archived
JOIN v3_application_environments mapped ON mapped.application_id=app.id AND mapped.environment_id=v.environment_id
JOIN application_environments e ON e.application_id=app.id AND e.key=v.environment_id AND NOT e.archived
WHERE v.environment_id=ANY($1::text[]) AND ($3='' OR app.id=$3 OR app.key=$3)
AND v.started_at >= $4 AND v.started_at <= $5
AND `+visibleAgentGrantSQL+`
AND (app.owner_team_id IS NULL OR EXISTS (
 SELECT 1 FROM v3_effective_role_grants t WHERE t.user_id=$2 AND t.module='agents' AND t.action='view'
 AND ((t.scope_kind='global' AND t.scope_id='*')
   OR (t.scope_kind='environment' AND t.scope_id=v.environment_id)
   OR (t.scope_kind='team' AND t.scope_id=app.owner_team_id))
))
AND EXISTS (SELECT 1 FROM v3_effective_role_grants t
 WHERE t.user_id=$2 AND t.module='traces' AND t.action='view'
 AND ((t.scope_kind='global' AND t.scope_id='*')
   OR (t.scope_kind='environment' AND t.scope_id=v.environment_id)
   OR (t.scope_kind='application' AND t.scope_id=v.application_id)
   OR (app.owner_team_id IS NOT NULL AND t.scope_kind='team' AND t.scope_id=app.owner_team_id)))
ORDER BY v.started_at DESC,v.id DESC LIMIT 2001 FOR SHARE OF v`,
		scope.EnvironmentIDs, httpapi.CurrentUser(r).ID, service, result.WindowStartAt, result.WindowEndAt)
	if err != nil {
		return result, err
	}
	candidates := make([]run, 0)
	for rows.Next() {
		v, scanErr := scanRun(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		candidates = append(candidates, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}

	type bindingKey struct{ app, env string }
	agentRuns := map[string]run{}
	bindings := map[bindingKey]bool{}
	for _, v := range candidates {
		agentRuns[v.AgentID] = v
		bindings[bindingKey{v.App, v.Env}] = true
	}
	agentIDs := make([]string, 0, len(agentRuns))
	for id := range agentRuns {
		agentIDs = append(agentIDs, id)
	}
	sort.Strings(agentIDs)
	agents := map[string]agent{}
	for _, id := range agentIDs {
		a, lockErr := lockRunAgent(r.Context(), tx, agentRuns[id])
		if errors.Is(lockErr, pgx.ErrNoRows) || errors.Is(lockErr, store.ErrNotFound) {
			continue
		}
		if lockErr != nil {
			return result, lockErr
		}
		agents[id] = a
	}
	bindingKeys := make([]bindingKey, 0, len(bindings))
	for key := range bindings {
		bindingKeys = append(bindingKeys, key)
	}
	sort.Slice(bindingKeys, func(i, j int) bool {
		if bindingKeys[i].app != bindingKeys[j].app {
			return bindingKeys[i].app < bindingKeys[j].app
		}
		return bindingKeys[i].env < bindingKeys[j].env
	})
	teams := map[bindingKey]string{}
	for _, key := range bindingKeys {
		team, lockErr := lockRunActiveScope(r.Context(), tx, key.app, key.env)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			continue
		}
		if lockErr != nil {
			return result, lockErr
		}
		teams[key] = team
	}
	if err = lockRunAuthority(r.Context(), tx, r); err != nil {
		return result, err
	}
	authorized := map[bindingKey]bool{}
	visible := make([]runRecord, 0, len(candidates))
	for _, v := range candidates {
		a, found := agents[v.AgentID]
		if !found {
			continue
		}
		key := bindingKey{v.App, v.Env}
		team, found := teams[key]
		if !found {
			continue
		}
		rec := runRecord{Value: v, Agent: a, Team: team}
		allowed, seen := authorized[key]
		if !seen {
			err = s.authorizeRunTx(r.Context(), tx, r, rec, "view")
			if err == nil {
				err = s.authorizeTraceRunTx(r, tx, rec)
			}
			if err != nil && !errors.Is(err, store.ErrForbidden) {
				return result, err
			}
			allowed = err == nil
			authorized[key] = allowed
		}
		if allowed {
			visible = append(visible, rec)
		}
	}
	// Reject even if a concurrent revoke removed some initial candidates: a
	// limited scan cannot prove that later authorized runs were absent.
	if len(candidates) > 2000 {
		return result, ErrTraceRunScopeLimit
	}
	ids := make([]string, 0, len(visible))
	type applicationMetadata struct{ name, key string }
	apps := map[string]applicationMetadata{}
	for _, rec := range visible {
		v := rec.Value
		app, known := apps[v.App]
		if !known {
			// lockRunActiveScope already holds this catalog row through commit.
			if err = tx.QueryRow(r.Context(), `SELECT name,key FROM applications WHERE id=$1`, v.App).Scan(&app.name, &app.key); err != nil {
				return result, err
			}
			apps[v.App] = app
		}
		if service != "" && service != v.App && service != app.key {
			continue
		}
		switch v.Status {
		case "running":
			result.ExcludedRunning++
			continue
		case "success", "failed", "blocked":
			if v.Finished == nil || v.Finished.Before(v.Started) {
				return result, errors.New("invalid persisted Agent trace completion")
			}
		default:
			return result, errors.New("unknown persisted Agent trace status")
		}
		ids = append(ids, v.ID)
		result.Items = append(result.Items, TraceRunObservation{
			ID: v.ID, AgentID: v.AgentID, AgentName: rec.Agent.Name,
			ApplicationID: v.App, ApplicationName: app.name, EnvironmentID: v.Env,
			Version: v.Version, Trigger: v.Trigger, Status: v.Status,
			StartedAt: v.Started.UTC(), FinishedAt: v.Finished.UTC(),
			TokenInput: v.Input, TokenOutput: v.Output, ModelIDs: []string{},
		})
	}
	counts, models, err := traceRunSpanReports(r.Context(), tx, ids)
	if err != nil {
		return result, err
	}
	for i := range result.Items {
		item := &result.Items[i]
		item.SpanCount, item.ErrorSpanCount = counts[item.ID][0], counts[item.ID][1]
		if ids := models[item.ID]; ids != nil {
			item.ModelIDs = ids
		}
	}
	if err = currentRunSession(r.Context(), tx, r); err != nil {
		return result, err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) authorizeTraceRunTx(r *http.Request, tx pgx.Tx, rec runRecord) error {
	user := httpapi.CurrentUser(r)
	err := s.Access.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{
		Module: "traces", Action: "view", EnvironmentID: rec.Value.Env, ApplicationID: rec.Value.App,
	})
	if errors.Is(err, store.ErrForbidden) && rec.Team != "" {
		return s.Access.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{
			Module: "traces", Action: "view", EnvironmentID: rec.Value.Env, TeamID: rec.Team,
		})
	}
	return err
}

func traceRunSpanReports(ctx context.Context, tx pgx.Tx, ids []string) (map[string][2]int, map[string][]string, error) {
	counts := map[string][2]int{}
	models := map[string][]string{}
	if len(ids) == 0 {
		return counts, models, nil
	}
	// Producer hooks lock the parent Run before appending spans; the reader's
	// shared Run locks therefore hold reported coverage stable until commit.
	rows, err := tx.Query(ctx, `SELECT run_id,count(*),count(*) FILTER (WHERE status IN ('failed','blocked'))
FROM v3_agent_run_spans WHERE run_id=ANY($1::text[]) GROUP BY run_id ORDER BY run_id`, ids)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		var count [2]int
		if err = rows.Scan(&id, &count[0], &count[1]); err != nil {
			break
		}
		counts[id] = count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	// Bound distinct run/model metadata independently of reported span count;
	// never trim model IDs and call the result complete. Each producer model ID
	// is at most 160 characters and must retain the existing metadata syntax.
	rows, err = tx.Query(ctx, `SELECT run_id,model_id FROM v3_agent_run_spans
WHERE run_id=ANY($1::text[]) AND model_id IS NOT NULL AND model_id<>''
GROUP BY run_id,model_id ORDER BY run_id,model_id LIMIT 20001`, ids)
	if err != nil {
		return nil, nil, err
	}
	modelCount := 0
	for rows.Next() {
		var id, model string
		if err = rows.Scan(&id, &model); err != nil {
			break
		}
		modelCount++
		if modelCount > 20000 || !safeCode(model, 160) {
			err = ErrTraceRunScopeLimit
			break
		}
		models[id] = append(models[id], model)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	return counts, models, nil
}
