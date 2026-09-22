package rbac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Action and kind must match the same binding, not two different resources in a
// permission bundle. This exercises the server's total and paginated items.
func TestPermissionListFiltersResourceOperation(t *testing.T) {
	st := isolatedDB(t)
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	s := &Service{Store: st}
	p, err := s.SavePermission(ctx, Permission{Name: "filter-bundle"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 0 || p.CreatedAt == nil || p.UpdatedAt == nil {
		t.Fatal("new permission projection", p)
	}
	if _, err = st.DB.Exec(ctx, "INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('filter-mysql','mysql','Filter MySQL','manual')"); err != nil {
		t.Fatal(err)
	}
	p, err = s.SaveResources(ctx, p.ID, ResourceDelta{Revision: p.Revision, Upsert: []ResourceBinding{binding("resource", "r1", "view"), binding("resource", "filter-mysql", "update")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 2 || p.Actions[0] != "view" || p.Actions[1] != "update" || p.UpdatedAt == nil || !p.UpdatedAt.After(*p.CreatedAt) {
		t.Fatal("binding projection/timestamp", p)
	}
	if _, err = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'a',now()+interval '1 hour')", store.Hash("permission-filter-test")); err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil)
	s.Register(api)
	for _, tc := range []struct {
		action string
		total  int
	}{{"view", 0}, {"update", 1}, {"view,update", 1}, {"view&action=update", 1}, {"%20update%20,update", 1}, {"view,approve", 0}} {
		r := httptest.NewRequest("GET", "/api/v1/rbac/permissions?q=filter-bundle&resource_kind=mysql&action="+tc.action+"&page_size=1", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "permission-filter-test"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		var page struct {
			Items    []Permission `json:"items"`
			Total    int          `json:"total"`
			PageSize int          `json:"page_size"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if page.Total != tc.total || len(page.Items) != tc.total || page.PageSize != 1 {
			t.Fatal("cross-resource filter mismatch", tc, page)
		}
	}
	second, err := s.SavePermission(ctx, Permission{Name: "filter-bundle-second"})
	if err != nil {
		t.Fatal(err)
	}
	second, err = s.SaveResources(ctx, second.ID, ResourceDelta{Revision: second.Revision, Upsert: []ResourceBinding{binding("resource", "filter-mysql", "delete")}})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.SavePermission(ctx, Permission{Name: "filter-empty"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query               string
		status, total, page int
		id                  string
	}{
		{"q=filter-bundle&action=update,delete&resource_kind=mysql&page=1", 200, 2, 1, p.ID},
		{"q=filter-bundle&action=update,delete&resource_kind=mysql&page=2", 200, 2, 2, second.ID},
		{"q=filter-bundle&action=update&action=delete,update&resource_kind=mysql&resource_kind=%20mysql%20&page=3", 200, 2, 2, second.ID},
		{"q=filter-bundle&action=view&resource_kind=host,mysql", 200, 1, 1, p.ID},
		{"q=filter-bundle&action=view,approve&resource_kind=mysql,redis", 200, 0, 1, ""},
		{"q=filter-bundle&action=delete&resource_kind=host&resource_kind=mysql", 200, 1, 1, second.ID},
		{"q=filter-empty&action=,%20&resource_kind=%20,", 200, 1, 1, empty.ID},
		{"q=filter-empty&action=view&resource_kind=host", 200, 0, 1, ""},
		{"action=view&action=manage", 422, 0, 0, ""},
		{"resource_kind=host,application", 422, 0, 0, ""},
		{"resource_kind=pod", 422, 0, 0, ""},
		{"resource_kind=event", 422, 0, 0, ""},
	} {
		r := httptest.NewRequest("GET", "/api/v1/rbac/permissions?page_size=1&"+tc.query, nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "permission-filter-test"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc.query, w.Code, w.Body.String())
		}
		if tc.status != 200 {
			continue
		}
		var page struct {
			Items    []Permission `json:"items"`
			Total    int          `json:"total"`
			Page     int          `json:"page"`
			PageSize int          `json:"page_size"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != tc.total || page.Page != tc.page || page.PageSize != 1 {
			t.Fatal("server filtered pagination mismatch", tc.query, page)
		}
		if tc.id == "" {
			if len(page.Items) != 0 {
				t.Fatal(tc.query, page.Items)
			}
		} else if len(page.Items) != 1 || page.Items[0].ID != tc.id {
			t.Fatal("unexpected page item", tc.query, page.Items)
		}
	}

}
