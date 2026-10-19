package v3traces

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeTempo stores whole traces: spans per trace ID, each with its service.
type fakeTempo struct {
	mu       sync.Mutex
	traces   map[string][]fakeSpan
	auth     []string
	searches int
	server   *httptest.Server
}

type fakeSpan struct {
	service, id, parent string
	start               time.Time
}

func newFakeTempo(t *testing.T, traces map[string][]fakeSpan) *fakeTempo {
	f := &fakeTempo{traces: traces}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/api/search":
			f.searches++
			if r.Method != http.MethodGet || r.URL.Query().Get("limit") != "20" {
				t.Errorf("tempo search %s %s", r.Method, r.URL)
			}
			query := r.URL.Query().Get("q")
			items := []map[string]string{}
			ids := make([]string, 0, len(f.traces))
			for id := range f.traces {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				for _, span := range f.traces[id] {
					if strings.Contains(query, `resource.service.name = "`+span.service+`"`) {
						items = append(items, map[string]string{"traceID": id})
						break
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			spans, ok := f.traces[strings.TrimPrefix(r.URL.Path, "/api/traces/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			batches := []any{}
			for _, span := range spans {
				item := map[string]any{"traceId": strings.TrimPrefix(r.URL.Path, "/api/traces/"), "spanId": span.id, "name": "GET /" + span.service,
					"startTimeUnixNano": strconv.FormatInt(span.start.UnixNano(), 10),
					"endTimeUnixNano":   strconv.FormatInt(span.start.Add(30*time.Millisecond).UnixNano(), 10)}
				if span.parent != "" {
					item["parentSpanId"] = span.parent
				}
				batches = append(batches, map[string]any{"resource": map[string]any{"attributes": []any{
					map[string]any{"key": "service.name", "value": map[string]any{"stringValue": span.service}},
					map[string]any{"key": "deployment.environment", "value": map[string]any{"stringValue": "lab"}},
				}}, "scopeSpans": []any{map[string]any{"spans": []any{item}}}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": batches})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTempo) lastAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auth) == 0 {
		return ""
	}
	return f.auth[len(f.auth)-1]
}

type traceVault struct{ empty atomic.Bool }

func (v *traceVault) Resolve(_ context.Context, reference string) (string, error) {
	if v.empty.Load() || reference != "vault://kv/lab/tempo#token" {
		return "", v3integrations.ErrCredentialUnavailable
	}
	return "tempo-token", nil
}

// PA-19 P3 stage 2 step 4: the list and the detail read registered
// trace_service targets: each connector's targets with its own client and
// Vault credential, several connectors under one budget, and fail closed on
// ambiguous, shared, overlapping, unready or unreadable sources.
func TestTracesReadTheRegistry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_traces_registry_" + store.ID()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-traces-password"); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-traces-password")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}

	// T1 crosses checkout and orders, T2 is orders only (both in Tempo A);
	// T3 is search, stored by Tempo B.
	const t1, t2, t3 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccc"
	base := time.Now().UTC().Add(-10 * time.Minute)
	tempoA := newFakeTempo(t, map[string][]fakeSpan{
		t1: {{"checkout", "1111111111111111", "", base}, {"orders", "2222222222222222", "1111111111111111", base.Add(time.Millisecond)}},
		t2: {{"orders", "3333333333333333", "", base.Add(time.Minute)}},
	})
	tempoB := newFakeTempo(t, map[string][]fakeSpan{t3: {{"search", "4444444444444444", "", base.Add(2 * time.Minute)}}})

	var adminID string
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	tools := `[{"name":"query_traces","desc":"Read-only","enabled":true}]`
	exec(`INSERT INTO applications(id,name,key,environment) VALUES
 ('app','Checkout','checkout','dev'),('app2','Orders','orders','dev'),('app3','Search','search','dev'),('app4','Billing','billing','dev');
INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('app-dev','app','dev','Dev','[{"kind":"tempo","resource":"checkout"}]'),
 ('app2-dev','app2','dev','Dev','[{"kind":"tempo","resource":"orders"}]'),
 ('app3-dev','app3','dev','Dev','[]'),('app4-dev','app4','dev','Dev','[]');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('app2','dev'),('app3','dev'),('app4','dev')`)
	exec(`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('tempo-a','dev','tempo','Tempo A','observe',$1,'bearer','vault://kv/lab/tempo#token',true,$3),
 ('tempo-b','dev','tempo','Tempo B','observe',$2,'none','',true,$3)`, tempoA.server.URL, tempoB.server.URL, tools)
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('t-checkout','app','dev','tempo-a','trace_service','checkout',$1),('t-orders','app2','dev','tempo-a','trace_service','orders',$1)`, adminID)
	scope := func(connector, app, resource, service string) map[string]any {
		return map[string]any{"connector_id": connector, "application_id": app, "environment_id": "dev", "resource_id": resource,
			"provider": "tempo", "tempo_service": service, "tempo_attributes": map[string]string{"deployment.environment": "lab"}}
	}
	catalogOf := func(scopes ...map[string]any) v3telemetry.Catalog {
		raw, _ := json.Marshal(map[string]any{"scopes": scopes})
		catalog, err := v3telemetry.ParseCatalog(raw)
		if err != nil {
			t.Fatal(err)
		}
		return catalog
	}
	catalog := catalogOf(scope("tempo-a", "app", "checkout", "checkout"), scope("tempo-a", "app2", "orders", "orders"),
		scope("tempo-b", "app3", "search", "search"))
	approvals := v3telemetry.NewApprovals([]v3integrations.CredentialBinding{{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "tempo-a",
		Provider: "tempo", Endpoint: tempoA.server.URL, CredentialRef: "vault://kv/lab/tempo#token"}})
	vault := &traceVault{}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	traces := New(st, policy)
	traces.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: approvals}
	traces.Clients = &v3telemetry.Clients{Tier: "nonprod", Credentials: vault, TempoSearchFailClosed: map[string]bool{"tempo-a": true, "tempo-b": true},
		Now: func() time.Time { return time.Unix(0, clock.Load()) }}
	traces.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	fails := func(path string, status int, code, message string) {
		t.Helper()
		w := request(path)
		var body struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || w.Code != status || body.Error.Code != code || !strings.Contains(body.Error.Message, message) {
			t.Fatalf("%s: %d %s, want %d %s", path, w.Code, w.Body.String(), status, code)
		}
	}
	type listed struct {
		Total int        `json:"total"`
		Items []TraceRow `json:"items"`
	}
	list := func(path string) listed {
		t.Helper()
		w := request(path)
		var body listed
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		for i := range body.Items {
			body.Items[i].DurationMS = 0 // compared separately below
		}
		return body
	}
	detail := func(path string) TraceView {
		t.Helper()
		w := request(path)
		var view TraceView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return view
	}

	// Read with the connector's credential.
	paths := []string{"/api/v1/traces?range=1h", "/api/v1/traces?range=1h&service=orders", "/api/v1/traces?range=1h&view=errors"}
	for _, path := range paths {
		list(path)
	}
	if all := list(paths[0]); all.Total != 2 {
		t.Fatalf("registered traces: %+v", all)
	}
	if t1Detail, t1Orders := detail("/api/v1/traces/"+t1), detail("/api/v1/traces/"+t1+"?service=orders"); len(t1Detail.Spans) != 2 ||
		!reflect.DeepEqual(t1Orders.Spans, t1Detail.Spans) {
		t.Fatalf("trace detail: %+v %+v", t1Detail, t1Orders)
	}
	if tempoA.lastAuth() != "Bearer tempo-token" {
		t.Fatalf("registered credential not used: %q", tempoA.lastAuth())
	}
	fails("/api/v1/traces/"+t2+"?service=checkout", 404, "not_found", "")

	// A second connector: its targets are read with their own client, the
	// list covers both, and a trace is found where it is stored.
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-search','app3','dev','tempo-b','trace_service','search',$1)`, adminID)
	if both := list("/api/v1/traces?range=1h"); both.Total != 3 {
		t.Fatalf("two connectors: %+v", both)
	}
	// The population is complete only when the catalog attests every
	// searched Tempo connector (PA-19 P4: tempo_search_fail_closed).
	traces.Clients.TempoSearchFailClosed = map[string]bool{"tempo-a": true}
	var unattested struct {
		Total         *int   `json:"total"`
		Partial       bool   `json:"partial"`
		PartialReason string `json:"partial_reason"`
	}
	if w := request("/api/v1/traces?range=1h"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &unattested) != nil ||
		unattested.Total != nil || !unattested.Partial || unattested.PartialReason != "search_unattested" {
		t.Fatalf("one unattested connector: %d %s", w.Code, w.Body.String())
	}
	traces.Clients.TempoSearchFailClosed = map[string]bool{"tempo-a": true, "tempo-b": true}
	if view := detail("/api/v1/traces/" + t3); len(view.Spans) != 1 || tempoB.lastAuth() != "" {
		t.Fatalf("trace in the second Tempo: %+v auth %q", view, tempoB.lastAuth())
	}
	// Tempo B also holding T1 cannot show it in its scope: T1 comes from A.
	tempoB.mu.Lock()
	tempoB.traces[t1] = tempoA.traces[t1]
	tempoB.mu.Unlock()
	if view := detail("/api/v1/traces/" + t1); len(view.Spans) != 2 {
		t.Fatalf("trace outside the second Tempo's scope: %+v", view)
	}
	tempoB.mu.Lock()
	delete(tempoB.traces, t1)
	tempoB.mu.Unlock()

	// Fail closed: not ready, credential unavailable, overlapping selectors
	// (here with a hidden service's scope), two targets of one service. One
	// resource bound to two services is refused by the database.
	exec(`UPDATE v3_connectors SET tools='[{"name":"query_traces","desc":"Read-only","enabled":false}]' WHERE id='tempo-b'`)
	fails("/api/v1/traces?range=1h", 503, "source_not_ready", "tool_disabled")
	exec(`UPDATE v3_connectors SET tools=$1 WHERE id='tempo-b'`, tools)
	vault.empty.Store(true)
	clock.Add(int64(61 * time.Second))
	fails("/api/v1/traces?range=1h&service=orders", 503, "source_unavailable", "credential")
	vault.empty.Store(false)
	traces.Registry.Catalog = catalogOf(scope("tempo-a", "app", "checkout", "checkout"), scope("tempo-a", "app2", "orders", "orders"),
		scope("tempo-b", "app3", "search", "search"), scope("tempo-b", "app4", "orders-copy", "orders"))
	fails("/api/v1/traces?range=1h&service=orders", 503, "not_configured", "")
	traces.Registry.Catalog = catalog
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-checkout-b','app','dev','tempo-b','trace_service','checkout',$1)`, adminID)
	fails("/api/v1/traces?range=1h", 409, "ambiguous_binding", "")
	exec(`DELETE FROM v3_connector_targets WHERE id='t-checkout-b'`)

	// No fallback: a legacy binding without a registry target is no source.
	exec(`DELETE FROM v3_connector_targets WHERE id='t-checkout'`)
	fails("/api/v1/traces?range=1h&service=checkout", 404, "not_found", "no accessible trace source")
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-checkout','app','dev','tempo-a','trace_service','checkout',$1)`, adminID)

	// Authorization is per target, as before: with traces.view on orders
	// only, T2 is readable and T1 (it crosses checkout) is not.
	exec(`DELETE FROM v3_role_grants WHERE role_id='admin' AND module='traces' AND action='view';
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','traces','view','application','app2')`)
	if view := detail("/api/v1/traces/" + t2); len(view.Spans) != 1 {
		t.Fatalf("authorized trace: %+v", view)
	}
	fails("/api/v1/traces/"+t1, 404, "not_found", "")
}
