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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
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
		at := time.Now().UTC().Add(-time.Minute)
		bucket := at.Add(-time.Minute).UnixMilli()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hits": map[string]any{
				"total": map[string]any{"value": 1, "relation": "eq"},
				"hits": []any{map[string]any{"_source": map[string]any{"@timestamp": at.Format(time.RFC3339Nano),
					"message": "checkout accepted", "cluster": "lab", "level": "info"}}},
			},
			"aggregations": map[string]any{"volume": map[string]any{"buckets": []any{map[string]any{"key": bucket, "doc_count": 1}}},
				"errors": map[string]any{"doc_count": 0}},
		})
	}))
	defer upstream.Close()
	telemetry, err := connectors.NewTelemetry(connectors.TelemetryConfig{
		Elasticsearch: connectors.TelemetryEndpoint{URL: upstream.URL, AllowHTTP: true},
		Applications: map[string]connectors.ApplicationTelemetry{"registered-app": {
			ElasticsearchIndex: "app-logs", ElasticsearchFilters: map[string]string{"cluster": "lab"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, telemetry).Register(api)
	api.MarkV3ObjectRoute("GET /api/v1/logs", "logs", "view")
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
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	var body struct {
		Total     string    `json:"total"`
		Errors    string    `json:"errors"`
		Histogram [][]int64 `json:"histogram"`
		Items     []logLine `json:"items"`
		Patterns  []any     `json:"patterns"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != "1" || body.Errors != "0" || len(body.Items) != 1 || body.Items[0].Level != "INFO" ||
		body.Items[0].Service != "app" || body.Items[0].Message != "checkout accepted" || len(body.Histogram) != 1 ||
		len(body.Histogram[0]) != 3 || body.Histogram[0][2] != 1 || len(body.Patterns) != 0 {
		t.Fatalf("unexpected scoped log result: %+v", body)
	}
	before := providerCalls.Load()
	w = request("?service=app&levels=")
	if w.Code != 200 || providerCalls.Load() != before || !strings.Contains(w.Body.String(), `"total":"0"`) {
		t.Fatalf("empty explicit level selection queried provider: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='logs' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	checkError(request("?service=app&levels=INFO&q=checkout"), 403, "forbidden")
	if providerCalls.Load() != before {
		t.Fatal("provider queried after logs grant was revoked")
	}
}
