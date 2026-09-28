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

func TestMembersHTTPRespectsV3ScopeAndShowsDisabledAssignments(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_members_test_" + store.ID()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	actor, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Directory Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES
 ('member-reader','Member Reader'),('dev-member','Dev Member'),('test-member','Test Member'),
 ('team-member','Team Member'),('app-member','App Member')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('member-reader','environment','view','environment','dev'),
 ('member-reader','members','view','environment','dev'),
 ('dev-member','services','view','environment','dev'),
 ('test-member','services','view','environment','test'),
 ('team-member','services','view','team','ops'),
 ('app-member','services','view','application','app')`,
		`INSERT INTO teams(id,name) VALUES('ops','运维部')`,
		`INSERT INTO applications(id,name,key,environment,owner_team_id) VALUES('app','App','app','dev','ops')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO users(id,email,name,role,password_hash,department_id,disabled) VALUES
 ('dev-user','dev@example.invalid','张明','viewer','',NULL,false),
 ('disabled-user','disabled@example.invalid','停用成员','viewer','','ops',true),
 ('team-user','team@example.invalid','团队成员','viewer','','ops',false),
 ('department-user','department@example.invalid','部门成员','viewer','','ops',false),
 ('app-user','app@example.invalid','应用成员','viewer','',NULL,false),
 ('test-user','test@example.invalid','测试成员','viewer','',NULL,false)`,
		`INSERT INTO rbac_department_roles(department_id,role_id) VALUES('ops','team-member')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES
 ('dev-user','dev-member'),('dev-user','test-member'),
 ('disabled-user','dev-member'),('team-user','team-member'),
 ('app-user','app-member'),('test-user','test-member')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'member-reader')`, actor.ID); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(path, environment, session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d body=%s; want %d", w.Code, w.Body.String(), status)
		}
	}
	assertStatus(request("/api/v1/members", "dev", ""), 401)
	assertStatus(request("/api/v1/members", "test", token), 403)
	assertStatus(request("/api/v1/members?filter=unknown", "dev", token), 422)
	w := request("/api/v1/members", "dev", token)
	assertStatus(w, 200)
	var view membersView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	byID := map[string]member{}
	for _, item := range view.Items {
		byID[item.ID] = item
	}
	if len(byID) != 7 || byID["test-user"].ID != "" || byID["team-user"].ID == "" || byID["department-user"].ID == "" || byID["app-user"].ID == "" ||
		byID["disabled-user"].Status != "disabled" || view.Counts["disabled"] != 1 {
		t.Fatalf("incorrect scoped members: %+v, counts=%+v", view.Items, view.Counts)
	}
	if len(byID["dev-user"].Roles) != 1 || byID["dev-user"].Roles[0] != "Dev Member" || byID["dev-user"].MFA != "na" {
		t.Fatalf("leaked cross-environment role or guessed MFA: %+v", byID["dev-user"])
	}
	w = request("/api/v1/members?filter=disabled", "dev", token)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Items) != 1 || view.Items[0].ID != "disabled-user" {
		t.Fatalf("disabled filter body=%s err=%v", w.Body.String(), err)
	}
	w = request("/api/v1/members?q=zhangming", "dev", token)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Items) != 1 || view.Items[0].ID != "dev-user" {
		t.Fatalf("pinyin filter body=%s err=%v", w.Body.String(), err)
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	w = request("/api/v1/members", "dev", token)
	assertStatus(w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	for _, item := range view.Items {
		if item.ID == "app-user" || item.ID == "team-user" || item.ID == "department-user" {
			t.Fatalf("archived app still exposed member %s", item.ID)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='member-reader' AND module='members'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(request("/api/v1/members", "dev", token), 403)
}
