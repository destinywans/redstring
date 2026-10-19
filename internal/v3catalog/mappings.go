package v3catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

type mappingCatalogRow struct {
	appID, key, env, teamID, envID string
	bindings                       []byte
	revision                       int64
	// targetRevision is the service environment's mapping revision once
	// mapping writes go to the registry (migration 172).
	targetRevision int64
}

const mappingQuery = `SELECT a.id,a.key,ve.environment_id,COALESCE(a.owner_team_id,''),e.id,e.bindings,e.binding_revision,
 COALESCE(r.revision,1)
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN v3_service_target_revisions r ON r.application_id=a.id AND r.environment_id=ve.environment_id
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 AND (a.key,ve.environment_id,a.id)>($2,$3,$4)
 ORDER BY a.key,ve.environment_id,a.id LIMIT 128`

func (s *Service) mappingBatch(ctx context.Context, envs []string, key, env, id string) ([]mappingCatalogRow, error) {
	rows, err := s.Store.DB.Query(ctx, mappingQuery, envs, key, env, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batch := make([]mappingCatalogRow, 0, catalogPageSize)
	for rows.Next() {
		var row mappingCatalogRow
		if err := rows.Scan(&row.appID, &row.key, &row.env, &row.teamID, &row.envID, &row.bindings, &row.revision, &row.targetRevision); err != nil {
			return nil, err
		}
		batch = append(batch, row)
	}
	return batch, rows.Err()
}

type mappingSystem struct {
	ID    string `json:"id"`
	Brand string `json:"brand"`
	Label string `json:"label"`
}
type mappingCell struct {
	State     string                  `json:"state"`
	Value     string                  `json:"value,omitempty"`
	Brand     string                  `json:"brand,omitempty"`
	Candidate *mappingCandidateReview `json:"candidate,omitempty"`
}
type mappingRow struct {
	Service string                 `json:"service"`
	Cells   map[string]mappingCell `json:"cells"`
	Save    *mappingSaveView       `json:"save,omitempty"`
}

// A display cell is not a save instruction. This snapshot exposes the exact
// stored identities and revision only to a caller that can write the object.
type mappingSaveView struct {
	ServiceID            string               `json:"service_id"`
	ServiceEnvironmentID string               `json:"service_environment_id"`
	BindingRevision      int64                `json:"binding_revision"`
	Bindings             []mappingSaveBinding `json:"bindings"`
	Editable             bool                 `json:"editable"`
	UnavailableReason    string               `json:"unavailable_reason,omitempty"`
}

type mappingSaveBinding struct {
	SystemID    string `json:"system_id"`
	Kind        string `json:"kind"`
	ConnectorID string `json:"connector_id"`
	Resource    string `json:"resource"`
}

func (s *Service) mappingSaveProjection(item mappingCatalogRow, persisted []core.Binding) mappingSaveView {
	view := mappingSaveView{ServiceID: item.appID, ServiceEnvironmentID: item.envID,
		BindingRevision: item.revision, Bindings: make([]mappingSaveBinding, 0, len(persisted)), Editable: true}
	seen := map[string]bool{}
	for _, binding := range persisted {
		systemID := mappingKind(binding.Kind)
		view.Bindings = append(view.Bindings, mappingSaveBinding{SystemID: systemID,
			Kind: binding.Kind, ConnectorID: binding.ConnectorID, Resource: binding.Resource})
		if systemID == "" || seen[systemID] || !mappingSaveIdentity(binding.Kind) ||
			!mappingSaveIdentity(binding.ConnectorID) || !mappingSaveResource(binding.Resource) ||
			(systemID == "code" && (binding.Kind != "gitlab" || !validMappingProject(binding.Resource))) {
			view.Editable = false
		}
		seen[systemID] = true
	}
	if len(persisted) > len(mappingSystems) {
		view.Editable = false
	}
	// Decoding into core.Binding drops unknown JSON fields. A full save must
	// never erase such data while replacing another binding in the same row.
	if !mappingStoredBindingShape(item.bindings) {
		view.Editable = false
	}
	// A save keeps the stored bindings of the other systems as they are, so
	// they are not checked against the legacy telemetry file and Kubernetes
	// registrations, which a V3 instance no longer loads (PA-19 P4).
	if !view.Editable {
		view.UnavailableReason = "unsupported_persisted_bindings"
	}
	return view
}

// PostgreSQL jsonb has already resolved duplicate object keys, but may hold
// historical binding objects with fields that the current Go type does not
// know. Require the exact current wire shape before offering a replacement.
func mappingStoredBindingShape(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return false
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return false
	}
	for _, entry := range entries {
		object := bytes.TrimSpace(entry)
		if len(object) < 2 || object[0] != '{' || object[len(object)-1] != '}' {
			return false
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(object, &fields); err != nil || len(fields) != 3 {
			return false
		}
		for _, key := range []string{"kind", "connector_id", "resource"} {
			value, found := fields[key]
			value = bytes.TrimSpace(value)
			if !found || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
				return false
			}
			var decoded string
			if err := json.Unmarshal(value, &decoded); err != nil {
				return false
			}
		}
	}
	return true
}

