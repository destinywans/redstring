package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wans/aiops-canvas/internal/actions"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"os"
)

type registeredKubernetes struct {
	ID           string                `json:"id"`
	Config       connectors.Config     `json:"config"`
	DemoRecovery bool                  `json:"demo_recovery"`
	Verification *BusinessVerification `json:"verification,omitempty"`
	recovery     actions.Connector
	connector    *connectors.Kubernetes
	digest       string
}
type platformRegistry struct {
	store      *store.Store
	kubernetes map[string]registeredKubernetes
}

func configurePlatform(ctx context.Context, s *store.Store, api *httpapi.Server, acts *actions.Service, worker *httpapi.Worker, legacy *connectors.Kubernetes, legacyConfig connectors.Config) (*platformRegistry, error) {
	registry := &platformRegistry{store: s, kubernetes: map[string]registeredKubernetes{}}
	var telemetry *connectors.Telemetry
	telemetryDigest := ""
	telemetryConfig := connectors.TelemetryConfig{}
	if path := os.Getenv("AIOPS_TELEMETRY_CONFIG"); path != "" {
		config, e := connectors.LoadTelemetryConfig(path)
		if e != nil {
			return nil, e
		}
		telemetryConfig = config
		telemetry, e = connectors.NewTelemetry(config)
		if e != nil {
			return nil, e
		}
		raw, _ := json.Marshal(config)
		telemetryDigest = store.Hash(string(raw))
	}
	if legacy != nil {
		raw, _ := json.Marshal(legacyConfig)
		registry.kubernetes["demo-kubernetes"] = registeredKubernetes{ID: "demo-kubernetes", Config: legacyConfig, DemoRecovery: true, connector: legacy, recovery: legacy, digest: store.Hash(string(raw))}
	}
	if path := os.Getenv("AIOPS_KUBERNETES_REGISTRY"); path != "" {
		raw, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		var entries []registeredKubernetes
		if e = json.Unmarshal(raw, &entries); e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if entry.ID == "" {
				return nil, errors.New("registered connector ID required")
			}
			if _, ok := registry.kubernetes[entry.ID]; ok {
				return nil, errors.New("duplicate registered connector")
			}
			entry.connector, e = connectors.New(entry.Config)
			if e != nil {
				return nil, e
			}
			if entry.DemoRecovery {
				entry.recovery = entry.connector
			} else if entry.Verification != nil {
				registered, ok := telemetryConfig.Applications[entry.Verification.TelemetryApplication]
				if !ok {
					return nil, errors.New("verification telemetry application is not registered")
				}
				for _, id := range []string{entry.Verification.RequestCounterQueryID, entry.Verification.ErrorCounterQueryID, entry.Verification.SampleAgeQueryID} {
					if registered.PrometheusQueries[id] == "" {
						return nil, errors.New("verification counter and freshness queries must be registered")
					}
				}
				entry.recovery, e = newBusinessRecovery(entry.connector, telemetry, *entry.Verification)
				if e != nil {
					return nil, e
				}
			}
			raw, _ := json.Marshal(entry)
			entry.digest = store.Hash(string(raw) + telemetryDigest)
			registry.kubernetes[entry.ID] = entry
		}
	}
	// Import old registrations only into untouched legacy default environments.
	// Removing a binding increments its revision and can never be undone by restart.
	legacyBindings := map[string][]core.Binding{}
	for app, v := range telemetryConfig.Applications {
		if telemetryConfig.Prometheus.URL != "" && len(v.PrometheusQueries) > 0 {
			legacyBindings[app] = append(legacyBindings[app], core.Binding{Kind: "prometheus", ConnectorID: "prometheus", Resource: app})
		}
		if telemetryConfig.Loki.URL != "" && len(v.LokiLabels) > 0 {
			legacyBindings[app] = append(legacyBindings[app], core.Binding{Kind: "loki", ConnectorID: "loki", Resource: app})
		}
	}
	if legacy != nil {
		legacyBindings["demo"] = append(legacyBindings["demo"], core.Binding{Kind: "kubernetes", ConnectorID: "demo-kubernetes", Resource: "deployment"})
	}
	for app, bindings := range legacyBindings {
		raw, _ := json.Marshal(bindings)
		if _, e := s.DB.Exec(ctx, "UPDATE application_environments SET bindings=$2,binding_revision=2 WHERE id=$1 AND binding_revision=1 AND bindings='[]'::jsonb", app+"-default", raw); e != nil {
			return nil, e
		}
	}
	api.EnvironmentCapabilities = func(env core.Environment) []string {
		caps := []string{"investigate"}
		if env.Archived {
			return []string{}
		}
		for _, b := range env.Bindings {
			switch b.Kind {
			case "kubernetes":
				if reg, ok := registry.kubernetes[b.ConnectorID]; ok {
					caps = append(caps, "deployment.read")
					if reg.recovery != nil && (!reg.DemoRecovery || env.ApplicationID == "demo") {
						caps = append(caps, "rollback.propose", "rollback.execute_after_approval", "recovery.verify")
					}
				}
			case "prometheus":
				if v, ok := telemetryConfig.Applications[b.Resource]; ok && len(v.PrometheusQueries) > 0 && telemetryConfig.Prometheus.URL != "" {
					caps = append(caps, "metrics.read")
				}
			case "loki":
				if v, ok := telemetryConfig.Applications[b.Resource]; ok && len(v.LokiLabels) > 0 && telemetryConfig.Loki.URL != "" {
					caps = append(caps, "logs.read")
				}
			}
		}
		return caps
	}
	api.ValidateBindings = func(bindings []core.Binding) error {
		seen := map[string]bool{}
		for _, b := range bindings {
			if seen[b.Kind] {
				return store.ErrInvalidReference
			}
			seen[b.Kind] = true
			switch b.Kind {
			case "kubernetes":
				if _, ok := registry.kubernetes[b.ConnectorID]; !ok || b.Resource != "deployment" {
					return store.ErrInvalidReference
				}
			case "prometheus", "loki":
				registration, exists := telemetryConfig.Applications[b.Resource]
				if b.ConnectorID != b.Kind || !exists || (b.Kind == "prometheus" && (telemetryConfig.Prometheus.URL == "" || len(registration.PrometheusQueries) == 0)) || (b.Kind == "loki" && (telemetryConfig.Loki.URL == "" || len(registration.LokiLabels) == 0)) {
					return store.ErrInvalidReference
				}
			default:
				return store.ErrInvalidReference
			}
		}
		return nil
	}
	api.Register("GET /api/v1/connector-registrations", "admin", func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{}
		for id, v := range registry.kubernetes {
			items = append(items, map[string]any{"id": id, "kind": "kubernetes", "namespace": v.Config.Namespace, "deployment": v.Config.Deployment, "resource": "deployment", "rollback_available": v.recovery != nil})
		}
		for resource, v := range telemetryConfig.Applications {
			if telemetryConfig.Prometheus.URL != "" && len(v.PrometheusQueries) > 0 {
				items = append(items, map[string]any{"id": "prometheus", "kind": "prometheus", "resource": resource, "rollback_available": false})
			}
			if telemetryConfig.Loki.URL != "" && len(v.LokiLabels) > 0 {
				items = append(items, map[string]any{"id": "loki", "kind": "loki", "resource": resource, "rollback_available": false})
			}
		}
		httpapi.WriteJSON(w, 200, map[string]any{"items": items})
	})
	acts.Resolve = func(ctx context.Context, inv string, p *connectors.RollbackPlan) (actions.Connector, error) {
		var app, env string
		if e := s.DB.QueryRow(ctx, "SELECT application_id,COALESCE(environment_id,'') FROM investigations WHERE id=$1", inv).Scan(&app, &env); e != nil {
			return nil, e
		}
		if e := s.AuthorizeApplication(ctx, store.Principal(ctx), app, "operate"); e != nil {
			return nil, e
		}
		v, reg, e := registry.resolve(ctx, app, env)
		if e != nil {
			return nil, e
		}
		if reg.recovery == nil || (reg.DemoRecovery && app != "demo") {
			return nil, errors.New("business recovery verification is not registered; read-only investigation remains available")
		}
		if p.Digest != "" && (p.EnvironmentID != v.ID || p.BindingRevision != v.BindingRevision || p.ConnectorID != reg.ID || p.ConfigurationDigest != reg.digest) {
			return nil, store.ErrConflict
		}
		p.EnvironmentID = v.ID
		p.BindingRevision = v.BindingRevision
		p.ConnectorID = reg.ID
		p.ConfigurationDigest = reg.digest
		return reg.recovery, nil
	}
	previous := worker.Tools
	worker.Tools = func(ctx context.Context, app string) (result []inference.EvidenceTool, resultErr error) {
		frozen, e := s.ResolveEnvironment(ctx, app, store.EnvironmentID(ctx))
		if e != nil {
			return nil, e
		}
		defer func() {
			if resultErr == nil {
				result = freezeEnvironmentTools(s.ResolveEnvironment, app, frozen, result)
			}
		}()
		tools := []inference.EvidenceTool{}
		if previous != nil {
			v, e := previous(ctx, app)
			if e != nil {
				return nil, e
			}
			for _, t := range v {
				if t.Name != "inspect_deployment" {
					tools = append(tools, t)
				}
			}
		}
		_, reg, e := registry.resolve(ctx, app, store.EnvironmentID(ctx))
		if errors.Is(e, store.ErrNotFound) {
			return tools, nil
		}
		if e != nil {
			return nil, e
		}
		tools = append(tools, inference.EvidenceTool{Name: "inspect_deployment", Description: "Read the environment's registered Kubernetes Deployment and rollout revisions; the demo adapter also returns demo health, traffic counters and Pod logs. Never executes commands.", Read: func(ctx context.Context, _ string) (inference.Evidence, error) {
			if reg.DemoRecovery && app == "demo" {
				evidence, e := reg.connector.ReadEvidence(ctx)
				if e != nil {
					return inference.Evidence{}, e
				}
				raw, e := json.Marshal(evidence)
				return inference.Evidence{Kind: "kubernetes", Title: "Registered demo Deployment and business evidence", Source: "kubernetes://" + reg.ID + "/" + reg.Config.Namespace + "/" + reg.Config.Deployment, Content: string(raw)}, e
			}
			snap, e := reg.connector.Snapshot(ctx)
			if e != nil {
				return inference.Evidence{}, e
			}
			raw, e := json.Marshal(snap)
			return inference.Evidence{Kind: "kubernetes", Title: "Environment Deployment", Source: "kubernetes://" + reg.ID + "/" + reg.Config.Namespace + "/" + reg.Config.Deployment, Content: string(raw)}, e
		}})
		return tools, nil
	}
	return registry, nil
}
func (r *platformRegistry) resolve(ctx context.Context, app, env string) (core.Environment, registeredKubernetes, error) {
	v, e := r.store.ResolveEnvironment(ctx, app, env)
	if e != nil {
		return v, registeredKubernetes{}, e
	}
	for _, b := range v.Bindings {
		if b.Kind == "kubernetes" {
			reg, ok := r.kubernetes[b.ConnectorID]
			if !ok {
				return v, reg, store.ErrNotFound
			}
			return v, reg, nil
		}
	}
	return v, registeredKubernetes{}, store.ErrNotFound
}

// The selected environment and revision are part of a tool's authority. A
// queued or in-flight run must never silently follow a newly rebound connector.
func freezeEnvironmentTools(resolve func(context.Context, string, string) (core.Environment, error), app string, frozen core.Environment, tools []inference.EvidenceTool) []inference.EvidenceTool {
	for i := range tools {
		read := tools[i].Read
		tools[i].Read = func(ctx context.Context, query string) (inference.Evidence, error) {
			current, e := resolve(ctx, app, frozen.ID)
			if e != nil {
				return inference.Evidence{}, e
			}
			if current.Archived || current.ID != frozen.ID || current.ApplicationID != app || current.BindingRevision != frozen.BindingRevision {
				return inference.Evidence{}, store.ErrConflict
			}
			return read(ctx, query)
		}
	}
	return tools
}
