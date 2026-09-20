package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

// Telemetry registration is operator configuration, never model-supplied URLs or queries.
func configureTelemetry(api *httpapi.Server, worker *httpapi.Worker) error {
	path := os.Getenv("AIOPS_TELEMETRY_CONFIG")
	if path == "" {
		api.Integrations = append(api.Integrations,
			httpapi.NewIntegration("prometheus", "Prometheus", "connector", "prometheus", false, nil, []string{"metrics.read"}),
			httpapi.NewIntegration("loki", "Loki", "connector", "loki", false, nil, []string{"logs.read"}))
		return nil
	}
	config, e := connectors.LoadTelemetryConfig(path)
	if e != nil {
		return e
	}
	metricApps, logApps := []string{}, []string{}
	for id, registration := range config.Applications {
		if config.Prometheus.URL != "" && len(registration.PrometheusQueries) > 0 {
			metricApps = append(metricApps, id)
		}
		if config.Loki.URL != "" && len(registration.LokiLabels) > 0 {
			logApps = append(logApps, id)
		}
	}
	sort.Strings(metricApps)
	sort.Strings(logApps)
	api.Integrations = append(api.Integrations,
		httpapi.NewIntegration("prometheus", "Prometheus", "connector", "prometheus", config.Prometheus.URL != "", metricApps, []string{"metrics.read"}),
		httpapi.NewIntegration("loki", "Loki", "connector", "loki", config.Loki.URL != "", logApps, []string{"logs.read"}))
	client, e := connectors.NewTelemetry(config)
	if e != nil {
		return e
	}
	previous := worker.Tools
	worker.Tools = func(ctx context.Context, app string) ([]inference.EvidenceTool, error) {
		var tools []inference.EvidenceTool
		if previous != nil {
			var err error
			tools, err = previous(ctx, app)
			if err != nil {
				return nil, err
			}
		}
		metricScope, _ := telemetryScope(ctx, api, app, store.EnvironmentID(ctx), "prometheus")
		logScope, _ := telemetryScope(ctx, api, app, store.EnvironmentID(ctx), "loki")
		registration := config.Applications[metricScope]
		logRegistration := config.Applications[logScope]
		ids := make([]string, 0, len(registration.PrometheusQueries))
		for id := range registration.PrometheusQueries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if config.Prometheus.URL != "" && len(ids) > 0 {
			tools = append(tools, inference.EvidenceTool{Name: "query_metrics", Description: "Read metrics for the current registered application over a recent time window bounded by server limits. Query must be one of these IDs: " + strings.Join(ids, ", ") + ". Raw PromQL is not accepted. Empty results are not healthy evidence.", Read: func(ctx context.Context, q string) (inference.Evidence, error) {
				id := strings.TrimSpace(q)
				if id == "" {
					id = ids[0]
				}
				result, e := client.QueryMetrics(ctx, metricScope, id, connectors.TimeWindow{})
				if e != nil {
					return inference.Evidence{}, e
				}
				b, e := json.Marshal(result)
				return inference.Evidence{Kind: "metrics", Title: "Prometheus: " + id, Content: string(b), Source: result.Source.URL}, e
			}})
		}
		if config.Loki.URL != "" && len(logRegistration.LokiLabels) > 0 {
			tools = append(tools, inference.EvidenceTool{Name: "query_logs", Description: "Read at most 100 application-scoped Loki log entries over a recent time window bounded by server limits. Query text cannot change the registered log scope.", Read: func(ctx context.Context, _ string) (inference.Evidence, error) {
				result, e := client.QueryLogs(ctx, logScope, connectors.TimeWindow{}, 0)
				if e != nil {
					return inference.Evidence{}, e
				}
				b, e := json.Marshal(result)
				return inference.Evidence{Kind: "logs", Title: "Loki application logs", Content: string(b), Source: result.Source.URL}, e
			}})
		}
		return tools, nil
	}
	api.Register("GET /api/v1/applications/{id}/telemetry", "viewer", func(w http.ResponseWriter, r *http.Request) {
		metricScope, _ := telemetryScope(r.Context(), api, r.PathValue("id"), r.URL.Query().Get("environment_id"), "prometheus")
		logScope, _ := telemetryScope(r.Context(), api, r.PathValue("id"), r.URL.Query().Get("environment_id"), "loki")
		a := config.Applications[metricScope]
		logs := config.Applications[logScope]
		ids := []string{}
		for id := range a.PrometheusQueries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		httpapi.WriteJSON(w, 200, map[string]any{"metric_query_ids": ids, "logs_configured": config.Loki.URL != "" && len(logs.LokiLabels) > 0})
	})
	api.Register("POST /api/v1/applications/{id}/metrics", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			EnvironmentID string    `json:"environment_id"`
			QueryID       string    `json:"query_id"`
			Start         time.Time `json:"start"`
			End           time.Time `json:"end"`
			StepSeconds   int       `json:"step_seconds"`
		}
		if !httpapi.Decode(w, r, &v) {
			return
		}
		scope, e := telemetryScope(r.Context(), api, r.PathValue("id"), v.EnvironmentID, "prometheus")
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		result, e := client.QueryMetrics(r.Context(), scope, v.QueryID, connectors.TimeWindow{Start: v.Start, End: v.End, StepSeconds: v.StepSeconds})
		if e != nil {
			telemetryError(w, e)
			return
		}
		httpapi.WriteJSON(w, 200, result)
	})
	api.Register("POST /api/v1/applications/{id}/logs", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			EnvironmentID string    `json:"environment_id"`
			Start         time.Time `json:"start"`
			End           time.Time `json:"end"`
			Limit         int       `json:"limit"`
		}
		if !httpapi.Decode(w, r, &v) {
			return
		}
		scope, e := telemetryScope(r.Context(), api, r.PathValue("id"), v.EnvironmentID, "loki")
		if e != nil {
			httpapi.AuthorizationError(w, e)
			return
		}
		result, e := client.QueryLogs(r.Context(), scope, connectors.TimeWindow{Start: v.Start, End: v.End}, v.Limit)
		if e != nil {
			telemetryError(w, e)
			return
		}
		httpapi.WriteJSON(w, 200, result)
	})
	return nil
}
func telemetryError(w http.ResponseWriter, e error) {
	var te *connectors.TelemetryError
	if errors.As(e, &te) && te.Kind == "scope" && te.HTTPStatus == 0 {
		httpapi.Error(w, 422, "invalid_telemetry_query", te.Message)
		return
	}
	httpapi.Error(w, 503, "telemetry_unavailable", "Telemetry query failed; check registration, time window and provider availability.")
}

func telemetryScope(ctx context.Context, api *httpapi.Server, app, envID, kind string) (string, error) {
	s, ok := api.Store.(*store.Store)
	if !ok {
		return app, nil
	}
	env, e := s.ResolveEnvironment(ctx, app, envID)
	if e != nil {
		return "", e
	}
	for _, b := range env.Bindings {
		if (kind == "" && (b.Kind == "prometheus" || b.Kind == "loki")) || b.Kind == kind {
			return b.Resource, nil
		}
	}
	return "", store.ErrNotFound
}
