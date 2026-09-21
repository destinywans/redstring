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

func TestRoleTextLimits(t *testing.T) {
	if !validName("角色", strings.Repeat("描", 2000)) || validName("角色", strings.Repeat("描", 2001)) || validName(strings.Repeat("角", 201), "") {
		t.Fatal("text limits must count characters, not UTF-8 bytes")
	}
	for _, tc := range []struct {
		err  error
		code string
	}{{ErrRoleNameConflict, "role_name_conflict"}, {ErrLegacyMembership, "legacy_role_membership"}, {ErrLastManager, "last_rbac_manager"}} {
		w := httptest.NewRecorder()
		respond(w, nil, tc.err)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("conflict response: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestRoleRefinementPostgres(t *testing.T) {
	st := isolatedDB(t)
	s := &Service{Store: st}
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	role, err := s.SaveRole(ctx, Role{Name: "Unique role", Description: strings.Repeat("描", 2000)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveRole(ctx, Role{Name: " Unique role "}); !errors.Is(err, ErrRoleNameConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	other, err := s.SaveRole(ctx, Role{Name: "Other role"})
	if err != nil {
		t.Fatal(err)
	}
	other.Name = role.Name
	if _, err = s.SaveRole(ctx, other); !errors.Is(err, ErrRoleNameConflict) {
		t.Fatalf("duplicate rename: %v", err)
	}

	admin, err := s.Role(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision, Users: Delta{Add: []string{"v"}}, Departments: Delta{Add: []string{"d2"}}, Permissions: Delta{Remove: []string{"legacy-execute"}}})
	if err != nil {
		t.Fatalf("builtin associations: %v", err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "manage", "management", "rbac"); err != nil {
		t.Fatalf("direct grant not effective: %v", err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "u"}, "manage", "management", "rbac"); err != nil {
		t.Fatalf("department grant not effective: %v", err)
	}
	admin, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision, Users: Delta{Remove: []string{"v"}}, Departments: Delta{Remove: []string{"d2"}}, Permissions: Delta{Add: []string{"legacy-execute"}}})
	if err != nil {
		t.Fatalf("builtin association removal/re-add: %v", err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "v"}, "manage", "management", "rbac"); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("revocation: %v", err)
	}

	if _, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision, Users: Delta{Remove: []string{"a"}}}); !errors.Is(err, ErrLegacyMembership) {
		t.Fatalf("legacy identity changed: %v", err)
	}
	if _, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision, Permissions: Delta{Remove: []string{"manage-rbac"}}}); !errors.Is(err, ErrLastManager) {
		t.Fatalf("last manager lost: %v", err)
	}
	after, _ := s.Role(ctx, admin.ID)
	if after.Revision != admin.Revision {
		t.Fatal("rejected change bumped revision")
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "a"}, "manage", "management", "rbac"); err != nil {
		t.Fatal("rejected change did not roll back", err)
	}
	if admin, err = s.SaveRole(ctx, admin); err != nil {
		t.Fatalf("initialized role metadata cannot edit: %v", err)
	}
	if _, err = s.SaveAssociations(store.WithPrincipal(context.Background(), core.User{ID: "v"}), role.ID, Associations{Revision: role.Revision}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("transaction accepted revoked caller: %v", err)
	}

	api := httpapi.New(st, nil)
	s.Register(api)
	if _, err = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'a',now()+interval '1 hour')", store.Hash("role-refinement-test")); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/rbac/roles/admin/associations/users", nil)
	request.AddCookie(&http.Cookie{Name: "aiops_session", Value: "role-refinement-test"})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, request)
	var candidates struct {
		Items []Candidate `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &candidates) != nil {
		t.Fatalf("candidates: %d %s", w.Code, w.Body.String())
	}
	found := false
	for _, candidate := range candidates.Items {
		if candidate.ID == "a" {
			found = candidate.Selected && candidate.Locked && candidate.LockReason != ""
		}
		if candidate.ID == "v" && candidate.Locked {
			t.Fatal("ordinary user locked")
		}
	}
	if !found {
		t.Fatal("base-role member missing locked explanation")
	}

	// This path was unreachable while all built-in role associations were fixed.
	// Transfer RBAC management to a custom role, then verify account, department,
	// role and permission revocation cannot strand the workspace.
	management, err := s.Permission(ctx, "manage-rbac")
	if err != nil {
		t.Fatal(err)
	}
	role, err = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Permissions: Delta{Add: []string{management.ID}}, Users: Delta{Add: []string{"v"}}})
	if err != nil {
		t.Fatal(err)
	}
	admin, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision, Permissions: Delta{Remove: []string{"manage-rbac"}}})
	if err != nil {
		t.Fatal(err)
	}
	vctx := store.WithPrincipal(context.Background(), core.User{ID: "v", Role: "viewer"})
	if err = st.DeleteMember(vctx, "v"); !errors.Is(err, store.ErrLastRBACManager) {
		t.Fatalf("deleted last custom manager: %v", err)
	}
	disabled := true
	if _, err = st.UpdateMember(vctx, "v", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Disabled: &disabled}}); !errors.Is(err, store.ErrLastRBACManager) {
		t.Fatalf("disabled last custom manager: %v", err)
	}
	if _, err = s.SaveResources(vctx, management.ID, ResourceDelta{Revision: management.Revision, Remove: []ResourceTarget{{ScopeType: "management", ResourceID: "rbac"}}}); !errors.Is(err, ErrLastManager) {
		t.Fatalf("removed final management scope: %v", err)
	}
	if _, err = s.SaveAssociations(ctx, admin.ID, Associations{Revision: admin.Revision}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("revoked manager still allowed: %v", err)
	}
	role, err = s.SaveAssociations(vctx, role.ID, Associations{Revision: role.Revision, Users: Delta{Remove: []string{"v"}}, Departments: Delta{Add: []string{"d2"}}})
	if err != nil {
		t.Fatal(err)
	}
	uctx := store.WithPrincipal(context.Background(), core.User{ID: "u", Role: "operator"})
	if err = st.SetTeamMembers(uctx, "d2", []string{}); !errors.Is(err, store.ErrLastRBACManager) {
		t.Fatalf("removed last department manager: %v", err)
	}
	if _, err = st.DB.Exec(uctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'u',now()+interval '1 hour')", store.Hash("department-manager-test")); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPatch, "/api/v1/rbac/users/u/department", strings.NewReader(`{"department_id":null}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "aiops_session", Value: "department-manager-test"})
	w = httptest.NewRecorder()
	api.ServeHTTP(w, request)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "last_rbac_manager") {
		t.Fatalf("cleared final department through API: %d %s", w.Code, w.Body.String())
	}
}

