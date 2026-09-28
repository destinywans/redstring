package v3catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type mappingCatalogRow struct {
	appID, key, env, teamID string
	bindings                []byte
}

const mappingQuery = `SELECT a.id,a.key,ve.environment_id,COALESCE(a.owner_team_id,''),e.bindings
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
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
		if err := rows.Scan(&row.appID, &row.key, &row.env, &row.teamID, &row.bindings); err != nil {
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
	State string `json:"state"`
	Value string `json:"value,omitempty"`
	Brand string `json:"brand,omitempty"`
}
type mappingRow struct {
	Service string                 `json:"service"`
	Cells   map[string]mappingCell `json:"cells"`
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
			if singleEnvironment {
				visibleBindings[item.appID] = bindings
			}
			row, found := byName[item.key]
			if !found {
				row = &mappingRow{Service: item.key, Cells: map[string]mappingCell{}}
				byName[item.key] = row
				order = append(order, item.key)
			}
			for _, b := range bindings {
				id := mappingKind(b.Kind)
				if id == "" || strings.TrimSpace(b.Resource) == "" {
					continue
				}
				value := b.Resource
				if len(envs) > 1 {
					value = item.env + ": " + value
				}
				cell := row.Cells[id]
				if cell.State == "ok" {
					if !strings.Contains("\n"+cell.Value+"\n", "\n"+value+"\n") {
						cell.Value += ", " + value
					}
				} else {
					cell = mappingCell{State: "ok", Value: value, Brand: b.Kind}
				}
				row.Cells[id] = cell
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
				bindings := append([]core.Binding(nil), visibleBindings[candidate.appID]...)
				bindings = append(bindings, core.Binding{Kind: "gitlab", ConnectorID: candidate.connectorID, Resource: candidate.externalID})
				if !s.mappingBindingSupported(bindings) {
					continue
				}
				row := byName[candidate.key]
				if row == nil || row.Cells["code"].State != "" {
					continue
				}
				row.Cells["code"] = mappingCell{State: "suggested", Value: "Configured GitLab project #" + candidate.externalID, Brand: "gitlab"}
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
	httpapi.WriteJSON(w, 200, map[string]any{"systems": mappingSystems, "rows": output, "suggestions": suggestions})
}
