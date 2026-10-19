package v3catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceMappingsHTTPV3ObjectScopeAndPersistedBindings(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_service_mappings_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.MaxConns = 1 // Catalog and mapping reads must close each page before policy checks.
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES('mapping-app-reader','Mapping App Reader'),('mapping-team-reader','Mapping Team Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('mapping-app-reader','services','view','application','app-visible'),
 ('mapping-team-reader','services','view','team','ops')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','services','view','global','*') ON CONFLICT DO NOTHING`,
		`INSERT INTO teams(id,name) VALUES('ops','Operations')`,
		`INSERT INTO applications(id,name,key,environment,owner_team_id,archived) VALUES
 ('app-visible','Visible','visible','dev',NULL,false),
 ('app-team','Team','team-owned','dev','ops',false),
 ('app-hidden','Hidden','hidden','dev',NULL,false),
 ('app-empty','Empty','unmapped','dev',NULL,false),
 ('app-archived','Archived','archived','dev',NULL,true)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('visible-dev','app-visible','dev','Dev','[{"kind":"argocd","resource":"job=visible"},{"kind":"prometheus","resource":"legacy-metric"}]'),
 ('visible-test','app-visible','test','Test','[{"kind":"grafana","resource":"secret-test-log"}]'),
 ('team-dev','app-team','dev','Dev','[{"kind":"argocd","resource":"ops/team"}]'),
 ('hidden-dev','app-hidden','dev','Dev','[{"kind":"kibana","resource":"secret-hidden-trace"}]'),
 ('empty-dev','app-empty','dev','Dev','[]'),
 ('archived-dev','app-archived','dev','Dev','[{"kind":"argocd","resource":"secret-archive"}]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-visible','dev'),('app-visible','test'),('app-team','dev'),('app-hidden','dev'),('app-empty','dev'),('app-archived','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	appUser, err := st.CreateUser(ctx, core.User{Email: "app-reader@example.invalid", Name: "App Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	teamUser, err := st.CreateUser(ctx, core.User{Email: "team-reader@example.invalid", Name: "Team Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'mapping-app-reader'),($2,'mapping-team-reader')`, appUser.ID, teamUser.ID); err != nil {
		t.Fatal(err)
	}
	login := func(email string) string {
		t.Helper()
		password := "isolated-reader-password"
		if email == "admin@example.invalid" {
			password = "isolated-admin-password"
		}
		_, token, err := st.Login(ctx, email, password)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, "dev", "test").Register(api)
	api.MarkV3ObjectRoute("GET /api/v1/services", "services", "view")
	api.MarkV3ObjectRoute("GET /api/v1/services/{id}", "services", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(environment, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/service-mappings", nil)
		r.Header.Set("X-AIOps-Environment", environment)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := func(environment, token string) ServiceMappingForTest {
		t.Helper()
		w := request(environment, token)
		if w.Code != http.StatusOK {
			t.Fatalf("GET service-mappings status=%d body=%s", w.Code, w.Body.String())
		}
		var result ServiceMappingForTest
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Systems) != 7 || result.Suggestions != 0 {
			t.Fatalf("invented or malformed mapping metadata: %+v", result)
		}
		for _, row := range result.Rows {
			for _, cell := range row.Cells {
				if cell.State == "suggested" {
					t.Fatalf("fabricated suggestion in %+v", row)
				}
			}
		}
		return result
	}
	if w := request("dev", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", w.Code, w.Body.String())
	}
	appToken := login("app-reader@example.invalid")
	teamToken := login("team-reader@example.invalid")
	adminToken := login("admin@example.invalid")
	appView := read("dev", appToken)
	// Metrics, logs, traces and workload come from registry targets only: the
	// stored prometheus binding is not shown (PA-19 P4).
	if len(appView.Rows) != 1 || appView.Rows[0].Service != "visible" || appView.Rows[0].Cells["deploy"].Value != "job=visible" ||
		appView.Rows[0].Cells["dash"].State != "missing" || appView.Rows[0].Cells["metrics"].State != "missing" {
		t.Fatalf("application scope leaked another service or environment: %+v", appView.Rows)
	}
	// `all` contains only environments this user can view; test bindings from
	// the same application must remain hidden to a dev-only caller.
	appAll := read("all", appToken)
	if len(appAll.Rows) != 1 || appAll.Rows[0].Cells["dash"].State != "missing" {
		t.Fatalf("all view crossed environment scope: %+v", appAll.Rows)
	}
	if w := request("test", appToken); w.Code != http.StatusForbidden {
		t.Fatalf("cross-environment status=%d body=%s", w.Code, w.Body.String())
	}
	teamView := read("dev", teamToken)
	if len(teamView.Rows) != 1 || teamView.Rows[0].Service != "team-owned" || teamView.Rows[0].Cells["deploy"].Value != "ops/team" {
		t.Fatalf("team scope leaked another service: %+v", teamView.Rows)
	}
	allView := read("all", adminToken)
	if len(allView.Rows) != 4 {
		t.Fatalf("admin all should include four active services, got %+v", allView.Rows)
	}
	byName := map[string]mappingRow{}
	for _, row := range allView.Rows {
		byName[row.Service] = row
	}
	if byName["visible"].Cells["deploy"].Value != "dev: job=visible" || byName["visible"].Cells["dash"].Value != "test: secret-test-log" ||
		byName["unmapped"].Cells["deploy"].State != "missing" || byName["archived"].Service != "" {
		t.Fatalf("incorrect mapped/unmapped all result: %+v", allView.Rows)
	}
	// A page of unrelated catalog rows must not consume the visible result
	// budget before the service policy has run.
	for i := 0; i < 140; i++ {
		id := fmt.Sprintf("hidden-catalog-%03d", i)
		key := fmt.Sprintf("a-hidden-%03d", i)
		if _, err := db.Exec(ctx, `WITH app AS (
 INSERT INTO applications(id,name,key,environment) VALUES($1,$2,$2,'dev') RETURNING id
 ), app_env AS (
 INSERT INTO application_environments(id,application_id,key,name)
 SELECT id||'-dev',id,'dev','Dev' FROM app RETURNING application_id
 ) INSERT INTO v3_application_environments(application_id,environment_id)
 SELECT application_id,'dev' FROM app_env`, id, key); err != nil {
			t.Fatal(err)
		}
	}
	catalogReq := httptest.NewRequest(http.MethodGet, "/api/v1/services?q=%25", nil)
	catalogReq.Header.Set("X-AIOps-Environment", "dev")
	catalogReq.AddCookie(&http.Cookie{Name: "aiops_session", Value: appToken})
	catalogResponse := httptest.NewRecorder()
	api.ServeHTTP(catalogResponse, catalogReq)
	if catalogResponse.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogResponse.Code, catalogResponse.Body.String())
	}
	var wildcardResult struct {
		Items []catalogRecord `json:"items"`
		Total int             `json:"total"`
	}
	if err := json.Unmarshal(catalogResponse.Body.Bytes(), &wildcardResult); err != nil {
		t.Fatal(err)
	}
	if wildcardResult.Total != 0 || len(wildcardResult.Items) != 0 {
		t.Fatalf("search wildcard was treated as a broad query: %+v", wildcardResult)
	}
	catalogReq = httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	catalogReq.Header.Set("X-AIOps-Environment", "dev")
	catalogReq.AddCookie(&http.Cookie{Name: "aiops_session", Value: appToken})
	catalogResponse = httptest.NewRecorder()
	api.ServeHTTP(catalogResponse, catalogReq)
	if catalogResponse.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalogResponse.Code, catalogResponse.Body.String())
	}
	var result struct {
		Items []catalogRecord `json:"items"`
		Total int             `json:"total"`
	}
	if err := json.Unmarshal(catalogResponse.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].Key != "visible" || result.Items[0].Environment != "dev" {
		t.Fatalf("hidden records displaced the authorized service: %+v", result)
	}
	if result.Items[0].Status.State != "unavailable" || result.Items[0].SLO.BudgetLeft.State != "unavailable" {
		t.Fatalf("catalog invented live state: %+v", result.Items[0])
	}
	serviceReq := httptest.NewRequest(http.MethodGet, "/api/v1/services/"+result.Items[0].ID, nil)
	serviceReq.Header.Set("X-AIOps-Environment", "dev")
	serviceReq.AddCookie(&http.Cookie{Name: "aiops_session", Value: appToken})
	serviceResponse := httptest.NewRecorder()
	api.ServeHTTP(serviceResponse, serviceReq)
	if serviceResponse.Code != http.StatusOK {
		t.Fatalf("service detail status=%d body=%s", serviceResponse.Code, serviceResponse.Body.String())
	}
	hiddenReq := httptest.NewRequest(http.MethodGet, "/api/v1/services/"+recordID("app-hidden", "dev"), nil)
	hiddenReq.Header.Set("X-AIOps-Environment", "dev")
	hiddenReq.AddCookie(&http.Cookie{Name: "aiops_session", Value: appToken})
	hiddenResponse := httptest.NewRecorder()
	api.ServeHTTP(hiddenResponse, hiddenReq)
	if hiddenResponse.Code != http.StatusNotFound {
		t.Fatalf("hidden service detail status=%d body=%s", hiddenResponse.Code, hiddenResponse.Body.String())
	}
}

type ServiceMappingForTest struct {
	Systems     []mappingSystem `json:"systems"`
	Rows        []mappingRow    `json:"rows"`
	Suggestions int             `json:"suggestions"`
}
