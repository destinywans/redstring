package v3telemetry

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"

	"github.com/destinywans/redstring/internal/connectors"
)

// Legacy is one file-configured source: a persisted binding resolved through
// AIOPS_TELEMETRY_CONFIG, as the legacy readers use it.
type Legacy struct {
	Provider   string
	Resource   string
	Endpoint   string
	AuthMethod string
	TenantID   string
	Username   string
	CustomCA   bool
	Scope      connectors.ApplicationTelemetry
	Queries    map[string]string
}

// LegacySource resolves a binding through the file registration. ok is false
// when the legacy reader itself could not serve it (provider endpoint or
// resource scope missing).
func LegacySource(cfg connectors.TelemetryConfig, provider, resource string) (Legacy, bool) {
	a, registered := cfg.Applications[resource]
	endpoint, known := endpointOf(cfg, provider)
	if !registered || !known || endpoint.URL == "" {
		return Legacy{}, false
	}
	switch provider {
	case "prometheus":
		registered = len(a.PrometheusQueries) > 0
	case "loki":
		registered = len(a.LokiLabels) > 0
	case "elasticsearch":
		registered = a.ElasticsearchIndex != "" && len(a.ElasticsearchFilters) > 0
	case "tempo":
		registered = a.TempoService != "" && len(a.TempoAttributes) > 0
	}
	if !registered {
		return Legacy{}, false
	}
	legacy := Legacy{Provider: provider, Resource: resource, Endpoint: canonicalEndpoint(endpoint.URL),
		AuthMethod: "none", TenantID: endpoint.TenantID, Username: endpoint.Username, CustomCA: endpoint.CAFile != "",
		Scope: ProviderScope(provider, a)}
	if endpoint.BearerTokenFile != "" {
		legacy.AuthMethod = "bearer"
	} else if endpoint.Username != "" || endpoint.PasswordFile != "" {
		legacy.AuthMethod = "basic"
	}
	if provider == "prometheus" {
		legacy.Queries = make(map[string]string, len(a.PrometheusQueries))
		for id, expression := range a.PrometheusQueries {
			legacy.Queries[id] = expression
		}
	}
	return legacy, true
}

func endpointOf(cfg connectors.TelemetryConfig, provider string) (connectors.TelemetryEndpoint, bool) {
	switch provider {
	case "prometheus":
		return cfg.Prometheus, true
	case "loki":
		return cfg.Loki, true
	case "elasticsearch":
		return cfg.Elasticsearch, true
	case "tempo":
		return cfg.Tempo, true
	}
	return connectors.TelemetryEndpoint{}, false
}

// Comparison states. Only IDs and these codes are ever reported; endpoints,
// selectors and expressions stay in the process.
const (
	StateMatch              = "match"
	StateMatchNotReady      = "match_not_ready"
	StateDiffers            = "differs"
	StateMissing            = "missing"
	StateAmbiguous          = "ambiguous"
	StateLegacyUnregistered = "legacy_unregistered"
	StateUnavailable        = "unavailable"
	// StateRegistryOnly is a registry target of a data plane the service has
	// no legacy binding for: a mapping edit wrote only the registry (stage 2
	// step 6). A legacy reader would read nothing there.
	StateRegistryOnly = "registry_only"
	// StateRegistryNative is a workload target on an instance without a
	// legacy Kubernetes registration: no legacy reader exists for that plane
	// (rollback reads the registry only), so rolling a reader back does not
	// change what is read. Listed, not a difference.
	StateRegistryNative = "registry_native"
)

// Result is one comparison of a legacy binding with the registry.
type Result struct {
	State   string   `json:"state"`
	Reasons []string `json:"reasons,omitempty"`
}

