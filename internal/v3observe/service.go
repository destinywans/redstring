// Package v3observe adapts persisted CMDB data and registered telemetry to the
// V3 read contracts without manufacturing health, service levels or log counts.
package v3observe

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3conversation"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Telemetry interface {
	QueryMetrics(context.Context, string, string, connectors.TimeWindow) (connectors.MetricEvidence, error)
	QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error)
	QueryTracesBatchID(context.Context, []string, connectors.TimeWindow, int, string) (map[string]connectors.TraceEvidence, error)
}

type MetricSamplesReader interface {
	QueryService(context.Context, httpapi.V3EnvironmentScope, core.User, string, string, string,
		connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error)
}

// MetricsConversation accepts a verified, bounded metric snapshot as one
// durable conversation turn. Its implementation rechecks source access before
// model execution and every later read of this metrics-backed conversation.
type MetricsConversation interface {
	FindMetricsQuestion(context.Context, v3conversation.MetricsQuestionLookup) (v3conversation.MetricsQuestionResult, bool, error)
	StartMetricsQuestion(context.Context, v3conversation.MetricsQuestionInput) (v3conversation.MetricsQuestionResult, error)
}

type Service struct {
	Store               *store.Store
	Access              Access
	Telemetry           Telemetry
	MetricReader        MetricSamplesReader
	MetricsConversation MetricsConversation
	// SourceCompare, when set, is told which file-configured bindings were
	// read (PA-19 compare mode). It never changes the result.
	SourceCompare v3telemetry.Observer
}

type observationTarget struct {
	ApplicationID string
	EnvironmentID string
	TeamID        string
	Resource      string
	Provider      string
}

var errAmbiguousObservationBinding = errors.New("multiple telemetry bindings for one application and environment")
var errObservationScopeLimit = errors.New("authorized observation scope exceeds bound")

func New(st *store.Store, access Access, telemetry Telemetry) *Service {
	return &Service{Store: st, Access: access, Telemetry: telemetry}
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/services/discover", "viewer", s.discover)
	api.MarkV3ObjectRoute("GET /api/v1/services/discover", "services", "view")
	api.Register("GET /api/v1/metrics/dashboard", "viewer", s.metricDashboard)
	api.MarkV3ObjectRoute("GET /api/v1/metrics/dashboard", "services", "view")
	api.Register("GET /api/v1/metrics/samples", "viewer", s.metricSamples)
	api.MarkV3ObjectRoute("GET /api/v1/metrics/samples", "services", "view")
	api.Register("POST /api/v1/metrics/ask", "viewer", s.metricsAsk)
	api.MarkV3ObjectRoute("POST /api/v1/metrics/ask", "conversation", "change")
	api.Register("GET /api/v1/logs", "viewer", s.logs)
	api.MarkV3ObjectRoute("GET /api/v1/logs", "logs", "view")
}

func (s *Service) allowedEnvironments(w http.ResponseWriter, r *http.Request, module string) ([]string, bool) {
	if s.Store == nil || s.Access == nil {
		httpapi.Error(w, 503, "observe_unavailable", "Observation storage or policy is not configured")
		return nil, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	if module == "" {
		return scope.EnvironmentIDs, true
	}
	allowed := make([]string, 0, len(scope.EnvironmentIDs))
	for _, id := range scope.EnvironmentIDs {
		if err := s.Access.Authorize(r.Context(), httpapi.CurrentUser(r), rbac.V3Decision{Module: module, Action: "view", EnvironmentID: id}); err == nil {
			allowed = append(allowed, id)
		} else if !errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 503, "observe_unavailable", "Observation policy is unavailable")
			return nil, false
		}
	}
	if len(allowed) == 0 {
		httpapi.Error(w, 403, "forbidden", "This observation is outside your authorized scope")
		return nil, false
	}
	return allowed, true
}

// observationTargets use only persisted application/environment bindings. An
// arbitrary query string never becomes a provider URL or an unbounded scope.
func (s *Service) observationTargets(ctx context.Context, user core.User, environments []string, module, service string, providers []string) ([]observationTarget, error) {
	// A broad grant can scan many hidden bindings. Bound the full database and
	// policy walk by elapsed time without treating hidden rows as a result cap.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := []observationTarget{}
	seen := map[string]observationTarget{}
	var cursor observationTarget
	hasCursor := false
	hadCandidates := false
	for {
		// Release the query connection before authorization, which may need
		// the same pool. The cursor covers the complete sort key; identical
		// duplicate bindings are interchangeable and may be skipped at a page
		// boundary. Hidden candidates never consume the authorized result cap.
		batch, err := s.observationBatch(ctx, environments, service, providers, cursor, hasCursor)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		hadCandidates = true
		for _, target := range batch {
			cursor = target
			hasCursor = true
			if err = s.authorizeObservationTarget(ctx, user, module, target); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			key := target.ApplicationID + "\x00" + target.EnvironmentID
			if previous, exists := seen[key]; exists {
				if previous.Provider != target.Provider || previous.Resource != target.Resource {
					return nil, errAmbiguousObservationBinding
				}
				continue
			}
			seen[key] = target
			result = append(result, target)
			if len(result) > 100 {
				return nil, errObservationScopeLimit
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
	if s.SourceCompare != nil {
		for _, target := range result {
			s.SourceCompare.ObserveLegacy(v3telemetry.Use{ApplicationID: target.ApplicationID, EnvironmentID: target.EnvironmentID,
				Provider: target.Provider, Resource: target.Resource})
		}
	}
	return result, nil
}

func (s *Service) authorizeObservationTarget(ctx context.Context, user core.User, module string, target observationTarget) error {
	err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: target.EnvironmentID, ApplicationID: target.ApplicationID})
	if errors.Is(err, store.ErrForbidden) && target.TeamID != "" {
		return s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view", EnvironmentID: target.EnvironmentID, TeamID: target.TeamID})
	}
	return err
}

func (s *Service) observationBatch(ctx context.Context, environments []string, service string, providers []string, cursor observationTarget, hasCursor bool) ([]observationTarget, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,''),b.kind,b.resource
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, resource text) ON b.kind=ANY($3::text[])
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[]) AND ($2='' OR a.id=$2 OR a.key=$2)
 AND b.resource<>''
 AND (NOT $4::boolean OR (a.id,ve.environment_id,b.kind,b.resource)>($5,$6,$7,$8))
 ORDER BY a.id,ve.environment_id,b.kind,b.resource LIMIT 200`, environments, service, providers,
		hasCursor, cursor.ApplicationID, cursor.EnvironmentID, cursor.Provider, cursor.Resource)
	if err != nil {
		return nil, err
	}
	batch := make([]observationTarget, 0, 200)
	for rows.Next() {
		var target observationTarget
		if err = rows.Scan(&target.ApplicationID, &target.EnvironmentID, &target.TeamID, &target.Provider, &target.Resource); err != nil {
			break
		}
		batch = append(batch, target)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return batch, nil
}

func observationWindow(lookback time.Duration) connectors.TimeWindow {
	// Loki's count and histogram queries require millisecond-aligned window
	// boundaries. Derive both ends from one instant to preserve the requested
	// lookback duration exactly.
	now := time.Now().UTC().Truncate(time.Millisecond)
	return connectors.TimeWindow{Start: now.Add(-lookback), End: now}
}
