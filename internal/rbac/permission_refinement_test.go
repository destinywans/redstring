package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPermissionCreationCatalog(t *testing.T) {
	want := []string{"view", "update", "approve", "delete"}
	if len(Actions) != len(want) {
		t.Fatal(Actions)
	}
	for i, action := range Actions {
		if action["id"] != want[i] || action["scope_type"] != "" {
			t.Fatal(action)
		}
	}
}

func binding(scope, id string, actions ...string) ResourceBinding {
	return ResourceBinding{ResourceTarget: ResourceTarget{ScopeType: scope, ResourceID: id}, Actions: actions}
}
func TestPermissionResourceActionsPostgres(t *testing.T) {
	st := isolatedDB(t)
	s := &Service{Store: st}
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	p, err := s.SavePermission(ctx, Permission{Name: "Mixed resources", Description: "Named bundle"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ResourceCount != 0 {
		t.Fatal("empty permission gained resources", p)
	}
	if _, err = s.SavePermission(ctx, Permission{Name: " Mixed resources "}); !errors.Is(err, ErrPermissionNameConflict) {
		t.Fatal("duplicate name", err)
	}
	role, err := s.SaveRole(ctx, Role{Name: "Mixed-resource role"})
	if err != nil {
		t.Fatal(err)
	}
	role, err = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Permissions: Delta{Add: []string{p.ID}}, Users: Delta{Add: []string{"v"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "view", "resource", "r1"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("unbound permission authorized", err)
	}
	api := httpapi.New(st, nil)
	s.Register(api)
	if _, err = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'a',now()+interval '1 hour')", store.Hash("binding-test")); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "binding-test"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/api/v1/rbac/permissions", `{"name":"Plain API permission","description":"No implicit operation"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var raw map[string]any
	if json.Unmarshal(w.Body.Bytes(), &raw) != nil {
		t.Fatal(w.Body.String())
	}
	for _, key := range []string{"action", "scope_type", "resource_scope_mode"} {
		if _, ok := raw[key]; ok {
			t.Fatal("permission still has operation field", key)
		}
	}
	w = request("POST", "/api/v1/rbac/permissions", `{"name":"Invalid old API","action":"view","scope_type":"resource"}`)
	if w.Code != 422 {
		t.Fatal("obsolete operation accepted", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/rbac/permissions/"+p.ID+"/resource-tree?limit=1", "")
	var tree struct {
		Revision int64               `json:"revision"`
		Groups   []ResourceTreeGroup `json:"groups"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tree) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	found := false
	for _, g := range tree.Groups {
		if g.Kind == "resource:host" {
			found = g.Total == 2 && g.HasMore && len(g.Items) == 1
			for _, item := range g.Items {
				if len(item.AssignedActions) != 0 {
					t.Fatal("empty bundle selected tree leaf")
				}
			}
		}
	}
	if !found {
		t.Fatal("grouped load-more tree", tree)
	}
	p, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r1", "view", "update"), binding("application", "app2", "approve")}})
	if err != nil || p.ResourceCount != 2 {
		t.Fatal("mixed actions", p, err)
	}
	for _, action := range []string{"view", "update"} {
		if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, action, "resource", "r1"); err != nil {
			t.Fatal("resource action ineffective", action, err)
		}
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "delete", "resource", "r1"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("unselected resource action allowed", err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app2", "approve"); err != nil {
		t.Fatal("approval does not reach real application guard", err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app2", "execute"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("approval implied execution", err)
	}
	for _, invalid := range []ResourceDelta{
		{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r2", "view", "view")}},
		{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r2", "approve")}},
		{Revision: p.Revision, Upsert: []ResourceBinding{binding("application", "app1", "execute")}},
		{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r2", "view"), binding("resource", "missing", "view")}},
		{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r2", "view")}, Remove: []ResourceTarget{{ScopeType: "resource", ResourceID: "r2"}}},
	} {
		if _, err = s.SaveResources(ctx, p.ID, invalid); !errors.Is(err, store.ErrInvalidReference) {
			t.Fatal("invalid binding accepted", invalid, err)
		}
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "view", "resource", "r2"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("partial invalid batch committed", err)
	}
	if _, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision - 1, Upsert: []ResourceBinding{binding("resource", "r1", "view")}}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale overwrite", err)
	}
	p, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r1", "update")}})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app2", "approve"); err != nil {
		t.Fatal("unloaded application binding overwritten", err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "view", "resource", "r1"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("removed operation remained", err)
	}
	w = request("GET", "/api/v1/rbac/permissions/"+p.ID+"/resource-tree?kind=resource:host&limit=1&offset=1", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tree) != nil || len(tree.Groups) != 1 || tree.Groups[0].HasMore || tree.Groups[0].Offset != 1 {
		t.Fatal("load more", w.Code, w.Body.String())
	}
	w = request("GET", "/api/v1/rbac/permissions/"+p.ID+"/resource-tree?q=Two", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tree) != nil {
		t.Fatal("tree search", w.Code, w.Body.String())
	}
	for _, g := range tree.Groups {
		for _, item := range g.Items {
			if !strings.Contains(item.Name, "Two") {
				t.Fatal("search mismatch", item)
			}
		}
	}
	p, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Remove: []ResourceTarget{{ScopeType: "resource", ResourceID: "r1"}, {ScopeType: "application", ResourceID: "app2"}}})
	if err != nil || p.ResourceCount != 0 {
		t.Fatal("clear bindings", p, err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "v"}, "app2", "approve"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("empty permission still authorizes", err)
	}
}

func TestPermissionMigrationAndHistoryPostgres(t *testing.T) {
	st := isolatedDB(t)
	s := &Service{Store: st}
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	legacy, err := s.Permission(ctx, "legacy-view")
	if err != nil || legacy.ResourceCount != 2 {
		t.Fatal(legacy, err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "u"}, "app1", "execute"); err != nil {
		t.Fatal("historical execution lost", err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "u"}, "app2", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("legacy ACL broadened", err)
	}
	if _, err = st.DB.Exec(ctx, "INSERT INTO applications(id,key,name) VALUES('future','future','Future')"); err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "a"}, "future", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("snapshot auto-expanded", err)
	}
	if _, err = s.SaveResources(ctx, legacy.ID, ResourceDelta{Revision: legacy.Revision, Upsert: []ResourceBinding{binding("legacy_application", "future", "view")}}); !errors.Is(err, store.ErrInvalidReference) {
		t.Fatal("new legacy association allowed", err)
	}
	legacy, err = s.SaveResources(ctx, legacy.ID, ResourceDelta{Revision: legacy.Revision, Upsert: []ResourceBinding{binding("legacy_application", "app1", "view")}})
	if err != nil {
		t.Fatal("historical action not preserved", err)
	}
	legacy, err = s.SaveResources(ctx, legacy.ID, ResourceDelta{Revision: legacy.Revision, Remove: []ResourceTarget{{ScopeType: "legacy_application", ResourceID: "app1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeApplication(ctx, core.User{ID: "a"}, "app1", "view"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("legacy removal ineffective", err)
	}
	management, err := s.Permission(ctx, "manage-rbac")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveResources(ctx, management.ID, ResourceDelta{Revision: management.Revision, Remove: []ResourceTarget{{ScopeType: "management", ResourceID: "rbac"}}}); !errors.Is(err, ErrLastManager) {
		t.Fatal("last manager lost", err)
	}
	if _, err = s.SaveResources(ctx, management.ID, ResourceDelta{Revision: management.Revision, Upsert: []ResourceBinding{binding("management", "rbac", "manage")}}); err != nil {
		t.Fatal("preserving historical management failed", err)
	}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	legacy, err = s.Permission(ctx, "legacy-view")
	if err != nil || legacy.ResourceCount != 1 {
		t.Fatal("migration reset explicit grants", legacy, err)
	}
}
