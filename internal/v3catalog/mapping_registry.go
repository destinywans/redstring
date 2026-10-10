package v3catalog

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

// PA-19 P3 stage 2 step 2: with the mappings reader switched, the metrics,
// logs, traces and workload systems of the service mapping are read from the
// connector registry (v3_connector_targets), so connector_id is a real
// connector ID. Until step 6 they are read-only (registry_cutover); code keeps
// its legacy binding and the GitLab candidate flow, and deploy and dash are
// read-only (system_not_supported).

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
	if s.MappingWritesUnified {
		// Saves write targets; their token is the target revision.
		view.BindingRevision = item.targetRevision
	}
	return view
}

// registrySaveCheck compares the requested bindings with what the registry
// and the read-only systems hold now; only the code system may change. It
// returns the legacy bindings to store and an error code when another system
// changed.
func registrySaveCheck(before []core.Binding, targets []registryTarget, requested []mappingSaveBinding) ([]core.Binding, string) {
	current, ambiguous := registryBindings(targets)
	if ambiguous {
		return nil, "ambiguous_targets"
	}
	want := map[string]mappingSaveBinding{}
	for _, binding := range current {
		want[binding.SystemID] = binding
	}
	for _, binding := range saveResponseBindings(before) {
		if legacyOnlySystem(binding.SystemID) {
			want[binding.SystemID] = binding
		}
	}
	got := map[string]mappingSaveBinding{}
	for _, binding := range requested {
		got[binding.SystemID] = binding
	}
	for _, system := range mappingSystems {
		if system.ID == "code" || got[system.ID] == want[system.ID] {
			continue
		}
		if legacyOnlySystem(system.ID) {
			return nil, "system_not_supported"
		}
		return nil, "registry_cutover"
	}
	// Keep the stored order: an unchanged save must not bump the revision.
	// A requested code binding replaces the stored one in place; adding one
	// is refused later (code_mapping_requires_accept).
	code, keepCode := got["code"]
	after := make([]core.Binding, 0, len(before)+1)
	for _, binding := range before {
		if mappingKind(binding.Kind) != "code" {
			after = append(after, binding)
		} else if keepCode {
			after = append(after, core.Binding{Kind: code.Kind, ConnectorID: code.ConnectorID, Resource: code.Resource})
			keepCode = false
		}
	}
	if keepCode {
		after = append(after, core.Binding{Kind: code.Kind, ConnectorID: code.ConnectorID, Resource: code.Resource})
	}
	return after, ""
}

// registryResponseBindings is what a save returns in registry mode.
func registryResponseBindings(persisted []core.Binding, targets []registryTarget) []mappingSaveBinding {
	out := saveResponseBindings(legacyKept(persisted))
	fromTargets, _ := registryBindings(targets)
	return append(out, fromTargets...)
}
