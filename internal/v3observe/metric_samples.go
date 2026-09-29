package v3observe

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type metricSamplesResponse struct {
	// These IDs name the authorized persisted binding. The registered PromQL
	// must be reviewed to ensure it selects that binding's data domain.
	ApplicationID string                    `json:"application_id"`
	EnvironmentID string                    `json:"environment_id"`
	QueryID       string                    `json:"query_id"`
	Source        string                    `json:"source"`
	Window        connectors.TimeWindow     `json:"window"`
	CapturedAt    time.Time                 `json:"captured_at"`
	Status        string                    `json:"status"`
	Samples       []connectors.MetricSample `json:"samples"`
}

func metricSamplesRequest(r *http.Request) (string, string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 2 || len(query["service"]) != 1 || len(query["query_id"]) != 1 {
		return "", "", false
	}
	service, queryID := query.Get("service"), query.Get("query_id")
	if service == "" || len(service) > 200 || strings.TrimSpace(service) != service ||
		(queryID != "request_rate" && queryID != "http_p95_seconds") {
		return "", "", false
	}
	return service, queryID, true
}

func metricSamplesTargetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Service metrics are outside your authorized scope")
	case errors.Is(err, errAmbiguousObservationBinding):
		httpapi.Error(w, 409, "ambiguous_binding", "Select one Prometheus binding for this service")
	case errors.Is(err, errObservationScopeLimit):
		httpapi.Error(w, 422, "scope_limit", "Narrow the service and environment")
	default:
		httpapi.Error(w, 503, "metric_scope_unavailable", "Metric scope lookup failed")
	}
}

func sameMetricTarget(a, b observationTarget) bool {
	return a.ApplicationID == b.ApplicationID && a.EnvironmentID == b.EnvironmentID &&
		a.Provider == b.Provider && a.Resource == b.Resource
}

func metricResultSamples(result connectors.MetricEvidence, window connectors.TimeWindow) ([]connectors.MetricSample, string, bool) {
	if result.LimitReached || len(result.Source.Warnings) > 0 || len(result.Series) > 1 ||
		result.Source.Provider != "prometheus" || result.Source.CapturedAt.IsZero() ||
		result.Source.HTTPStatus != http.StatusOK ||
		!result.Source.Window.Start.Equal(window.Start) || !result.Source.Window.End.Equal(window.End) ||
		result.Source.Window.StepSeconds != window.StepSeconds {
		return nil, "", false
	}
	samples := make([]connectors.MetricSample, 0, 32)
	for _, series := range result.Series {
		if len(series.Samples) > 32 {
			return nil, "", false
		}
		previous := math.Inf(-1)
		for _, sample := range series.Samples {
			value, err := strconv.ParseFloat(sample.Value, 64)
			if len(sample.Value) == 0 || len(sample.Value) > 64 || err != nil || math.IsNaN(value) || math.IsInf(value, 0) ||
				math.IsNaN(sample.Timestamp) || math.IsInf(sample.Timestamp, 0) ||
				sample.Timestamp <= previous || sample.Timestamp < float64(window.Start.Unix()) ||
				sample.Timestamp > float64(window.End.Unix()) {
				return nil, "", false
			}
			previous = sample.Timestamp
			samples = append(samples, sample)
		}
	}
	if result.Empty != (len(samples) == 0) {
		return nil, "", false
	}
	if len(samples) == 0 {
		return []connectors.MetricSample{}, "no_samples", true
	}
	return samples, "observed", true
}

// metricSamples is a complete, bounded read of one registered aggregate
// Prometheus expression. Configuration owns the query's data domain; this
// handler owns authorization, provenance, result completeness and safe output.
// It never invents dashboard status splits, SLO budgets or change markers.
func (s *Service) metricSamples(w http.ResponseWriter, r *http.Request) {
	service, queryID, valid := metricSamplesRequest(r)
	if !valid {
		httpapi.Error(w, 422, "invalid_metric_request", "Choose one service and registered metric query")
		return
	}
	environments, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return
	}
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	if scope.All || len(environments) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Choose one environment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	targets, err := s.observationTargets(ctx, user, environments, "services", service, []string{"prometheus"})
	if err != nil {
		metricSamplesTargetError(w, err)
		return
	}
	if len(targets) == 0 {
		httpapi.Error(w, 503, "not_configured", "No authorized Prometheus binding is configured for this service")
		return
	}
	if len(targets) != 1 {
		httpapi.Error(w, 409, "ambiguous_binding", "Select one Prometheus binding for this service")
		return
	}
	if s.Telemetry == nil {
		httpapi.Error(w, 503, "not_configured", "Prometheus is not configured")
		return
	}
	target := targets[0]
	end := time.Now().UTC().Truncate(time.Second)
	window := connectors.TimeWindow{Start: end.Add(-15 * time.Minute), End: end, StepSeconds: 30}
	result, queryErr := s.Telemetry.QueryMetrics(ctx, target.Resource, queryID, window)
	// A provider request can outlive a grant or binding update. Re-resolve the
	// current object before returning any upstream value to the caller.
	current, currentErr := s.observationTargets(ctx, user, environments, "services", service, []string{"prometheus"})
	if currentErr != nil {
		metricSamplesTargetError(w, currentErr)
		return
	}
	if len(current) != 1 || !sameMetricTarget(target, current[0]) {
		httpapi.Error(w, 409, "metric_source_changed", "Service metric source changed during the query")
		return
	}
	if queryErr != nil || result.Source.Failure != nil {
		httpapi.Error(w, 503, "metric_query_unavailable", "Registered Prometheus query is unavailable")
		return
	}
	if result.Source.ApplicationID != target.Resource || result.Source.QueryID != queryID {
		httpapi.Error(w, 503, "metric_result_incomplete", "Prometheus result provenance is incomplete")
		return
	}
	samples, status, complete := metricResultSamples(result, window)
	if !complete || ctx.Err() != nil {
		httpapi.Error(w, 503, "metric_result_incomplete", "Prometheus result is incomplete")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, metricSamplesResponse{
		ApplicationID: target.ApplicationID, EnvironmentID: target.EnvironmentID,
		QueryID: queryID, Source: "prometheus", Window: window,
		CapturedAt: result.Source.CapturedAt, Status: status, Samples: samples,
	})
}
