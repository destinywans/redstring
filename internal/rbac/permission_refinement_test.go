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
	"github.com/wans/aiops-canvas/internal/store"
)

func TestPermissionCreationCatalog(t *testing.T) {
	want := []string{"view", "update", "approve", "delete"}
	if len(Actions) != len(want) {
		t.Fatalf("creation actions: %#v", Actions)
	}
	for i, action := range Actions {
		if action["id"] != want[i] || !creatablePermission(Permission{Name: "Permission", Action: action["id"], ScopeType: action["scope_type"]}) {
			t.Fatalf("creation action %d: %#v", i, action)
		}
	}
	for _, action := range ExistingActions {
		if !validPermission(Permission{Name: "Existing", Action: action["id"], ScopeType: action["scope_type"]}) {
			t.Fatal("existing runtime action lost", action)
		}
	}
}

func TestPermissionRefinementPostgres(t *testing.T) {
	st := isolatedDB(t)
	s := &Service{Store: st}
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	permission, err := s.SavePermission(ctx, Permission{Name: "Unique permission", Action: "view", ScopeType: "resource"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SavePermission(ctx, Permission{Name: " Unique permission ", Action: "view", ScopeType: "resource"}); !errors.Is(err, ErrPermissionNameConflict) {
		t.Fatalf("duplicate create: %v", err)
	}
	other, err := s.SavePermission(ctx, Permission{Name: "Other permission", Action: "view", ScopeType: "resource"})
	if err != nil {
		t.Fatal(err)
	}
	other.Name = permission.Name
	if _, err = s.SavePermission(ctx, other); !errors.Is(err, ErrPermissionNameConflict) {
		t.Fatalf("duplicate rename: %v", err)
	}
	w := httptest.NewRecorder()
	respond(w, nil, ErrPermissionNameConflict)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "permission_name_conflict") {
		t.Fatal("unclear name conflict", w.Body.String())
	}

	api := httpapi.New(st, nil)
	s.Register(api)
	if _, err = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'a',now()+interval '1 hour')", store.Hash("permission-refinement-test")); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "permission-refinement-test"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, pair := range [][2]string{{"investigate", "application"}, {"execute", "application"}, {"manage", "management"}} {
		w = request("POST", "/api/v1/rbac/permissions", `{"name":"new hidden action","action":"`+pair[0]+`","scope_type":"`+pair[1]+`"}`)
		if w.Code != 422 {
			t.Fatalf("hidden action created through API: %d %s", w.Code, w.Body.String())
		}
	}
	legacy, err := s.Permission(ctx, "legacy-view")
	if err != nil || legacy.ResourceScopeMode != "legacy_acl" || legacy.ResourceCount != 2 {
		t.Fatalf("legacy default: %#v %v", legacy, err)
	}
	w = request("GET", "/api/v1/rbac/permissions/legacy-view/resources?selected=yes", "")
	var candidates struct {
		Items []Candidate `json:"items"`
		Total int         `json:"total"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &candidates) != nil || candidates.Total != legacy.ResourceCount {
		t.Fatalf("legacy candidates: %d %s", w.Code, w.Body.String())
	}
	for _, c := range candidates.Items {
		if !c.Selected {
			t.Fatal("implicit scope candidate not selected")
		}
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "u"}, "app2", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("migration broadened original ACL", err)
	}
	legacy, err = s.SaveResources(ctx, legacy.ID, ResourceDelta{Revision: legacy.Revision, Delta: Delta{Remove: []string{"app1"}}})
	if err != nil || legacy.ResourceScopeMode != "explicit" || legacy.ResourceCount != 1 {
		t.Fatalf("legacy narrow: %#v %v", legacy, err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "a"}, "app1", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("removed target still allowed", err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "a"}, "app2", "view"); err != nil {
		t.Fatal("remaining admin target lost", err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "u"}, "app2", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("selected application bypassed original ACL", err)
	}
	if _, err = st.DB.Exec(ctx, "INSERT INTO applications(id,key,name) VALUES('new-app','new-app','New application')"); err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "a"}, "new-app", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("explicit scope auto-expanded", err)
	}
	if _, err = s.SaveResources(ctx, legacy.ID, ResourceDelta{Revision: legacy.Revision - 1, Delta: Delta{Add: []string{"app1"}}}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale edit accepted", err)
	}
	legacy, err = s.SaveResources(ctx, legacy.ID, ResourceDelta{Revision: legacy.Revision, Delta: Delta{Remove: []string{"app2"}}})
	if err != nil || legacy.ResourceCount != 0 {
		t.Fatal("clear explicit scope", legacy, err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "a"}, "app2", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("empty scope reverted to legacy", err)
	}
	legacy, err = s.SaveResources(ctx, legacy.ID, ResourceDelta{Revision: legacy.Revision, Delta: Delta{Add: []string{"app1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "u"}, "app1", "view"); err != nil {
		t.Fatal("re-added allowed ACL target lost", err)
	}

	resource, err := s.Permission(ctx, "resource-view")
	if err != nil {
		t.Fatal(err)
	}
	resource, err = s.SaveResources(ctx, resource.ID, ResourceDelta{Revision: resource.Revision, Delta: Delta{Remove: []string{"r1"}}})
	if err != nil {
		t.Fatal("builtin resource cannot edit", err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "a"}, "view", "resource", "r1"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("builtin resource revoke ineffective", err)
	}
	management, err := s.Permission(ctx, "manage-rbac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveResources(ctx, management.ID, ResourceDelta{Revision: management.Revision, Delta: Delta{Remove: []string{"rbac"}}}); !errors.Is(err, ErrLastManager) {
		t.Fatal("builtin management lost last manager", err)
	}
	management, err = s.Permission(ctx, "manage-users")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveResources(ctx, management.ID, ResourceDelta{Revision: management.Revision, Delta: Delta{Remove: []string{"users"}, Add: []string{"teams"}}}); err != nil {
		t.Fatal("builtin management cannot edit", err)
	}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal("migration replay", err)
	}
	legacy, err = s.Permission(ctx, "legacy-view")
	if err != nil || legacy.ResourceScopeMode != "explicit" || legacy.ResourceCount != 1 {
		t.Fatal("migration reset managed scope", legacy, err)
	}
}
