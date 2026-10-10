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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// The registered route must use the explicit V3 target and reviewed catalog,
// not a historical application_environments.bindings telemetry alias.
func TestMetricSamplesV3RegisteredConnectorRoute(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_metric_samples_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
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
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-metric-samples-password"); err != nil {
		t.Fatal(err)
	}
	user, session, err := st.Login(ctx, "admin@example.invalid", "isolated-metric-samples-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
INSERT INTO application_environments(id,application_id,key,name,bindings)
 VALUES('app-dev','app','dev','Dev','[{"kind":"prometheus","resource":"legacy-only"}]');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}

	const expression = `sum(rate(http_requests_total{application="app",environment="dev"}[5m]))`
	var calls atomic.Int32
	var mode atomic.Int32 // 0 observed, 1 empty, 2 warning, 3 unavailable, 4 multi-series, 5 >32 samples
	var gateMu sync.Mutex
	var entered, release chan struct{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" || r.FormValue("query") != expression {
			t.Errorf("unexpected registered query: %s", r.URL)
			http.Error(w, "invalid query", 400)
			return
		}
		calls.Add(1)
		gateMu.Lock()
		waitEntered, waitRelease := entered, release
		gateMu.Unlock()
		if waitEntered != nil {
			close(waitEntered)
			select {
			case <-waitRelease:
			case <-r.Context().Done():
				return
			}
		}
		if mode.Load() == 3 {
			http.Error(w, "private upstream failure", 503)
			return
		}
		if mode.Load() == 1 {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
			return
		}
		end, parseErr := time.Parse(time.RFC3339Nano, r.FormValue("end"))
		if parseErr != nil {
			t.Errorf("invalid query window: %v", parseErr)
			return
		}
		values := []any{[]any{end.Unix(), "1.25"}}
		if mode.Load() == 5 {
			values = values[:0]
			for i := 32; i >= 0; i-- {
				values = append(values, []any{end.Add(-time.Duration(i) * 15 * time.Second).Unix(), "1.25"})
			}
		}
		series := []any{map[string]any{"metric": map[string]string{"private_label": "do-not-expose"}, "values": values}}
		if mode.Load() == 4 {
			series = append(series, series[0])
		}
		response := map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": series}}
		if mode.Load() == 2 {
			response["warnings"] = []string{"partial upstream"}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors
 (id,environment_id,provider,name,category,endpoint,auth_method,tools,enabled)
 VALUES('metric-connector','dev','prometheus','Metrics','observe',$1,'none',
 '[{"name":"query_metrics","enabled":true}]',true)`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "reviewed-queries.json")
	config := fmt.Sprintf(`{"queries":[{"connector_id":"metric-connector","application_id":"app","environment_id":"dev","resource_id":"app-metrics","query_id":"request_rate","expression":%q}]}`, expression)
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
	service := New(st, policy)
	service.MetricReader = reader
	service.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	requestIn := func(query, environment string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/samples"+query, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	request := func(query string) *httptest.ResponseRecorder {
		return requestIn(query, "dev")
	}
	checkError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != status || body.Error.Code != code {
			t.Fatalf("status=%d body=%s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	valid := "?service=app&query_id=request_rate"
	checkError(request(valid+"&unexpected=1"), 422, "invalid_metric_request")
	checkError(requestIn(valid, "all"), 422, "invalid_environment")
	checkError(request(valid), 503, "not_configured") // Legacy binding cannot enable V3 read.
	if calls.Load() != 0 {
		t.Fatal("provider queried before explicit V3 target")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('metric-target','app','dev','metric-connector','metric_resource','app-metrics',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	noCatalog, err := v3integrations.NewPrometheusReader(db, "nonprod", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.MetricReader = noCatalog
	checkError(request(valid), 503, "metric_query_unavailable")
	if calls.Load() != 0 {
		t.Fatal("unreviewed target queried provider")
	}
	service.MetricReader = reader
	checkError(request("?service=app&query_id=arbitrary"), 422, "invalid_metric_request")
	w := request(valid)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(w.Body.String(), `"status":"observed"`) || !strings.Contains(w.Body.String(), `"value":"1.25"`) {
		t.Fatalf("observed sample response: %d %s", w.Code, w.Body.String())
	}
	for _, private := range []string{expression, upstream.URL, "legacy-only", "private_label", "do-not-expose", "app-metrics"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("private source detail leaked: %q %s", private, w.Body.String())
		}
	}
	mode.Store(1)
	w = request(valid)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"no_samples"`) || !strings.Contains(w.Body.String(), `"samples":[]`) {
		t.Fatalf("empty sample response: %d %s", w.Code, w.Body.String())
	}
	mode.Store(2)
	checkError(request(valid), 503, "metric_query_unavailable")
	mode.Store(3)
	checkError(request(valid), 503, "metric_query_unavailable")
	mode.Store(4)
	checkError(request(valid), 503, "metric_result_incomplete")
	mode.Store(5)
	checkError(request(valid), 503, "metric_result_incomplete")
	mode.Store(0)
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('metric-target-two','app','dev','metric-connector','metric_resource','other-metrics',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	checkError(request(valid), 409, "ambiguous_binding")
	if calls.Load() != before {
		t.Fatal("ambiguous binding queried provider")
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='metric-target-two'`); err != nil {
		t.Fatal(err)
	}
	gate := func() (chan struct{}, chan struct{}) {
		gateMu.Lock()
		defer gateMu.Unlock()
		entered, release = make(chan struct{}), make(chan struct{})
		return entered, release
	}
	clearGate := func() {
		gateMu.Lock()
		entered, release = nil, nil
		gateMu.Unlock()
	}
	begin, done := gate()
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- request(valid) }()
	select {
	case <-begin:
	case <-time.After(5 * time.Second):
		t.Fatal("provider query did not start")
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	close(done)
	w = <-responses
	clearGate()
	checkError(w, 403, "forbidden")
	// A fresh request must not distinguish a hidden registered service from
	// a nonexistent service by status or error code.
	hidden := request(valid)
	missing := request("?service=missing-service&query_id=request_rate")
	if hidden.Code != missing.Code || hidden.Body.String() != missing.Body.String() {
		t.Fatalf("hidden service is distinguishable: hidden=%d %s missing=%d %s",
			hidden.Code, hidden.Body.String(), missing.Code, missing.Body.String())
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','services','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	begin, done = gate()
	go func() { responses <- request(valid) }()
	select {
	case <-begin:
	case <-time.After(5 * time.Second):
		t.Fatal("provider query did not start")
	}
	if _, err = db.Exec(ctx, `UPDATE v3_connector_targets SET external_id='changed-metrics' WHERE id='metric-target'`); err != nil {
		t.Fatal(err)
	}
	close(done)
	w = <-responses
	clearGate()
	checkError(w, 409, "metric_source_changed")
	before = calls.Load()
	checkError(request(valid), 503, "metric_query_unavailable")
	if calls.Load() != before {
		t.Fatal("unreviewed changed target queried provider")
	}
}
