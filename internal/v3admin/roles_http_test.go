package v3admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestRolesHTTPReadsCurrentGrantsWithinAuthorizedEnvironment(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_roles_test_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-roles-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Scoped Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
VALUES ('viewer','environment','view','environment','dev'),
       ('viewer','roles','view','environment','dev')`,
		`INSERT INTO rbac_roles(id,name,description) VALUES
 ('dev-role','Dev Role','Dev grant'),('test-role','Test Role','Test grant'),
	 ('partial-role','Partial Role','Partial grouped grant'),
	 ('team-role','Team Role','Team-scoped grant'),('app-role','Application Role','Application-scoped grant')`,
		`INSERT INTO teams(id,name) VALUES('ops-team','Operations')`,
		`INSERT INTO applications(id,name,key,environment,owner_team_id) VALUES('app','App','app','dev','ops-team')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('dev-role','services','view','environment','dev'),
 ('test-role','services','view','environment','test'),
	 ('partial-role','members','view','environment','dev'),
	 ('team-role','services','view','team','ops-team'),
	 ('app-role','services','view','application','app')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES ($1,'dev-role')`, viewer.ID); err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-roles-password")
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
		return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
	}); err != nil {
		t.Fatal(err)
	}
	request := func(path, environment, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code {
			t.Fatalf("status=%d body=%s; want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	assertError(request("/api/v1/roles", "dev", ""), 401, "unauthorized")
	assertError(request("/api/v1/roles/test-role", "dev", viewerToken), 404, "not_found")
	assertError(request("/api/v1/roles", "test", viewerToken), 403, "forbidden")
	w := request("/api/v1/roles", "dev", viewerToken)
	if w.Code != 200 {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var listed struct {
		Items []role `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	seen := map[string]role{}
	for _, item := range listed.Items {
		seen[item.ID] = item
	}
	if seen["dev-role"].Members != 1 || seen["dev-role"].Kind != "business" || seen["test-role"].ID != "" ||
		seen["partial-role"].ID == "" || seen["team-role"].ID == "" || seen["app-role"].ID == "" {
		t.Fatalf("scoped role list=%+v", listed.Items)
	}
	w = request("/api/v1/roles/dev-role", "dev", viewerToken)
	if w.Code != 200 {
		t.Fatalf("detail status=%d body=%s", w.Code, w.Body.String())
	}
	var detail roleDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "Dev Role" || detail.Desc != "Dev grant" || detail.Members != 1 ||
		len(detail.MemberNames) != 1 || detail.MemberNames[0] != "Scoped Viewer" ||
		len(detail.Scope) != 1 || detail.Scope[0] != "environment:dev" ||
		len(detail.Actions) != 5 || len(detail.Matrix) != len(matrixCatalog) {
		t.Fatalf("detail=%+v", detail)
	}
	for _, tc := range []struct{ id, scope string }{{"team-role", "team:ops-team"}, {"app-role", "application:app"}} {
		w = request("/api/v1/roles/"+tc.id, "dev", viewerToken)
		if w.Code != 200 {
			t.Fatalf("%s detail status=%d body=%s", tc.id, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.Scope) != 1 || detail.Scope[0] != tc.scope {
			t.Fatalf("%s detail scope=%+v err=%v", tc.id, detail.Scope, err)
		}
	}
	assertError(request("/api/v1/roles/partial-role", "dev", viewerToken), 409, "matrix_editor_required")
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	assertError(request("/api/v1/roles/team-role", "dev", viewerToken), 404, "not_found")
	assertError(request("/api/v1/roles/app-role", "dev", viewerToken), 404, "not_found")
	w = request("/api/v1/roles", "all", adminToken)
	if w.Code != 200 {
		t.Fatalf("global list status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	globalIDs := map[string]bool{}
	for _, item := range listed.Items {
		globalIDs[item.ID] = true
	}
	if !globalIDs["test-role"] {
		t.Fatalf("global manager could not inspect test-scoped role: %+v", listed.Items)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='roles' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assertError(request("/api/v1/roles", "dev", viewerToken), 403, "forbidden")
}
