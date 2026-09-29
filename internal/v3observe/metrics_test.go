package v3observe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// The legacy telemetry stub is shared by independent target and trace tests.
type metricTelemetryStub struct {
	calls []string
	empty bool
}

func (m *metricTelemetryStub) QueryMetrics(_ context.Context, app, query string, _ connectors.TimeWindow) (connectors.MetricEvidence, error) {
	m.calls = append(m.calls, app+"/"+query)
	result := connectors.MetricEvidence{Source: connectors.TelemetrySource{HTTPStatus: 200, QueryID: query}, Empty: m.empty}
	if !m.empty {
		result.Series = []connectors.MetricSeries{{Samples: []connectors.MetricSample{{Timestamp: 1, Value: "1"}}}}
	}
	return result, nil
}

func (*metricTelemetryStub) QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error) {
	return connectors.LogEvidence{}, nil
}
func (*metricTelemetryStub) QueryTracesBatchID(context.Context, []string, connectors.TimeWindow, int, string) (map[string]connectors.TraceEvidence, error) {
	return nil, nil
}

func TestDashboardSeriesRequiresFullGridAndReviewedLabels(t *testing.T) {
	end := time.Now().UTC().Truncate(100 * time.Second)
	window := testDashboardWindow(end)
	result := testDashboardEvidence(window)
	if _, _, ok := dashboardSeries(result, window); !ok {
		t.Fatal("complete fixture rejected")
	}
	result.Series[0].Samples = result.Series[0].Samples[1:]
	if _, _, ok := dashboardSeries(result, window); ok {
		t.Fatal("accepted missing baseline")
	}
	result = testDashboardEvidence(window)
	result.Series[0].Labels["secret"] = "tenant-private"
	if _, _, ok := dashboardSeries(result, window); ok {
		t.Fatal("accepted extra upstream label")
	}
	result = testDashboardEvidence(window)
	result.Series[0].Samples[3].Timestamp++
	if _, _, ok := dashboardSeries(result, window); ok {
		t.Fatal("accepted off-grid point")
	}
	result = testDashboardEvidence(window)
	result.Series[0].Labels["panel"] = "unknown_panel"
	if _, _, ok := dashboardSeries(result, window); ok {
		t.Fatal("accepted unreviewed panel")
	}
	result = testDashboardEvidence(window)
	for i := range result.Series {
		if result.Series[i].Labels["panel"] == "latency_heat" {
			result.Series[i].Samples[1].Value = "1.1"
			break
		}
	}
	if _, _, ok := dashboardSeries(result, window); ok {
		t.Fatal("accepted unnormalized heat cell")
	}
	result = testDashboardEvidence(window)
	for i := range result.Series {
		if result.Series[i].Labels["panel"] == "endpoint_p99_ms" {
			result.Series[i].Labels["endpoint"] = ""
			break
		}
	}
	if _, _, ok := dashboardSeries(result, window); ok {
		t.Fatal("accepted empty endpoint label")
	}
}

func TestDashboardRejectsUndefinedRatioAndContradictorySLO(t *testing.T) {
	end := time.Now().UTC().Truncate(100 * time.Second)
	window := testDashboardWindow(end)
	marker := dashboardMarkers{release: end.Add(-30 * time.Minute), rollback: end.Add(-10 * time.Minute), incidentStart: end.Add(-25 * time.Minute), incidentEnd: end.Add(-5 * time.Minute)}
	result := testDashboardEvidence(window)
	if _, ok := buildDashboard(result, window, marker, 99.9); !ok {
		t.Fatal("valid dashboard rejected")
	}
	boundary := dashboardMarkers{release: window.Start.Add(50 * time.Second), rollback: window.End.Add(-10 * time.Second), incidentStart: window.Start.Add(40 * time.Second), incidentEnd: window.Start.Add(60 * time.Second)}
	near, ok := buildDashboard(result, window, boundary, 99.9)
	if !ok || near.Window.ReleaseAt != 0 || near.Window.RollbackAt != 35 || near.Window.Incident != [2]int{0, 1} {
		t.Fatalf("first-bin markers: %+v complete=%v", near.Window, ok)
	}
	if _, ok := buildDashboard(result, window, marker, 100); ok {
		t.Fatal("accepted undefined 100% SLO budget")
	}
	for i := range result.Series {
		if result.Series[i].Labels["panel"] == "requests_total" {
			for j := range result.Series[i].Samples {
				result.Series[i].Samples[j].Value = "0"
			}
			break
		}
	}
	if _, ok := buildDashboard(result, window, marker, 99.9); ok {
		t.Fatal("accepted zero request denominator")
	}
	result = testDashboardEvidence(window)
	for i := range result.Series {
		if result.Series[i].Labels["panel"] == "slo_budget_remaining_pct" {
			for j := range result.Series[i].Samples {
				result.Series[i].Samples[j].Value = "1"
			}
			break
		}
	}
	if _, ok := buildDashboard(result, window, marker, 99.9); ok {
		t.Fatal("accepted contradictory SLO budget")
	}
	result = testDashboardEvidence(window)
	for i := range result.Series {
		switch result.Series[i].Labels["panel"] {
		case "requests_2xx", "requests_4xx", "requests_5xx", "requests_total":
			for j := range result.Series[i].Samples {
				result.Series[i].Samples[j].Value = "1e308"
			}
		}
	}
	if _, ok := buildDashboard(result, window, marker, 99.9); ok {
		t.Fatal("accepted overflowing request totals")
	}
	old := end.Add(-2 * time.Hour)
	aligned, ok := alignedDashboardEnd(end, old)
	if !ok || !aligned.Equal(old.Add(30*time.Minute).Truncate(100*time.Second)) {
		t.Fatalf("incident alignment: %s %v", aligned, ok)
	}
}

