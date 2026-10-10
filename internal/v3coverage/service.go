// Package v3coverage reports the configuration evidence behind the V3
// coverage matrix. A registered target is not proof that telemetry is live.
package v3coverage

import (
	"context"
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
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
	// Registry answers the metrics, logs and traces cells from the
	// connector registry (PA-19 P3 stage 2 step 1, PA-67): ready, registered
	// but not ready (with the reasons), ambiguous or not registered. Legacy
	// application bindings do not count (P4). Without it those cells stay
	// unknown.
	Registry *v3telemetry.Resolver
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

// columns lists the coverage column IDs; labels come from text.columns.
var columns = textFor("").columns()

type profile struct {
	AppID, Key, EnvironmentID, EnvironmentRowID, TeamID, Tier, Owner string
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
	if len(query["app"]) > 1 || len(query) > 1 || (len(query) > 0 && !query.Has("app")) ||
		len(strings.TrimSpace(query.Get("app"))) > 200 {
		httpapi.Error(w, 422, "invalid_filter", "Choose at most one application")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	t := textFor(r.Header.Get("Accept-Language"))
	unknownValue := t.pick("未知", "Unknown")
	result := response{KPIs: []kpi{}, Types: []coverageType{}, Columns: t.columns(),
		Rows: []coverageRow{}, Fixes: []fix{}, Impact: impact{
			Answers: t.pick("未接入数据缺失归因", "Missing-data attribution is not connected"), Flagged: unknownValue, Delta: unknownValue, Lowered: unknownValue,
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
			row, rowFixes, rowErr := s.project(ctx, user, p, t)
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
		{ID: "services", Label: t.pick("服务", "Services"), Value: fmt.Sprint(len(result.Rows)), Sub: t.pick("当前授权的应用环境记录", "Authorized application environments"), Series: []float64{}},
		{ID: "full", Label: t.pick("完整覆盖", "Full coverage"), Value: unknownValue, Sub: t.pick("实时覆盖尚未验证", "Live coverage is not verified"), Series: []float64{}},
		{ID: "partial", Label: t.pick("部分覆盖", "Partial coverage"), Value: unknownValue, Sub: t.pick("登记不等于数据覆盖", "Registration is not data coverage"), Series: []float64{}},
		{ID: "gaps", Label: t.pick("严重缺失", "Critical gaps"), Value: unknownValue, Sub: t.pick("缺少可信覆盖评分", "No trusted coverage score"), Series: []float64{}},
	}
	for _, c := range result.Columns {
		result.Types = append(result.Types, coverageType{ID: c.ID, Label: c.Label, Tone: "neutral"})
	}
	httpapi.WriteJSON(w, 200, result)
}

var errLimit = errors.New("coverage candidate limit")

func (s *Service) loadProfiles(ctx context.Context, environments []string, app string, cursor profile) ([]profile, error) {
	rows, err := s.DB.Query(ctx, `SELECT a.id,a.key,ve.environment_id,e.id,COALESCE(a.owner_team_id,''),
 COALESCE(ep.tier,''),COALESCE(NULLIF(ep.owner_label,''),u.name,'')
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
			&p.TeamID, &p.Tier, &p.Owner); err != nil {
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

// notReady is a registered source that cannot be read yet (PA-19 P3).
func notReady(brand, reason string) cell {
	return cell{State: "partial", Brand: brand, Text: reason, Availability: "not_ready"}
}

func (s *Service) project(ctx context.Context, user core.User, p profile, t text) (coverageRow, []fix, error) {
	row := coverageRow{ID: p.AppID + ":" + p.EnvironmentID, Service: p.Key,
		EnvironmentID: p.EnvironmentID, Tier: p.Tier, Owner: p.Owner, Cells: map[string]cell{}}
	fixes := []fix{}
	for _, c := range columns {
		row.Cells[c.ID] = unknown(t.pick("无可验证来源", "No verifiable source"))
	}
	if p.Tier == "" {
		row.Tier = "unknown"
	}
	canConnect, err := s.authorized(ctx, user, "connectors", p)
	if err != nil {
		return row, nil, err
	}
	if canConnect && s.Registry != nil {
		if err := s.registryCells(ctx, p, t, &row, &fixes); err != nil {
			return row, nil, err
		}
	} else if !canConnect {
		for _, id := range []string{"metrics", "logs", "traces", "changes"} {
			row.Cells[id] = unknown(t.pick("无权查看连接器配置", "No permission to view connector configuration"))
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
			row.Cells["resources"] = configured("cmdb", t.pick("资源已关联，实时状态未验证", "Resources linked; live state not verified"))
		} else {
			row.Cells["resources"] = absent(t.pick("未关联非示例资源", "No non-sample resource linked"))
		}
	} else {
		row.Cells["resources"] = unknown(t.pick("无权查看资源目录", "No permission to view the resource catalog"))
	}
	canKnowledge, err := s.authorized(ctx, user, "knowledge", p)
	if err != nil {
		return row, nil, err
	}
	if !canKnowledge {
		row.Cells["runbooks"] = unknown(t.pick("无权查看 Runbook", "No permission to view Runbooks"))
	} else {
		// knowledge_documents carries an application ID, but not a verified
		// environment or the per-document visibility decision. Even one visible
		// application environment does not make draft/private review documents
		// visible here or prove operational Runbook coverage.
		row.Cells["runbooks"] = unknown(t.pick("缺少当前环境与文档可见性证据", "No evidence of environment and document visibility"))
	}
	row.Cells["rules"] = unknown(t.pick("告警规则缺少可归属的覆盖来源", "No attributable coverage source for alert rules"))
	return row, fixes, nil
}

// registryCells answers the source cells from the registry: metrics, logs and
// traces from the resolver, changes from code and delivery targets.
func (s *Service) registryCells(ctx context.Context, p profile, t text, row *coverageRow, fixes *[]fix) error {
	addFix := func(id string) {
		if len(*fixes) == 0 {
			*fixes = append(*fixes, fix{Text: p.Key + " · " + p.EnvironmentID + t.pick("：配置"+t.label(id)+"来源", ": configure a "+strings.ToLower(t.label(id))+" source"),
				Impact: t.pick("影响未量化", "Impact not quantified"), Action: t.pick("查看连接器", "View connectors"), Href: "/integrations"})
		}
	}
	for _, surface := range []v3telemetry.Surface{v3telemetry.Metrics, v3telemetry.Logs, v3telemetry.Traces} {
		id := string(surface)
		source, err := s.Registry.Resolve(ctx, s.DB, p.AppID, p.EnvironmentID, surface)
		switch {
		case errors.Is(err, v3telemetry.ErrNotConfigured):
			row.Cells[id] = absent(t.pick("未登记来源", "No source registered"))
			addFix(id)
		case errors.Is(err, v3telemetry.ErrAmbiguous):
			row.Cells[id] = notReady("", t.pick("登记了多个来源，读取会失败关闭", "More than one source registered; reads fail closed"))
			addFix(id)
		case err != nil:
			return err
		case len(source.NotReady) > 0:
			reasons := make([]string, 0, len(source.NotReady))
			for _, reason := range source.NotReady {
				reasons = append(reasons, t.readiness(reason))
			}
			row.Cells[id] = notReady(source.Provider, t.pick("来源已登记但未就绪：", "Source registered but not ready: ")+strings.Join(reasons, t.pick("、", ", ")))
			addFix(id)
		default:
			row.Cells[id] = configured(source.Provider, t.pick("来源已登记，实时数据覆盖未验证", "Source registered; live data coverage not verified"))
		}
	}
	changes, err := s.targetColumns(ctx, p)
	if err != nil {
		return err
	}
	if brand := changes["changes"]; brand != "" {
		row.Cells["changes"] = configured(brand, t.pick("来源已登记，实时数据覆盖未验证", "Source registered; live data coverage not verified"))
	} else {
		row.Cells["changes"] = absent(t.pick("未登记来源", "No source registered"))
		addFix("changes")
	}
	return nil
}

// targetColumns maps the providers of the service's registered targets to
// coverage columns.
func (s *Service) targetColumns(ctx context.Context, p profile) (map[string]string, error) {
	result := map[string]string{}
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
