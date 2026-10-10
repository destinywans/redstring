package v3catalog

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

// PA-19 P3 stage 2 step 2: the metrics, logs, traces and workload systems of
// the service mapping are read from the connector registry
// (v3_connector_targets), so connector_id is a real connector ID; saves write
// them as targets (step 6, mapping_write.go). Code keeps its stored binding
// and the GitLab candidate flow, deploy and dash are read-only
// (system_not_supported). Since P4 this is the only mapping path.

// registryMappingSystem maps a registry target kind to its mapping system.
var registryMappingSystem = map[string]string{
	"metric_resource": "metrics",
	"log_stream":      "logs",
	"log_index":       "logs",
	"trace_service":   "traces",
	"workload":        "workload",
}

// registrySystem reports whether a mapping system is read from the registry.
func registrySystem(systemID string) bool {
	switch systemID {
	case "metrics", "logs", "traces", "workload":
		return true
	}
	return false
}

// legacyOnlySystem reports a system without registry target kinds.
func legacyOnlySystem(systemID string) bool { return systemID == "deploy" || systemID == "dash" }

type registryTarget struct {
	TargetID, ConnectorID, Provider, Kind, Resource, System string
}

const maxMappingTargets = 10000

// mappingRegistryTargets reads the registry targets of the given environments
// (and application, when not empty), keyed by application and environment.
func mappingRegistryTargets(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, envs []string, app string) (map[string][]registryTarget, error) {
	rows, err := q.Query(ctx, `SELECT t.application_id,t.environment_id,t.id,t.connector_id,c.provider,t.target_kind,t.external_id
 FROM v3_connector_targets t JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.environment_id=ANY($1::text[]) AND ($2='' OR t.application_id=$2)
 AND t.target_kind IN ('metric_resource','log_stream','log_index','trace_service','workload')
 ORDER BY t.application_id,t.environment_id,t.target_kind,t.id LIMIT $3`, envs, app, maxMappingTargets+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]registryTarget{}
	count := 0
	for rows.Next() {
		var appID, env string
		var target registryTarget
		if err := rows.Scan(&appID, &env, &target.TargetID, &target.ConnectorID, &target.Provider, &target.Kind, &target.Resource); err != nil {
			return nil, err
		}
		if count++; count > maxMappingTargets {
			return nil, errMappingTargetLimit
		}
		target.System = registryMappingSystem[target.Kind]
		out[appID+"\x00"+env] = append(out[appID+"\x00"+env], target)
	}
	return out, rows.Err()
}

var errMappingTargetLimit = errors.New("too many registry targets for the service mapping")

// legacyKept drops the bindings of systems the registry now answers.
func legacyKept(bindings []core.Binding) []core.Binding {
	kept := make([]core.Binding, 0, len(bindings))
	for _, binding := range bindings {
		if !registrySystem(mappingKind(binding.Kind)) {
			kept = append(kept, binding)
		}
	}
	return kept
}

// registryBindings projects targets as save bindings, in system order, and
// reports whether a system has more than one target.
func registryBindings(targets []registryTarget) ([]mappingSaveBinding, bool) {
	sorted := append([]registryTarget(nil), targets...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].System < sorted[j].System })
	out := make([]mappingSaveBinding, 0, len(sorted))
	seen := map[string]bool{}
	ambiguous := false
	for _, target := range sorted {
		if seen[target.System] {
			ambiguous = true
		}
		seen[target.System] = true
		out = append(out, mappingSaveBinding{SystemID: target.System, Kind: target.Provider,
			ConnectorID: target.ConnectorID, Resource: target.Resource})
	}
	return out, ambiguous
}

// registrySaveProjection is the save snapshot with the registry systems
// answered from targets. The legacy shape rules still decide editability.
func (s *Service) registrySaveProjection(item mappingCatalogRow, legacy []core.Binding, targets []registryTarget) mappingSaveView {
	view := s.mappingSaveProjection(item, legacy)
	view.Bindings = saveResponseBindings(legacyKept(legacy))
	fromTargets, ambiguous := registryBindings(targets)
	view.Bindings = append(view.Bindings, fromTargets...)
	if view.Editable && ambiguous {
		view.Editable, view.UnavailableReason = false, "ambiguous_targets"
	}
	// Saves write targets; their token is the target revision.
	view.BindingRevision = item.targetRevision
	return view
}

// registryResponseBindings is what a save returns.
func registryResponseBindings(persisted []core.Binding, targets []registryTarget) []mappingSaveBinding {
	out := saveResponseBindings(legacyKept(persisted))
	fromTargets, _ := registryBindings(targets)
	return append(out, fromTargets...)
}