func testDashboardWindow(end time.Time) connectors.TimeWindow {
	return connectors.TimeWindow{Start: end.Add(-time.Hour), End: end, StepSeconds: dashboardStep}
}

func testDashboardEvidence(window connectors.TimeWindow) v3integrations.PrometheusMetricEvidence {
	result := v3integrations.PrometheusMetricEvidence{ApplicationID: "app", EnvironmentID: "dev", BindingID: "target", ConnectorID: "connector", ResourceID: "app-metrics", QueryID: "dashboard", CapturedAt: time.Now().UTC()}
	appendPanel := func(panel, key, label string, value float64) {
		labels := map[string]string{"panel": panel}
		if key != "" {
			labels[key] = label
		}
		series := connectors.MetricSeries{Labels: labels}
		for i := 0; i < dashboardSamples; i++ {
			series.Samples = append(series.Samples, connectors.MetricSample{Timestamp: float64(window.Start.Unix() + int64(i*dashboardStep)), Value: fmt.Sprintf("%.3f", value)})
		}
		result.Series = append(result.Series, series)
	}
	for panel := range dashboardPanels {
		switch panel {
		case "latency_heat":
			for _, bucket := range dashboardHeatBuckets {
				appendPanel(panel, "bucket", bucket, 0.25)
			}
		case "endpoint_p99_ms":
			appendPanel(panel, "endpoint", "GET /cart", 25)
		case "instance_wait_p95_ms":
			appendPanel(panel, "instance", "pod-1", 5)
		default:
			value := float64(1)
			if panel == "requests_2xx" {
				value = 998
			}
			if panel == "requests_other" {
				value = 0
			}
			if panel == "requests_total" {
				value = 1000
			}
			if strings.HasPrefix(panel, "latency_p50") {
				value = 20
			}
			if strings.HasPrefix(panel, "latency_p95") {
				value = 40
			}
			if strings.HasPrefix(panel, "latency_p99") {
				value = 60
			}
			if panel == "slo_month_availability_pct" {
				value = 99.95
			}
			if panel == "slo_budget_remaining_pct" {
				value = 50
			}
			appendPanel(panel, "", "", value)
		}
	}
	return result
}

