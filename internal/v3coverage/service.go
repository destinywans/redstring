// Package v3coverage reports the configuration evidence behind the V3
// coverage matrix. A registered target is not proof that telemetry is live.
package v3coverage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/coverage", "viewer", s.coverage)
	// The handler resolves and authorizes every persisted application before
	// revealing its profile or any narrower source fact.
	api.MarkV3ObjectRoute("GET /api/v1/coverage", "services", "view")
}

type kpi struct {
	ID     string    `json:"id"`
	Label  string    `json:"label"`
	Value  string    `json:"value"`
	Sub    string    `json:"sub,omitempty"`
	Series []float64 `json:"series"`
}
type coverageType struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Pct   *int   `json:"pct"`
	Tone  string `json:"tone"`
}
type column struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}
type cell struct {
	State        string `json:"state"`
	Brand        string `json:"brand,omitempty"`
	Text         string `json:"text,omitempty"`
	Availability string `json:"availability"`
}
type coverageRow struct {
	ID             string          `json:"id"`
	Service        string          `json:"service"`
	EnvironmentID  string          `json:"environment_id"`
	Tier           string          `json:"tier"`
	Owner          string          `json:"owner,omitempty"`
	Cells          map[string]cell `json:"cells"`
	Score          *int            `json:"score"`
	ScoreAvailable bool            `json:"score_available"`
}
type impactType struct {
	Label string `json:"label"`
	Value *int   `json:"value"`
}
type impact struct {
	Answers      string       `json:"answers"`
	Flagged      string       `json:"flagged"`
	Delta        string       `json:"delta"`
	Lowered      string       `json:"lowered"`
	ByType       []impactType `json:"by_type"`
	EvalFailures *int         `json:"eval_failures"`
	Availability string       `json:"availability"`
}
type fix struct {
	Text   string `json:"text"`
	Impact string `json:"impact"`
	Action string `json:"action"`
	Href   string `json:"href"`
}
type response struct {
	KPIs         []kpi          `json:"kpis"`
	Types        []coverageType `json:"types"`
	Columns      []column       `json:"columns"`
	Rows         []coverageRow  `json:"rows"`
	Impact       impact         `json:"impact"`
	Fixes        []fix          `json:"fixes"`
	Availability struct {
		LiveCoverage string `json:"live_coverage"`
		Scoring      string `json:"scoring"`
		AgentImpact  string `json:"agent_impact"`
	} `json:"availability"`
}

var columns = []column{
	{"metrics", "指标"}, {"logs", "日志"}, {"traces", "链路"},
	{"changes", "变更"}, {"resources", "资源"}, {"runbooks", "Runbook"},
	{"rules", "告警规则"},
}

type profile struct {
	AppID, Key, EnvironmentID, EnvironmentRowID, TeamID, Tier, Owner string
	Bindings                                                         []byte
}

func (s *Service) coverage(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "coverage_unavailable", "Coverage storage or authorization is unavailable")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	query := r.URL.Query()
	if len(query["app"]) > 1 || len(query) > 1 || len(strings.TrimSpace(query.Get("app"))) > 200 {
		httpapi.Error(w, 422, "invalid_filter", "Choose at most one application")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	result := response{KPIs: []kpi{}, Types: []coverageType{}, Columns: columns,
		Rows: []coverageRow{}, Fixes: []fix{}, Impact: impact{
			Answers: "未接入数据缺失归因", Flagged: "未知", Delta: "未知", Lowered: "未知",
			ByType: []impactType{}, Availability: "not_connected",
		}}
	result.Availability.LiveCoverage = "unverified"
	result.Availability.Scoring = "unavailable"
	result.Availability.AgentImpact = "not_connected"
	var cursor profile
	for {
		profiles, err := s.loadProfiles(ctx, scope.EnvironmentIDs, strings.TrimSpace(query.Get("app")), cursor)
		if err != nil {
			httpapi.Error(w, 503, "coverage_unavailable", "Coverage source unavailable")
			return
		}
		for _, p := range profiles {
			allowed, authErr := s.authorized(ctx, user, "services", p)
			if authErr != nil {
				httpapi.Error(w, 503, "authorization_unavailable", "Coverage policy unavailable")
				return
			}
			if !allowed {
				continue
			}
			if len(result.Rows) == 200 {
				httpapi.Error(w, 422, "result_limit", "Choose an application to narrow coverage")
				return
			}
			row, rowFixes, rowErr := s.project(ctx, user, p)
			if rowErr != nil {
				httpapi.Error(w, 503, "coverage_unavailable", "Coverage source or policy unavailable")
				return
			}
			result.Rows = append(result.Rows, row)
			if len(result.Fixes) < 12 {
				remaining := 12 - len(result.Fixes)
				if len(rowFixes) > remaining {
					rowFixes = rowFixes[:remaining]
				}
				result.Fixes = append(result.Fixes, rowFixes...)
			}
		}
		if len(profiles) < 128 {
			break
		}
		cursor = profiles[len(profiles)-1]
	}
	result.KPIs = []kpi{
		{ID: "services", Label: "服务", Value: fmt.Sprint(len(result.Rows)), Sub: "当前授权的应用环境记录", Series: []float64{}},
		{ID: "full", Label: "完整覆盖", Value: "未知", Sub: "实时覆盖尚未验证", Series: []float64{}},
		{ID: "partial", Label: "部分覆盖", Value: "未知", Sub: "登记不等于数据覆盖", Series: []float64{}},
		{ID: "gaps", Label: "严重缺失", Value: "未知", Sub: "缺少可信覆盖评分", Series: []float64{}},
	}
	for _, c := range columns {
		result.Types = append(result.Types, coverageType{ID: c.ID, Label: c.Label, Tone: "neutral"})
	}
	httpapi.WriteJSON(w, 200, result)
}

