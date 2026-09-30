package v3serviceviews

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/deployment"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

type completeReader struct {
	now   time.Time
	calls []string
}

type tokenRevokingReader struct {
	*completeReader
	tokenPath string
	revoked   bool
}

func (r *tokenRevokingReader) QueryService(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User,
	service, env, query string, window connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error) {
	evidence, err := r.completeReader.QueryService(ctx, scope, user, service, env, query, window)
	if err == nil && !r.revoked {
		r.revoked = true
		err = os.Remove(r.tokenPath)
	}
	return evidence, err
}

func (r *completeReader) QueryService(_ context.Context, scope httpapi.V3EnvironmentScope, _ core.User,
	service, env, query string, window connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error) {
	r.calls = append(r.calls, service+"/"+env)
	if len(scope.EnvironmentIDs) != 1 || scope.All || scope.EnvironmentIDs[0] != "test" || env != "test" || query != "dashboard" || window.StepSeconds != 100 {
		return v3integrations.PrometheusMetricEvidence{}, fmt.Errorf("invalid metric scope")
	}
	e := serviceFixture(r.now)
	e.EnvironmentID = "test"
	switch service {
	case "checkout":
		e.ApplicationID = "checkout-app"
		e.BindingID = "metric-checkout"
		e.ResourceID = "checkout-metrics"
	case "payments":
		e.ApplicationID = "payments-app"
		e.BindingID = "metric-payments"
		e.ResourceID = "payments-metrics"
		for i := range e.Series {
			var value string
			switch e.Series[i].Labels["panel"] {
			case "requests_2xx":
				value = "999"
			case "requests_5xx", "slo_burn_1h":
				value = "0"
			case "slo_month_availability_pct":
				value = "99.980"
			case "slo_budget_remaining_pct":
				value = "80"
			}
			if value != "" {
				for j := range e.Series[i].Samples {
					e.Series[i].Samples[j].Value = value
				}
			}
		}
	default:
		return v3integrations.PrometheusMetricEvidence{}, fmt.Errorf("unknown service")
	}
	return e, nil
}