func TestMetricDashboardRegisteredSourceAndMarkers(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_URL required")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_dashboard_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-dashboard-password"); err != nil {
		t.Fatal(err)
	}
	user, session, err := st.Login(ctx, "admin@example.invalid", "isolated-dashboard-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev');
 INSERT INTO v3_service_environment_profiles(application_id,environment_id,slo_target_percent) VALUES('app','dev',99.9);
 CREATE TABLE deployment_plans(id text PRIMARY KEY,application_id text,environment_id text);
 CREATE TABLE deployment_executions(id text PRIMARY KEY,plan_id text,status text);
 CREATE TABLE deployment_events(execution_id text,type text,created_at timestamptz);
	CREATE TABLE action_plans(id text PRIMARY KEY,investigation_id text,proposal jsonb);
	CREATE TABLE executions(id text,plan_id text,status text,updated_at timestamptz);
 CREATE TABLE ops_incidents(application_id text,environment_id text,suspected boolean,starts_at timestamptz,ends_at timestamptz);`); err != nil {
		t.Fatal(err)
	}
	var mode atomic.Int32
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	const expression = `sum(rate(http_requests_total{application="app",environment="dev"}[5m]))`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" || r.URL.Query().Get("query") != expression {
			t.Errorf("unexpected query: %s", r.URL)
			http.Error(w, "bad query", 400)
			return
		}
		calls.Add(1)
		if mode.Load() == 2 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		start, parseErr := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		if parseErr != nil {
			t.Error(parseErr)
			return
		}
		result := testDashboardEvidence(testDashboardWindow(start.Add(time.Hour)))
		if mode.Load() == 1 {
			result.Series = result.Series[1:]
		}
		rows := make([]any, 0, len(result.Series))
		for _, line := range result.Series {
			values := make([]any, 0, len(line.Samples))
			for _, sample := range line.Samples {
				values = append(values, []any{sample.Timestamp, sample.Value})
			}
			rows = append(rows, map[string]any{"metric": line.Labels, "values": values})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": rows}})
	}))
	defer upstream.Close()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,tools,enabled)
 VALUES('connector','dev','prometheus','Prometheus','observe',$1,'none','[{"name":"query_metrics","enabled":true}]',true)`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('target','app','dev','connector','metric_resource','app-metrics',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "queries.json")
	config := fmt.Sprintf(`{"queries":[{"connector_id":"connector","application_id":"app","environment_id":"dev","resource_id":"app-metrics","query_id":"dashboard","expression":%q}]}`, expression)
	if err = os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3integrations.LoadPrometheusQueryCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := v3integrations.NewPrometheusReader(db, "nonprod", nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	s := New(st, policy, nil)
	s.MetricReader = reader
	s.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/dashboard"+query, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != code {
			t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	check(request("?service=app&range=6h"), 422, "invalid_metric_request")
	check(request("?service=app"), 503, "dashboard_markers_unavailable")
	end := time.Now().UTC().Truncate(100 * time.Second)
	if _, err = db.Exec(ctx, `INSERT INTO deployment_plans VALUES('plan','app','app-dev');
 INSERT INTO deployment_executions VALUES('deploy','plan','succeeded');
 INSERT INTO investigations(id,application_id,title,environment_id) VALUES('investigation','app','I','app-dev');
	INSERT INTO action_plans VALUES('rollback','investigation','{"target_revision":"v1","target_template_digest":"digest"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO deployment_events VALUES('deploy','succeeded',$1)`, end.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO executions VALUES('rollback-execution','rollback','succeeded',$1)`, end.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO events(investigation_id,type,payload,created_at)
	 VALUES('investigation','execution.succeeded','{"execution_id":"rollback-execution"}',$1)`, end.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ops_incidents VALUES('app','app-dev',false,$1,$2)`, end.Add(-25*time.Minute), end.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	w := request("?service=app&range=1h&align=true")
	if w.Code != 200 {
		t.Fatalf("configured dashboard: %d %s", w.Code, w.Body.String())
	}
	var body metricsDashboard
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Window.Labels) != dashboardPoints || len(body.KPIs) != 5 || len(body.Requests.OK) != dashboardPoints || len(body.Latency.Heat) != 8 || len(body.SLO.Burn) != 3 || len(body.Endpoints) != 1 || len(body.Instances) != 1 || body.Window.IncidentLabel == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("incomplete dashboard: %+v", body)
	}
	beforeCalls := calls.Load()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
	 VALUES('second-target','app','dev','connector','metric_resource','another-resource',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	check(request("?service=app"), 409, "ambiguous_binding")
	if calls.Load() != beforeCalls {
		t.Fatal("provider called for ambiguous bindings")
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='second-target'`); err != nil {
		t.Fatal(err)
	}
	beforeCalls = calls.Load()
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	check(request("?service=app"), 403, "forbidden")
	check(request("?service=absent"), 403, "forbidden")
	if calls.Load() != beforeCalls {
		t.Fatal("provider called without connector grant")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','connectors','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	mode.Store(1)
	check(request("?service=app"), 503, "metric_result_incomplete")
	mode.Store(0)
	mode.Store(2)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- request("?service=app") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream was not reached")
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='events' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case w = <-response:
		check(w, 403, "forbidden")
	case <-time.After(10 * time.Second):
		t.Fatal("revoked request stalled")
	}
	mode.Store(0)
	check(request("?service=app"), 403, "forbidden")
	if calls.Load() < 3 {
		t.Fatalf("expected controlled upstream calls, got %d", calls.Load())
	}
}
