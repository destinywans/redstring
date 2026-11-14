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
	"github.com/jackc/pgx/v5"
)

type mappingCatalogRow struct {
	appID, key, env, teamID, envID string
	bindings                       []byte
	revision                       int64
	// targetRevision is the service environment's mapping revision once
	// mapping writes go to the registry (migration 172).
	targetRevision int64
}

const mappingCandidatesQuery = `SELECT a.id,a.key,ve.environment_id,COALESCE(a.owner_team_id,''),e.id,e.bindings,e.binding_revision,
 COALESCE(r.revision,1)
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN v3_service_target_revisions r ON r.application_id=a.id AND r.environment_id=ve.environment_id
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 AND (a.key,ve.environment_id,a.id)>($2,$3,$4)`

func mappingBatch(ctx context.Context, q mappingQuerier, query string, args ...any) ([]mappingCatalogRow, error) {
	rows, err := q.Query(ctx, query, args...)
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
	user := httpapi.CurrentUser(r)
	var q mappingQuerier = s.Store.DB
	authorize := func(d rbac.V3Decision) error {
		return s.Access.Authorize(ctx, user, d)
	}
	var tx pgx.Tx
	if access, ok := s.transactionAccess(); ok {
		// Read the authorized pairs and their registry targets from the same
		// snapshot, including the revision used by the mapping editor.
		var err error
		tx, err = s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
			return
		}
		defer tx.Rollback(ctx)
		q = tx
		authorize = func(d rbac.V3Decision) error {
			return access.AuthorizeWithTx(ctx, tx, user, d)
		}
	}
	serviceAccess := func(action, app, env, team string) error {
		decision := rbac.V3Decision{Module: "services", Action: action, EnvironmentID: env, ApplicationID: app}
		err := authorize(decision)
		if errors.Is(err, store.ErrForbidden) && team != "" {
			decision.ApplicationID, decision.TeamID = "", team
			return authorize(decision)
		}
		return err
	}
	byName := map[string]*mappingRow{}
	visibleAppIDs := []string{}
	visibleAppKeys := map[string]string{}
	visibleBindings := map[string][]core.Binding{}
	visibleRows := map[string]mappingCatalogRow{}
	visibleTargets := map[string][]registryTarget{}
	visible := make([]mappingCatalogRow, 0)
	targetApps, targetEnvs := []string{}, []string{}
	query := mappingCandidatesQuery
	_, useGrantFilter := s.Access.(*rbac.V3Policy)
	if useGrantFilter {
		query = "WITH " + rbac.V3ListGrantsCTE("$6") + " " + query + " AND (" +
			rbac.V3ListScopePredicate("ve.environment_id", "a.id", "a.owner_team_id", "services", "view") + ")"
	}
	query += " ORDER BY a.key,ve.environment_id,a.id LIMIT $5"
	var cursorKey, cursorEnv, cursorID string
	for {
		args := []any{envs, cursorKey, cursorEnv, cursorID, catalogPageSize}
		if useGrantFilter {
			args = append(args, user.ID)
		}
		batch, err := mappingBatch(ctx, q, query, args...)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
			return
		}
		for _, item := range batch {
			err := serviceAccess("view", item.appID, item.env, item.teamID)
			if errors.Is(err, store.ErrForbidden) {
				continue
			}
			if err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
				return
			}
			visible = append(visible, item)
			if len(visible) > 1000 {
				httpapi.Error(w, 422, "result_limit", "Narrow the environment scope")
				return
			}
			targetApps, targetEnvs = append(targetApps, item.appID), append(targetEnvs, item.env)
		}
		if len(batch) < catalogPageSize {
			break
		}
		last := batch[len(batch)-1]
		cursorKey, cursorEnv, cursorID = last.key, last.env, last.appID
	}
	targets, err := mappingRegistryTargetsForPairs(ctx, q, targetApps, targetEnvs)
	if errors.Is(err, errMappingTargetLimit) {
		httpapi.Error(w, 422, "result_limit", "Narrow the environment scope")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
		return
	}
	order := []string{}
	for _, item := range visible {
		if singleEnvironment {
			visibleAppIDs = append(visibleAppIDs, item.appID)
			visibleAppKeys[item.appID] = item.key
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
	output := make([]mappingRow, 0, len(order))
	suggestions := 0
	if singleEnvironment && len(visibleAppIDs) > 0 {
		connectorAllowed := true
		for _, action := range []string{"view", "manage"} {
			err := authorize(rbac.V3Decision{
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
				changeErr := serviceAccess("change", appID, envs[0], item.teamID)
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
			pending, err := currentMappingSuggestions(ctx, q, envs[0], visibleAppIDs)
			if err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Mapping candidates unavailable")
				return
			}
			for _, candidate := range pending {
				if visibleAppKeys[candidate.appID] != candidate.key {
					continue
				}
				err := serviceAccess("view", candidate.appID, envs[0], candidate.teamID)
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				if err != nil {
					httpapi.Error(w, 503, "authorization_unavailable", "Mapping policy unavailable")
					return
				}
				changeErr := serviceAccess("change", candidate.appID, envs[0], candidate.teamID)
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
	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
			return
		}
	}
	httpapi.WriteJSON(w, 200, map[string]any{"systems": mappingSystemsFor(r.Header.Get("Accept-Language")), "rows": output, "suggestions": suggestions})
}
