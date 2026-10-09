package v3traces

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Source interface {
	QueryCompleteTraces(context.Context, connectors.CompleteTraceQuery) ([]connectors.CompleteTrace, error)
	// QueryObservedTraces serves the list without requiring a deployment
	// attestation of search completeness; it reports whether one exists.
	QueryObservedTraces(context.Context, connectors.CompleteTraceQuery) (connectors.TraceObservation, error)
}

type Service struct {
	Store  *store.Store
	Access Access
	Source Source
	// SourceCompare, when set, is told which file-configured bindings were
	// read (PA-19 compare mode). It never changes the result.
	SourceCompare v3telemetry.Observer
}

func New(st *store.Store, access Access, source Source) *Service {
	return &Service{Store: st, Access: access, Source: source}
}

func (s *Service) Register(api *httpapi.Server) {
	s.registerViews(api)
	api.Register("GET /api/v1/traces", "viewer", s.List)
	api.MarkV3ObjectRoute("GET /api/v1/traces", "traces", "view")
	api.Register("GET /api/v1/traces/{id}", "viewer", s.Detail)
	api.MarkV3ObjectRoute("GET /api/v1/traces/{id}", "traces", "view")
}

type target struct {
	ApplicationID string
	EnvironmentID string
	TeamID        string
	Provider      string
	Resource      string
	Revision      int64
}

var (
	errScopeLimit = errors.New("trace scope exceeds bound")
	errAmbiguous  = errors.New("conflicting Tempo bindings")
)

func (s *Service) allowedEnvironments(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if s.Store == nil || s.Store.DB == nil || s.Access == nil || s.Source == nil {
		httpapi.Error(w, 503, "traces_unavailable", "Trace storage, policy or provider is unavailable")
		return nil, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	// Team and application grants may authorize a target even when a broad
	// environment-only decision does not. Check each persisted target below.
	return scope.EnvironmentIDs, true
}

func (s *Service) authorizeTarget(ctx context.Context, user core.User, item target) error {
	err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "traces", Action: "view", EnvironmentID: item.EnvironmentID, ApplicationID: item.ApplicationID})
	if errors.Is(err, store.ErrForbidden) && item.TeamID != "" {
		return s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "traces", Action: "view", EnvironmentID: item.EnvironmentID, TeamID: item.TeamID})
	}
	return err
}