var mappingSystems = []mappingSystem{
	{ID: "metrics", Brand: "prometheus", Label: "指标"},
	{ID: "logs", Brand: "loki", Label: "日志"},
	{ID: "traces", Brand: "tempo", Label: "链路"},
	{ID: "code", Brand: "gitlab", Label: "代码"},
	{ID: "deploy", Brand: "argo", Label: "部署"},
	{ID: "dash", Brand: "grafana", Label: "看板"},
	{ID: "workload", Brand: "kubernetes", Label: "工作负载"},
}

var mappingSystemLabelsEN = map[string]string{
	"metrics": "Metrics", "logs": "Logs", "traces": "Traces", "code": "Code",
	"deploy": "Deploy", "dash": "Dashboards", "workload": "Workloads",
}

// mappingSystemsFor returns the category labels in the request language
// (English when Accept-Language starts with en); IDs and brands are fixed.
func mappingSystemsFor(acceptLanguage string) []mappingSystem {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLanguage)), "en") {
		return mappingSystems
	}
	out := make([]mappingSystem, len(mappingSystems))
	for i, system := range mappingSystems {
		system.Label = mappingSystemLabelsEN[system.ID]
		out[i] = system
	}
	return out
}

func mappingKind(kind string) string {
	switch strings.ToLower(kind) {
	case "prometheus", "victoriametrics":
		return "metrics"
	case "loki", "elasticsearch":
		return "logs"
	case "tempo", "jaeger":
		return "traces"
	case "gitlab", "github", "githubactions":
		return "code"
	case "argocd", "argo", "jenkins":
		return "deploy"
	case "grafana", "kibana":
		return "dash"
	case "kubernetes":
		return "workload"
	default:
		return ""
	}
}

