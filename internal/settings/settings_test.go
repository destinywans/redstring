package settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRolesPreserveOperatorApproval(t *testing.T) {
	roles := Roles()
	if !roles[1].Permissions["approve"] || !roles[1].Permissions["execute"] || roles[1].Permissions["manage"] || roles[2].Permissions["approve"] {
		t.Fatal("role matrix changed authorization semantics")
	}
}

func TestPostgresSettingsBoundaries(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := "settings_test_" + store.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	db := &store.Store{DB: pool}
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES ('admin','admin@example.invalid','Admin','admin','unused'),('viewer','viewer@example.invalid','Viewer','viewer','unused');
 INSERT INTO users(id,email,name,role,password_hash,disabled,source,external_id,department) VALUES('mock','mock@example.invalid','Mock','admin','',true,'settings_demo','mock','平台工程部');
 INSERT INTO teams(id,name) VALUES('team','Example team');
 INSERT INTO team_members VALUES('team','mock');
 INSERT INTO settings_team_profiles(team_id,is_mock) VALUES('team',true);
 INSERT INTO applications(id,key,name,owner_team_id) VALUES('app','app','App','team');
 INSERT INTO application_grants VALUES('app','team','team','view');
 INSERT INTO management_audit(actor_id,action,target,outcome,created_at) VALUES('admin','account.created','viewer','success','2026-01-02T03:04:05Z');`); e != nil {
		t.Fatal(e)
	}
	for _, statement := range []string{`UPDATE users SET disabled=false WHERE id='mock'`, `UPDATE users SET password_hash='hash' WHERE id='mock'`} {
		if _, e = pool.Exec(ctx, statement); e == nil {
			t.Fatal("mock login constraint missing")
		}
	}
	disabled := true
	if _, e = db.UpdateMember(ctx, "admin", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Disabled: &disabled}}); !errors.Is(e, store.ErrForbidden) {
		t.Fatalf("missing principal authorized privileged account mutation: %v", e)
	}
	actor, err := db.ActiveUser(ctx, "admin")
	if err != nil || actor.Role != "admin" {
		t.Fatalf("persisted administrator role fixture unavailable: role=%q err=%v", actor.Role, err)
	}
	adminCtx := store.WithPrincipal(ctx, actor)
	if _, e = db.UpdateMember(adminCtx, "admin", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Disabled: &disabled}}); !errors.Is(e, store.ErrLastAdmin) {
		t.Fatalf("mock admin weakened last-admin protection: %v", e)
	}
	for _, id := range []string{"admin", "viewer"} {
		if _, e = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, store.Hash("settings-test-"+id), id); e != nil {
			t.Fatal(e)
		}
	}
	api := httpapi.New(db, nil)
	service := New(db, "test")
	service.Register(api)
	request := func(user, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "settings-test-" + user})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		user, method, path, body string
		want                     int
	}{
		{"", "GET", "/api/v1/settings/overview", "", 401},
		{"viewer", "GET", "/api/v1/settings/directory", "", 403},
		{"viewer", "PATCH", "/api/v1/settings/overview", `{"revision":1,"name":"Oops"}`, 403},
		{"admin", "PATCH", "/api/v1/settings/overview", `{"revision":1,"owner_id":"mock"}`, 422},
		{"admin", "PATCH", "/api/v1/settings/overview", `{"revision":1,"language":"en"}`, 422},
		{"admin", "PATCH", "/api/v1/settings/overview", `{"revision":1,"timezone":"invalid/place"}`, 422},
		{"admin", "PATCH", "/api/v1/settings/overview", `{"revision":1,"name":"平台工作空间","description":"真实保存"}`, 200},
		{"admin", "PATCH", "/api/v1/settings/overview", `{"revision":1,"name":"Stale"}`, 409},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `{"owner_id":"viewer","department":"SRE"}`, 422},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `{"owner_id":"mock","department":"SRE"}`, 200},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `{}`, 422},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `null`, 422},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `{"department":"Changed"}`, 422},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `{"owner_id":"mock"}`, 422},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `{"owner_id":null,"department":"Changed"}`, 422},
		{"admin", "PATCH", "/api/v1/settings/teams/team/profile", `{"owner_id":"mock","department":null}`, 422},
	} {
		w := request(tc.user, tc.method, tc.path, tc.body)
		if w.Code != tc.want {
			t.Fatalf("%s %s: got %d want %d: %s", tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}
	w := request("viewer", "GET", "/api/v1/settings/overview", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var overview struct {
		Workspace    Workspace            `json:"workspace"`
		Integrations []IntegrationSummary `json:"integrations"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &overview); e != nil {
		t.Fatal(e)
	}
	if overview.Workspace.Name != "平台工作空间" || overview.Workspace.Revision != 2 || overview.Workspace.OwnerID != "admin" || overview.Workspace.OwnerName != "Admin" || overview.Integrations[1].State != "restricted" {
		t.Fatalf("overview incorrect: %#v", overview)
	}
	w = request("admin", "GET", "/api/v1/settings/directory", "")
	var d Directory
	if e = json.Unmarshal(w.Body.Bytes(), &d); e != nil {
		t.Fatal(e)
	}
	if w.Code != 200 || len(d.Members) != 3 || len(d.Teams) != 1 || !d.Teams[0].IsMock || d.Teams[0].OwnerID != "mock" || len(d.Teams[0].Applications) != 1 || d.Teams[0].Applications[0].Relationship != "owner_and_grant" {
		t.Fatalf("directory: %s", w.Body.String())
	}
	for _, m := range d.Members {
		if m.ID == "mock" && (!m.IsMock || !m.Disabled || len(m.TeamIDs) != 1) {
			t.Fatalf("mock projection: %#v", m)
		}
	}
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='admin' AND action IN ('workspace.updated','team.profile.updated')`).Scan(&count); e != nil || count != 2 {
		t.Fatalf("atomic audit count=%d err=%v", count, e)
	}
	// A description-only update must remain visible in the management audit.
	w = request("admin", "PATCH", "/api/v1/settings/overview", `{"revision":2,"description":"仅简介更新"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var description string
	if e = pool.QueryRow(ctx, `SELECT changes->>'description' FROM management_audit WHERE action='workspace.updated' ORDER BY id DESC LIMIT 1`).Scan(&description); e != nil || description != "仅简介更新" {
		t.Fatalf("description audit: %q %v", description, e)
	}
	// Deleted members and members removed from a team cannot remain effective owners.
	assertNoOwner := func() {
		t.Helper()
		d, e := service.Directory(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if d.Teams[0].OwnerID != "" || d.Teams[0].OwnerName != "" {
			t.Fatalf("stale team owner: %#v", d.Teams[0])
		}
	}
	if _, e = pool.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE id='mock'`); e != nil {
		t.Fatal(e)
	}
	assertNoOwner()
	deletedDirectory, err := service.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundDeleted := false
	for _, m := range deletedDirectory.Members {
		if m.ID == "mock" {
			foundDeleted = m.DeletedAt != nil
		}
	}
	adminMembers := -1
	for _, role := range deletedDirectory.Roles {
		if role.ID == "admin" {
			adminMembers = role.MemberCount
		}
	}
	if !foundDeleted || adminMembers != 1 {
		t.Fatalf("deleted members must be browseable but excluded from role counts: %#v", deletedDirectory)
	}
	for _, department := range deletedDirectory.Departments {
		if department.Name == "平台工程部" && department.MemberCount != 0 {
			t.Fatal("deleted member counted in department")
		}
	}
	if _, e = pool.Exec(ctx, `UPDATE users SET deleted_at=NULL WHERE id='mock'; DELETE FROM team_members WHERE team_id='team' AND user_id='mock'`); e != nil {
		t.Fatal(e)
	}
	assertNoOwner()
	// Execute the Python seed's exact SQL in this test schema, never the review schema.
	seed := func(apps string) {
		t.Helper()
		script := `import importlib.util,json,sys
spec=importlib.util.spec_from_file_location('settings_seed','../../scripts/seed-settings-demo.py')
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
print(module.build_seed_sql(json.loads(sys.argv[1])))`
		sql, e := exec.Command("python3", "-c", script, apps).Output()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(sql)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = pool.Exec(ctx, `INSERT INTO teams(id,name) VALUES('settings-demo-commerce','Existing mock team'); INSERT INTO settings_team_profiles(team_id,is_mock) VALUES('settings-demo-commerce',true); INSERT INTO team_members VALUES('settings-demo-commerce','viewer')`); e != nil {
		t.Fatal(e)
	}
	seed(`["app"]`)
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM application_grants WHERE subject_id LIKE 'settings-demo-%'`).Scan(&count); e != nil || count != 2 {
		t.Fatalf("first seed must skip team with real member: %d %v", count, e)
	}
	// A real member has one department. Move the fixture membership before
	// checking that a seed rerun cannot expand or restore application grants.
	if _, e = pool.Exec(ctx, `INSERT INTO applications(id,key,name) VALUES('new-app','aaa-new','New app'); DELETE FROM team_members WHERE user_id='viewer'; INSERT INTO team_members VALUES('settings-demo-infrastructure','viewer'); DELETE FROM application_grants WHERE subject_id='settings-demo-data'`); e != nil {
		t.Fatal(e)
	}
	seed(`["new-app","app"]`)
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM application_grants WHERE application_id='new-app' OR subject_id='settings-demo-data'`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("rerun must not add grants or resurrect removed grants: %d %v", count, e)
	}
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE source='settings_demo' AND id LIKE 'settings-demo-%'`).Scan(&count); e != nil || count != 8 {
		t.Fatalf("seed identities must remain idempotent: %d %v", count, e)
	}

	finalDirectory, e := service.Directory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	datedMock := 0
	for _, m := range finalDirectory.Members {
		if strings.HasPrefix(m.ID, "settings-demo-") && m.CreatedAt != nil {
			datedMock++
		}
		if m.ID == "admin" && m.CreatedAt != nil {
			t.Fatal("created date fabricated for bootstrap account")
		}
		if m.ID == "viewer" && (m.CreatedAt == nil || m.CreatedAt.UTC().Format("2006-01-02T15:04:05Z") != "2026-01-02T03:04:05Z") {
			t.Fatalf("account creation audit not projected: %#v", m)
		}
	}
	if datedMock != 8 {
		t.Fatalf("expected 8 seed-evidenced member dates, got %d", datedMock)
	}
	var ownerStillNull bool
	if e = pool.QueryRow(ctx, `SELECT owner_id IS NULL FROM workspace_settings WHERE id='default'`).Scan(&ownerStillNull); e != nil || !ownerStillNull {
		t.Fatal("fallback must not rewrite persisted ownership")
	}

}
