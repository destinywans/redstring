package v3observe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
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
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// labVault answers the two approved references; empty simulates a dev-mode
// Vault after a restart.
type labVault struct{ empty atomic.Bool }

func (v *labVault) Resolve(_ context.Context, reference string) (string, error) {
	if v.empty.Load() {
		return "", v3integrations.ErrCredentialUnavailable
	}
	switch reference {
	case "vault://kv/lab/loki#token":
		return "loki-token", nil
	case "vault://aiops/lab/elasticsearch#password":
		return "es-password", nil
	}
	return "", v3integrations.ErrCredentialUnavailable
}

// PA-19 P3 stage 2 step 3: the log page reads registry targets through each
// connector's own client (Loki bearer and Elasticsearch basic with the Vault
// password), never falls back to a legacy binding, and fails closed on
// unready, ambiguous or unreadable sources.
func TestLogsReadTheRegistry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_logs_registry_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-logs-password"); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-logs-password")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var lokiAuth, esAuth, lokiQueries []string
	loki := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lokiAuth = append(lokiAuth, r.Header.Get("Authorization"))
		lokiQueries = append(lokiQueries, r.FormValue("query"))
		mu.Unlock()
		if r.URL.Path != "/loki/api/v1/query_range" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		end, _ := strconv.ParseInt(r.FormValue("end"), 10, 64)
		at := strconv.FormatInt(end-int64(time.Second), 10)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams",
			"result": []any{map[string]any{"stream": map[string]string{"app": "checkout"},
				"values": []any{[]string{at, `{"level":"error","msg":"checkout failed"}`}}}}}})
	}))
	defer loki.Close()
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		mu.Lock()
		esAuth = append(esAuth, user+"|"+pass)
		mu.Unlock()
		switch r.URL.Path {
		case "/lab-search-logs/_mapping/field/service.name,service.name.keyword":
			_, _ = w.Write([]byte(`{"lab-search-logs":{"mappings":{"service.name":{"full_name":"service.name","mapping":{"name":{"type":"keyword"}}}}}}`))
			return
		case "/lab-search-logs/_settings/index.mapping.ignore_above":
			_, _ = w.Write([]byte(`{"lab-search-logs":{"settings":{}}}`))
			return
		case "/lab-search-logs/_search":
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		filters := request["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
		window := filters[0].(map[string]any)["range"].(map[string]any)["@timestamp"].(map[string]any)
		start, _ := time.Parse(time.RFC3339Nano, window["gte"].(string))
		end, _ := time.Parse(time.RFC3339Nano, window["lt"].(string))
		step := end.Sub(start) / 30
		volume := make([]any, 30)
		for i := range volume {
			count := 0
			if i == 29 {
				count = 1
			}
			volume[i] = map[string]any{"key": start.Add(time.Duration(i) * step).UnixMilli(), "doc_count": count}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"timed_out": false, "_shards": map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits": map[string]any{"total": map[string]any{"value": 1, "relation": "eq"}, "hits": []any{map[string]any{"_source": map[string]any{
				"@timestamp": start.Add(29 * step).Format(time.RFC3339Nano), "message": "search failed", "service": map[string]any{"name": "search"}, "level": "error"}}}},
			"aggregations": map[string]any{"volume": map[string]any{"buckets": volume}, "errors": map[string]any{"doc_count": 1},
				"missing_patterns": map[string]any{"doc_count": 0}},
		})
	}))
	defer es.Close()

	tools := `[{"name":"search_logs","desc":"Read-only","enabled":true}]`
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES
 ('app','Checkout','checkout','dev'),('app2','Search','search','dev'),('app3','Orders','orders','dev');
INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('app-dev','app','dev','Dev','[{"kind":"loki","resource":"checkout"}]'),
 ('app2-dev','app2','dev','Dev','[{"kind":"elasticsearch","resource":"search"}]'),
 ('app3-dev','app3','dev','Dev','[{"kind":"loki","resource":"orders"}]');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('app2','dev'),('app3','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools,options) VALUES
 ('loki-1','dev','loki','Loki','observe',$1,'bearer','vault://kv/lab/loki#token',true,$3,'{}'),
 ('es-1','dev','elasticsearch','Elasticsearch','observe',$2,'basic','vault://aiops/lab/elasticsearch#password',true,$3,'{"username":"reader"}')`,
		loki.URL, es.URL, tools); err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('t-loki','app','dev','loki-1','log_stream','checkout',$1),('t-es','app2','dev','es-1','log_index','search',$1)`, adminID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"scopes": []any{
		map[string]any{"connector_id": "loki-1", "application_id": "app", "environment_id": "dev", "resource_id": "checkout",
			"provider": "loki", "loki_labels": map[string]string{"app": "checkout"}},
		map[string]any{"connector_id": "es-1", "application_id": "app2", "environment_id": "dev", "resource_id": "search",
			"provider": "elasticsearch", "elasticsearch_index": "lab-search-logs", "elasticsearch_filters": map[string]string{"service.name": "search"}},
	}})
	catalog, err := v3telemetry.ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	vault := &labVault{}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	observe := New(st, policy)
	observe.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals([]v3integrations.CredentialBinding{
		{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "loki-1", Provider: "loki", Endpoint: loki.URL, CredentialRef: "vault://kv/lab/loki#token"},
		{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "es-1", Provider: "elasticsearch", Endpoint: es.URL, CredentialRef: "vault://aiops/lab/elasticsearch#password"},
	})}
	observe.Clients = &v3telemetry.Clients{Tier: "nonprod", Credentials: vault,
		Now: func() time.Time { return time.Unix(0, clock.Load()) }}
	observe.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, nil); err != nil {
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
	type logsBody struct {
		Total       *string           `json:"total"`
		Errors      *string           `json:"errors"`
		Items       []logLine         `json:"items"`
		Unavailable map[string]string `json:"unavailable"`
	}
	read := func(query string) logsBody {
		t.Helper()
		w := request(query)
		var body logsBody
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
		return body
	}
	fails := func(query string, status int, code, message string) {
		t.Helper()
		w := request(query)
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != status || body.Error.Code != code || !strings.Contains(body.Error.Message, message) {
			t.Fatalf("%s: %d %s, want %d %s", query, w.Code, w.Body.String(), status, code)
		}
	}
	lines := func(body logsBody) []string {
		out := []string{}
		for _, item := range body.Items {
			out = append(out, item.Service+" "+item.Level+" "+item.Message)
		}
		return out
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}

	// Each service's lines, read with its connector's Vault credential.
	const checkout = "?service=checkout&levels=ERROR&range=1h"
	after := read(checkout)
	afterSearch := read("?service=search&levels=ERROR&range=1h")
	if !reflect.DeepEqual(lines(after), []string{`app ERROR {"level":"error","msg":"checkout failed"}`}) ||
		!reflect.DeepEqual(lines(afterSearch), []string{"app2 ERROR search failed"}) ||
		afterSearch.Total == nil || *afterSearch.Total != "1" || afterSearch.Errors == nil || *afterSearch.Errors != "1" {
		t.Fatalf("registry answers:\n%+v\n%+v", after, afterSearch)
	}
	mu.Lock()
	lastLoki, lastES := lokiAuth[len(lokiAuth)-1], esAuth[len(esAuth)-1]
	mu.Unlock()
	if lastLoki != "Bearer loki-token" || lastES != "reader|es-password" {
		t.Fatalf("registered credentials not used: loki %q es %q", lastLoki, lastES)
	}
	if all := read("?levels=ERROR&range=1h"); len(all.Items) != 2 {
		t.Fatalf("all registered services: %+v", all)
	}

	// No fallback: a service with only a legacy binding has no log source,
	// and a provider filter that matches no target is not configured.
	mu.Lock()
	queried := len(lokiQueries)
	mu.Unlock()
	for _, query := range []string{"?service=orders&levels=ERROR", "?service=checkout&provider=elasticsearch&levels=ERROR"} {
		if body := read(query); body.Items != nil || body.Unavailable["items"] != "not_configured" {
			t.Fatalf("%s: %+v", query, body)
		}
	}
	mu.Lock()
	if len(lokiQueries) != queried {
		t.Fatalf("an unregistered source was queried: %v", lokiQueries[queried:])
	}
	mu.Unlock()

	// A registered source that is not ready says why, before any query.
	exec(`UPDATE v3_connectors SET tools='[{"name":"search_logs","desc":"Read-only","enabled":false}]' WHERE id='loki-1'`)
	fails(checkout, 503, "source_not_ready", "tool_disabled")
	exec(`UPDATE v3_connectors SET tools=$1,credential_ref='vault://kv/lab/other#token' WHERE id='loki-1'`, tools)
	fails(checkout, 503, "source_not_ready", "credential_not_approved")
	exec(`UPDATE v3_connectors SET credential_ref='vault://kv/lab/loki#token' WHERE id='loki-1'`)
	exec(`UPDATE v3_connectors SET options='{}' WHERE id='es-1'`)
	fails("?service=search&levels=ERROR", 503, "source_not_ready", "options_invalid")
	exec(`UPDATE v3_connectors SET options='{"username":"reader"}' WHERE id='es-1'`)

	// Two log targets are ambiguous whichever provider is selected.
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-es-checkout','app','dev','es-1','log_index','checkout',$1)`, adminID)
	fails(checkout+"&provider=loki", 409, "ambiguous_binding", "")
	exec(`DELETE FROM v3_connector_targets WHERE id='t-es-checkout'`)

	// An empty Vault is a source outage once the cached credential expires.
	vault.empty.Store(true)
	read(checkout) // still within the 60 s credential cache
	clock.Add(int64(61 * time.Second))
	fails(checkout, 503, "source_unavailable", "credential")
	vault.empty.Store(false)
	read(checkout)

	// Authorization is per target, as before: an application grant shows
	// that service only.
	exec(`DELETE FROM v3_role_grants WHERE role_id='admin' AND module='logs' AND action='view';
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','logs','view','application','app2')`)
	if scoped := read("?levels=ERROR&range=1h"); len(scoped.Items) != 1 || scoped.Items[0].Service != "app2" {
		t.Fatalf("application grant: %+v", scoped)
	}
	fails(checkout, 403, "forbidden", "")

	// Without a registry or clients the switched page is not configured.
	observe.Clients = nil
	fails("?service=search&levels=ERROR", 503, "not_configured", "")
}
