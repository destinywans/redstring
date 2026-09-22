package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/resources"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestInfrastructurePermissionsPostgres(t *testing.T) {
	st := isolatedDB(t)
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	// Explicit reset is test-local. Migration itself must not delete old definitions.
	var historical int
	if e := st.DB.QueryRow(ctx, "SELECT count(*) FROM rbac_permissions WHERE id='manage-rbac'").Scan(&historical); e != nil || historical != 1 {
		t.Fatal("migration unexpectedly erased data", e, historical)
	}
	_, err := st.DB.Exec(ctx, `DELETE FROM rbac_role_permissions; DELETE FROM rbac_user_roles; DELETE FROM rbac_department_roles; DELETE FROM rbac_permissions; DELETE FROM rbac_roles;
 INSERT INTO rbac_roles(id,name) VALUES('admin','平台管理员'),('operator','运维人员'),('viewer','只读成员'),('change-approver','变更审批员');
 INSERT INTO rbac_user_roles(user_id,role_id,origin) SELECT id,role,'legacy' FROM users WHERE source<>'settings_demo' AND deleted_at IS NULL;
 INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('app-record','application','Application metadata','manual');
 ALTER TABLE rbac_permission_resources VALIDATE CONSTRAINT rbac_infrastructure_binding;`)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: st}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "a"}, "manage", "management", "rbac"); err != nil {
		t.Fatal("actual admin cannot manage", err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "a"}, "view", "resource", "r1"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("admin gained unbound resource authority", err)
	}
	p, err := s.SavePermission(ctx, Permission{Name: "基础设施操作"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := s.SaveRole(ctx, Role{Name: "Selected targets"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Users: Delta{Add: []string{"v"}}, Permissions: Delta{Add: []string{p.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r1", "view", "update", "approve", "delete")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"view", "update", "approve", "delete"} {
		if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, action, "resource", "r1"); err != nil {
			t.Fatal(action, err)
		}
	}
	for _, b := range []ResourceBinding{binding("application", "app1", "approve"), binding("management", "rbac", "manage"), binding("resource", "app-record", "view"), binding("resource", "r1", "execute"), binding("resource", "r1", "investigate")} {
		if _, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{b}}); !errors.Is(err, store.ErrInvalidReference) {
			t.Fatal("retired binding accepted", b, err)
		}
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app1", "approve"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("resource approval leaked to entire app", err)
	}
	admin, err := s.Role(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "roles", admin.ID, admin.Revision); !errors.Is(err, store.ErrLastRBACManager) {
		t.Fatal("last administrator deleted", err)
	}
	admin, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision, Users: Delta{Add: []string{"v"}}, Departments: Delta{Add: []string{"d2"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"v", "u"} {
		user, e := st.ActiveUser(ctx, id)
		if e != nil || user.Role != "admin" {
			t.Fatal("associated platform role ineffective", user, e)
		}
	}
	admin, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision, Users: Delta{Remove: []string{"v"}}, Departments: Delta{Remove: []string{"d2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "manage", "management", "rbac"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("revoked admin still manages", err)
	}
	if _, err = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'a',now()+interval '1 hour')", store.Hash("infra-test")); err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil)
	s.Register(api)
	r := httptest.NewRequest("GET", "/api/v1/rbac/permissions/"+p.ID+"/resource-tree", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "infra-test"})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	var tree struct {
		Groups []ResourceTreeGroup `json:"groups"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tree) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, g := range tree.Groups {
		if !strings.HasPrefix(g.Kind, "resource:") || g.Kind == "resource:application" {
			t.Fatal(g)
		}
		for _, item := range g.Items {
			if len(item.AvailableActions) != 4 {
				t.Fatal(item)
			}
		}
	}
	// A matched real immutable target is authorized, not another deployment sharing its application.
	_, err = st.DB.Exec(ctx, `INSERT INTO application_environments(id,application_id,key,name) VALUES('env1','app1','production','Production');
 INSERT INTO cmdb_resources(id,kind,name,source_kind,source_id,external_id,attributes) VALUES('deploy1','deployment','checkout','kubernetes','connector1','uid1','{"namespace":"commerce"}');
 INSERT INTO cmdb_resource_applications VALUES('deploy1','app1','env1');`)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "deploy1", "approve")}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(uid, action string) error {
		return st.AuthorizeRecoveryTarget(ctx, "v", "app1", "env1", "connector1", "commerce", "checkout", uid, action)
	}
	if err = check("uid1", "approve"); err != nil {
		t.Fatal("exact approval rejected", err)
	}
	if err = check("uid2", "approve"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("wrong UID accepted", err)
	}
	if err = check("uid1", "update"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("approval implied execution", err)
	}
	// Runtime children inherit only a single discovered owner/object edge.
	p, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "deploy1", "view", "approve")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(ctx, `INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('pod-ok','pod','Owned Pod','manual'),('pod-unowned','pod','Unowned Pod','manual'),('event-ok','event','Owned event','manual'),('event-bad','event','Unrelated event','manual');
    INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind) VALUES
    ('owner','pod-ok','deploy1','belongs_to','discovered','test'),('object','event-ok','pod-ok','involved_object','discovered','test'),('not-owner','event-bad','deploy1','depends_on','discovered','test');
    INSERT INTO cmdb_resource_applications VALUES('r2','app1','env1');`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pod-ok", "event-ok"} {
		if _, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", id, "view")}}); !errors.Is(err, store.ErrInvalidReference) {
			t.Fatal("runtime record accepted standalone binding", id, err)
		}
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app1", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("one target broadened app read", err)
	}
	if err = check("uid1", "approve"); err != nil {
		t.Fatal("exact approval wrongly requires app-wide access", err)
	}
	if _, err = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'v',now()+interval '1 hour')", store.Hash("runtime-test")); err != nil {
		t.Fatal(err)
	}
	resources.New(st.DB).Register(api)
	r = httptest.NewRequest("GET", "/api/v1/resources?page_size=100", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "runtime-test"})
	w = httptest.NewRecorder()
	api.ServeHTTP(w, r)
	var runtimePage resources.Page
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &runtimePage) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	visible := map[string]bool{}
	for _, item := range runtimePage.Items {
		visible[item.ID] = true
	}
	if !visible["pod-ok"] || !visible["event-ok"] || visible["pod-unowned"] || visible["event-bad"] || visible["r2"] {
		t.Fatal("runtime inheritance leaked or lost records", visible)
	}
	if _, err = st.DB.Exec(ctx, "UPDATE cmdb_resources SET is_mock=true WHERE id='deploy1'"); err != nil {
		t.Fatal(err)
	}
	if err = check("uid1", "approve"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("mock allowed real approval", err)
	}
	if err = s.Delete(ctx, "permissions", p.ID, p.Revision); err != nil {
		t.Fatal("resource permission unexpectedly protects platform admin", err)
	}
}

func TestInfrastructureFreshInstallPostgres(t *testing.T) {
	st := isolatedDBSeeded(t, false)
	ctx := context.Background()
	var roles, permissions, bindings int
	if err := st.DB.QueryRow(ctx, "SELECT (SELECT count(*) FROM rbac_roles),(SELECT count(*) FROM rbac_permissions),(SELECT count(*) FROM rbac_permission_resources)").Scan(&roles, &permissions, &bindings); err != nil || roles != 4 || permissions != 4 || bindings != 0 {
		t.Fatal(roles, permissions, bindings, err)
	}
	if _, err := st.DB.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES('a','a@test.invalid','Admin','admin','hash')"); err != nil {
		t.Fatal(err)
	}
	ctx = store.WithPrincipal(ctx, core.User{ID: "a", Role: "admin"})
	s := &Service{Store: st}
	role, err := s.Role(ctx, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	role.Name = "Updated default"
	role, err = s.SaveRole(ctx, role)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveRole(ctx, Role{Name: role.Name}); !errors.Is(err, ErrRoleNameConflict) {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "roles", role.ID, role.Revision); err != nil {
		t.Fatal(err)
	}
	p, err := s.Permission(ctx, "resource-view")
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "Updated permission"
	p, err = s.SavePermission(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SavePermission(ctx, Permission{Name: p.Name}); !errors.Is(err, ErrPermissionNameConflict) {
		t.Fatal(err)
	}
	before, err := s.Role(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "permissions", p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	after, err := s.Role(ctx, "admin")
	if err != nil || after.Revision <= before.Revision {
		t.Fatal("role revision not invalidated", after, err)
	}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Role(ctx, "viewer"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("startup reseeded deleted role", err)
	}
	if _, err = s.Permission(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("startup reseeded deleted permission", err)
	}
}
