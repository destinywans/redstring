package v3observe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
)

func TestLogsRegisteredRouteUsesScopedElasticsearchAndRealCounts(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_logs_test_" + store.ID()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-logs-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-logs-password")
	if err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	var completePatterns atomic.Bool
	var blockBindingChange, blockRevocation atomic.Bool
	bindingQueryStarted, revokedQueryStarted := make(chan struct{}), make(chan struct{})
	releaseBindingQuery, releaseRevokedQuery := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		if blockBindingChange.CompareAndSwap(true, false) {
			close(bindingQueryStarted)
			<-releaseBindingQuery
		}
		if blockRevocation.CompareAndSwap(true, false) {
			close(revokedQueryStarted)
			<-releaseRevokedQuery
		}
		if r.Method != http.MethodPost || r.URL.Path != "/app-logs/_search" {
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", 404)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(request["query"])
		if !strings.Contains(string(raw), `"cluster.keyword":"lab"`) ||
			!strings.Contains(string(raw), `"aiops_level":"info"`) ||
			!strings.Contains(string(raw), `"match_phrase":{"message":"checkout"}`) {
			t.Errorf("provider query escaped the registered scope or filters: %s", raw)
		}
		if request["track_total_hits"] != true || request["size"] != float64(100) {
			t.Errorf("incomplete or unbounded provider query: %+v", request)
		}
		aggregations, ok := request["aggs"].(map[string]any)
		if !ok || aggregations["patterns"] == nil || aggregations["missing_patterns"] == nil {
			t.Errorf("missing full-population pattern aggregation: %+v", request["aggs"])
		}
		if patterns, ok := aggregations["patterns"].(map[string]any); ok {
			terms, _ := patterns["terms"].(map[string]any)
			if terms["field"] != "message_template.keyword" {
				t.Errorf("missing registered template field: %+v", patterns)
			}
		}
		filters := request["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
		startText := filters[0].(map[string]any)["range"].(map[string]any)["@timestamp"].(map[string]any)["gte"].(string)
		start, err := time.Parse(time.RFC3339Nano, startText)
		if err != nil {
			t.Error(err)
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		at := start.Add(29 * time.Minute)
		volume := make([]any, 30)
		trend := make([]any, 30)
		for i := range volume {
			count := 0
			if i == 29 {
				count = 1
			}
			bucket := map[string]any{"key": start.Add(time.Duration(i) * time.Minute).UnixMilli(), "doc_count": count}
			volume[i], trend[i] = bucket, bucket
		}
		response := map[string]any{
			"timed_out": false,
			"_shards":   map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits": map[string]any{
				"total": map[string]any{"value": 1, "relation": "eq"},
				"hits": []any{map[string]any{"_source": map[string]any{"@timestamp": at.Format(time.RFC3339Nano),
					"message": "checkout accepted", "cluster": "lab", "level": "info"}}},
			},
			"aggregations": map[string]any{"volume": map[string]any{"buckets": volume},
				"errors": map[string]any{"doc_count": 0}, "missing_patterns": map[string]any{"doc_count": 0}},
		}
		if completePatterns.Load() {
			response["aggregations"].(map[string]any)["patterns"] = map[string]any{"sum_other_doc_count": 0, "doc_count_error_upper_bound": 0, "buckets": []any{
				map[string]any{"key": "checkout accepted", "doc_count": 1, "doc_count_error_upper_bound": 0, "trend": map[string]any{"buckets": trend}},
			}}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()
	telemetry, err := connectors.NewTelemetry(connectors.TelemetryConfig{
		Elasticsearch: connectors.TelemetryEndpoint{URL: upstream.URL, AllowHTTP: true},
		Applications: map[string]connectors.ApplicationTelemetry{"registered-app": {
			ElasticsearchIndex: "app-logs", ElasticsearchPatternField: "message_template.keyword", ElasticsearchFilters: map[string]string{"cluster": "lab"},
		}},
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
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/logs"+query, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
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
	checkError(request("?service=app&levels=DEBUG"), 422, "invalid_levels")
	checkError(request("?service=app&levels=INFO&q=checkout"), 503, "not_configured")
	if providerCalls.Load() != 0 {
		t.Fatal("provider queried without persisted binding")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"elasticsearch","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	w := request("?service=app&levels=INFO&q=checkout")
	checkError(w, 503, "log_patterns_unavailable")
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	var body struct {
		Available struct {
			Total     string    `json:"total"`
			Errors    string    `json:"errors"`
			Histogram [][]int64 `json:"histogram"`
			Items     []logLine `json:"items_sample"`
		} `json:"available"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Available.Total != "1" || body.Available.Errors != "0" || len(body.Available.Items) != 1 || body.Available.Items[0].Level != "INFO" ||
		body.Available.Items[0].Service != "app" || body.Available.Items[0].Message != "checkout accepted" || len(body.Available.Histogram) != 30 ||
		len(body.Available.Histogram[29]) != 3 || body.Available.Histogram[29][2] != 1 || len(body.Missing) == 0 {
		t.Fatalf("unexpected scoped log result: %+v", body)
	}
	completePatterns.Store(true)
	w = request("?service=app&levels=INFO&q=checkout")
	var complete struct {
		Total     string    `json:"total"`
		Errors    string    `json:"errors"`
		Histogram [][]int64 `json:"histogram"`
		Items     []logLine `json:"items"`
		Patterns  []struct {
			Template string  `json:"template"`
			Level    string  `json:"level"`
			Count    string  `json:"count"`
			Trend    []int64 `json:"trend"`
		} `json:"patterns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &complete); err != nil || w.Code != 200 ||
		complete.Total != "1" || complete.Errors != "0" || len(complete.Histogram) != 30 ||
		len(complete.Items) != 1 || len(complete.Patterns) != 1 ||
		complete.Patterns[0].Template != "checkout accepted" || complete.Patterns[0].Level != "INFO" ||
		complete.Patterns[0].Count != "1" || len(complete.Patterns[0].Trend) != 30 || complete.Patterns[0].Trend[29] != 1 {
		t.Fatalf("incomplete scoped log pattern response: status=%d body=%s", w.Code, w.Body.String())
	}
	blockBindingChange.Store(true)
	changedResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { changedResult <- request("?service=app&levels=INFO&q=checkout") }()
	select {
	case <-bindingQueryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Elasticsearch query did not start")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"elasticsearch","resource":"replacement-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	close(releaseBindingQuery)
	checkError(<-changedResult, 409, "log_scope_changed")
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"elasticsearch","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	before := providerCalls.Load()
	w = request("?service=app&levels=")
	if w.Code != 200 || providerCalls.Load() != before || !strings.Contains(w.Body.String(), `"total":"0"`) {
		t.Fatalf("empty explicit level selection queried provider: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"elasticsearch","resource":"registered-app"},{"kind":"loki","resource":"same-app-different-source"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	checkError(request("?service=app&levels=INFO"), 409, "ambiguous_binding")
	if providerCalls.Load() != before {
		t.Fatal("ambiguous log providers must not be queried")
	}
	w = request("?service=app&levels=INFO&q=checkout&provider=elasticsearch")
	if w.Code != 200 || providerCalls.Load() != before+1 {
		t.Fatalf("explicit persisted Elasticsearch source failed: status=%d calls=%d body=%s", w.Code, providerCalls.Load(), w.Body.String())
	}
	before = providerCalls.Load()
	checkError(request("?service=app&levels=INFO&provider=loki"), 503, "not_configured")
	checkError(request("?service=app&levels=INFO&provider=unknown"), 422, "invalid_provider")
	checkError(request("?service=app&levels=INFO&provider=loki&provider=elasticsearch"), 422, "invalid_provider")
	if providerCalls.Load() != before {
		t.Fatal("unconfigured or invalid source queried Elasticsearch")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"elasticsearch","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	blockRevocation.Store(true)
	revokedResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { revokedResult <- request("?service=app&levels=INFO&q=checkout") }()
	select {
	case <-revokedQueryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Elasticsearch query did not start before revocation")
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='logs' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	close(releaseRevokedQuery)
	checkError(<-revokedResult, 403, "forbidden")
	before = providerCalls.Load()
	checkError(request("?service=app&levels=INFO&q=checkout"), 403, "forbidden")
	if providerCalls.Load() != before {
		t.Fatal("provider queried after logs grant was revoked")
	}
}
