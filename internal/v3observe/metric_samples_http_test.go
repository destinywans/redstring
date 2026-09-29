package v3observe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// This exercises the registered HTTP route against an isolated persisted scope
// and a real Prometheus protocol adapter, without contacting a shared cluster.
func TestMetricSamplesRegisteredRouteAndSourceRecheck(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_metric_samples_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-metric-samples-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-metric-samples-password")
	if err != nil {
		t.Fatal(err)
	}

	const registeredRate = `sum(rate(http_requests_total{application="registered-app"}[5m]))`
	const registeredP95 = `histogram_quantile(0.95, sum(rate(http_request_duration_seconds_bucket{application="registered-app"}[5m])) by (le))`
	var calls atomic.Int32
	var mode atomic.Int32 // 0 sample, 1 empty, 2 warning, 3 nonfinite, 4 truncated, 5 multiple series
	var gateMu sync.Mutex
	var entered, release chan struct{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/query_range" {
			t.Errorf("unexpected Prometheus request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		if q.Get("query") != registeredRate && q.Get("query") != registeredP95 {
			t.Errorf("unregistered PromQL reached provider: %q", q.Get("query"))
		}
		start, startErr := time.Parse(time.RFC3339Nano, q.Get("start"))
		end, endErr := time.Parse(time.RFC3339Nano, q.Get("end"))
		if startErr != nil || endErr != nil || end.Sub(start) != 15*time.Minute || q.Get("step") != "30" || q.Get("limit") != "100" {
			t.Errorf("unbounded or changed metric window: %s", r.URL.RawQuery)
		}
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
		if mode.Load() == 1 {
			fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
			return
		}
		value := "1.25"
		if mode.Load() == 3 {
			value = "+Inf"
		}
		series := []any{map[string]any{"metric": map[string]string{"application": "registered-app", "secret": "do-not-expose"}, "values": []any{[]any{end.Add(-30 * time.Second).Unix(), value}}}}
		if mode.Load() == 4 {
			for len(series) < 100 {
				series = append(series, series[0])
			}
		}
		if mode.Load() == 5 {
			series = append(series, series[0])
		}
		response := map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": series}}
		if mode.Load() == 2 {
			response["warnings"] = []string{"partial backend"}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()
	telemetry, err := connectors.NewTelemetry(connectors.TelemetryConfig{
		Prometheus: connectors.TelemetryEndpoint{URL: upstream.URL, AllowHTTP: true},
		Applications: map[string]connectors.ApplicationTelemetry{"registered-app": {PrometheusQueries: map[string]string{
			"request_rate": registeredRate, "http_p95_seconds": registeredP95,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, telemetry).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
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
	request := func(query string) *httptest.ResponseRecorder { return requestIn(query, "dev") }
	checkError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code {
			t.Fatalf("status=%d body=%s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	valid := "?service=app&query_id=request_rate"
	for _, query := range []string{"", "?service=app", "?service=app&query_id=up", valid + "&extra=1", valid + "&service=other"} {
		checkError(request(query), 422, "invalid_metric_request")
	}
	checkError(request(valid), 503, "not_configured")
	if calls.Load() != 0 {
		t.Fatal("provider queried before valid binding")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"prometheus","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	checkError(requestIn(valid, "all"), 422, "invalid_environment")
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"prometheus","resource":"registered-app"},{"kind":"prometheus","resource":"other-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	checkError(request(valid), 409, "ambiguous_binding")
	if calls.Load() != 0 {
		t.Fatal("ambiguous binding queried provider")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"prometheus","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	checkError(request(valid), 403, "forbidden")
	if calls.Load() != 0 {
		t.Fatal("provider queried after services.view was revoked")
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','services','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	w := request(valid)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("sample response status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	var sampleBody struct {
		ApplicationID string                `json:"application_id"`
		EnvironmentID string                `json:"environment_id"`
		QueryID       string                `json:"query_id"`
		Source        string                `json:"source"`
		Window        connectors.TimeWindow `json:"window"`
		CapturedAt    time.Time             `json:"captured_at"`
		Status        string                `json:"status"`
		Samples       []struct {
			Timestamp float64 `json:"timestamp"`
			Value     string  `json:"value"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &sampleBody); err != nil {
		t.Fatal(err)
	}
	if sampleBody.ApplicationID != "app" || sampleBody.EnvironmentID != "dev" || sampleBody.QueryID != "request_rate" || sampleBody.Source != "prometheus" || sampleBody.Status != "observed" || sampleBody.CapturedAt.IsZero() || sampleBody.Window.End.Sub(sampleBody.Window.Start) != 15*time.Minute || sampleBody.Window.StepSeconds != 30 || len(sampleBody.Samples) != 1 || sampleBody.Samples[0].Value != "1.25" {
		t.Fatalf("wrong observed samples: %s", w.Body.String())
	}
	for _, secret := range []string{"registered-app", "http_requests_total", "do-not-expose", upstream.URL, "labels", "query\""} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("provider detail %q leaked: %s", secret, w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one registered provider query, got %d", calls.Load())
	}
	mode.Store(1)
	w = request("?service=app&query_id=http_p95_seconds")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"no_samples"`) || !strings.Contains(w.Body.String(), `"samples":[]`) {
		t.Fatalf("empty provider response was not explicit: %d %s", w.Code, w.Body.String())
	}
	for _, failureMode := range []int32{2, 3, 4, 5} {
		mode.Store(failureMode)
		checkError(request(valid), 503, "metric_result_incomplete")
	}
	mode.Store(0)

	// Hold the upstream response while revoking the grant. The handler must
	// recheck permission after its provider call and discard observed samples.
	makeGate := func() (chan struct{}, chan struct{}) {
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
	begin, done := makeGate()
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- request(valid) }()
	select {
	case <-begin:
	case <-time.After(5 * time.Second):
		t.Fatal("metric provider call did not begin")
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	close(done)
	w = <-response
	clearGate()
	checkError(w, 403, "forbidden")
	if strings.Contains(w.Body.String(), "1.25") {
		t.Fatalf("revoked response leaked samples: %s", w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','services','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}

	begin, done = makeGate()
	go func() { response <- request(valid) }()
	select {
	case <-begin:
	case <-time.After(5 * time.Second):
		t.Fatal("second metric provider call did not begin")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"prometheus","resource":"changed-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	close(done)
	w = <-response
	clearGate()
	checkError(w, 409, "metric_source_changed")
	if strings.Contains(w.Body.String(), "1.25") {
		t.Fatalf("changed-source response leaked samples: %s", w.Body.String())
	}
	// The changed resource is absent from server-side registration. A later
	// request must not reuse the old metric result or accept arbitrary query text.
	before := calls.Load()
	checkError(request(valid), 503, "metric_query_unavailable")
	if calls.Load() != before {
		t.Fatal("unregistered changed binding reached provider")
	}
}
