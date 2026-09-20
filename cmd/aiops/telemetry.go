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
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

// Telemetry registration is operator configuration, never model-supplied URLs or queries.
func configureTelemetry(api *httpapi.Server, worker *httpapi.Worker) error {
	path := os.Getenv("AIOPS_TELEMETRY_CONFIG")
	if path == "" {
		registerTelemetryMetadata(api, connectors.TelemetryConfig{}, nil)
		api.Integrations = append(api.Integrations,
			httpapi.NewIntegration("prometheus", "Prometheus", "connector", "prometheus", false, nil, []string{"metrics.read"}),
			httpapi.NewIntegration("loki", "Loki", "connector", "loki", false, nil, []string{"logs.read"}))
		return nil
	}
	config, e := connectors.LoadTelemetryConfig(path)
	if e != nil {
		return e
	}
	metrics := httpapi.NewIntegration("prometheus", "Prometheus", "connector", "prometheus", config.Prometheus.URL != "", nil, []string{"metrics.read"})
	logs := httpapi.NewIntegration("loki", "Loki", "connector", "loki", config.Loki.URL != "", nil, []string{"logs.read"})
	for id, registration := range config.Applications {
		if config.Prometheus.URL != "" && len(registration.PrometheusQueries) > 0 {
			metrics.RegisteredBindings = append(metrics.RegisteredBindings, core.Binding{Kind: "prometheus", ConnectorID: "prometheus", Resource: id})
		}
		if config.Loki.URL != "" && len(registration.LokiLabels) > 0 {
			logs.RegisteredBindings = append(logs.RegisteredBindings, core.Binding{Kind: "loki", ConnectorID: "loki", Resource: id})
		}
	}
	api.Integrations = append(api.Integrations, metrics, logs)
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
			tools = append(tools, logEvidenceTool(client, logScope))
		}
		return tools, nil
	}
	registerTelemetryMetadata(api, config, client)
	api.Register("POST /api/v1/applications/{id}/metrics", "viewer", func(w http.ResponseWriter, r *http.Request) {
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
	api.Register("POST /api/v1/applications/{id}/logs", "viewer", func(w http.ResponseWriter, r *http.Request) {
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

// The model receives a small bounded sample. Interactive log browsing retains
// its independently configured limits and all returned evidence is persisted.
func logEvidenceTool(client *connectors.Telemetry, scope string) inference.EvidenceTool {
	return inference.EvidenceTool{Name: "query_logs", Description: "Read a bounded sample of at most 10 application-scoped Loki log entries (or the lower server limit) over a recent time window. Results may omit other entries; do not infer overall health from this small sample. Query text cannot change the registered log scope.", Read: func(ctx context.Context, _ string) (inference.Evidence, error) {
		result, err := client.QueryLogs(ctx, scope, connectors.TimeWindow{}, min(10, client.Limits().MaxLogEntries))
		if err != nil {
			return inference.Evidence{}, err
		}
		raw, err := json.Marshal(result)
		return inference.Evidence{Kind: "logs", Title: "Loki application log sample", Content: string(raw), Source: result.Source.URL}, err
	}}
}

// Registration metadata is available even when no telemetry connector is
// configured. A missing binding is an empty capability; an invalid environment
// or denied application remains an error, never an apparently healthy result.
func registerTelemetryMetadata(api *httpapi.Server, config connectors.TelemetryConfig, client *connectors.Telemetry) {
	api.Register("GET /api/v1/applications/{id}/telemetry", "viewer", func(w http.ResponseWriter, r *http.Request) {
		app, envID := r.PathValue("id"), r.URL.Query().Get("environment_id")
		scopes := map[string]string{"prometheus": app, "loki": app}
		if repository, ok := api.Store.(*store.Store); ok {
			env, err := repository.ResolveEnvironment(r.Context(), app, envID)
			if err != nil {
				httpapi.AuthorizationError(w, err)
				return
			}
			scopes = map[string]string{}
			for _, binding := range env.Bindings {
				if binding.Kind == "prometheus" || binding.Kind == "loki" {
					scopes[binding.Kind] = binding.Resource
				}
			}
		}
		ids := []string{}
		if config.Prometheus.URL != "" {
			for id := range config.Applications[scopes["prometheus"]].PrometheusQueries {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		logsConfigured := config.Loki.URL != "" && len(config.Applications[scopes["loki"]].LokiLabels) > 0
		var limits *connectors.TelemetryLimits
		if client != nil {
			value := client.Limits()
			limits = &value
		}
		httpapi.WriteJSON(w, 200, map[string]any{"metric_query_ids": ids, "logs_configured": logsConfigured, "configured": len(ids) > 0 || logsConfigured, "limits": limits})
	})
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