// Compare reports whether the registry would read the same source as the
// file configuration. Reasons name the differing fields (or query IDs), or
// for match_not_ready why the registered source is not readable yet.
func Compare(legacy Legacy, legacyOK bool, unified Source, err error) Result {
	switch {
	case !legacyOK:
		return Result{State: StateLegacyUnregistered}
	case errors.Is(err, ErrNotConfigured):
		return Result{State: StateMissing}
	case errors.Is(err, ErrAmbiguous):
		return Result{State: StateAmbiguous}
	case err != nil:
		return Result{State: StateUnavailable}
	}
	var reasons []string
	differ := func(name string, equal bool) {
		if !equal {
			reasons = append(reasons, name)
		}
	}
	differ("provider", legacy.Provider == unified.Provider)
	differ("endpoint", legacy.Endpoint == canonicalEndpoint(unified.Endpoint))
	differ("auth_method", legacy.AuthMethod == unified.AuthMethod)
	differ("tenant_id", legacy.TenantID == unified.TenantID)
	differ("username", legacy.Username == unified.Username)
	differ("custom_ca", !legacy.CustomCA)
	notReady := make([]string, 0, len(unified.NotReady))
	scopeMissing := false
	for _, reason := range unified.NotReady {
		if reason == "scope_missing" {
			scopeMissing = true
		} else {
			notReady = append(notReady, reason)
		}
	}
	if legacy.Provider == unified.Provider {
		if legacy.Provider == "prometheus" {
			ids := make([]string, 0, len(legacy.Queries))
			for id := range legacy.Queries {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				expression, ok := unified.Queries[id]
				if !ok {
					reasons = append(reasons, "query_missing:"+id)
				} else if expression != legacy.Queries[id] {
					reasons = append(reasons, "query_changed:"+id)
				}
			}
		} else if scopeMissing {
			reasons = append(reasons, "scope_missing")
		} else {
			reasons = append(reasons, scopeDifferences(legacy.Provider, legacy.Scope, unified.Scope)...)
		}
	}
	if len(reasons) > 0 {
		return Result{State: StateDiffers, Reasons: reasons}
	}
	if len(notReady) > 0 {
		return Result{State: StateMatchNotReady, Reasons: notReady}
	}
	return Result{State: StateMatch}
}

func scopeDifferences(provider string, legacy, unified connectors.ApplicationTelemetry) []string {
	l, u := normalize(ProviderScope(provider, legacy)), normalize(ProviderScope(provider, unified))
	var out []string
	for _, field := range []struct {
		name  string
		equal bool
	}{
		{"loki_labels", reflect.DeepEqual(l.LokiLabels, u.LokiLabels)},
		{"loki_pattern_field", l.LokiPatternField == u.LokiPatternField},
		{"loki_complete_lookback_seconds", l.LokiCompleteLookbackSeconds == u.LokiCompleteLookbackSeconds},
		{"elasticsearch_index", l.ElasticsearchIndex == u.ElasticsearchIndex},
		{"elasticsearch_pattern_field", l.ElasticsearchPatternField == u.ElasticsearchPatternField},
		{"elasticsearch_filters", reflect.DeepEqual(l.ElasticsearchFilters, u.ElasticsearchFilters)},
		{"elasticsearch_prefixes", reflect.DeepEqual(l.ElasticsearchPrefixes, u.ElasticsearchPrefixes)},
		{"tempo_service", l.TempoService == u.TempoService},
		{"tempo_attributes", reflect.DeepEqual(l.TempoAttributes, u.TempoAttributes)},
		{"tempo_safe_operations", reflect.DeepEqual(l.TempoSafeOperations, u.TempoSafeOperations)},
	} {
		if !field.equal {
			out = append(out, "scope."+field.name)
		}
	}
	return out
}

// CompareWorkloads adds every legacy Kubernetes binding: the registry must
// resolve the same API server and Deployment. The demo connector is not
// imported and not compared.
func (r *CompareReport) CompareWorkloads(ctx context.Context, q Querier, resolver *Resolver, workloads map[string]LegacyWorkload, state State) {
	bindings := append([]LegacyBinding(nil), state.Bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindingKey(bindings[i]) < bindingKey(bindings[j]) })
	for _, b := range bindings {
		legacy, registered := workloads[b.ConnectorID]
		if b.Kind != WorkloadProvider || b.ConnectorID == demoKubernetesID || (registered && legacy.Demo) {
			continue
		}
		item := CompareItem{ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID, Surface: WorkloadTargetKind,
			LegacyProvider: b.Kind}
		unified, err := resolver.ResolveWorkload(ctx, q, b.ApplicationID, b.EnvironmentID)
		item.Result = compareWorkload(legacy, registered, unified, err)
		r.Items = append(r.Items, item)
		r.Counts[item.State]++
		r.Checked++
	}
}

