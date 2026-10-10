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
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// The log page reads the registry targets of the service (PA-19 P3 stage 2
// step 3; the only path since P4) and proves counts and patterns only when
// the source does.
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
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
	var lastWindow atomic.Int64
	var lokiCalls atomic.Int32
	var completePatterns atomic.Bool
	var zeroMatches atomic.Bool
	var blockBindingChange, blockRevocation atomic.Bool
	bindingQueryStarted, revokedQueryStarted := make(chan struct{}), make(chan struct{})
	releaseBindingQuery, releaseRevokedQuery := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/app-logs/_mapping/field/cluster,cluster.keyword" {
			_ = json.NewEncoder(w).Encode(map[string]any{"app-logs": map[string]any{"mappings": map[string]any{
				"cluster.keyword": map[string]any{"full_name": "cluster.keyword", "mapping": map[string]any{
					"keyword": map[string]any{"type": "keyword", "ignore_above": 256},
				}},
			}}})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/app-logs/_settings/index.mapping.ignore_above" && r.URL.Query().Get("flat_settings") == "true" {
			_ = json.NewEncoder(w).Encode(map[string]any{"app-logs": map[string]any{"settings": map[string]any{}}})
			return
		}
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
		endText := filters[0].(map[string]any)["range"].(map[string]any)["@timestamp"].(map[string]any)["lt"].(string)
		end, err := time.Parse(time.RFC3339Nano, endText)
		if err != nil {
			t.Error(err)
			return
		}
		lastWindow.Store(int64(end.Sub(start)))
		step := end.Sub(start) / 30
		at := start.Add(29 * step)
		count := 1
		if zeroMatches.Load() {
			count = 0
		}
		volume := make([]any, 30)
		trend := make([]any, 30)
		for i := range volume {
			bucketCount := 0
			if i == 29 {
				bucketCount = count
			}
			bucket := map[string]any{"key": start.Add(time.Duration(i) * step).UnixMilli(), "doc_count": bucketCount}
			volume[i], trend[i] = bucket, bucket
		}
		hits := []any{}
		if count > 0 {
			hits = append(hits, map[string]any{"_source": map[string]any{"@timestamp": at.Format(time.RFC3339Nano),
				"message": "checkout accepted", "cluster": "lab", "level": "info"}})
		}
		response := map[string]any{
			"timed_out": false,
			"_shards":   map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits": map[string]any{
				"total": map[string]any{"value": count, "relation": "eq"},
				"hits":  hits,
			},
			"aggregations": map[string]any{"volume": map[string]any{"buckets": volume},
				"errors": map[string]any{"doc_count": 0}, "missing_patterns": map[string]any{"doc_count": 0}},
		}
		if completePatterns.Load() {
			buckets := []any{}
			if count > 0 {
				buckets = append(buckets, map[string]any{"key": "checkout accepted", "doc_count": count, "doc_count_error_upper_bound": 0, "trend": map[string]any{"buckets": trend}})
			}
			response["aggregations"].(map[string]any)["patterns"] = map[string]any{"sum_other_doc_count": 0, "doc_count_error_upper_bound": 0, "buckets": buckets}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer upstream.Close()
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lokiCalls.Add(1)
		query := r.FormValue("query")
		if !strings.Contains(query, `{application="checkout",environment="dev"}`) ||
			!strings.Contains(query, `|= "checkout"`) || !strings.Contains(query, "level") {
			t.Errorf("Loki query escaped registered scope or filters: %s", query)
		}
		switch r.URL.Path {
		case "/loki/api/v1/query_range":
			if r.FormValue("limit") != "100" || r.FormValue("direction") != "backward" ||
				r.FormValue("start") == "" || r.FormValue("end") == "" {
				t.Errorf("unbounded Loki sample: %s", r.URL)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[]}}`))
		case "/loki/api/v1/query":
			if r.FormValue("time") == "" || !strings.Contains(query, "count_over_time(") {
				t.Errorf("unbounded Loki aggregation: %s", r.URL)
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			t.Errorf("unexpected Loki route: %s", r.URL)
			http.Error(w, "missing", http.StatusNotFound)
		}
	}))
	defer loki.Close()
	var adminID string
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('es-1','dev','elasticsearch','Elasticsearch','observe',$1,'none','',true,'[{"name":"search_logs","desc":"","enabled":true}]'),
 ('loki-1','dev','loki','Loki','observe',$2,'none','',true,'[{"name":"search_logs","desc":"","enabled":true}]')`, upstream.URL, loki.URL); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"scopes":[
 {"connector_id":"es-1","application_id":"app","environment_id":"dev","resource_id":"registered-app","provider":"elasticsearch",
  "elasticsearch_index":"app-logs","elasticsearch_pattern_field":"message_template.keyword","elasticsearch_filters":{"cluster":"lab"}},
 {"connector_id":"loki-1","application_id":"app","environment_id":"dev","resource_id":"same-app-different-source","provider":"loki",
  "loki_labels":{"application":"checkout","environment":"dev"},"loki_pattern_field":"message_template","loki_complete_lookback_seconds":7200},
 {"connector_id":"loki-1","application_id":"app","environment_id":"dev","resource_id":"loki-without-lookback","provider":"loki",
  "loki_labels":{"application":"checkout","environment":"dev"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// bind replaces the service's log targets: each is "connector/external ID".
	bind := func(targets ...string) {
		t.Helper()
		if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE application_id='app'`); err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			connector, external, _ := strings.Cut(target, "/")
			kind := "log_index"
			if connector == "loki-1" {
				kind = "log_stream"
			}
			if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES($1,'app','dev',$2,$3,$4,$5)`, "t-"+connector, connector, kind, external, adminID); err != nil {
				t.Fatal(err)
			}
		}
	}
	const es, lokiTarget, lokiWithoutLookback = "es-1/registered-app", "loki-1/same-app-different-source", "loki-1/loki-without-lookback"
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	observe := New(st, policy)
	observe.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)}
	observe.Clients = &v3telemetry.Clients{Tier: "nonprod"}
	observe.Register(api)
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
	type partialLogs struct {
		Total       *string           `json:"total"`
		Errors      *string           `json:"errors"`
		Histogram   [][]int64         `json:"histogram"`
		Items       []logLine         `json:"items"`
		Patterns    []json.RawMessage `json:"patterns"`
		Unavailable map[string]string `json:"unavailable"`
	}
	decodePartial := func(w *httptest.ResponseRecorder) partialLogs {
		t.Helper()
		var out partialLogs
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Unavailable == nil {
			t.Fatalf("partial logs response: status=%d body=%s", w.Code, w.Body.String())
		}
		return out
	}
	checkError(request("?service=app&levels=DEBUG"), 422, "invalid_levels")
	unbound := decodePartial(request("?service=app&levels=INFO&q=checkout"))
	if unbound.Total != nil || unbound.Errors != nil || unbound.Histogram != nil || unbound.Items != nil || unbound.Patterns != nil ||
		unbound.Unavailable["items"] != "not_configured" || unbound.Unavailable["total"] != "not_configured" || len(unbound.Unavailable) != 5 {
		t.Fatalf("unbound log scope must be explicitly not configured, not empty: %+v", unbound)
	}
	if providerCalls.Load() != 0 {
		t.Fatal("provider queried without a registered target")
	}
	bind(es)
	w := request("?service=app&levels=INFO&q=checkout")
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	// Exact counts and lines without a complete pattern aggregation: patterns
	// are unknown, never an empty list.
	body := decodePartial(w)
	if body.Total == nil || *body.Total != "1" || body.Errors == nil || *body.Errors != "0" || len(body.Items) != 1 || body.Items[0].Level != "INFO" ||
		body.Items[0].Service != "app" || body.Items[0].Message != "checkout accepted" || len(body.Histogram) != 30 ||
		len(body.Histogram[29]) != 3 || body.Histogram[29][2] != 1 || body.Patterns != nil ||
		len(body.Unavailable) != 1 || body.Unavailable["patterns"] != "unavailable" {
		t.Fatalf("unexpected scoped log result: %s", w.Body.String())
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
		!strings.Contains(w.Body.String(), `"unavailable":{}`) ||
		complete.Total != "1" || complete.Errors != "0" || len(complete.Histogram) != 30 ||
		len(complete.Items) != 1 || len(complete.Patterns) != 1 ||
		complete.Patterns[0].Template != "checkout accepted" || complete.Patterns[0].Level != "INFO" ||
		complete.Patterns[0].Count != "1" || len(complete.Patterns[0].Trend) != 30 || complete.Patterns[0].Trend[29] != 1 {
		t.Fatalf("incomplete scoped log pattern response: status=%d body=%s", w.Code, w.Body.String())
	}
	zeroMatches.Store(true)
	w = request("?service=app&levels=INFO&q=checkout")
	var empty struct {
		Total     string    `json:"total"`
		Errors    string    `json:"errors"`
		Histogram [][]int64 `json:"histogram"`
		Items     []logLine `json:"items"`
		Patterns  []struct {
			Template string `json:"template"`
		} `json:"patterns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &empty); err != nil || w.Code != 200 ||
		empty.Total != "0" || empty.Errors != "0" || len(empty.Histogram) != 30 ||
		len(empty.Items) != 0 || len(empty.Patterns) != 0 {
		t.Fatalf("exact empty scoped log response failed: status=%d body=%s", w.Code, w.Body.String())
	}
	for _, bucket := range empty.Histogram {
		if len(bucket) != 3 || bucket[2] != 0 {
			t.Fatalf("exact empty scoped log histogram failed: %v", empty.Histogram)
		}
	}
	zeroMatches.Store(false)
	blockBindingChange.Store(true)
	changedResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { changedResult <- request("?service=app&levels=INFO&q=checkout") }()
	select {
	case <-bindingQueryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Elasticsearch query did not start")
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connector_targets SET external_id='replacement-app' WHERE id='t-es-1'`); err != nil {
		t.Fatal(err)
	}
	close(releaseBindingQuery)
	checkError(<-changedResult, 409, "log_scope_changed")
	bind(es)
	before := providerCalls.Load()
	w = request("?service=app&levels=")
	if w.Code != 200 || providerCalls.Load() != before || !strings.Contains(w.Body.String(), `"total":"0"`) {
		t.Fatalf("empty explicit level selection queried provider: %d %s", w.Code, w.Body.String())
	}
	// Two log targets are ambiguous whichever provider is selected.
	bind(es, lokiTarget)
	checkError(request("?service=app&levels=INFO"), 409, "ambiguous_binding")
	checkError(request("?service=app&levels=INFO&source=elasticsearch"), 409, "ambiguous_binding")
	if providerCalls.Load() != before {
		t.Fatal("ambiguous log providers must not be queried")
	}
	bind(es)
	w = request("?service=app&levels=INFO&q=checkout&source=elasticsearch&range=1h")
	if w.Code != 200 || providerCalls.Load() != before+1 {
		t.Fatalf("explicit persisted Elasticsearch source failed: status=%d calls=%d body=%s", w.Code, providerCalls.Load(), w.Body.String())
	}
	if time.Duration(lastWindow.Load()) != time.Hour {
		t.Fatalf("selected range did not reach Elasticsearch: %s", time.Duration(lastWindow.Load()))
	}
	beforeLimit := providerCalls.Load()
	checkError(request("?service=app&levels=INFO&q=checkout&source=elasticsearch&range=6h"), 503, "telemetry_unavailable")
	if providerCalls.Load() != beforeLimit {
		t.Fatal("unsupported provider window should not fall back or query")
	}
	checkError(request("?service=app&source=loki&provider=elasticsearch"), 422, "conflicting_source")
	bind(lokiTarget)
	before = providerCalls.Load()
	w = request("?service=app&levels=INFO&q=checkout&provider=loki")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":"0"`) ||
		!strings.Contains(w.Body.String(), `"patterns":[]`) || lokiCalls.Load() == 0 {
		t.Fatalf("configured Loki exact zero failed: status=%d calls=%d body=%s", w.Code, lokiCalls.Load(), w.Body.String())
	}
	bind(lokiWithoutLookback)
	countQueries := lokiCalls.Load()
	unregistered := decodePartial(request("?service=app&levels=INFO,ERROR&q=checkout&provider=loki"))
	if unregistered.Items == nil || len(unregistered.Items) != 0 || unregistered.Total != nil || unregistered.Errors != nil ||
		unregistered.Histogram != nil || unregistered.Patterns != nil || unregistered.Unavailable["total"] != "not_configured" ||
		unregistered.Unavailable["errors"] != "not_configured" || unregistered.Unavailable["patterns"] != "not_configured" {
		t.Fatalf("Loki without a registered lookback must keep lines and leave counts unknown: %+v", unregistered)
	}
	if lokiCalls.Load() != countQueries+2 {
		t.Fatalf("unregistered Loki lookback must not issue count queries: %d", lokiCalls.Load()-countQueries)
	}
	bind(es, lokiTarget)
	checkError(request("?service=app&levels=INFO&provider=unknown"), 422, "invalid_provider")
	checkError(request("?service=app&levels=INFO&provider=loki&provider=elasticsearch"), 422, "invalid_provider")
	if providerCalls.Load() != before {
		t.Fatal("unconfigured or invalid source queried Elasticsearch")
	}
	bind(es)
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
	bind(es, lokiTarget)
	lokiBefore := lokiCalls.Load()
	checkError(request("?service=app&levels=INFO&provider=loki"), 403, "forbidden")
	if lokiCalls.Load() != lokiBefore {
		t.Fatal("Loki queried after logs grant was revoked")
	}
}