var errLimit = errors.New("coverage candidate limit")

func (s *Service) loadProfiles(ctx context.Context, environments []string, app string, cursor profile) ([]profile, error) {
	rows, err := s.DB.Query(ctx, `SELECT a.id,a.key,ve.environment_id,e.id,COALESCE(a.owner_team_id,''),
 COALESCE(ep.tier,''),COALESCE(NULLIF(ep.owner_label,''),u.name,''),e.bindings
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=ve.environment_id
 LEFT JOIN users u ON u.id=ep.oncall_user_id
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 AND ($2='' OR a.id=$2 OR a.key=$2)
 AND (a.key,ve.environment_id,a.id)>($3,$4,$5)
 ORDER BY a.key,ve.environment_id,a.id LIMIT 128`,
		environments, app, cursor.Key, cursor.EnvironmentID, cursor.AppID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]profile, 0, 32)
	for rows.Next() {
		var p profile
		if err := rows.Scan(&p.AppID, &p.Key, &p.EnvironmentID, &p.EnvironmentRowID,
			&p.TeamID, &p.Tier, &p.Owner, &p.Bindings); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) authorized(ctx context.Context, user core.User, module string, p profile) (bool, error) {
	err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view",
		EnvironmentID: p.EnvironmentID, ApplicationID: p.AppID})
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, store.ErrForbidden) {
		return false, err
	}
	if p.TeamID == "" {
		return false, nil
	}
	err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: "view",
		EnvironmentID: p.EnvironmentID, TeamID: p.TeamID})
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

func unknown(reason string) cell {
	return cell{State: "unknown", Text: reason, Availability: "unavailable"}
}
func absent(reason string) cell {
	return cell{State: "missing", Text: reason, Availability: "not_configured"}
}
func configured(brand, reason string) cell {
	return cell{State: "partial", Brand: brand, Text: reason, Availability: "configured_unverified"}
}