func serviceTestDB(t *testing.T) (*store.Store, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_service_views_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 2
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	if err = operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = (&deployment.Service{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st, schema
}

func seedServiceViews(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	ctx := t.Context()
	statements := []string{
		`INSERT INTO teams(id,name) VALUES('ops','Operations'),('payments','Payments')`,
		`INSERT INTO applications(id,key,name,description,environment,owner_team_id) VALUES
 ('checkout-app','checkout','Checkout','Checkout API','test','ops'),
 ('payments-app','payments','Payments','Payments API','test','payments')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('checkout-test','checkout-app','test','Test'),('payments-test','payments-app','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('checkout-app','test'),('payments-app','test')`,
		`INSERT INTO v3_service_environment_profiles(application_id,environment_id,tier,slo_target_percent,oncall_user_id)
 SELECT 'checkout-app','test','T0',99.9,id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO v3_service_environment_profiles(application_id,environment_id,tier,slo_target_percent,oncall_user_id)
 SELECT 'payments-app','test','T1',99.9,id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
	 SELECT 'admin',module,'view','global','*' FROM (VALUES('services'),('connectors'),('resources'),('events'),('canvas'),('proposals')) AS x(module)
 ON CONFLICT DO NOTHING`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled,tools)
 VALUES('prom','test','prometheus','Prometheus','observability','https://prom.example.invalid','none',true,
 '[{"name":"query_metrics","enabled":true}]')`,
		`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 SELECT 'metric-checkout','checkout-app','test','prom','metric_resource','checkout-metrics',id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 SELECT 'metric-payments','payments-app','test','prom','metric_resource','payments-metrics',id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO investigations(id,application_id,environment_id,title) VALUES('INV-checkout','checkout-app','checkout-test','Checkout review')`,
		`INSERT INTO branches(id,investigation_id,title,revision) VALUES('branch-checkout','INV-checkout','Root',1)`,
		`INSERT INTO context_revisions(branch_id,revision,messages) VALUES('branch-checkout',1,
 '[{"id":"m1","role":"assistant","content":"Observed elevated errors after the release.","model_id":"investigator-v1"}]')`,
		`INSERT INTO runs(id,branch_id,request_id,request_hash,status,model_id,input,context_revision)
 VALUES('run-checkout','branch-checkout','request-checkout','hash','completed','investigator-v1','{}',1)`,
		`INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES
 ('res-checkout','service','checkout','manual'),('res-payments','service','payments','manual')`,
		`INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES
 ('res-checkout','checkout-app','checkout-test'),('res-payments','payments-app','payments-test')`,
		`INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind)
 VALUES('checkout-payments','res-checkout','res-payments','depends_on','declared','manual')`,
	}
	for _, sql := range statements {
		if _, err := st.DB.Exec(ctx, sql); err != nil {
			t.Fatalf("seed %s: %v", sql, err)
		}
	}
	if _, err := st.DB.Exec(ctx, `UPDATE runs SET created_at=$1 WHERE id='run-checkout'`, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `UPDATE context_revisions SET created_at=$1 WHERE branch_id='branch-checkout'`, now.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `UPDATE context_revisions
 SET messages=jsonb_set(jsonb_set(messages,'{0,run_id}',to_jsonb('run-checkout'::text)),
 '{0,created_at}',to_jsonb($1::text)) WHERE branch_id='branch-checkout'`,
		now.Add(-5*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ app, envID, id string }{{"checkout-app", "checkout-test", "checkout"}, {"payments-app", "payments-test", "payments"}} {
		_, err := st.DB.Exec(ctx, `INSERT INTO deployment_plans(id,application_id,environment_id,target_id,request_key,digest,snapshot,created_by)
 SELECT $1,$2,$3,'target',$1,'digest',jsonb_build_object('target_kind','gitops','source',jsonb_build_object('commit_sha','abc123')),id
 FROM users WHERE email='admin@example.invalid'`, "plan-"+v.id, v.app, v.envID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.DB.Exec(ctx, `INSERT INTO deployment_executions(id,plan_id,status,created_by,result)
 SELECT $1,$2,'succeeded',id,jsonb_build_object('revision',$3::text) FROM users WHERE email='admin@example.invalid'`, "exec-"+v.id, "plan-"+v.id, "rev-"+v.id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.DB.Exec(ctx, `INSERT INTO deployment_events(execution_id,type,payload,created_at)
 VALUES($1,'succeeded','{}',$2)`, "exec-"+v.id, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := st.DB.Exec(ctx, `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
 VALUES('alert-checkout','service-alerts','checkout-app','checkout-test','fingerprint','High errors','critical','firing',$1)`, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceViewsCompleteHTTPAndBoundaries(t *testing.T) {
	st, _ := serviceTestDB(t)
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	seedServiceViews(t, st, now)
	policy := &rbac.V3Policy{Store: st}
	reader := &completeReader{now: now}
	covered, tokenPath := configuredAlertCoverage(t, st)
	svc := New(st, policy, reader, covered)
	svc.Now = func() time.Time { return now }
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := get("/services")
	if w.Code != 200 {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var list catalog
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.KPIs[0].Value != "2" || list.KPIs[1].Value != "1" || list.KPIs[2].Value != "1" ||
		len(list.Facets) != 4 || list.Items[0].Release.Version == "" || list.Items[0].Connectors[0] != "prometheus" {
		t.Fatalf("incorrect source projection: %+v", list)
	}
	if w = get("/services?tier=T1&status=ok"); w.Code != 200 {
		t.Fatalf("filter status=%d body=%s", w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Name != "Payments" || list.KPIs[0].Value != "1" {
		t.Fatalf("incorrect filters: %+v", list)
	}
	if w = get("/services?team=Operations&q=check"); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Checkout"`) {
		t.Fatalf("team/q filter status=%d body=%s", w.Code, w.Body.String())
	}
	w = get("/services/" + encodeID("checkout-app", "test"))
	if w.Code != 200 {
		t.Fatalf("detail status=%d body=%s", w.Code, w.Body.String())
	}
	var d detail
	if err = json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.AI.Investigation != "INV-checkout" || !strings.Contains(d.AI.Text, "Observed elevated") ||
		len(d.Deps.Downstream) != 1 || d.Deps.Downstream[0].Name != "Payments" || d.Deps.Downstream[0].Status != "ok" ||
		len(d.Changes) != 1 || d.Tabs["deps"] != 1 || d.Tabs["incidents"] != 1 || len(d.Signals) != 5 {
		t.Fatalf("incorrect detail relationships: %+v", d)
	}
	if _, err = st.DB.Exec(t.Context(), `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
 VALUES('other-checkout','other-source','checkout-app','checkout-test','other-checkout','Duplicate','critical','firing',$1),
 ('other-payments','other-source','payments-app','payments-test','other-payments','Foreign critical','critical','firing',$1)`, now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if w = get("/services"); w.Code != 200 {
		t.Fatalf("cross-source catalog status=%d body=%s", w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].Alerts != 1 || list.Items[1].Alerts != 0 || list.Items[1].Status != "ok" || list.KPIs[1].Value != "1" {
		t.Fatalf("unbound alert source changed count or status: %+v", list)
	}
	svc.MetricReader = &tokenRevokingReader{completeReader: reader, tokenPath: tokenPath}
	if w = get("/services"); w.Code != 503 {
		t.Fatalf("source revoked after metric read exposed catalog: status=%d body=%s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(tokenPath, []byte("fixture-alertmanager-token-at-least-24"), 0600); err != nil {
		t.Fatal(err)
	}
	svc.MetricReader = reader
	svc.AlertCovered = nil
	if w = get("/services"); w.Code != 503 {
		t.Fatalf("missing coverage binding exposed catalog: status=%d body=%s", w.Code, w.Body.String())
	}
	svc.AlertCovered = covered
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if w = get("/services"); w.Code != 503 {
		t.Fatalf("withdrawn alert source exposed catalog: status=%d body=%s", w.Code, w.Body.String())
	}
	if w = get("/services/" + encodeID("checkout-app", "test")); w.Code != 503 {
		t.Fatalf("withdrawn alert source exposed detail: status=%d body=%s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(tokenPath, []byte("fixture-alertmanager-token-at-least-24"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(t.Context(), `INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind)
 VALUES('checkout-payments-second','res-checkout','res-payments','depends_on','declared','manual-second')`); err != nil {
		t.Fatal(err)
	}
	if w = get("/services/" + encodeID("checkout-app", "test")); w.Code != 200 || strings.Count(w.Body.String(), `"CMDB depends_on"`) != 1 {
		t.Fatalf("duplicate resource edge inflated service dependency: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = st.DB.Exec(t.Context(), `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id)
 VALUES('res-payments','checkout-app','checkout-test')`); err != nil {
		t.Fatal(err)
	}
	if w = get("/services/" + encodeID("checkout-app", "test")); w.Code != 503 {
		t.Fatalf("shared resource was attributed to one service: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = st.DB.Exec(t.Context(), `DELETE FROM cmdb_resource_applications WHERE resource_id='res-payments' AND application_id='checkout-app'`); err != nil {
		t.Fatal(err)
	}
	// A new current context revision may retain an older assistant message.
	// Its own run and timestamp, rather than the revision timestamp, govern freshness.
	if _, err = st.DB.Exec(t.Context(), `UPDATE context_revisions
 SET messages=jsonb_set(messages,'{0,created_at}',to_jsonb($1::text))
 WHERE branch_id='branch-checkout'`, now.Add(-2*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if w = get("/services/" + encodeID("checkout-app", "test")); w.Code != 503 {
		t.Fatalf("stale AI text was shown as current: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = st.DB.Exec(t.Context(), `UPDATE context_revisions
 SET messages=jsonb_set(jsonb_set(messages,'{0,created_at}',to_jsonb($1::text)),
 '{0,run_id}',to_jsonb('older-run'::text)) WHERE branch_id='branch-checkout'`,
		now.Add(-5*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if w = get("/services/" + encodeID("checkout-app", "test")); w.Code != 503 {
		t.Fatalf("reply from another run was shown as current: status=%d body=%s", w.Code, w.Body.String())
	}
	if w = get("/services/invalid*"); w.Code != 404 {
		t.Fatalf("invalid identity status=%d", w.Code)
	}
	if w = get("/services?status=unknown"); w.Code != 422 {
		t.Fatalf("invalid status status=%d", w.Code)
	}
	if _, err = st.DB.Exec(t.Context(), `DELETE FROM deployment_events WHERE execution_id='exec-checkout'`); err != nil {
		t.Fatal(err)
	}
	if w = get("/services"); w.Code != 503 || !strings.Contains(w.Body.String(), "service_view_incomplete") {
		t.Fatalf("missing release was hidden: status=%d body=%s", w.Code, w.Body.String())
	}
}

func configuredAlertCoverage(t *testing.T, st *store.Store) (AlertCovered, string) {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "source-token")
	if err := os.WriteFile(tokenPath, []byte("fixture-alertmanager-token-at-least-24"), 0600); err != nil {
		t.Fatal(err)
	}
	source := operations.Source{ID: "service-alerts", TokenFile: tokenPath, Mappings: []operations.Mapping{
		{Labels: map[string]string{"application": "checkout"}, ApplicationID: "checkout-app", EnvironmentID: "checkout-test"},
		{Labels: map[string]string{"application": "payments"}, ApplicationID: "payments-app", EnvironmentID: "payments-test"},
	}}
	config := AlertCoverageConfig{Bindings: []AlertCoverageBinding{
		{SourceID: source.ID, ApplicationID: "checkout-app", EnvironmentID: "test", Labels: source.Mappings[0].Labels, Complete: true},
		{SourceID: source.ID, ApplicationID: "payments-app", EnvironmentID: "test", Labels: source.Mappings[1].Labels, Complete: true},
	}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "coverage.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	instance := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}
	covered, err := LoadAlertCoverage(path, operations.Config{Sources: []operations.Source{source}}, instance, st.DB)
	if err != nil {
		t.Fatal(err)
	}
	if covered(t.Context(), "checkout-app", "test") != "service-alerts" || covered(t.Context(), "payments-app", "test") != "service-alerts" || covered(t.Context(), "checkout-app", "prod") != "" || covered(t.Context(), "hidden", "test") != "" {
		t.Fatal("coverage accepted an unbound app/environment or rejected an exact binding")
	}
	return covered, tokenPath
}

func TestAlertCoverageRequiresExactLiveBinding(t *testing.T) {
	st, _ := serviceTestDB(t)
	seedServiceViews(t, st, time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC))
	covered, tokenPath := configuredAlertCoverage(t, st)
	if absent, err := LoadAlertCoverage("", operations.Config{}, httpapi.V3InstanceConfig{}, st.DB); err != nil || absent != nil {
		t.Fatalf("missing declaration must fail closed: coverage=%v err=%v", absent != nil, err)
	}
	if _, err := st.DB.Exec(t.Context(), `UPDATE application_environments SET archived=true WHERE id='checkout-test'`); err != nil {
		t.Fatal(err)
	}
	if covered(t.Context(), "checkout-app", "test") != "" || covered(t.Context(), "payments-app", "test") != "service-alerts" {
		t.Fatal("coverage survived removal of exact V3 application/environment binding")
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if covered(t.Context(), "payments-app", "test") != "" {
		t.Fatal("coverage survived removal of ingress authentication")
	}
}

func TestAlertCoverageRejectsAmbiguousOrUnassertedSource(t *testing.T) {
	st, _ := serviceTestDB(t)
	seedServiceViews(t, st, time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC))
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("fixture-alertmanager-token-at-least-24"), 0600); err != nil {
		t.Fatal(err)
	}
	source := operations.Source{ID: "alerts", TokenFile: tokenPath, Mappings: []operations.Mapping{
		{Labels: map[string]string{"application": "checkout"}, ApplicationID: "checkout-app", EnvironmentID: "checkout-test"},
		{Labels: map[string]string{"application": "checkout", "team": "ops"}, ApplicationID: "payments-app", EnvironmentID: "payments-test"},
	}}
	binding := AlertCoverageBinding{SourceID: "alerts", ApplicationID: "checkout-app", EnvironmentID: "test", Labels: source.Mappings[0].Labels, Complete: true}
	path := filepath.Join(dir, "coverage.json")
	load := func() error {
		raw, err := json.Marshal(AlertCoverageConfig{Bindings: []AlertCoverageBinding{binding}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		_, err = LoadAlertCoverage(path, operations.Config{Sources: []operations.Source{source}},
			httpapi.V3InstanceConfig{Tier: "nonprod", EnvironmentIDs: []string{"test"}}, st.DB)
		return err
	}
	if err := load(); err == nil {
		t.Fatal("overlapping selector was accepted as complete coverage")
	}
	source.Mappings = source.Mappings[:1]
	binding.Complete = false
	if err := load(); err == nil {
		t.Fatal("unasserted selector was accepted as complete coverage")
	}
	binding.Complete = true
	binding.Labels = map[string]string{"application": "other"}
	if err := load(); err == nil {
		t.Fatal("selector absent from configured source was accepted")
	}
}

func TestAlertRangeCoverageRequiresAttestedCompleteInterval(t *testing.T) {
	st, _ := serviceTestDB(t)
	seedServiceViews(t, st, time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC))
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("fixture-alertmanager-token-at-least-24"), 0600); err != nil {
		t.Fatal(err)
	}
	source := operations.Source{ID: "range-alerts", TokenFile: tokenPath, Mappings: []operations.Mapping{
		{Labels: map[string]string{"application": "checkout"}, ApplicationID: "checkout-app", EnvironmentID: "checkout-test"},
	}}
	instance := httpapi.V3InstanceConfig{Tier: "nonprod", EnvironmentIDs: []string{"test"}}
	path := filepath.Join(dir, "coverage.json")
	binding := AlertCoverageBinding{SourceID: source.ID, ApplicationID: "checkout-app", EnvironmentID: "test",
		Labels: source.Mappings[0].Labels, Complete: true}
	load := func() AlertRangeCovered {
		t.Helper()
		raw, err := json.Marshal(AlertCoverageConfig{Bindings: []AlertCoverageBinding{binding}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		covered, err := LoadAlertRangeCoverage(path, operations.Config{Sources: []operations.Source{source}}, instance, st.DB)
		if err != nil {
			t.Fatal(err)
		}
		return covered
	}
	if absent, err := LoadAlertRangeCoverage("", operations.Config{}, instance, st.DB); err != nil || absent != nil {
		t.Fatalf("absent historical coverage must be unavailable: callback=%v err=%v", absent != nil, err)
	}
	now := time.Now().UTC()
	start, end := now.Add(-24*time.Hour), now.Add(-time.Second)
	if got := load()(t.Context(), "checkout-app", "test", start, end); got != "" {
		t.Fatalf("missing complete_from claimed history from %q", got)
	}
	completeFrom := now.Add(-48 * time.Hour)
	binding.CompleteFrom = &completeFrom
	covered := load()
	if got := covered(t.Context(), "checkout-app", "test", start, end); got != source.ID {
		t.Fatalf("attested interval not covered by exact source: %q", got)
	}
	for _, span := range []struct{ start, end time.Time }{
		{now.Add(-7 * 24 * time.Hour), end},
		{now.Add(-time.Hour), now.Add(time.Hour)},
		{end, start},
	} {
		if got := covered(t.Context(), "checkout-app", "test", span.start, span.end); got != "" {
			t.Fatalf("uncovered interval claimed by %q", got)
		}
	}
	completeFrom = now.Add(-8 * 24 * time.Hour)
	covered = load()
	if got := covered(t.Context(), "checkout-app", "test", now.Add(-7*24*time.Hour), end); got != source.ID {
		t.Fatalf("attested seven-day interval not covered by exact source: %q", got)
	}
	if got := covered(t.Context(), "other-app", "test", start, end); got != "" {
		t.Fatalf("other application claimed source %q", got)
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if got := covered(t.Context(), "checkout-app", "test", start, end); got != "" {
		t.Fatalf("revoked token retained historical coverage %q", got)
	}
}

type reassignReader struct {
	completeReader
	db      *pgxpool.Pool
	changed bool
	err     error
}

type sourceMutationReader struct {
	completeReader
	db      *pgxpool.Pool
	query   string
	changed bool
	err     error
}

func (r *sourceMutationReader) QueryService(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User,
	service, env, query string, window connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error) {
	if !r.changed {
		r.changed = true
		_, r.err = r.db.Exec(ctx, r.query)
		if r.err != nil {
			return v3integrations.PrometheusMetricEvidence{}, r.err
		}
	}
	return r.completeReader.QueryService(ctx, scope, user, service, env, query, window)
}

func (r *reassignReader) QueryService(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User,
	service, env, query string, window connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error) {
	if !r.changed {
		r.changed = true
		_, r.err = r.db.Exec(ctx, `UPDATE applications SET owner_team_id='payments' WHERE id='checkout-app'`)
		if r.err != nil {
			return v3integrations.PrometheusMetricEvidence{}, r.err
		}
	}
	return r.completeReader.QueryService(ctx, scope, user, service, env, query, window)
}

func TestServiceViewsEventGrantAndOwnershipRecheck(t *testing.T) {
	st, _ := serviceTestDB(t)
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	seedServiceViews(t, st, now)
	ctx := t.Context()
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES('service-team-reader','Service Team Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('service-team-reader','environment','view','environment','test'),
 ('service-team-reader','services','view','team','ops'),
 ('service-team-reader','connectors','view','team','ops')`,
	} {
		if _, err := st.DB.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	user, err := st.CreateUser(ctx, core.User{Email: "team@example.invalid", Name: "Team Reader", Role: "viewer"}, "reader-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'service-team-reader')`, user.ID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	reader := &completeReader{now: now}
	svc := New(st, policy, reader, func(_ context.Context, _, env string) string {
		if env == "test" {
			return "service-alerts"
		}
		return ""
	})
	svc.Now = func() time.Time { return now }
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "team@example.invalid", "reader-password")
	if err != nil {
		t.Fatal(err)
	}
	get := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/services?q=checkout", nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := get(); w.Code != 503 || len(reader.calls) != 0 || strings.Contains(w.Body.String(), "alert-checkout") {
		t.Fatalf("events-only data leaked: status=%d body=%s reads=%d", w.Code, w.Body.String(), len(reader.calls))
	}
	if _, err = st.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('service-team-reader','events','view','team','ops')`); err != nil {
		t.Fatal(err)
	}
	if w := get(); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Checkout"`) || strings.Contains(w.Body.String(), `"name":"Payments"`) {
		t.Fatalf("team-scoped catalog status=%d body=%s", w.Code, w.Body.String())
	}
	hooked := &reassignReader{completeReader: completeReader{now: now}, db: st.DB}
	svc.MetricReader = hooked
	if w := get(); w.Code != 403 || !hooked.changed || hooked.err != nil || strings.Contains(w.Body.String(), `"name":"Checkout"`) {
		t.Fatalf("ownership changed during source read: status=%d body=%s changeErr=%v", w.Code, w.Body.String(), hooked.err)
	}
	if _, err = st.DB.Exec(ctx, `UPDATE applications SET owner_team_id='ops' WHERE id='checkout-app'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, mutate, restore string }{
		{"target", `UPDATE v3_connector_targets SET external_id='other-metrics' WHERE id='metric-checkout'`,
			`UPDATE v3_connector_targets SET external_id='checkout-metrics' WHERE id='metric-checkout'`},
		{"endpoint", `UPDATE v3_connectors SET endpoint='https://other.example.invalid' WHERE id='prom'`,
			`UPDATE v3_connectors SET endpoint='https://prom.example.invalid' WHERE id='prom'`},
		{"tool", `UPDATE v3_connectors SET tools='[{"name":"query_metrics","enabled":false}]' WHERE id='prom'`,
			`UPDATE v3_connectors SET tools='[{"name":"query_metrics","enabled":true}]' WHERE id='prom'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutating := &sourceMutationReader{completeReader: completeReader{now: now}, db: st.DB, query: tc.mutate}
			svc.MetricReader = mutating
			if w := get(); w.Code != 503 || !mutating.changed || mutating.err != nil {
				t.Fatalf("metric source changed during read: status=%d body=%s changeErr=%v", w.Code, w.Body.String(), mutating.err)
			}
			if _, err := st.DB.Exec(ctx, tc.restore); err != nil {
				t.Fatal(err)
			}
		})
	}
}
