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

type Service struct {
	Store  *store.Store
	Access Access
	// Registry and Clients serve the list and detail from the connector
	// registry (PA-19 P3 stage 2 step 4, registry.go; the only path since
	// P4). Without them traces are unavailable.
	Registry *v3telemetry.Resolver
	Clients  *v3telemetry.Clients
	// clientFor replaces Clients.ForGroup in tests.
	clientFor func(env string, sources []v3telemetry.Source) (traceSource, error)
}

func New(st *store.Store, access Access) *Service {
	return &Service{Store: st, Access: access}
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
	// TargetID and ConnectorID name the registry target.
	TargetID    string
	ConnectorID string
}

var (
	errScopeLimit = errors.New("trace scope exceeds bound")
	errAmbiguous  = errors.New("conflicting Tempo bindings")
)

func (s *Service) allowedEnvironments(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if s.Store == nil || s.Store.DB == nil || s.Access == nil || s.Registry == nil || s.Clients == nil {
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
		// Release the query connection before authorization, which may need
		// the same pool.
		batch, err := s.registryBatch(ctx, environments, service, cursor, hasCursor)
		if err != nil {
			return nil, err
		}
		done, err := s.admit(ctx, user, batch, &cursor, &hasCursor, &hadCandidates, seen, &result)
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if hadCandidates && len(result) == 0 {
		return nil, store.ErrForbidden
	}
	return result, nil
}

// admit authorizes one batch of candidate targets into result. It reports
// done after the last batch.
func (s *Service) admit(ctx context.Context, user core.User, batch []target, cursor *target, hasCursor, hadCandidates *bool,
	seen map[string]target, result *[]target) (bool, error) {
	if len(batch) == 0 {
		return true, nil
	}
	*hadCandidates = true
	for _, item := range batch {
		*cursor, *hasCursor = item, true
		if err := s.authorizeTarget(ctx, user, item); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if errors.Is(err, store.ErrForbidden) {
				continue
			}
			return false, err
		}
		key := item.ApplicationID + "\x00" + item.EnvironmentID
		if previous, exists := seen[key]; exists {
			if previous.Resource != item.Resource || previous.TargetID != item.TargetID {
				return false, errAmbiguous
			}
			continue
		}
		seen[key] = item
		*result = append(*result, item)
		if len(*result) > 6 {
			return false, errScopeLimit
		}
	}
	return len(batch) < 200, nil
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
	case "request_too_long":
		httpapi.Error(w, 422, "query_too_long", "Trace search exceeds the 8 KiB request limit; shorten the filter")
	case "lookback":
		// PA-79: the range is longer than the deployment keeps searchable.
		limit := time.Duration(telemetry.LimitSeconds) * time.Second
		httpapi.WriteJSON(w, 422, map[string]any{"error": map[string]any{
			"code": "range_exceeds_lookback", "message": "Choose a range within the configured trace lookback",
			"max_lookback_seconds": telemetry.LimitSeconds, "max_range": v3pagequery.LongestRange(limit)}})
	default:
		httpapi.Error(w, 503, "telemetry_unavailable", "Scoped trace query is incomplete or unavailable")
	}
}

// readError reports a failed trace read: the registry's own failures, then
// the provider's as before.
func (s *Service) readError(w http.ResponseWriter, err error, detail bool) {
	var notReady notReadyError
	var telemetry *connectors.TelemetryError
	switch {
	case errors.Is(err, errAmbiguous):
		targetError(w, err)
	case errors.Is(err, errScopeChanged):
		httpapi.Error(w, 409, "trace_scope_changed", "Trace source or authorization changed during the query")
	case errors.As(err, &notReady):
		httpapi.Error(w, 503, "source_not_ready", "Trace source is not ready: "+strings.Join(notReady.reasons, ", "))
	case errors.Is(err, errClient):
		httpapi.Error(w, 503, "not_configured", "Registered Tempo connector cannot be read")
	case errors.As(err, &telemetry) && telemetry.Kind == "credentials":
		// The approved Vault credential could not be read.
		httpapi.Error(w, 503, "source_unavailable", "Trace source credential is unavailable")
	case !errors.As(err, &telemetry):
		httpapi.Error(w, 503, "trace_scope_unavailable", "Trace scope lookup failed")
	default:
		sourceError(w, err, detail)
	}
}

// observed runs the list search per registered connector.
func (s *Service) observed(ctx context.Context, all, selected []target, q connectors.CompleteTraceQuery) (connectors.TraceObservation, error) {
	groups, err := s.registryGroups(ctx, all, selected)
	if err != nil {
		return connectors.TraceObservation{}, err
	}
	return registryObserved(ctx, groups, q)
}

// complete reads one trace by ID per registered connector.
func (s *Service) complete(ctx context.Context, all, selected []target, q connectors.CompleteTraceQuery) ([]connectors.CompleteTrace, error) {
	groups, err := s.registryGroups(ctx, all, selected)
	if err != nil {
		return nil, err
	}
	return registryTrace(ctx, groups, q)
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
	observed, err := s.observed(r.Context(), all, targets, connectors.CompleteTraceQuery{Window: window, Filter: filter,
		ViewFilter: viewFilter(view)})
	if err != nil {
		s.readError(w, err, false)
		return
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if !s.unchanged(w, r, environments, targets, service) || !s.unchanged(w, r, environments, all, "") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// A search that reached its budget covers only its newest part (PA-78).
	covered := window
	if !observed.WindowStart.IsZero() && observed.WindowStart.After(covered.Start) {
		covered.Start = observed.WindowStart
	}
	startAt, endAt := covered.Start.UTC().Format(time.RFC3339), covered.End.UTC().Format(time.RFC3339)
	if !observed.PopulationComplete {
		// Search completeness is not attested for this deployment: return
		// the closed traces found, never counts or charts of that sample.
		list := BuildObservedList(observed.Traces, view, service, time.Since(start), "not_configured")
		list.WindowStartAt, list.WindowEndAt, list.PartialReason = startAt, endAt, "search_unattested"
		if observed.Truncated {
			list.PartialReason = "search_budget"
		}
		httpapi.WriteJSON(w, 200, list)
		return
	}
	list := BuildList(observed.Traces, covered, view, service, time.Since(start))
	list.WindowStartAt, list.WindowEndAt = startAt, endAt
	if observed.Truncated {
		list.Partial, list.PartialReason = true, "search_budget"
	}
	httpapi.WriteJSON(w, 200, list)
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
	traces, err := s.complete(r.Context(), all, targets, connectors.CompleteTraceQuery{TraceID: id})
	if err != nil {
		s.readError(w, err, true)
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
