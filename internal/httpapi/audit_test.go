package httpapi

import (
	"context"
	"encoding/json"
	"github.com/destinywans/redstring/internal/store"
	"net/http"
	"testing"
)

func TestAuditHTTPFiltersAndAdminBoundary(t *testing.T) {
	api, server := testServer(t)
	repo := api.Store.(*store.Store)
	ctx := context.Background()
	_, token, err := repo.Login(ctx, "admin@test.local", "test-secure-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "aiops_session", Value: token}
	if err = repo.Audit(ctx, "", "scope.check", "target", "success", nil); err != nil {
		t.Fatal(err)
	}
	response := call(t, server, "GET", "/api/v1/audit?action=scope.check&limit=1", "", cookie)
	var out store.AuditPage
	if err = json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || len(out.Items) != 1 {
		t.Fatalf("search not routed: %+v", out)
	}
	for _, path := range []string{"/api/v1/audit?limit=0", "/api/v1/audit?from=bad", "/api/v1/audit?cursor=garbage"} {
		response = call(t, server, "GET", path, "", cookie)
		response.Body.Close()
		if response.StatusCode != 422 {
			t.Fatalf("invalid filter: %s %d", path, response.StatusCode)
		}
	}
	member, err := repo.CreateMember(ctx, store.MemberCreate{Email: "auditviewer@example.invalid", Name: "Viewer", Role: "viewer", Password: "isolated-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err = repo.Login(ctx, member.Email, "isolated-test-password")
	if err != nil {
		t.Fatal(err)
	}
	response = call(t, server, "GET", "/api/v1/audit?q=scope", "", &http.Cookie{Name: "aiops_session", Value: token})
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("audit permissions widened")
	}
}
