package v3catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// Catalog reads deliberately have a smaller shape than ServiceCatalog and
// ServiceDetail. A configured target is not an observed SLO, healthy service,
// successful release or live connector. The V3 frontend DTO must be revised or
// supplied with real evidence before these routes can be marked implemented.
type evidenceField struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type catalogValue struct {
	State string `json:"state"`
	Value string `json:"value,omitempty"`
}

type catalogConnector struct {
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	State    string `json:"state"`
}

type catalogSLO struct {
	TargetPercent catalogValue  `json:"target_percent"`
	Availability  evidenceField `json:"availability"`
	BudgetLeft    evidenceField `json:"budget_left"`
}

type catalogRecord struct {
	ID          string             `json:"id"`
	CompleteV3  bool               `json:"complete_v3_dto"`
	Key         string             `json:"key"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Environment string             `json:"environment"`
	Team        catalogValue       `json:"team"`
	Tier        catalogValue       `json:"tier"`
	Owner       catalogValue       `json:"owner"`
	Oncall      catalogValue       `json:"oncall"`
	Status      evidenceField      `json:"status"`
	Health      evidenceField      `json:"health"`
	SLO         catalogSLO         `json:"slo"`
	Release     evidenceField      `json:"release"`
	Alerts      evidenceField      `json:"alerts"`
	Connectors  []catalogConnector `json:"connectors"`
}

type catalogRow struct {
	appID, key, name, description, env, teamID, teamName string
	tier, owner, oncall, target                          string
	bindings                                             []byte
}

func value(v string) catalogValue {
	if strings.TrimSpace(v) == "" {
		return catalogValue{State: "unavailable"}
	}
	return catalogValue{State: "configured", Value: v}
}

func recordID(appID, env string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(appID + "\x00" + env))
}

func parseRecordID(id string) (string, string, bool) {
	if id == "" || len(id) > 1024 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || recordID(parts[0], parts[1]) != id {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func projectCatalog(row catalogRow) (catalogRecord, error) {
	var bindings []core.Binding
	if err := json.Unmarshal(row.bindings, &bindings); err != nil {
		return catalogRecord{}, err
	}
	connectors := make([]catalogConnector, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Kind != "" && strings.TrimSpace(binding.Resource) != "" {
			connectors = append(connectors, catalogConnector{Kind: binding.Kind, Resource: binding.Resource, State: "configured"})
		}
	}
	return catalogRecord{
		ID: recordID(row.appID, row.env), Key: row.key, Name: row.name,
		Description: row.description, Environment: row.env, Team: value(row.teamName),
		Tier: value(row.tier), Owner: value(row.owner), Oncall: value(row.oncall),
		Status: evidenceField{State: "unavailable", Reason: "live health evidence is not configured"},
		Health: evidenceField{State: "unavailable", Reason: "live health evidence is not configured"},
		SLO: catalogSLO{TargetPercent: value(row.target),
			Availability: evidenceField{State: "unavailable", Reason: "measured availability is not configured"},
			BudgetLeft:   evidenceField{State: "unavailable", Reason: "SLO window and measured availability are not configured"}},
		Release:    evidenceField{State: "unavailable", Reason: "verified deployment history is not configured"},
		Alerts:     evidenceField{State: "unavailable", Reason: "alert aggregation is not configured"},
		Connectors: connectors,
	}, nil
}

func (s *Service) canView(ctx context.Context, user core.User, row catalogRow) (bool, error) {
	decision := rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: row.env, ApplicationID: row.appID}
	err := s.Access.Authorize(ctx, user, decision)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, store.ErrForbidden) {
		return false, err
	}
	if row.teamID == "" {
		return false, nil
	}
	// The V3 policy accepts one target scope at a time. A persisted owner team
	// may grant access when the application-specific decision is forbidden.
	err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: row.env, TeamID: row.teamID})
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

const catalogQuery = `SELECT a.id,a.key,a.name,a.description,ve.environment_id,
 COALESCE(a.owner_team_id,''),COALESCE(t.name,''),COALESCE(ep.tier,''),
 COALESCE(ep.owner_label,''),COALESCE(u.name,''),
 COALESCE(ep.slo_target_percent::text,''),e.bindings
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN teams t ON t.id=a.owner_team_id
 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=ve.environment_id
 LEFT JOIN users u ON u.id=ep.oncall_user_id
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 AND ($2='' OR a.id=$2) AND ($3='' OR ve.environment_id=$3)
 AND ($4='' OR a.key ILIKE '%' || $4 || '%' OR a.name ILIKE '%' || $4 || '%')
 AND ($5='' OR ep.tier=$5)
 AND ($6='' OR a.owner_team_id=$6 OR t.name=$6)
 ORDER BY a.key,ve.environment_id,a.id LIMIT 1001`

func (s *Service) loadCatalog(ctx context.Context, user core.User, envs []string, appID, env, q, tier, team string) ([]catalogRecord, error) {
	rows, err := s.Store.DB.Query(ctx, catalogQuery, envs, appID, env, q, tier, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []catalogRecord{}
	count := 0
	for rows.Next() {
		count++
		if count > 1000 {
			return nil, errCatalogLimit
		}
		var row catalogRow
		if err = rows.Scan(&row.appID, &row.key, &row.name, &row.description, &row.env,
			&row.teamID, &row.teamName, &row.tier, &row.owner, &row.oncall, &row.target, &row.bindings); err != nil {
			return nil, err
		}
		allowed, authErr := s.canView(ctx, user, row)
		if authErr != nil {
			return nil, authErr
		}
		if !allowed {
			continue
		}
		projected, projectionErr := projectCatalog(row)
		if projectionErr != nil {
			return nil, projectionErr
		}
		items = append(items, projected)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

var errCatalogLimit = errors.New("catalog result limit")

func (s *Service) services(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "view")
	if !ok {
		return
	}
	params := r.URL.Query()
	q, tier, team, status := strings.TrimSpace(params.Get("q")), strings.TrimSpace(params.Get("tier")), strings.TrimSpace(params.Get("team")), strings.TrimSpace(params.Get("status"))
	if len(q) > 200 || len(team) > 200 || (tier != "" && tier != "T0" && tier != "T1" && tier != "T2" && tier != "T3") {
		httpapi.Error(w, 422, "invalid_filter", "Invalid catalog filter")
		return
	}
	if status != "" {
		httpapi.Error(w, 422, "filter_unavailable", "Live service status is unavailable")
		return
	}
	items, err := s.loadCatalog(r.Context(), httpapi.CurrentUser(r), envs, "", "", q, tier, team)
	if err != nil {
		switch {
		case errors.Is(err, errCatalogLimit):
			httpapi.Error(w, 422, "result_limit", "Narrow the catalog filter")
		default:
			httpapi.Error(w, 503, "catalog_unavailable", "Service catalog or policy unavailable")
		}
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "total": len(items), "complete_v3_dto": false})
}

func (s *Service) service(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, "view")
	if !ok {
		return
	}
	appID, env, valid := parseRecordID(r.PathValue("id"))
	if !valid {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	items, err := s.loadCatalog(r.Context(), httpapi.CurrentUser(r), envs, appID, env, "", "", "")
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Service catalog or policy unavailable")
		return
	}
	if len(items) != 1 {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	httpapi.WriteJSON(w, 200, items[0])
}
