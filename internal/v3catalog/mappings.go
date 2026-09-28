package v3catalog

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

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
	rows, err := s.Store.DB.Query(r.Context(), `SELECT a.id,a.key,ve.environment_id,COALESCE(a.owner_team_id,''),e.bindings
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 ORDER BY a.key,ve.environment_id`, envs)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
		return
	}
	defer rows.Close()
	byName := map[string]*mappingRow{}
	order := []string{}
	count := 0
	for rows.Next() {
		var appID, key, env, teamID string
		var raw []byte
		if err = rows.Scan(&appID, &key, &env, &teamID, &raw); err != nil {
			break
		}
		visible, authErr := s.canView(r.Context(), httpapi.CurrentUser(r), catalogRow{appID: appID, env: env, teamID: teamID})
		if authErr != nil {
			err = authErr
			break
		}
		if !visible {
			continue
		}
		// The limit is on authorized mappings, not all catalog entries. An
		// unrelated service must not truncate this user's result or disclose
		// its existence through a result-limit error.
		count++
		if count > 1000 {
			httpapi.Error(w, 422, "result_limit", "Narrow the environment scope")
			return
		}
		var bindings []core.Binding
		if err = json.Unmarshal(raw, &bindings); err != nil {
			break
		}
		row, found := byName[key]
		if !found {
			row = &mappingRow{Service: key, Cells: map[string]mappingCell{}}
			byName[key] = row
			order = append(order, key)
		}
		for _, b := range bindings {
			id := mappingKind(b.Kind)
			if id == "" || strings.TrimSpace(b.Resource) == "" {
				continue
			}
			value := b.Resource
			if len(envs) > 1 {
				value = env + ": " + value
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
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Service mappings unavailable")
		return
	}
	output := make([]mappingRow, 0, len(order))
	for _, key := range order {
		row := byName[key]
		for _, system := range mappingSystems {
			if _, found := row.Cells[system.ID]; !found {
				row.Cells[system.ID] = mappingCell{State: "missing"}
			}
		}
		output = append(output, *row)
	}
	httpapi.WriteJSON(w, 200, map[string]any{"systems": mappingSystems, "rows": output, "suggestions": 0})
}