func TestRoleViewerIdentityPostgres(t *testing.T) {
	st := isolatedDB(t)
	s := &Service{Store: st}
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	usersManager, err := s.SaveRole(ctx, Role{Name: "Members only"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAssociations(ctx, usersManager.ID, Associations{Revision: usersManager.Revision, Permissions: Delta{Add: []string{"manage-users"}}, Users: Delta{Add: []string{"u"}}}); err != nil {
		t.Fatal(err)
	}
	uctx := store.WithPrincipal(context.Background(), core.User{ID: "u", Role: "operator"})
	if err = st.AuthorizeCapability(uctx, core.User{ID: "u"}, "manage", "management", "users"); err != nil {
		t.Fatal(err)
	}
	input := store.MemberCreate{Email: "safe@test.invalid", Name: "Safe viewer", Role: "viewer", Password: "test-password-only-123"}
	if _, err = st.CreateMember(uctx, input); err != nil {
		t.Fatalf("ordinary viewer creation blocked: %v", err)
	}
	viewer, err := s.Role(ctx, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAssociations(ctx, viewer.ID, Associations{Revision: viewer.Revision, Permissions: Delta{Add: []string{"manage-rbac"}}}); err != nil {
		t.Fatal(err)
	}
	input.Email = "elevated@test.invalid"
	if _, err = st.CreateMember(uctx, input); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("users manager created elevated viewer: %v", err)
	}
	password, email := "changed-password-123", "hijack@test.invalid"
	for _, update := range []store.MemberUpdate{
		{AccountUpdate: store.AccountUpdate{Password: &password}},
		{Email: &email},
	} {
		if _, err = st.UpdateMember(uctx, "v", update); !errors.Is(err, store.ErrForbidden) {
			t.Fatalf("users manager changed privileged viewer credentials: %v", err)
		}
	}
	if _, err = st.DB.Exec(ctx, "UPDATE users SET disabled=true WHERE id='v'"); err != nil {
		t.Fatal(err)
	}
	enabled := false
	if _, err = st.UpdateMember(uctx, "v", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Disabled: &enabled}}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("users manager enabled privileged viewer: %v", err)
	}
	if _, err = st.CreateMember(ctx, input); err != nil {
		t.Fatalf("RBAC manager could not create explicitly authorized viewer: %v", err)
	}
	viewer, err = s.Role(ctx, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAssociations(ctx, viewer.ID, Associations{Revision: viewer.Revision, Permissions: Delta{Remove: []string{"manage-rbac"}, Add: []string{"resource-view"}}}); err != nil {
		t.Fatal(err)
	}
	input.Email = "private-reader@test.invalid"
	if _, err = st.CreateMember(uctx, input); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("users manager cloned explicit private resource grant: %v", err)
	}
}
