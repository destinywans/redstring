package v3observe

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// metricProbe reports only whether a registered, scoped query returned data.
// It deliberately omits PromQL, provider URLs and incomplete KPI values.
type metricProbe struct {
	Status  string `json:"status"`
	Series  int    `json:"series"`
	Samples int    `json:"samples"`
}

func probeMetric(result connectors.MetricEvidence, err error) metricProbe {
	if err != nil || result.Source.Failure != nil {
		var telemetryErr *connectors.TelemetryError
		if errors.As(err, &telemetryErr) || errors.As(result.Source.Failure, &telemetryErr) {
			switch telemetryErr.Kind {
			case "scope":
				return metricProbe{Status: "not_registered"}
			case "configuration":
				return metricProbe{Status: "not_configured"}
			}
		}
		return metricProbe{Status: "unavailable"}
	}
	if result.LimitReached {
		return metricProbe{Status: "truncated"}
	}
	probe := metricProbe{Status: "no_samples", Series: len(result.Series)}
	for _, series := range result.Series {
		probe.Samples += len(series.Samples)
	}
	if probe.Samples > 0 {
		probe.Status = "observed"
	}
	return probe
}

// metricDashboard resolves one persisted service/environment binding before
// making any provider call. The current connector registrations expose request
// rate and p95 only. They cannot populate the V3 dashboard's status breakdown,
// SLO budget, full latency distribution or change markers truthfully, so the
// endpoint returns an explicit partial-capability error instead of a false 200.
func (s *Service) metricDashboard(w http.ResponseWriter, r *http.Request) {
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if service == "" || len(service) > 200 {
		httpapi.Error(w, 422, "invalid_service", "Select one service")
		return
	}
	if value := r.URL.Query().Get("range"); value != "" && value != "1h" {
		httpapi.Error(w, 422, "invalid_range", "Only the last hour is supported")
		return
	}
	align := r.URL.Query().Get("align")
	if align != "" && align != "false" && align != "true" {
		httpapi.Error(w, 422, "invalid_align", "Align must be true or false")
		return
	}
	envs, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return
	}
	targets, err := s.observationTargets(r.Context(), httpapi.CurrentUser(r), envs, "services", service, []string{"prometheus"})
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Service metrics are outside your authorized scope")
			return
		}
		httpapi.Error(w, 503, "metric_scope_unavailable", "Metric scope lookup failed")
		return
	}
	if len(targets) == 0 {
		httpapi.Error(w, 503, "not_configured", "No authorized Prometheus binding is configured for this service")
		return
	}
	if len(targets) != 1 {
		httpapi.Error(w, 422, "scope_limit", "Select one service and environment")
		return
	}
	if s.Telemetry == nil {
		httpapi.Error(w, 503, "not_configured", "Prometheus is not configured")
		return
	}
	target := targets[0]
	window := observationWindow(time.Hour)
	window.StepSeconds = 100 // 36 bounded points over the visible hour.
	available := make(map[string]metricProbe, 2)
	for _, queryID := range []string{"request_rate", "http_p95_seconds"} {
		result, err := s.Telemetry.QueryMetrics(r.Context(), target.Resource, queryID, window)
		available[queryID] = probeMetric(result, err)
	}
	missing := []string{"requests_by_status", "slo_budget", "latency_p50", "latency_p99", "latency_heatmap", "change_markers"}
	for _, queryID := range []string{"request_rate", "http_p95_seconds"} {
		if available[queryID].Status != "observed" {
			missing = append(missing, queryID)
		}
	}
	if align == "true" {
		missing = append(missing, "incident_alignment")
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 503, map[string]any{
		"error":   map[string]string{"code": "dashboard_unavailable", "message": "The complete metrics dashboard is not available from registered sources"},
		"service": target.ApplicationID, "environment_id": target.EnvironmentID,
		"available": available, "missing": missing,
	})
}
