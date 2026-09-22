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
	}{{"view", 0}, {"update", 1}} {
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
}