// mappings displays only bindings persisted for environments the caller can
// actually view. A mapping is not a live connector-health assertion.
func (s *Service) mappings(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "view")
	if !ok {
		return
	}
	requestScope, _ := httpapi.V3ScopeFromContext(r.Context())
	singleEnvironment := len(envs) == 1 && !requestScope.All
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	byName := map[string]*mappingRow{}
	visibleAppIDs := []string{}
	visibleAppKeys := map[string]string{}
	visibleBindings := map[string][]core.Binding{}
	visibleRows := map[string]mappingCatalogRow{}
	visibleTargets := map[string][]registryTarget{}
	targets, err := mappingRegistryTargets(ctx, s.Store.DB, envs, "")
	if errors.Is(err, errMappingTargetLimit) {
		httpapi.Error(w, 422, "result_limit", "Narrow the environment scope")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
		return
	}
	order := []string{}
	count := 0
	var cursorKey, cursorEnv, cursorID string
	for {
		batch, err := s.mappingBatch(ctx, envs, cursorKey, cursorEnv, cursorID)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
			return
		}
		for _, item := range batch {
			visible, authErr := s.canView(ctx, httpapi.CurrentUser(r), catalogRow{appID: item.appID, env: item.env, teamID: item.teamID})
			if authErr != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
				return
			}
			if !visible {
				continue
			}
			if singleEnvironment {
				visibleAppIDs = append(visibleAppIDs, item.appID)
				visibleAppKeys[item.appID] = item.key
			}
			// Hidden records cannot consume the visible result budget.
			count++
			if count > 1000 {
				httpapi.Error(w, 422, "result_limit", "Narrow the environment scope")
				return
			}
			var bindings []core.Binding
			if err := json.Unmarshal(item.bindings, &bindings); err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
				return
			}
			serviceTargets := targets[item.appID+"\x00"+item.env]
			if singleEnvironment {
				visibleBindings[item.appID] = bindings
				visibleRows[item.appID] = item
				visibleTargets[item.appID] = serviceTargets
			}
			row, found := byName[item.key]
			if !found {
				row = &mappingRow{Service: item.key, Cells: map[string]mappingCell{}}
				byName[item.key] = row
				order = append(order, item.key)
			}
			addCell := func(id, value, brand string) {
				if id == "" || strings.TrimSpace(value) == "" {
					return
				}
				if len(envs) > 1 {
					value = item.env + ": " + value
				}
				cell := row.Cells[id]
				if cell.State == "ok" {
					if !strings.Contains("\n"+cell.Value+"\n", "\n"+value+"\n") {
						cell.Value += ", " + value
					}
				} else {
					cell = mappingCell{State: "ok", Value: value, Brand: brand}
				}
				row.Cells[id] = cell
			}
			// Registry systems come from targets, the rest from the stored
			// bindings.
			for _, target := range serviceTargets {
				addCell(target.System, target.Resource, target.Provider)
			}
			for _, b := range legacyKept(bindings) {
				addCell(mappingKind(b.Kind), b.Resource, b.Kind)
			}
		}
		if len(batch) < catalogPageSize {
			break
		}
		last := batch[len(batch)-1]
		cursorKey, cursorEnv, cursorID = last.key, last.env, last.appID
	}
	output := make([]mappingRow, 0, len(order))
	suggestions := 0
	if singleEnvironment && len(visibleAppIDs) > 0 {
		connectorAllowed := true
		for _, action := range []string{"view", "manage"} {
			err := s.Access.Authorize(ctx, httpapi.CurrentUser(r), rbac.V3Decision{
				Module: "connectors", Action: action, EnvironmentID: envs[0],
			})
			if errors.Is(err, store.ErrForbidden) {
				connectorAllowed = false
				break
			}
			if err != nil {
				httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
				return
			}
		}
		if connectorAllowed {
			for appID, item := range visibleRows {
				decision := rbac.V3Decision{Module: "services", Action: "change", EnvironmentID: envs[0], ApplicationID: appID}
				changeErr := s.Access.Authorize(ctx, httpapi.CurrentUser(r), decision)
				if errors.Is(changeErr, store.ErrForbidden) && item.teamID != "" {
					decision.ApplicationID = ""
					decision.TeamID = item.teamID
					changeErr = s.Access.Authorize(ctx, httpapi.CurrentUser(r), decision)
				}
				if errors.Is(changeErr, store.ErrForbidden) {
					continue
				}
				if changeErr != nil {
					httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
					return
				}
				view := s.registrySaveProjection(item, visibleBindings[appID], visibleTargets[appID])
				byName[item.key].Save = &view
			}
			pending, err := s.currentMappingSuggestions(ctx, envs[0], visibleAppIDs)
			if err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Mapping candidates unavailable")
				return
			}
			for _, candidate := range pending {
				if visibleAppKeys[candidate.appID] != candidate.key {
					continue
				}
				visibleNow, err := s.canView(ctx, httpapi.CurrentUser(r), catalogRow{
					appID: candidate.appID, env: envs[0], teamID: candidate.teamID,
				})
				if err != nil {
					httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
					return
				}
				if !visibleNow {
					continue
				}
				changeDecision := rbac.V3Decision{Module: "services", Action: "change", EnvironmentID: envs[0], ApplicationID: candidate.appID}
				changeErr := s.Access.Authorize(ctx, httpapi.CurrentUser(r), changeDecision)
				if errors.Is(changeErr, store.ErrForbidden) && candidate.teamID != "" {
					changeDecision.ApplicationID = ""
					changeDecision.TeamID = candidate.teamID
					changeErr = s.Access.Authorize(ctx, httpapi.CurrentUser(r), changeDecision)
				}
				if errors.Is(changeErr, store.ErrForbidden) {
					continue
				}
				if changeErr != nil {
					httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
					return
				}
				bindings := withoutCode(visibleBindings[candidate.appID])
				expected := core.Binding{Kind: "gitlab", ConnectorID: candidate.connectorID, Resource: candidate.externalID}
				bindings = append(bindings, expected)
				if !mappingBindingSupported(bindings, expected) {
					continue
				}
				row := byName[candidate.key]
				if row == nil || row.Cells["code"].State != "" {
					continue
				}
				row.Cells["code"] = mappingCell{State: "suggested", Value: "Configured GitLab project #" + candidate.externalID, Brand: "gitlab", Candidate: &candidate.review}
				suggestions++
			}
		}
	}
	for _, key := range order {
		row := byName[key]
		for _, system := range mappingSystems {
			if _, found := row.Cells[system.ID]; !found {
				row.Cells[system.ID] = mappingCell{State: "missing"}
			}
		}
		output = append(output, *row)
	}
	httpapi.WriteJSON(w, 200, map[string]any{"systems": mappingSystemsFor(r.Header.Get("Accept-Language")), "rows": output, "suggestions": suggestions})
}
