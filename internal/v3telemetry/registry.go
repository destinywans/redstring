package v3telemetry

import (
	"strings"

	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// Surface is one data plane of a service.
type Surface string

const (
	Metrics Surface = "metrics"
	Logs    Surface = "logs"
	Traces  Surface = "traces"
)

// providerInfo maps each telemetry provider to its data plane, the target
// kind it is bound with and the registry tool that gates reads.
type providerInfo struct {
	surface    Surface
	targetKind string
	tool       string
}

var telemetryProviders = map[string]providerInfo{
	"prometheus":    {Metrics, "metric_resource", "query_metrics"},
	"loki":          {Logs, "log_stream", "search_logs"},
	"elasticsearch": {Logs, "log_index", "search_logs"},
	"tempo":         {Traces, "trace_service", "query_traces"},
}

// SurfaceOf returns the data plane of a telemetry provider.
func SurfaceOf(provider string) (Surface, bool) {
	info, ok := telemetryProviders[provider]
	return info.surface, ok
}

// TargetKind returns the target kind a provider is bound with.
func TargetKind(provider string) string { return telemetryProviders[provider].targetKind }

// surfacePairs lists the "target_kind:provider" pairs that serve a surface.
func surfacePairs(surface Surface) []string {
	var out []string
	for provider, info := range telemetryProviders {
		if info.surface == surface {
			out = append(out, info.targetKind+":"+provider)
		}
	}
	return out
}

// Approvals is the server-side credential-use list
// (AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS) as an exact-match set. It answers
// whether a configuration is approved without contacting Vault.
type Approvals map[v3integrations.CredentialBinding]struct{}

// NewApprovals indexes already validated bindings.
func NewApprovals(bindings []v3integrations.CredentialBinding) Approvals {
	out := make(Approvals, len(bindings))
	for _, binding := range bindings {
		binding.Endpoint = canonicalEndpoint(binding.Endpoint)
		out[binding] = struct{}{}
	}
	return out
}

// Allowed reports an exact approval of reference for this connector.
func (a Approvals) Allowed(tier, environmentID, connectorID, provider, endpoint, reference string) bool {
	_, ok := a[v3integrations.CredentialBinding{Tier: tier, EnvironmentID: environmentID, ConnectorID: connectorID,
		Provider: provider, Endpoint: canonicalEndpoint(endpoint), CredentialRef: reference}]
	return ok
}

// AnyFor reports whether some reference is approved for this connector.
func (a Approvals) AnyFor(tier, environmentID, connectorID, provider, endpoint string) bool {
	endpoint = canonicalEndpoint(endpoint)
	for binding := range a {
		if binding.Tier == tier && binding.EnvironmentID == environmentID && binding.ConnectorID == connectorID &&
			binding.Provider == provider && binding.Endpoint == endpoint {
			return true
		}
	}
	return false
}

// canonicalEndpoint matches how the registry stores endpoints.
func canonicalEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}