func compareWorkload(legacy LegacyWorkload, registered bool, unified Workload, err error) Result {
	switch {
	case !registered:
		return Result{State: StateLegacyUnregistered}
	case errors.Is(err, ErrNotConfigured):
		return Result{State: StateMissing}
	case errors.Is(err, ErrAmbiguous):
		return Result{State: StateAmbiguous}
	case err != nil:
		return Result{State: StateUnavailable}
	}
	var reasons, notReady []string
	if canonicalEndpoint(legacy.Endpoint) != canonicalEndpoint(unified.Endpoint) {
		reasons = append(reasons, "endpoint")
	}
	if !legacy.Token || unified.AuthMethod != "bearer" {
		reasons = append(reasons, "auth_method")
	}
	for _, reason := range unified.NotReady {
		if reason == "scope_missing" {
			reasons = append(reasons, "scope_missing")
		} else {
			notReady = append(notReady, reason)
		}
	}
	if !slices.Contains(reasons, "scope_missing") {
		if unified.Scope.Namespace != legacy.Namespace {
			reasons = append(reasons, "scope.namespace")
		}
		if unified.Scope.Deployment != legacy.Deployment {
			reasons = append(reasons, "scope.deployment")
		}
	}
	if len(reasons) > 0 {
		return Result{State: StateDiffers, Reasons: reasons}
	}
	if len(notReady) > 0 {
		return Result{State: StateMatchNotReady, Reasons: notReady}
	}
	return Result{State: StateMatch}
}

// CompareRegistryOnly adds the reverse direction, needed before rolling back
// a reader once mapping writes go to the registry (design §10.4): every
// telemetry or workload target whose service has no legacy binding of that
// data plane. Together with CompareAll and CompareWorkloads, a report
// without differences means the legacy readers would read what the
// registry reads. legacyWorkloads says whether the instance has a legacy
// Kubernetes registration; without one, workload targets are
// registry_native.
func (r *CompareReport) CompareRegistryOnly(state State, legacyWorkloads bool) {
	bound := map[string]bool{}
	for _, b := range state.Bindings {
		plane := WorkloadTargetKind
		if surface, ok := SurfaceOf(b.Kind); ok {
			plane = string(surface)
		} else if b.Kind != WorkloadProvider {
			continue
		}
		bound[b.ApplicationID+"\x00"+b.EnvironmentID+"\x00"+plane] = true
	}
	targets := append([]ExistingTarget(nil), state.Targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	for _, t := range targets {
		var plane string
		if t.TargetKind == WorkloadTargetKind && t.Provider == WorkloadProvider {
			plane = WorkloadTargetKind
		} else if surface, ok := SurfaceOf(t.Provider); ok && TargetKind(t.Provider) == t.TargetKind {
			plane = string(surface)
		} else {
			continue
		}
		if bound[t.ApplicationID+"\x00"+t.EnvironmentID+"\x00"+plane] {
			continue
		}
		state := StateRegistryOnly
		if plane == WorkloadTargetKind && !legacyWorkloads {
			state = StateRegistryNative
		}
		item := CompareItem{ApplicationID: t.ApplicationID, EnvironmentID: t.EnvironmentID, Surface: plane,
			Result: Result{State: state, Reasons: []string{"target:" + t.Provider}}}
		r.Items = append(r.Items, item)
		r.Counts[item.State]++
		r.Checked++
	}
}

// Summarize counts the services with at least one difference, the number to
// check before a rollback.
func (r *CompareReport) Summarize() {
	diverged := map[string]bool{}
	for _, item := range r.Items {
		switch item.State {
		case StateDiffers, StateMissing, StateAmbiguous, StateUnavailable, StateRegistryOnly:
			diverged[item.ApplicationID+"\x00"+item.EnvironmentID] = true
		}
	}
	r.DivergedServices = len(diverged)
}
