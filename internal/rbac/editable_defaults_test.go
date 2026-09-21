package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestEditableDefaultsPostgres(t *testing.T) {
	st := isolatedDB(t)
	s := &Service{Store: st}
	ctx := store.WithPrincipal(context.Background(), core.User{ID: "a", Role: "admin"})
	admin, err := s.Role(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin.Name = "Renamed initial administrator"
	admin.Description = "Editable initialized role"
	admin, err = s.SaveRole(ctx, admin)
	if err != nil {
		t.Fatal("initialized role edit", err)
	}
	if _, err = s.SaveRole(ctx, Role{ID: admin.ID, Name: admin.Name, Revision: admin.Revision - 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("role stale edit", err)
	}
	permission, err := s.Permission(ctx, "resource-view")
	if err != nil {
		t.Fatal(err)
	}
	permission.Name = "Renamed initial reader"
	permission.Description = "Editable initialized permission"
	permission, err = s.SavePermission(ctx, permission)
	if err != nil {
		t.Fatal("initialized permission edit", err)
	}
	if _, err = s.SavePermission(ctx, Permission{Name: permission.Name}); !errors.Is(err, ErrPermissionNameConflict) {
		t.Fatal("name uniqueness lost", err)
	}
	adminBefore, err := s.Role(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "permissions", permission.ID, permission.Revision); err != nil {
		t.Fatal("referenced initialized permission delete", err)
	}
	adminAfter, err := s.Role(ctx, "admin")
	if err != nil || adminAfter.Revision != adminBefore.Revision+1 {
		t.Fatal("dependent role draft not invalidated", adminAfter, err)
	}
	var count int
	if err = st.DB.QueryRow(ctx, "SELECT count(*) FROM rbac_permission_resources WHERE permission_id=$1", permission.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("permission resources remain", count, err)
	}
	if err = st.DB.QueryRow(ctx, "SELECT count(*) FROM cmdb_resources").Scan(&count); err != nil || count != 2 {
		t.Fatal("target resources deleted", count, err)
	}
	if err = s.Delete(ctx, "roles", "admin", adminAfter.Revision); !errors.Is(err, ErrLastManager) {
		t.Fatal("last managing role deleted", err)
	}
	management, err := s.Permission(ctx, "manage-rbac")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "permissions", management.ID, management.Revision); !errors.Is(err, ErrLastManager) {
		t.Fatal("last managing permission deleted", err)
	}
	admin, err = s.Role(ctx, "admin")
	if err != nil || admin.Revision != adminAfter.Revision {
		t.Fatal("rejected delete changed role revision", admin, err)
	}

	// Transfer management to a regular role before deleting the initial role.
	role, err := s.SaveRole(ctx, Role{Name: "Replacement managers"})
	if err != nil {
		t.Fatal(err)
	}
	role, err = s.SaveAssociations(ctx, role.ID, Associations{Revision: role.Revision, Permissions: Delta{Add: []string{"manage-rbac", "manage-users"}}, Users: Delta{Add: []string{"v"}}})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := s.SaveRole(ctx, Role{Name: "Ordinary retained compatibility grants"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAssociations(ctx, reader.ID, Associations{Revision: reader.Revision, Permissions: Delta{Add: []string{"legacy-view", "legacy-execute"}}, Users: Delta{Add: []string{"a"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = st.DB.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'a',now()+interval '1 hour')", store.Hash("old-admin-session")); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "roles", "admin", admin.Revision); err != nil {
		t.Fatal("initialized role cascade delete", err)
	}
	user, err := st.Session(ctx, "old-admin-session")
	if err != nil || user.Role != "viewer" {
		t.Fatal("deleted role kept legacy admin rank", user, err)
	}
	user, err = st.ActiveUser(ctx, "a")
	if err != nil || user.Role != "viewer" {
		t.Fatal("background actor kept deleted base rank", user, err)
	}
	if _, err = st.DB.Exec(ctx, "UPDATE users SET password_hash=$1 WHERE id='a'", store.PasswordHash("retained-account-test-123")); err != nil {
		t.Fatal(err)
	}
	user, _, err = st.Login(ctx, "a@test.invalid", "retained-account-test-123")
	if err != nil || user.Role != "viewer" {
		t.Fatal("login restored deleted base role rank", user, err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "a"}, "manage", "management", "rbac"); !errors.Is(err, store.ErrForbidden) {
		t.Fatal("deleted role permissions remain", err)
	}
	for _, action := range []string{"view", "execute"} {
		if err = st.AuthorizeApplication(ctx, core.User{ID: "a"}, "app2", action); !errors.Is(err, store.ErrForbidden) {
			t.Fatal("deleted admin retained implicit legacy ACL bypass", action, err)
		}
	}
	vctx := store.WithPrincipal(context.Background(), core.User{ID: "v", Role: "viewer"})
	name := "Original account retained"
	if _, err = st.UpdateMember(vctx, "a", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Name: &name}}); err != nil {
		t.Fatal("updating account with removed base role failed", err)
	}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Role(ctx, "admin"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("migration resurrected deleted role", err)
	}
	if err = st.DB.QueryRow(ctx, "SELECT count(*) FROM rbac_user_roles WHERE user_id='a' AND role_id='admin'").Scan(&count); err != nil || count != 0 {
		t.Fatal("account update recreated base assignment", count, err)
	}
	if _, err = st.CreateMember(vctx, store.MemberCreate{Email: "new-admin@test.invalid", Name: "Not silently assigned", Role: "admin", Password: "test-password-only-123"}); !errors.Is(err, store.ErrBaseRoleUnavailable) {
		t.Fatal("created missing base role", err)
	}
	base := "admin"
	if _, err = st.UpdateMember(vctx, "v", store.MemberUpdate{AccountUpdate: store.AccountUpdate{Role: &base}}); !errors.Is(err, store.ErrBaseRoleUnavailable) {
		t.Fatal("switched to missing base role", err)
	}
	// New explicit RBAC roles remain usable despite the removed legacy identity.
	role, err = s.SaveAssociations(vctx, role.ID, Associations{Revision: role.Revision, Users: Delta{Add: []string{"a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AuthorizeCapability(ctx, core.User{ID: "a"}, "manage", "management", "rbac"); err != nil {
		t.Fatal("explicit replacement grant lost", err)
	}
	if err = st.DB.QueryRow(ctx, "SELECT count(*) FROM users WHERE id='a'").Scan(&count); err != nil || count != 1 {
		t.Fatal("role deletion deleted account", count, err)
	}
}