func (s *Service) project(ctx context.Context, user core.User, p profile) (coverageRow, []fix, error) {
	row := coverageRow{ID: p.AppID + ":" + p.EnvironmentID, Service: p.Key,
		EnvironmentID: p.EnvironmentID, Tier: p.Tier, Owner: p.Owner, Cells: map[string]cell{}}
	fixes := []fix{}
	for _, c := range columns {
		row.Cells[c.ID] = unknown("无可验证来源")
	}
	if p.Tier == "" {
		row.Tier = "unknown"
	}
	canConnect, err := s.authorized(ctx, user, "connectors", p)
	if err != nil {
		return row, nil, err
	}
	if canConnect {
		bindings, err := s.connectorBindings(ctx, p)
		if err != nil {
			return row, nil, err
		}
		for _, id := range []string{"metrics", "logs", "traces", "changes"} {
			if brand := bindings[id]; brand != "" {
				row.Cells[id] = configured(brand, "来源已登记，实时数据覆盖未验证")
			} else {
				row.Cells[id] = absent("未登记精确来源")
				if len(fixes) == 0 {
					fixes = append(fixes, fix{Text: p.Key + " · " + p.EnvironmentID + "：配置" + label(id) + "来源",
						Impact: "影响未量化", Action: "查看连接器", Href: "/integrations"})
				}
			}
		}
	} else {
		for _, id := range []string{"metrics", "logs", "traces", "changes"} {
			row.Cells[id] = unknown("无权查看连接器配置")
		}
	}
	canResources, err := s.authorized(ctx, user, "resources", p)
	if err != nil {
		return row, nil, err
	}
	if canResources {
		var linked bool
		err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM cmdb_resource_applications ra
 JOIN cmdb_resources cr ON cr.id=ra.resource_id
 WHERE ra.application_id=$1 AND ra.environment_id=$2 AND NOT cr.is_mock)`,
			p.AppID, p.EnvironmentRowID).Scan(&linked)
		if err != nil {
			return row, nil, err
		}
		if linked {
			row.Cells["resources"] = configured("cmdb", "资源已关联，实时状态未验证")
		} else {
			row.Cells["resources"] = absent("未关联非示例资源")
		}
	} else {
		row.Cells["resources"] = unknown("无权查看资源目录")
	}
	canKnowledge, err := s.authorized(ctx, user, "knowledge", p)
	if err != nil {
		return row, nil, err
	}
	if !canKnowledge {
		row.Cells["runbooks"] = unknown("无权查看 Runbook")
	} else {
		// knowledge_documents carries an application ID, but not a verified
		// environment or the per-document visibility decision. Even one visible
		// application environment does not make draft/private review documents
		// visible here or prove operational Runbook coverage.
		row.Cells["runbooks"] = unknown("缺少当前环境与文档可见性证据")
	}
	row.Cells["rules"] = unknown("告警规则缺少可归属的覆盖来源")
	return row, fixes, nil
}

func label(id string) string {
	switch id {
	case "metrics":
		return "指标"
	case "logs":
		return "日志"
	case "traces":
		return "链路"
	default:
		return "变更"
	}
}

// Only a persisted V3 connector in the same environment can make a legacy
// application binding an identified configuration fact. No probe status is
// interpreted as live coverage or successful data delivery.
func (s *Service) connectorBindings(ctx context.Context, p profile) (map[string]string, error) {
	var bindings []core.Binding
	if err := json.Unmarshal(p.Bindings, &bindings); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(bindings))
	for _, b := range bindings {
		if b.ConnectorID != "" && strings.TrimSpace(b.Resource) != "" {
			ids = append(ids, b.ConnectorID)
		}
	}
	known := map[string]string{}
	if len(ids) > 0 {
		rows, err := s.DB.Query(ctx, `SELECT id,provider FROM v3_connectors WHERE environment_id=$1 AND id=ANY($2::text[])`,
			p.EnvironmentID, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, provider string
			if err = rows.Scan(&id, &provider); err != nil {
				break
			}
			known[id] = strings.ToLower(provider)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	result := map[string]string{}
	for _, b := range bindings {
		provider, ok := known[b.ConnectorID]
		if !ok || provider != strings.ToLower(b.Kind) || strings.TrimSpace(b.Resource) == "" {
			continue
		}
		if id := connectorColumn(provider); id != "" {
			result[id] = provider
		}
	}
	// Target registration is stronger than the historical application binding
	// for code/deployment sources, but still only proves configuration.
	rows, err := s.DB.Query(ctx, `SELECT c.provider FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.application_id=$1 AND t.environment_id=$2 ORDER BY c.provider LIMIT 101`, p.AppID, p.EnvironmentID)
	if err != nil {
		return nil, err
	}
	count := 0
	for rows.Next() {
		var provider string
		if err = rows.Scan(&provider); err != nil {
			break
		}
		count++
		if id := connectorColumn(strings.ToLower(provider)); id != "" {
			result[id] = strings.ToLower(provider)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if count > 100 {
		return nil, errLimit
	}
	return result, nil
}

func connectorColumn(provider string) string {
	switch provider {
	case "prometheus", "victoriametrics":
		return "metrics"
	case "loki", "elasticsearch":
		return "logs"
	case "tempo", "jaeger":
		return "traces"
	case "gitlab", "githubactions", "github", "argocd", "argo", "jenkins":
		return "changes"
	default:
		return ""
	}
}
