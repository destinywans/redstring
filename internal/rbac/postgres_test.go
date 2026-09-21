package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/resources"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/migrations"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func isolatedDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("invalid test database configuration")
	}
	schema := "rbac_test_" + store.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error("isolated schema cleanup failed")
		}
	})
	// Deliberately establish pre-RBAC data to exercise the real migration.
	_, e = db.Exec(ctx, "CREATE TABLE schema_migrations(name text PRIMARY KEY)")
	if e != nil {
		t.Fatal(e)
	}
	entries, _ := migrations.Files.ReadDir(".")
	for _, f := range entries {
		if !strings.HasSuffix(f.Name(), ".sql") || f.Name() >= "014" {
			continue
		}
		raw, _ := migrations.Files.ReadFile(f.Name())
		if _, e = db.Exec(ctx, string(raw)); e != nil {
			t.Fatal(f.Name(), e)
		}
		if _, e = db.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", f.Name()); e != nil {
			t.Fatal(e)
		}
	}
	_, e = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,department) VALUES('a','a@test.invalid','Admin','admin','unused',''),('u','u@test.invalid','Operator','operator','unused','Dept 2'),('v','v@test.invalid','Viewer','viewer','unused','');
 INSERT INTO users(id,email,name,role,password_hash,disabled,source,external_id) VALUES('mock','mock@test.invalid','Mock','admin','',true,'settings_demo','mock');
 INSERT INTO teams(id,name) VALUES('d1','Dept 1'),('d2','Dept 2'); INSERT INTO team_members VALUES('d1','u'),('d2','u');
 INSERT INTO applications(id,key,name) VALUES('app1','app1','One'),('app2','app2','Two');
 INSERT INTO application_grants VALUES('app1','team','d1','operate');
 INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('r1','host','Host One','manual'),('r2','host','Host Two','manual');`)
	if e != nil {
		t.Fatal(e)
	}
	st := &store.Store{DB: db}
	if e = st.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return st
}
func TestRBACPostgresBoundaries(t *testing.T) {
	st := isolatedDB(t)
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	s := &Service{Store: st}
	var dep string
	var count int
	if e := st.DB.QueryRow(ctx, "SELECT department_id FROM users WHERE id='u'").Scan(&dep); e != nil || dep != "d2" {
		t.Fatalf("department migration: %q %v", dep, e)
	}
	if e := st.AuthorizeApplication(ctx, core.User{ID: "u"}, "app1", "execute"); e != nil {
		t.Fatalf("lost legacy ACL during single-department migration: %v", e)
	}
	if e := st.AuthorizeApplication(ctx, core.User{ID: "u"}, "app2", "view"); !errors.Is(e, store.ErrForbidden) {
		t.Fatalf("expanded migration access: %v", e)
	}
	if _, e := st.DB.Exec(ctx, "INSERT INTO team_members VALUES('d1','u')"); e == nil {
		t.Fatal("second department accepted")
	}
	if e := st.DB.QueryRow(ctx, "SELECT count(*) FROM rbac_user_roles WHERE user_id='mock'").Scan(&count); e != nil || count != 0 {
		t.Fatal("mock granted", count, e)
	}
	if _, e := st.DB.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES('later','later@test.invalid','Later','admin','unused')"); e != nil {
		t.Fatal(e)
	}
	if e := st.AuthorizeCapability(ctx, core.User{ID: "later"}, "manage", "management", "rbac"); e != nil {
		t.Fatal("post-migration bootstrap not granted", e)
	}
	p, e := s.SavePermission(ctx, Permission{Name: "Only execute app2"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = st.DB.Exec(ctx, "INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES($1,'application','app2','execute')", p.ID)
	if e != nil {
		t.Fatal(e)
	}
	role, e := s.SaveRole(ctx, Role{Name: "Recovery executor"})
	if e != nil {
		t.Fatal(e)
	}
	role, e = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Permissions: Delta{Add: []string{p.ID}}, Users: Delta{Add: []string{"v"}}, Departments: Delta{Add: []string{"d2"}}})
	if e != nil {
		t.Fatal(e)
	}
	for _, user := range []string{"v", "u"} {
		if e = st.AuthorizeApplication(ctx, core.User{ID: user}, "app2", "execute"); e != nil {
			t.Fatal("missing direct/inherited execute", user, e)
		}
		for _, a := range []string{"approve", "investigate"} {
			if e = st.AuthorizeApplication(ctx, core.User{ID: user}, "app2", a); !errors.Is(e, store.ErrForbidden) {
				t.Fatal("action leaked", user, a, e)
			}
		}
	}
	// Revocation is read on every authorization, without session refresh.
	role, e = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Users: Delta{Remove: []string{"v"}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app2", "execute"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("stale privilege", e)
	}
	if _, e = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision - 1, Users: Delta{Add: []string{"v"}}}); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stale revision accepted", e)
	}
	if _, e = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Users: Delta{Add: []string{"v"}}, Permissions: Delta{Add: []string{"missing"}}}); !errors.Is(e, store.ErrInvalidReference) {
		t.Fatal("unknown accepted", e)
	}
	if e = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app2", "execute"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("partial delta committed", e)
	}
	if e = s.Delete(ctx, "roles", role.ID, role.Revision-1); !errors.Is(e, store.ErrConflict) {
		t.Fatal("dependent role deleted", e)
	}
	if _, e = s.SaveAssociations(ctx, "admin", Associations{Revision: 1, Users: Delta{Remove: []string{"a"}}}); !errors.Is(e, ErrLegacyMembership) {
		t.Fatal("legacy admin removed", e)
	}
	if e = s.Delete(ctx, "permissions", p.ID, p.Revision-1); !errors.Is(e, store.ErrConflict) {
		t.Fatal("referenced permission deleted", e)
	}
	rp, e := s.SavePermission(ctx, Permission{Name: "Only one resource"})
	if e != nil {
		t.Fatal(e)
	}
	rp, e = s.SaveResources(ctx, rp.ID, ResourceDelta{Revision: rp.Revision, Upsert: []ResourceBinding{{ResourceTarget: ResourceTarget{ScopeType: "resource", ResourceID: "r1"}, Actions: []string{"view"}}}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveResources(ctx, rp.ID, ResourceDelta{Revision: rp.Revision, Upsert: []ResourceBinding{{ResourceTarget: ResourceTarget{ScopeType: "resource", ResourceID: "r2"}, Actions: []string{"view"}}, {ResourceTarget: ResourceTarget{ScopeType: "resource", ResourceID: "unknown"}, Actions: []string{"view"}}}}); !errors.Is(e, store.ErrInvalidReference) {
		t.Fatal("unknown resource", e)
	}
	if e = st.DB.QueryRow(ctx, "SELECT count(*) FROM rbac_permission_resources WHERE permission_id=$1", rp.ID).Scan(&count); e != nil || count != 1 {
		t.Fatal("resource partial commit", count, e)
	}
	role, e = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Permissions: Delta{Add: []string{rp.ID}}, Users: Delta{Add: []string{"v"}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "view", "resource", "r1"); e != nil {
		t.Fatal(e)
	}
	if e = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "view", "resource", "r2"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("resource scope expanded", e)
	}
	// Exercise DB-backed count/LIMIT plus draft-selected filtering across pages.
	api := httpapi.New(st, nil)
	s.Register(api)
	resources.New(st.DB).Register(api)
	_, e = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'a',now()+interval '1 hour')", store.Hash("rbac-test"))
	if e != nil {
		t.Fatal(e)
	}
	get := func(path string) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "rbac-test"})
		rr := httptest.NewRecorder()
		api.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		var out map[string]json.RawMessage
		if e = json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	out := get("/api/v1/rbac/roles?page=2&page_size=2")
	if string(out["page"]) != "2" || string(out["total"]) != "7" {
		t.Fatal("server paging", out)
	}
	out = get("/api/v1/rbac/roles/" + role.ID + "/associations/users?selected=yes&add_ids=a&remove_ids=v&page_size=1")
	if string(out["total"]) != "1" {
		t.Fatal("draft count wrong", string(out["total"]))
	}
	var items []Candidate
	if e = json.Unmarshal(out["items"], &items); e != nil {
		t.Fatal(e)
	}
	if len(items) != 1 || items[0].ID != "a" || items[0].Selected {
		t.Fatal("draft corrupted stored selection", items)
	}
	get("/api/v1/rbac/permissions/" + rp.ID + "/resources?kind=host&page_size=1")
	get("/api/v1/rbac/permissions/" + p.ID + "/resources")
	get("/api/v1/rbac/permissions/manage-rbac/resources")
	get("/api/v1/rbac/permissions/legacy-view/resources")
	get("/api/v1/rbac/roles/" + role.ID + "/associations/departments")
	get("/api/v1/rbac/roles/" + role.ID + "/associations/permissions")
	get("/api/v1/rbac/permissions?page_size=2&action=manage")
	get("/api/v1/rbac/departments?page_size=1")
	// A users manager cannot assign legacy roles or take over another privileged account.
	role, e = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Permissions: Delta{Add: []string{"manage-users"}}})
	if e != nil {
		t.Fatal(e)
	}
	managerCtx := store.WithPrincipal(context.Background(), core.User{ID: "v", Role: "viewer"})
	// Joining, removing and transferring from a role-bearing department all
	// require RBAC management, independent of ordinary department administration.
	for _, change := range []struct {
		department string
		users      []string
	}{{"d2", []string{"u", "v"}}, {"d2", []string{}}, {"d1", []string{"u"}}} {
		if e = st.SetTeamMembers(managerCtx, change.department, change.users); !errors.Is(e, store.ErrForbidden) {
			t.Fatal("department manager changed inherited grants", change.department, e)
		}
	}
	adminRole := "admin"
	newPassword := "a-different-secret-password"
	if _, e = st.UpdateMember(managerCtx, "v", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Role: &adminRole}}); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("users manager self-promoted", e)
	}
	if _, e = st.CreateMember(managerCtx, store.MemberCreate{Email: "elevated@test.invalid", Name: "Not allowed", Role: "admin", Password: newPassword}); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("users manager created admin", e)
	}
	if _, e = st.UpdateMember(managerCtx, "a", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Password: &newPassword}}); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("users manager reset privileged password", e)
	}
	if e = st.DeleteMember(ctx, "v"); e != nil {
		t.Fatal(e)
	}
	role, e = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Users: Delta{Remove: []string{"v"}}})
	if e != nil {
		t.Fatal("deleted principal binding cannot be removed", e)
	}
	req := httptest.NewRequest("DELETE", "/api/v1/resources/r1", nil)
	req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "rbac-test"})
	rr := httptest.NewRecorder()
	api.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal("resource delete", rr.Code, rr.Body.String())
	}
	if e = st.DB.QueryRow(ctx, "SELECT count(*) FROM rbac_permission_resources WHERE resource_id='r1'").Scan(&count); e != nil || count != 0 {
		t.Fatal("resource delete left bindings", count, e)
	}
	rp, e = s.Permission(ctx, rp.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveResources(ctx, rp.ID, ResourceDelta{Revision: rp.Revision, Upsert: []ResourceBinding{{ResourceTarget: ResourceTarget{ScopeType: "resource", ResourceID: "r1"}, Actions: []string{"view"}}}}); !errors.Is(e, store.ErrInvalidReference) {
		t.Fatal("deleted resource can be rebound", e)
	}
	if _, e = st.DB.Exec(ctx, "INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES($1,'resource','old-orphan','view')", rp.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveResources(ctx, rp.ID, ResourceDelta{Revision: rp.Revision, Remove: []ResourceTarget{{ScopeType: "resource", ResourceID: "old-orphan"}}}); e != nil {
		t.Fatal("stale binding cannot be removed", e)
	}
	get("/api/v1/rbac/me")
	get("/api/v1/rbac/catalog")

}
