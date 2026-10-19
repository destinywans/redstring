package v3coverage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PA-19 P3 stage 2 step 1 (PA-67): with the coverage reader switched, the
// source cells come from the registry, so a legacy binding no longer counts
// and a registered but unreadable source says why. Without the switch the
// matrix is as before.
func TestCoverageFromTheRegistry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_coverage_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	tool := func(name string, on bool) string {
		raw, _ := json.Marshal([]map[string]any{{"name": name, "desc": "", "enabled": on}})
		return string(raw)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','services','view','environment','dev'),
 ('viewer','connectors','view','environment','dev')`,
		`INSERT INTO applications(id,key,name) VALUES('app','checkout','Checkout'),('app2','orders','Orders')`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('app-dev','app','dev','Dev','[{"kind":"tempo","connector_id":"tempo","resource":"checkout"}]'),
 ('app2-dev','app2','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('app2','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('prom','dev','prometheus','Prometheus','observe','http://127.0.0.1:19090','none','',true,'` + tool("query_metrics", true) + `'),
 ('loki','dev','loki','Loki','observe','http://127.0.0.1:13100','none','',false,'` + tool("search_logs", false) + `'),
 ('es','dev','elasticsearch','Elasticsearch','observe','https://es.example.invalid','api_key','',true,'` + tool("search_logs", true) + `'),
 ('gitlab','dev','gitlab','GitLab','delivery','https://gitlab.example.invalid','bearer','vault://kv/gitlab#token',true,'[]')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('t-metrics','app','dev','prom','metric_resource','checkout',$1),
 ('t-logs','app','dev','loki','log_stream','checkout',$1),
 ('t-code','app','dev','gitlab','project','group/checkout',$1),
 ('t2-loki','app2','dev','loki','log_stream','orders',$1),
 ('t2-es','app2','dev','es','log_index','orders',$1)`, viewer.ID); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"queries":[{"connector_id":"prom","application_id":"app","environment_id":"dev",
 "resource_id":"checkout","query_id":"request_rate","expression":"sum(rate(x[5m]))"}],
 "scopes":[{"connector_id":"loki","application_id":"app","environment_id":"dev","resource_id":"checkout","provider":"loki","loki_labels":{"app":"checkout"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	coverage := New(db, policy)
	coverage.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)}
	api := httpapi.New(st, nil, true)
	coverage.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session,
		func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	read := func() map[string]map[string]cell {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/coverage", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.Header.Set("Accept-Language", "en")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		var body struct {
			Rows []struct {
				Service string          `json:"service"`
				Cells   map[string]cell `json:"cells"`
			} `json:"rows"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("coverage: %d %s", w.Code, w.Body.String())
		}
		out := map[string]map[string]cell{}
		for _, row := range body.Rows {
			out[row.Service] = row.Cells
		}
		return out
	}
	expect := func(c cell, state, availability, brand, text string) bool {
		return c.State == state && c.Availability == availability && c.Brand == brand && strings.Contains(c.Text, text)
	}

	// The cells answer from the registry; the legacy tempo binding (not a
	// connector ID) does not count.
	after := read()
	checkout, orders := after["checkout"], after["orders"]
	if !expect(checkout["metrics"], "partial", "configured_unverified", "prometheus", "live data coverage not verified") ||
		!expect(checkout["logs"], "partial", "not_ready", "loki", "connector disabled, read tool switched off") ||
		!expect(checkout["traces"], "missing", "not_configured", "", "No source registered") ||
		!expect(checkout["changes"], "partial", "configured_unverified", "gitlab", "registered") {
		t.Fatalf("checkout from the registry: %+v", checkout)
	}
	if !expect(orders["logs"], "partial", "not_ready", "", "More than one source registered") ||
		!expect(orders["metrics"], "missing", "not_configured", "", "No source registered") {
		t.Fatalf("orders from the registry: %+v", orders)
	}

	// Without connectors.view the source cells stay unknown either way.
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors'`); err != nil {
		t.Fatal(err)
	}
	if hidden := read()["checkout"]; hidden["logs"].State != "unknown" || hidden["metrics"].State != "unknown" {
		t.Fatalf("without connectors.view: %+v", hidden)
	}
}
