// Package v3observe adapts persisted CMDB data and registered telemetry to the
// V3 read contracts without manufacturing health, service levels or log counts.
package v3observe

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
	MetricReader        MetricSamplesReader
	MetricsConversation MetricsConversation
	// Registry and Clients serve the log page from the connector registry
	// (PA-19 P3 stage 2 step 3; the only path since P4): each target is read
	// with its own connector's client. Without them the page is not
	// configured.
	Registry *v3telemetry.Resolver
	Clients  *v3telemetry.Clients
}

type observationTarget struct {
	ApplicationID string
	EnvironmentID string
	TeamID        string
	Resource      string
	Provider      string
	// TargetID and ConnectorID name the registry target.
	TargetID    string
	ConnectorID string
}

var errAmbiguousObservationBinding = errors.New("multiple telemetry bindings for one application and environment")
var errObservationScopeLimit = errors.New("authorized observation scope exceeds bound")

func New(st *store.Store, access Access) *Service {
	return &Service{Store: st, Access: access}
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

// observationTargets use only the registry targets of surface, authorized
// for module. An arbitrary query string never becomes a provider URL or an
// unbounded scope.
func (s *Service) observationTargets(ctx context.Context, user core.User, environments []string, module string, surface v3telemetry.Surface,
	service string, providers []string) ([]observationTarget, error) {
	// A broad grant can scan many hidden targets. Bound the full database and
	// policy walk by elapsed time without treating hidden rows as a result cap.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := []observationTarget{}
	seen := map[string]observationTarget{}
	var cursor observationTarget
	hasCursor := false
	hadCandidates, authorized := false, false
	for {
		// Release the query connection before authorization, which may need
		// the same pool. The cursor covers the complete sort key; identical
		// duplicate bindings are interchangeable and may be skipped at a page
		// boundary. Hidden candidates never consume the authorized result cap.
		batch, err := s.registryObservationBatch(ctx, environments, service, surface, cursor, hasCursor)
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
			authorized = true
			key := target.ApplicationID + "\x00" + target.EnvironmentID
			if previous, exists := seen[key]; exists {
				// Registry targets are enumerated for the whole surface, so
				// two targets are ambiguous whichever provider is selected.
				if previous.Provider != target.Provider || previous.Resource != target.Resource || previous.TargetID != target.TargetID {
					return nil, errAmbiguousObservationBinding
				}
				continue
			}
			seen[key] = target
			if !contains(providers, target.Provider) {
				continue
			}
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
	if hadCandidates && !authorized {
		return nil, store.ErrForbidden
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

// registryObservationBatch lists a page of the registry targets serving
// surface, for every provider of it (the caller filters providers after the
// ambiguity check).
func (s *Service) registryObservationBatch(ctx context.Context, environments []string, service string, surface v3telemetry.Surface, cursor observationTarget, hasCursor bool) ([]observationTarget, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,''),c.provider,t.external_id,t.connector_id,t.id
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN v3_connector_targets t ON t.application_id=a.id AND t.environment_id=ve.environment_id
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[]) AND ($2='' OR a.id=$2 OR a.key=$2)
 AND t.target_kind||':'||c.provider=ANY($3::text[])
 AND (NOT $4::boolean OR (a.id,ve.environment_id,t.id)>($5,$6,$7))
 ORDER BY a.id,ve.environment_id,t.id LIMIT 200`, environments, service, v3telemetry.SurfacePairs(surface),
		hasCursor, cursor.ApplicationID, cursor.EnvironmentID, cursor.TargetID)
	if err != nil {
		return nil, err
	}
	batch := make([]observationTarget, 0, 200)
	for rows.Next() {
		var target observationTarget
		if err = rows.Scan(&target.ApplicationID, &target.EnvironmentID, &target.TeamID, &target.Provider, &target.Resource,
			&target.ConnectorID, &target.TargetID); err != nil {
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

var (
	errRegistrySourceChanged = errors.New("registered source changed since the targets were listed")
	errRegistryClient        = errors.New("registered source cannot be read")
)

// sourceNotReadyError lists why a registered source cannot be read yet.
type sourceNotReadyError struct{ reasons []string }

func (e sourceNotReadyError) Error() string {
	return "source not ready: " + strings.Join(e.reasons, ", ")
}

// registryClient resolves the registered source of target and returns its
// connector's client and the resource key its queries use. It fails closed:
// an ambiguous, changed or unready source is an error, never a fallback to
// the file configuration.
func (s *Service) registryClient(ctx context.Context, target observationTarget, surface v3telemetry.Surface) (*connectors.Telemetry, string, error) {
	if s.Registry == nil || s.Clients == nil {
		return nil, "", errRegistryClient
	}
	source, err := s.Registry.Resolve(ctx, s.Store.DB, target.ApplicationID, target.EnvironmentID, surface)
	switch {
	case errors.Is(err, v3telemetry.ErrAmbiguous):
		return nil, "", errAmbiguousObservationBinding
	case errors.Is(err, v3telemetry.ErrNotConfigured):
		return nil, "", errRegistrySourceChanged
	case err != nil:
		return nil, "", err
	case source.TargetID != target.TargetID || source.ConnectorID != target.ConnectorID ||
		source.Provider != target.Provider || source.ResourceID != target.Resource:
		return nil, "", errRegistrySourceChanged
	case len(source.NotReady) > 0:
		return nil, "", sourceNotReadyError{reasons: source.NotReady}
	}
	client, resource, err := s.Clients.For(target.EnvironmentID, source)
	if err != nil {
		return nil, "", errRegistryClient
	}
	return client, resource, nil
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func observationWindow(lookback time.Duration) connectors.TimeWindow {
	// Loki's count and histogram queries require millisecond-aligned window
	// boundaries. Derive both ends from one instant to preserve the requested
	// lookback duration exactly.
	now := time.Now().UTC().Truncate(time.Millisecond)
	return connectors.TimeWindow{Start: now.Add(-lookback), End: now}
}