func (s *Service) targets(ctx context.Context, user core.User, environments []string, service string) ([]target, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := []target{}
	seen := map[string]target{}
	var cursor target
	hasCursor, hadCandidates := false, false
	for {
		rows, err := s.Store.DB.Query(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,''),b.kind,b.resource,e.binding_revision
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, resource text) ON b.kind='tempo'
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[]) AND ($2='' OR a.id=$2 OR a.key=$2)
 AND b.resource<>''
 AND (NOT $3::boolean OR (a.id,ve.environment_id,b.kind,b.resource)>($4,$5,$6,$7))
 ORDER BY a.id,ve.environment_id,b.kind,b.resource LIMIT 200`,
			environments, service, hasCursor, cursor.ApplicationID, cursor.EnvironmentID, cursor.Provider, cursor.Resource)
		if err != nil {
			return nil, err
		}
		batch := make([]target, 0, 200)
		for rows.Next() {
			var item target
			if err = rows.Scan(&item.ApplicationID, &item.EnvironmentID, &item.TeamID, &item.Provider, &item.Resource, &item.Revision); err != nil {
				break
			}
			batch = append(batch, item)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		hadCandidates = true
		for _, item := range batch {
			cursor, hasCursor = item, true
			if err := s.authorizeTarget(ctx, user, item); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				return nil, err
			}
			key := item.ApplicationID + "\x00" + item.EnvironmentID
			if previous, exists := seen[key]; exists {
				if previous.Resource != item.Resource || previous.Revision != item.Revision {
					return nil, errAmbiguous
				}
				continue
			}
			seen[key] = item
			result = append(result, item)
			if len(result) > 6 {
				return nil, errScopeLimit
			}
		}
		if len(batch) < 200 {
			break
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if hadCandidates && len(result) == 0 {
		return nil, store.ErrForbidden
	}
	// A static telemetry registration has one service/environment selector.
	// Reusing its resource ID in another persisted scope could expose spans from
	// a hidden application even when only one binding is visible to this user.
	for _, item := range result {
		var owners int
		err := s.Store.DB.QueryRow(ctx, `SELECT COUNT(DISTINCT (e.application_id,e.key))
 FROM application_environments e JOIN applications a ON a.id=e.application_id AND NOT a.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, resource text) ON b.kind='tempo'
 WHERE NOT e.archived AND b.resource=$1`, item.Resource).Scan(&owners)
		if err != nil {
			return nil, err
		}
		if owners != 1 {
			return nil, errAmbiguous
		}
	}
	if s.SourceCompare != nil {
		for _, item := range result {
			s.SourceCompare.ObserveLegacy(v3telemetry.Use{ApplicationID: item.ApplicationID, EnvironmentID: item.EnvironmentID,
				Provider: item.Provider, Resource: item.Resource})
		}
	}
	return result, nil
}

func targetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Traces are outside your authorized scope")
	case errors.Is(err, errScopeLimit):
		httpapi.Error(w, 422, "scope_limit", "Select a service or narrow the environment")
	case errors.Is(err, errAmbiguous):
		httpapi.Error(w, 409, "ambiguous_binding", "Select one Tempo binding for this service and environment")
	default:
		httpapi.Error(w, 503, "trace_scope_unavailable", "Trace scope lookup failed")
	}
}

func sourceError(w http.ResponseWriter, err error, detail bool) {
	var telemetry *connectors.TelemetryError
	if !errors.As(err, &telemetry) {
		httpapi.Error(w, 503, "telemetry_unavailable", "Scoped trace query failed")
		return
	}
	switch telemetry.Kind {
	case "configuration":
		httpapi.Error(w, 503, "not_configured", "Tempo provider or the trace resource registration is not configured")
	case "limit":
		if detail {
			httpapi.Error(w, 413, "trace_too_large", "Trace exceeds the bounded span limit")
		} else {
			httpapi.Error(w, 413, "trace_scope_limit", "Trace population exceeds the bounded search limit")
		}
	case "scope":
		if detail {
			httpapi.Error(w, 404, "not_found", "Trace not found in accessible scope")
		} else {
			httpapi.Error(w, 503, "trace_scope_unavailable", "Registered Tempo scope is incomplete")
		}
	default:
		httpapi.Error(w, 503, "telemetry_unavailable", "Scoped trace query is incomplete or unavailable")
	}
}

// scope resolves the authorized Tempo targets. With allowNone, an unfiltered
// scope without any binding is returned empty for the caller to report.
func (s *Service) scope(w http.ResponseWriter, r *http.Request, allowNone ...bool) ([]string, []target, bool) {
	environments, ok := s.allowedEnvironments(w, r)
	if !ok {
		return nil, nil, false
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if len(service) > 200 {
		httpapi.Error(w, 422, "invalid_service", "Service filter is too long")
		return nil, nil, false
	}
	targets, err := s.targets(r.Context(), httpapi.CurrentUser(r), environments, service)
	if err != nil {
		if service != "" && errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Service has no accessible trace source")
			return nil, nil, false
		}
		targetError(w, err)
		return nil, nil, false
	}
	if len(targets) == 0 {
		if service != "" {
			httpapi.Error(w, 404, "not_found", "Service has no accessible trace source")
			return nil, nil, false
		}
		if len(allowNone) > 0 && allowNone[0] {
			return environments, targets, true
		}
		httpapi.Error(w, 503, "not_configured", "No authorized trace binding is configured")
		return nil, nil, false
	}
	return environments, targets, true
}

func (s *Service) unchanged(w http.ResponseWriter, r *http.Request, environments []string, before []target, service string) bool {
	after, err := s.targets(r.Context(), httpapi.CurrentUser(r), environments, service)
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			targetError(w, err)
		} else {
			httpapi.Error(w, 409, "trace_scope_changed", "Trace source or authorization changed during the query")
		}
		return false
	}
	if !reflect.DeepEqual(before, after) {
		httpapi.Error(w, 409, "trace_scope_changed", "Trace source or authorization changed during the query")
		return false
	}
	// A Tempo or persisted-target read can outlive the session that authorized
	// it. Verify the original Cookie after both reads, before either response.
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Session is no longer active")
		return false
	}
	currentUser, sessionErr := s.Store.Session(r.Context(), cookie.Value)
	if sessionErr != nil {
		if errors.Is(store.ClassifyError(sessionErr), store.ErrNotFound) {
			httpapi.Error(w, 401, "unauthorized", "Session is no longer active")
		} else {
			httpapi.Error(w, 503, "session_unavailable", "Session verification failed")
		}
		return false
	}
	if currentUser.ID != httpapi.CurrentUser(r).ID {
		httpapi.Error(w, 401, "unauthorized", "Session is no longer active")
		return false
	}
	return true
}

func (s *Service) allTargets(w http.ResponseWriter, r *http.Request, environments []string, selected []target) ([]target, bool) {
	if strings.TrimSpace(r.URL.Query().Get("service")) == "" {
		return selected, true
	}
	all, err := s.targets(r.Context(), httpapi.CurrentUser(r), environments, "")
	if err != nil {
		targetError(w, err)
		return nil, false
	}
	return all, true
}

func resources(targets []target) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, target := range targets {
		if !seen[target.Resource] {
			result = append(result, target.Resource)
			seen[target.Resource] = true
		}
	}
	return result
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	duration, rangeErr := v3pagequery.ParseRange(r.URL.Query(), time.Hour)
	if rangeErr != nil {
		httpapi.Error(w, 422, "invalid_range", "Use one supported time range")
		return
	}
	source, sourceErr := v3pagequery.Single(r.URL.Query(), "source", "tempo", "jaeger")
	if sourceErr != nil {
		httpapi.Error(w, 422, "invalid_source", "Choose one trace provider")
		return
	}
	if source != "" && r.URL.Query().Get("scope") == "agent" {
		httpapi.Error(w, 422, "invalid_source", "Agent traces are platform records")
		return
	}
	if source == "jaeger" {
		httpapi.Error(w, 503, "trace_source_not_connected", "Jaeger complete trace provider is not connected")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	scope := r.URL.Query().Get("scope")
	if scope == "agent" {
		s.listAgent(w, r)
		return
	}
	if scope != "" && scope != "app" {
		httpapi.Error(w, 422, "invalid_scope", "Unknown trace scope")
		return
	}
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "all"
	}
	if view != "all" && view != "errors" && view != "slow" {
		httpapi.Error(w, 422, "invalid_view", "Unknown saved trace view")
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("q"))
	if !connectors.ValidTraceSelector(filter) {
		httpapi.Error(w, 422, "invalid_query", "Use one bounded TraceQL span selector")
		return
	}
	environments, targets, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if len(targets) == 0 {
		// No Tempo binding serves this scope. Recheck the session so the
		// answer still belongs to a live login, then report the state.
		if !s.unchanged(w, r, environments, targets, "") {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, 200, UnconfiguredList())
		return
	}
	all, ok := s.allTargets(w, r, environments, targets)
	if !ok {
		return
	}
	start := time.Now()
	end := start.UTC().Truncate(time.Second)
	window := connectors.TimeWindow{Start: end.Add(-duration), End: end}
	observed, err := s.Source.QueryObservedTraces(r.Context(), connectors.CompleteTraceQuery{
		Resources: resources(all), SearchResources: resources(targets), Window: window, Filter: filter})
	if err != nil {
		sourceError(w, err, false)
		return
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if !s.unchanged(w, r, environments, targets, service) || !s.unchanged(w, r, environments, all, "") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !observed.PopulationComplete {
		// Search completeness is not attested for this deployment: return
		// the closed traces found, never counts or charts of that sample.
		httpapi.WriteJSON(w, 200, BuildObservedList(observed.Traces, view, service, time.Since(start), "not_configured"))
		return
	}
	httpapi.WriteJSON(w, 200, BuildList(observed.Traces, window, view, service, time.Since(start)))
}

func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	id := r.PathValue("id")
	if !connectors.ValidTraceID(id) {
		httpapi.Error(w, 422, "invalid_trace", "Trace ID must be 32 hexadecimal characters")
		return
	}
	environments, targets, ok := s.scope(w, r)
	if !ok {
		return
	}
	all, ok := s.allTargets(w, r, environments, targets)
	if !ok {
		return
	}
	traces, err := s.Source.QueryCompleteTraces(r.Context(), connectors.CompleteTraceQuery{
		Resources: resources(all), SearchResources: resources(targets), TraceID: id})
	if err != nil {
		sourceError(w, err, true)
		return
	}
	if !s.unchanged(w, r, environments, targets, strings.TrimSpace(r.URL.Query().Get("service"))) || !s.unchanged(w, r, environments, all, "") {
		return
	}
	if len(traces) == 0 {
		httpapi.Error(w, 404, "not_found", "Trace not found in accessible scope")
		return
	}
	view, err := BuildView(traces[0])
	if err != nil {
		httpapi.Error(w, 503, "trace_incomplete", "Trace parent graph is incomplete")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, view)
}
