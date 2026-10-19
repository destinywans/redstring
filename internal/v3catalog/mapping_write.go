package v3catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
)

// PA-19 P3 stage 2 step 6: with mapping writes switched, a save changes the
// registry targets of the metrics, logs, traces and workload systems instead
// of the stored bindings. Each system has at most one target; a new target
// must name a connector of the environment that serves the system and a
// resource the catalog reviewed for this service. Code is the confirmed
// project of v3_service_code_projects (D20): only the candidate flow sets it,
// a save keeps or removes it. Deploy and dashboard stay read-only. The token is the service environment's target
// revision (migration 172, v3_service_target_revisions), which every target
// or binding change raises.

// registryTargetKind is the target kind a registry system binds a provider with.
func registryTargetKind(systemID, provider string) string {
	if systemID == "workload" && provider == v3telemetry.WorkloadProvider {
		return v3telemetry.WorkloadTargetKind
	}
	if !providerFor(systemID, provider) {
		return ""
	}
	return v3telemetry.TargetKind(provider)
}

// targetRevision reads the mapping revision of a locked service environment
// (1 until its first change).
func targetRevision(ctx context.Context, tx pgx.Tx, appID, env string) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM v3_service_target_revisions
 WHERE application_id=$1 AND environment_id=$2),1)`, appID, env).Scan(&revision)
	return revision, err
}

type mappingTargetChange struct {
	SystemID     string `json:"system_id"`
	Action       string `json:"action"`
	TargetBefore string `json:"target_before,omitempty"`
	TargetAfter  string `json:"target_after,omitempty"`
}

// registryMappingWrite saves one service item in registry mode. The service
// rows are locked and the caller has authorized the item. It writes the
// error response itself and reports whether to continue.
func (s *Service) registryMappingWrite(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, user core.User, row mappingCatalogRow,
	item mappingSaveInputItem) (mappingSaveCommittedItem, bool) {
	var none mappingSaveCommittedItem
	env := row.env
	revision, err := targetRevision(ctx, tx, row.appID, env)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return none, false
	}
	if row.envID != item.ServiceEnvironmentID || revision != item.BindingRevision {
		httpapi.Error(w, 409, "stale_mapping", "Mapping identity or revision changed")
		return none, false
	}
	if s.Registry == nil {
		httpapi.Error(w, 503, "catalog_unavailable", "The connector registry is not configured")
		return none, false
	}
	var before []core.Binding
	if err := json.Unmarshal(row.bindings, &before); err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Stored mapping is invalid")
		return none, false
	}
	if !s.mappingSaveProjection(row, before).Editable {
		httpapi.Error(w, 409, "unsupported_mapping", "Stored bindings require repair before full save")
		return none, false
	}
	all, err := mappingRegistryTargets(ctx, tx, []string{env}, row.appID)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return none, false
	}
	targets := all[row.appID+"\x00"+env]
	current := map[string]registryTarget{}
	for _, target := range targets {
		if _, twice := current[target.System]; twice {
			httpapi.Error(w, 409, "ambiguous_targets", "More than one registry target serves a system of this service")
			return none, false
		}
		current[target.System] = target
	}
	requested := map[string]mappingSaveBinding{}
	for _, binding := range *item.Bindings {
		requested[binding.SystemID] = binding
	}

	// Stored systems: deploy and dashboard as read.
	stored := map[string]mappingSaveBinding{}
	for _, binding := range saveResponseBindings(legacyKept(before)) {
		stored[binding.SystemID] = binding
	}
	for _, system := range []string{"deploy", "dash"} {
		if requested[system] != stored[system] {
			httpapi.Error(w, 422, "system_not_supported", "Deploy and dashboard mappings are read-only")
			return none, false
		}
	}
	// Code: the confirmed project is kept or removed, never set by a save.
	var removeCode *registryTarget
	code, keepCode := requested["code"]
	currentCode, hasCode := current["code"]
	switch {
	case keepCode && (!hasCode || code.Kind != currentCode.Provider || code.ConnectorID != currentCode.ConnectorID ||
		code.Resource != currentCode.Resource):
		httpapi.Error(w, 409, "code_mapping_requires_accept", "GitLab projects must be accepted from a verified candidate")
		return none, false
	case !keepCode && hasCode:
		removeCode = &currentCode
	}

	// Registry systems: validate every new target before writing any.
	type plan struct {
		system  string
		current *registryTarget
		want    *mappingSaveBinding
		kind    string
	}
	var plans []plan
	for _, system := range mappingSystems {
		if !registrySystem(system.ID) {
			continue
		}
		var cur *registryTarget
		if target, ok := current[system.ID]; ok {
			cur = &target
		}
		var want *mappingSaveBinding
		if binding, ok := requested[system.ID]; ok {
			want = &binding
		}
		if want == nil && cur == nil {
			continue
		}
		if want != nil && cur != nil && cur.ConnectorID == want.ConnectorID && cur.Resource == want.Resource && cur.Provider == want.Kind {
			continue
		}
		p := plan{system: system.ID, current: cur, want: want}
		if want != nil {
			var provider string
			err := tx.QueryRow(ctx, `SELECT provider FROM v3_connectors WHERE id=$1 AND environment_id=$2 FOR SHARE`,
				want.ConnectorID, env).Scan(&provider)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && (provider != want.Kind || registryTargetKind(system.ID, provider) == "")) {
				httpapi.Error(w, 422, "connector_unavailable", "Choose a connector of this environment that serves "+system.ID)
				return none, false
			}
			if err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
				return none, false
			}
			if !slices.Contains(s.Registry.Catalog.ResourceIDs(want.ConnectorID, row.appID, env, provider), want.Resource) {
				httpapi.Error(w, 422, "resource_not_in_catalog", "Choose a resource the catalog lists for this service and connector")
				return none, false
			}
			p.kind = registryTargetKind(system.ID, provider)
			var owner string
			err = tx.QueryRow(ctx, `SELECT application_id FROM v3_connector_targets
 WHERE environment_id=$1 AND connector_id=$2 AND target_kind=$3 AND external_id=$4`,
				env, want.ConnectorID, p.kind, want.Resource).Scan(&owner)
			if err == nil && owner != row.appID {
				httpapi.Error(w, 409, "target_owned_by_other_service", "This resource is already mapped to another service")
				return none, false
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
				return none, false
			}
		}
		plans = append(plans, p)
	}

	responseBefore := registryResponseBindings(before, targets)
	if len(plans) == 0 && removeCode == nil {
		return mappingSaveCommittedItem{ServiceID: row.appID, ServiceEnvironmentID: row.envID,
			BindingRevision: revision, Bindings: responseBefore}, true
	}
	audit := func(action, target, connector, kind, external string) error {
		_, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,'success',jsonb_build_object('environment_id',$5::text,'connector_id',$6::text,
 'target_kind',$7::text,'external_id',$8::text,'source','service_mapping'))`,
			user.ID, row.appID, action, target, env, connector, kind, external)
		return err
	}
	if removeCode != nil {
		tag, err := tx.Exec(ctx, `DELETE FROM v3_service_code_projects WHERE application_id=$1 AND environment_id=$2 AND target_id=$3`,
			row.appID, env, removeCode.TargetID)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
			return none, false
		}
		if tag.RowsAffected() != 1 {
			httpapi.Error(w, 409, "stale_mapping", "Mapping changed after selection")
			return none, false
		}
		if err := audit("v3.service_code_project.removed", removeCode.TargetID, removeCode.ConnectorID, removeCode.Kind, removeCode.Resource); err != nil {
			httpapi.Error(w, 503, "audit_unavailable", "Mapping audit unavailable")
			return none, false
		}
	}
	changes := []mappingTargetChange{}
	for _, p := range plans {
		change := mappingTargetChange{SystemID: p.system}
		if p.current != nil {
			if _, err := tx.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id=$1 AND application_id=$2 AND environment_id=$3`,
				p.current.TargetID, row.appID, env); err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
				return none, false
			}
			if err := audit("v3.connector.target_unbound", p.current.TargetID, p.current.ConnectorID, p.current.Kind, p.current.Resource); err != nil {
				httpapi.Error(w, 503, "audit_unavailable", "Mapping audit unavailable")
				return none, false
			}
			change.Action, change.TargetBefore = "unbound", p.current.TargetID
		}
		if p.want != nil {
			id := store.ID()
			if _, err := tx.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, id, row.appID, env, p.want.ConnectorID, p.kind, p.want.Resource, user.ID); err != nil {
				if errors.Is(store.ClassifyError(err), store.ErrInvalidReference) {
					// Another service took the resource, or the connector went.
					httpapi.Error(w, 409, "target_owned_by_other_service", "This resource was mapped elsewhere during the save")
				} else {
					httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
				}
				return none, false
			}
			if err := audit("v3.connector.target_bound", id, p.want.ConnectorID, p.kind, p.want.Resource); err != nil {
				httpapi.Error(w, 503, "audit_unavailable", "Mapping audit unavailable")
				return none, false
			}
			change.TargetAfter = id
			if change.Action == "" {
				change.Action = "bound"
			} else {
				change.Action = "rebound"
			}
		}
		changes = append(changes, change)
	}
	nextRevision, err := targetRevision(ctx, tx, row.appID, env)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return none, false
	}
	all, err = mappingRegistryTargets(ctx, tx, []string{env}, row.appID)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Mapping storage unavailable")
		return none, false
	}
	responseAfter := registryResponseBindings(before, all[row.appID+"\x00"+env])
	record, err := json.Marshal(map[string]any{"environment": env, "source": "registry",
		"binding_revision_before": revision, "binding_revision_after": nextRevision,
		"bindings_before": responseBefore, "bindings_after": responseAfter, "targets": changes,
		"code_project_removed": removeCode != nil})
	if err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Mapping audit unavailable")
		return none, false
	}
	if _, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.service_mapping.saved',$3,'success',$4)`, user.ID, row.appID, row.envID, record); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Mapping audit unavailable")
		return none, false
	}
	return mappingSaveCommittedItem{ServiceID: row.appID, ServiceEnvironmentID: row.envID,
		BindingRevision: nextRevision, Bindings: responseAfter}, true
}
