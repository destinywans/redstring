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
	"github.com/wans/aiops-canvas/internal/v3metricpanels"
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
	dashboardSeries := func(result v3integrations.PrometheusMetricEvidence, window connectors.TimeWindow) (any, any, bool) {
		_, ok := v3metricpanels.Parse(result.Series, window)
		return nil, nil, ok
	}
	if _, _, ok := dashboardSeries(result, window); !ok {
		t.Fatal("complete fixture rejected")
	}
	result.Series[0].Samples = result.Series[0].Samples[1:]
	if _, _, ok := dashboardSeries(result, window); !ok {
		t.Fatal("a missing baseline is a gap (PA-44), not a malformed series")
	}
	result = testDashboardEvidence(window)
	result.Series[0].Samples[3].Value = "+Inf"
	if _, _, ok := dashboardSeries(result, window); ok {
		t.Fatal("accepted an infinite value")
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

func at(t time.Time) *time.Time { return &t }

func TestDashboardRejectsUndefinedRatioAndContradictorySLO(t *testing.T) {
	end := time.Now().UTC().Truncate(100 * time.Second)
	window := testDashboardWindow(end)
	marker := dashboardMarkers{release: at(end.Add(-30 * time.Minute)), rollback: at(end.Add(-10 * time.Minute)), incidentStart: at(end.Add(-25 * time.Minute)), incidentEnd: at(end.Add(-5 * time.Minute))}
	target := 99.9
	build := func(result v3integrations.PrometheusMetricEvidence, marker dashboardMarkers, target *float64) (metricsDashboard, bool) {
		out := newDashboard(window, marker)
		ok := out.applyMetrics(result, window, target)
		return out, ok
	}
	result := testDashboardEvidence(window)
	valid, ok := build(result, marker, &target)
	if !ok || valid.SLO == nil || len(valid.Unavailable) != 0 || valid.Window.ReleaseAt == nil || valid.Window.Incident == nil {
		t.Fatalf("valid dashboard rejected: ok=%v unavailable=%v", ok, valid.Unavailable)
	}
	boundary := dashboardMarkers{release: at(window.Start.Add(50 * time.Second)), rollback: at(window.End.Add(-10 * time.Second)), incidentStart: at(window.Start.Add(40 * time.Second)), incidentEnd: at(window.Start.Add(60 * time.Second))}
	near, ok := build(result, boundary, &target)
	if !ok || *near.Window.ReleaseAt != 0 || *near.Window.RollbackAt != 35 || *near.Window.Incident != [2]int{0, 1} {
		t.Fatalf("first-bin markers: %+v complete=%v", near.Window, ok)
	}
	// Markers the platform did not record are null with a reason, never a bin.
	unmarked, ok := build(result, dashboardMarkers{}, &target)
	if !ok || unmarked.Window.ReleaseAt != nil || unmarked.Window.RollbackAt != nil || unmarked.Window.Incident != nil ||
		unmarked.Window.IncidentLabel != nil || unmarked.Unavailable["window.release_at"] != "not_generated" ||
		unmarked.Unavailable["window.incident"] != "not_generated" || unmarked.KPIs == nil || unmarked.SLO == nil {
		t.Fatalf("absent markers: %+v %v", unmarked.Window, unmarked.Unavailable)
	}
	undefined := 100.0
	for _, missing := range []*float64{nil, &undefined} {
		noBudget, ok := build(result, marker, missing)
		if !ok || noBudget.SLO != nil || noBudget.Unavailable["slo"] != "not_configured" || noBudget.KPIs == nil {
			t.Fatalf("undefined SLO target must only hide the SLO block: %v %+v", ok, noBudget.Unavailable)
		}
	}
	for i := range result.Series {
		if result.Series[i].Labels["panel"] == "requests_total" {
			for j := range result.Series[i].Samples {
				result.Series[i].Samples[j].Value = "0"
			}
			break
		}
	}
	if _, ok := build(result, marker, &target); ok {
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
	contradictory, ok := build(result, marker, &target)
	if !ok || contradictory.SLO != nil || contradictory.Unavailable["slo"] != "unavailable" || contradictory.Requests == nil {
		t.Fatalf("contradictory SLO budget must leave only the SLO unknown: %v %+v", ok, contradictory.Unavailable)
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
	if _, ok := build(result, marker, &target); ok {
		t.Fatal("accepted overflowing request totals")
	}
	old := end.Add(-2 * time.Hour)
	aligned, ok := alignedDashboardEnd(end, old)
	if !ok || !aligned.Equal(old.Add(30*time.Minute).Truncate(100*time.Second)) {
		t.Fatalf("incident alignment: %s %v", aligned, ok)
	}
}

// PA-41: a partial panel set fills the blocks it can; the lab has no
// connection wait, heat map or grouped panels.
func TestDashboardAcceptsPartialPanelSet(t *testing.T) {
	end := time.Now().UTC().Truncate(100 * time.Second)
	window := testDashboardWindow(end)
	result := testDashboardEvidence(window)
	kept := result.Series[:0:0]
	for _, line := range result.Series {
		switch line.Labels["panel"] {
		case "connection_wait_p95_ms", "latency_heat", "endpoint_p99_ms", "instance_wait_p95_ms", "slo_burn_6h":
			continue
		}
		kept = append(kept, line)
	}
	result.Series = kept
	target := 99.9
	out := newDashboard(window, dashboardMarkers{})
	if !out.applyMetrics(result, window, &target) {
		t.Fatal("partial panel set rejected")
	}
	ids := []string{}
	for _, k := range out.KPIs {
		ids = append(ids, k.ID)
	}
	if strings.Join(ids, ",") != "rps,err,p99,cpu" || out.Requests == nil || out.Latency == nil || out.Latency.Heat != nil ||
		out.Endpoints != nil || out.Instances != nil || out.SLO == nil || len(out.SLO.Burn) != 2 {
		t.Fatalf("partial dashboard: kpis=%v %+v", ids, out)
	}
	for field, want := range map[string]string{"kpis.wait": "not_configured", "latency.heat": "not_configured",
		"endpoints": "not_configured", "instances": "not_configured", "slo.burn.6h": "not_configured"} {
		if out.Unavailable[field] != want {
			t.Fatalf("%s reason %q: %v", field, out.Unavailable[field], out.Unavailable)
		}
	}
	if _, present := out.Unavailable["requests"]; present {
		t.Fatalf("present block reported missing: %v", out.Unavailable)
	}
	// Only SLO panels: no request, latency or KPI blocks, SLO still shown.
	sloOnly := testDashboardEvidence(window)
	kept = sloOnly.Series[:0:0]
	for _, line := range sloOnly.Series {
		if strings.HasPrefix(line.Labels["panel"], "slo_") {
			kept = append(kept, line)
		}
	}
	sloOnly.Series = kept
	out = newDashboard(window, dashboardMarkers{})
	if !out.applyMetrics(sloOnly, window, &target) || out.SLO == nil || len(out.SLO.Burn) != 3 || out.KPIs != nil ||
		out.Requests != nil || out.Latency != nil || out.Unavailable["kpis"] != "not_configured" || out.Unavailable["requests"] != "not_configured" {
		t.Fatalf("SLO-only dashboard: %+v %v", out, out.Unavailable)
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
		for i := 0; i <= dashboardPoints; i++ {
			series.Samples = append(series.Samples, connectors.MetricSample{Timestamp: float64(window.Start.Unix() + int64(i*dashboardStep)), Value: fmt.Sprintf("%.3f", value)})
		}
		result.Series = append(result.Series, series)
	}
	for _, panel := range append(append([]string{}, v3metricpanels.ScalarPanels...), "latency_heat", "endpoint_p99_ms", "instance_wait_p95_ms") {
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
	decode := func(w *httptest.ResponseRecorder) metricsDashboard {
		t.Helper()
		var out metricsDashboard
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Unavailable == nil {
			t.Fatalf("dashboard: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	// Metrics are served without any platform-recorded marker in the window.
	unmarked := decode(request("?service=app"))
	if unmarked.KPIs == nil || unmarked.Window.ReleaseAt != nil || unmarked.Window.Incident != nil ||
		unmarked.Unavailable["window.release_at"] != "not_generated" || unmarked.Unavailable["window.rollback_at"] != "not_generated" ||
		unmarked.Unavailable["kpis"] != "" || len(unmarked.Window.Labels) != dashboardPoints {
		t.Fatalf("unmarked dashboard: %+v", unmarked)
	}
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
	body := decode(w)
	if len(body.Window.Labels) != dashboardPoints || len(body.KPIs) != 5 || len(body.Requests.OK) != dashboardPoints || len(body.Latency.Heat) != 8 ||
		body.SLO == nil || len(body.SLO.Burn) != 3 || len(body.Endpoints) != 1 || len(body.Instances) != 1 || body.Window.IncidentLabel == nil ||
		len(body.Unavailable) != 0 || !strings.Contains(w.Body.String(), `"unavailable":{}`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("incomplete dashboard: %+v", body)
	}
	beforeCalls := calls.Load()
	check(request("?service=app&source=victoriametrics"), 503, "metrics_source_not_connected")
	check(request("?service=app&source=unknown"), 422, "invalid_source")
	if calls.Load() != beforeCalls {
		t.Fatal("unconnected source fell back to Prometheus")
	}
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
	// A visible service without connectors.view keeps the page: only the
	// metric blocks are unknown, and no source is looked up or read.
	noSource := decode(request("?service=app"))
	if noSource.KPIs != nil || noSource.Unavailable["kpis"] != "no_authorized_target" || noSource.Unavailable["slo"] != "no_authorized_target" ||
		noSource.Window.ReleaseAt == nil {
		t.Fatalf("missing connector grant: %+v", noSource)
	}
	check(request("?service=absent"), 403, "forbidden")
	if calls.Load() != beforeCalls {
		t.Fatal("provider called without connector grant")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','connectors','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	mode.Store(1)
	// PA-41: a missing panel (here requests_2xx) leaves only the blocks
	// that need it unknown; the rest of the dashboard is served.
	partial := decode(request("?service=app"))
	if partial.Requests != nil || partial.Unavailable["requests"] != "not_configured" || partial.Unavailable["kpis.rps"] != "not_configured" ||
		partial.Latency == nil || partial.SLO == nil || len(partial.KPIs) != 3 || partial.Window.ReleaseAt == nil {
		t.Fatalf("missing panel must leave only its blocks unknown: %+v %v", partial, partial.Unavailable)
	}
	mode.Store(0)
	// A connector without the enabled query tool is not a registered source.
	if _, err = db.Exec(ctx, `UPDATE v3_connectors SET tools='[{"name":"query_metrics","enabled":false}]' WHERE id='connector'`); err != nil {
		t.Fatal(err)
	}
	beforeCalls = calls.Load()
	unconfigured := decode(request("?service=app"))
	if unconfigured.KPIs != nil || unconfigured.Unavailable["kpis"] != "not_configured" || unconfigured.Unavailable["slo"] != "not_configured" ||
		unconfigured.Window.ReleaseAt == nil || calls.Load() != beforeCalls {
		t.Fatalf("unconfigured source: %+v calls=%d", unconfigured, calls.Load()-beforeCalls)
	}
	if _, err = db.Exec(ctx, `UPDATE v3_connectors SET tools='[{"name":"query_metrics","enabled":true}]' WHERE id='connector'`); err != nil {
		t.Fatal(err)
	}
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
		// Grants evaluated before the read changed: nothing is returned.
		check(w, 409, "metric_source_changed")
	case <-time.After(10 * time.Second):
		t.Fatal("revoked request stalled")
	}
	// Without events.view the markers are unknown; metrics are still served
	// and alignment, which needs an incident, is refused.
	mode.Store(0)
	noEvents := decode(request("?service=app"))
	if noEvents.KPIs == nil || noEvents.Window.ReleaseAt != nil || noEvents.Unavailable["window.release_at"] != "no_authorized_target" ||
		noEvents.Unavailable["window.incident"] != "no_authorized_target" {
		t.Fatalf("missing events grant: %+v", noEvents.Unavailable)
	}
	check(request("?service=app&align=true"), 403, "forbidden")
	mode.Store(2)
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','events','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	entered, release = make(chan struct{}), make(chan struct{})
	response = make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- request("?service=app") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream was not reached before logout")
	}
	if err = st.Logout(ctx, session); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case w = <-response:
		check(w, 401, "unauthorized")
		if strings.Contains(w.Body.String(), `"kpis"`) {
			t.Fatal("revoked session returned dashboard data")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("logged-out request stalled")
	}
	mode.Store(0)
	check(request("?service=app"), 401, "unauthorized")
	if calls.Load() < 3 {
		t.Fatalf("expected controlled upstream calls, got %d", calls.Load())
	}
}

// PA-24: KPI names, the SLO note and the incident label follow
// Accept-Language; English only for "en".
func TestDashboardLocalizesLabels(t *testing.T) {
	end := time.Now().UTC().Truncate(100 * time.Second)
	window := testDashboardWindow(end)
	marker := dashboardMarkers{incidentStart: at(end.Add(-25 * time.Minute)), incidentEnd: at(end.Add(-5 * time.Minute))}
	target := 99.9
	build := func(lang string) metricsDashboard {
		out := newDashboard(window, marker)
		if !out.applyMetrics(testDashboardEvidence(window), window, &target) {
			t.Fatal("fixture rejected")
		}
		out.localize(dashboardTextFor(lang))
		return out
	}
	zh, en := build("zh-CN,zh;q=0.9"), build("en-US")
	stamp := end.Add(-25*time.Minute).UTC().Format("15:04") + "–" + end.Add(-5*time.Minute).UTC().Format("15:04") + " UTC"
	if zh.KPIs[0].Label != "请求量" || zh.KPIs[0].Sub != "观测到的请求速率" || zh.SLO.Note != "观测到的错误预算消耗速率" ||
		*zh.Window.IncidentLabel != "事件 · "+stamp {
		t.Fatalf("Chinese dashboard: %+v %q %q", zh.KPIs[0], zh.SLO.Note, *zh.Window.IncidentLabel)
	}
	if en.KPIs[0].Label != "Requests" || en.SLO.Note != "Observed error budget burn rates" || *en.Window.IncidentLabel != "Incident · "+stamp {
		t.Fatalf("English dashboard: %+v %q %q", en.KPIs[0], en.SLO.Note, *en.Window.IncidentLabel)
	}
	if build("").SLO.Note != zh.SLO.Note {
		t.Fatal("no Accept-Language must default to Chinese")
	}
}

// PA-44: zero traffic and latency gaps in the last bins keep every block;
// values without a current sample are unknown and series carry null.
func TestDashboardKeepsBlocksThroughAnOutage(t *testing.T) {
	end := time.Now().UTC().Truncate(100 * time.Second)
	window := testDashboardWindow(end)
	result := testDashboardEvidence(window)
	for i := range result.Series {
		line := &result.Series[i]
		switch line.Labels["panel"] {
		case "requests_2xx", "requests_4xx", "requests_5xx", "requests_total":
			for j := dashboardPoints - 4; j <= dashboardPoints; j++ {
				line.Samples[j].Value = "0"
			}
		case "latency_p50_ms", "latency_p95_ms", "latency_p99_ms", "connection_wait_p95_ms", "endpoint_p99_ms", "instance_wait_p95_ms", "latency_heat":
			line.Samples[dashboardPoints].Value = "NaN"
		}
	}
	target := 99.9
	out := newDashboard(window, dashboardMarkers{})
	if !out.applyMetrics(result, window, &target) {
		t.Fatalf("outage dashboard rejected: %v", out.Unavailable)
	}
	kpis := map[string]dashboardKPI{}
	for _, k := range out.KPIs {
		kpis[k.ID] = k
	}
	if out.Requests == nil || out.Latency == nil || out.SLO == nil || out.Instances == nil || kpis["rps"].Value != "0.00" ||
		out.Unavailable["kpis.err"] != "not_generated" || out.Unavailable["kpis.p99"] != "not_generated" ||
		out.Unavailable["endpoints"] != "not_generated" || out.Endpoints != nil {
		t.Fatalf("outage dashboard: kpis=%v unavailable=%v", kpis, out.Unavailable)
	}
	if out.Latency.P99[dashboardPoints-1] != nil || out.Latency.P99[0] == nil || *out.Requests.OK[dashboardPoints-1] != 0 ||
		out.Instances[0].Values[dashboardPoints-1] != nil || out.Latency.Heat[0][dashboardPoints-1] != nil {
		t.Fatal("gaps must be null and zero traffic must be 0")
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("NaN leaked into JSON: %v", err)
	}
}
