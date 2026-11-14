package v3admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/destinywans/redstring/internal/core"
)

func TestDelegatedGrantCeilingHTTPPostgres(t *testing.T) {
	s, admin, adminToken, handler := accessHTTPFixture(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Store.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	user := func(name string) core.User {
		t.Helper()
		u, err := s.Store.CreateUser(ctx, core.User{
			Email: name + "@example.invalid", Name: name, Role: "viewer",
		}, "synthetic-grant-ceiling-password")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	login := func(u core.User) string {
		t.Helper()
		_, token, err := s.Store.Login(ctx, u.Email, "synthetic-grant-ceiling-password")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	manager := user("delegated-manager")
	ordinary := user("ordinary-member")
	approver := user("department-approver")
	inactive := user("inactive-member")
	legacyApprover := user("legacy-approver")
	legacyRoleHolder := user("legacy-role-holder")
	exec(`INSERT INTO rbac_roles(id,name) VALUES ('custom-manager','Manager'),
 ('custom-matrix','Matrix'), ('custom-beyond','Beyond'), ('custom-approver-role','Approver business role'),
 ('custom-inherited-beyond','Inherited beyond'), ('custom-legacy-held','Legacy held');
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('custom-manager','environment','view','global','*'),
 ('custom-manager','members','view','global','*'),
 ('custom-manager','members','manage','global','*'),
 ('custom-manager','roles','view','global','*'),
 ('custom-manager','roles','manage','global','*'),
 ('custom-manager','events','change','environment','dev'),
 ('custom-beyond','events','change','global','*'),
 ('custom-inherited-beyond','events','delete','global','*');
 INSERT INTO teams(id,name) VALUES ('synthetic-approvers','Approvers'),('synthetic-inactive','Inactive');
 INSERT INTO rbac_department_roles(department_id,role_id) VALUES
 ('synthetic-approvers','change-approver'),('synthetic-inactive','custom-inherited-beyond')`)
	exec(`INSERT INTO rbac_user_roles(user_id,role_id) VALUES
 ($1,'custom-manager'),($2,'custom-approver-role'),($3,'custom-beyond')`, manager.ID, approver.ID, ordinary.ID)
	exec(`INSERT INTO rbac_user_roles(user_id,role_id,origin) VALUES
 ($1,'change-approver','legacy'),($2,'custom-legacy-held','legacy')`, legacyApprover.ID, legacyRoleHolder.ID)
	exec(`INSERT INTO rbac_user_roles(user_id,role_id) VALUES ($1,'custom-legacy-held')`, legacyApprover.ID)
	exec(`UPDATE users SET department_id='synthetic-approvers' WHERE id=$1`, approver.ID)
	exec(`UPDATE users SET department_id='synthetic-inactive',disabled=true WHERE id=$1`, inactive.ID)
	managerToken := login(manager)

	newRole := func(id string, scope accessScope, module string, operation int) createRoleInput {
		matrix := accessTestMatrix()
		for i := range matrix {
			if matrix[i].Resource == module {
				matrix[i].Grants[operation] = 1
			}
		}
		return createRoleInput{ID: id, Name: id, Scopes: []accessScope{scope}, Matrix: matrix}
	}
	requireCode := func(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code {
			t.Fatalf("HTTP %d %s; want %d %s (decode: %v)", w.Code, w.Body.String(), status, code, err)
		}
	}
	putRole := func(t *testing.T, actorToken, id, resource string, operation, value int) *httptest.ResponseRecorder {
		t.Helper()
		path := "/api/v1/roles/" + id
		got := accessRequest(t, handler, "GET", path, "dev", adminToken, nil)
		requireAccessStatus(t, got, 200)
		var role roleDetail
		if err := json.Unmarshal(got.Body.Bytes(), &role); err != nil {
			t.Fatal(err)
		}
		found := false
		for i := range role.Matrix {
			entry, ok := catalogResource(role.Matrix[i].Resource)
			if ok && entry.Code == resource {
				role.Matrix[i].Grants[operation] = value
				found = true
			}
		}
		if !found {
			t.Fatalf("matrix lacks %s", resource)
		}
		raw, err := json.Marshal(matrixUpdate{Matrix: role.Matrix})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("PUT", path, bytes.NewReader(raw))
		req.Header.Set("X-AIOps-Environment", "dev")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Role-Matrix-Revision", got.Header().Get("X-Role-Matrix-Revision"))
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: actorToken})
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	putAccess := func(t *testing.T, actorToken, id string, enabled bool, roles ...string) *httptest.ResponseRecorder {
		t.Helper()
		input := memberAccessInput{
			ExpectedRevision: accessSnapshotHTTP(t, handler, id, adminToken).Revision,
			RoleIDs:          append([]string{}, roles...),
			RoleRevisions:    map[string]string{},
			Enabled:          &enabled,
		}
		for _, role := range roles {
			var revision int64
			if err := s.Store.DB.QueryRow(ctx, "SELECT v3_matrix_revision FROM rbac_roles WHERE id=$1", role).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			input.RoleRevisions[role] = strconv.FormatInt(revision, 10)
		}
		return accessRequest(t, handler, "PUT", "/api/v1/members/"+id+"/access", "dev", actorToken, input)
	}

	t.Run("creation rejects escalation and records no role or success audit", func(t *testing.T) {
		for _, tc := range []struct {
			role createRoleInput
			code string
		}{
			{newRole("custom-elevated", accessScope{"global", "*"}, "proposals", 3), "platform_admin_required"},
			{newRole("custom-uncovered", accessScope{"global", "*"}, "events", 1), "grant_exceeds_actor"},
			{newRole("custom-cross-env", accessScope{"environment", "test"}, "events", 1), "grant_exceeds_actor"},
		} {
			env := "dev"
			if tc.role.Scopes[0].ID == "test" {
				env = "test"
			}
			res := accessRequest(t, handler, "POST", "/api/v1/roles", env, managerToken, tc.role)
			requireCode(t, res, 403, tc.code)
			var written bool
			if err := s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_roles WHERE id=$1)
 OR EXISTS(SELECT 1 FROM management_audit WHERE action='v3.role.created' AND target=$1)`, tc.role.ID).Scan(&written); err != nil || written {
				t.Fatalf("denied create persisted state: %t %v", written, err)
			}
		}
	})

	t.Run("delegated scoped grant and removal succeed", func(t *testing.T) {
		role := newRole("custom-dev-events", accessScope{"environment", "dev"}, "events", 1)
		requireAccessStatus(t, accessRequest(t, handler, "POST", "/api/v1/roles", "dev", managerToken, role), 201)
		requireAccessStatus(t, putAccess(t, managerToken, ordinary.ID, true, "custom-beyond", role.ID), 200)
		requireAccessStatus(t, putAccess(t, managerToken, ordinary.ID, true, "custom-beyond"), 200)
	})

	t.Run("member writes cannot grant approval or revoke beyond the ceiling", func(t *testing.T) {
		requireCode(t, putAccess(t, managerToken, manager.ID, true, "custom-manager"), 403, "self_access_change_forbidden")
		requireCode(t, putAccess(t, managerToken, ordinary.ID, true, "custom-beyond", "change-approver"), 403, "platform_admin_required")
		requireCode(t, putAccess(t, managerToken, ordinary.ID, true), 403, "grant_exceeds_actor")
	})

	t.Run("role matrix protects privilege and still allows ordinary edits", func(t *testing.T) {
		requireCode(t, putRole(t, managerToken, "custom-matrix", "events", 1, 1), 403, "grant_exceeds_actor")
		requireCode(t, putRole(t, managerToken, "custom-matrix", "proposals", 3, 1), 403, "platform_admin_required")
		requireCode(t, putRole(t, managerToken, "admin", "identity", 4, 0), 403, "platform_admin_required")
		// The delegated role includes a dev-scoped grant and is not editable in
		// the flat matrix, so send a well-formed global matrix directly.
		selfRole := newRole("custom-manager", accessScope{"global", "*"}, "events", 0).Matrix
		raw, err := json.Marshal(matrixUpdate{Matrix: selfRole})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("PUT", "/api/v1/roles/custom-manager", bytes.NewReader(raw))
		req.Header.Set("X-AIOps-Environment", "dev")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Role-Matrix-Revision", "1")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: managerToken})
		self := httptest.NewRecorder()
		handler.ServeHTTP(self, req)
		requireCode(t, self, 403, "self_access_change_forbidden")
		requireCode(t, putRole(t, managerToken, "custom-approver-role", "events", 0, 1), 403, "platform_admin_required")
		requireCode(t, putRole(t, managerToken, "custom-legacy-held", "events", 0, 1), 403, "platform_admin_required")
		requireAccessStatus(t, putRole(t, managerToken, "custom-matrix", "events", 0, 1), 200)
	})

	t.Run("disabled targets keep privilege boundaries", func(t *testing.T) {
		requireAccessStatus(t, putAccess(t, managerToken, ordinary.ID, false, "custom-beyond"), 200)
		requireCode(t, putAccess(t, managerToken, ordinary.ID, true, "custom-beyond"), 403, "grant_exceeds_actor")
		requireCode(t, putAccess(t, managerToken, inactive.ID, true), 403, "grant_exceeds_actor")
		exec(`UPDATE users SET disabled=true WHERE id=$1`, approver.ID)
		requireCode(t, putRole(t, managerToken, "custom-approver-role", "events", 0, 1), 403, "platform_admin_required")
		requireCode(t, putAccess(t, managerToken, approver.ID, true, "custom-approver-role"), 403, "platform_admin_required")
	})

	t.Run("privileged contacts cannot be edited by delegates", func(t *testing.T) {
		path := "/api/v1/members/" + legacyApprover.ID
		got := accessRequest(t, handler, "GET", path, "dev", adminToken, nil)
		requireAccessStatus(t, got, 200)
		var member memberDetail
		if err := json.Unmarshal(got.Body.Bytes(), &member); err != nil {
			t.Fatal(err)
		}
		body := map[string]string{"expected_revision": member.Revision, "name": "New name"}
		requireCode(t, accessRequest(t, handler, "PATCH", path, "dev", managerToken, body), 403, "platform_admin_required")
		requireAccessStatus(t, accessRequest(t, handler, "PATCH", path, "dev", adminToken, body), 200)
	})

	t.Run("platform administrator assigns independent approvers and keeps an administration path", func(t *testing.T) {
		requireCode(t, putRole(t, adminToken, "admin", "proposals", 3, 1), 422, "admin_approval_forbidden")
		requireCode(t, putRole(t, adminToken, "admin", "identity", 4, 0), 409, "last_administrator")
		requireCode(t, putRole(t, adminToken, "admin", "identity", 0, 0), 409, "last_administrator")
		requireAccessStatus(t, putAccess(t, adminToken, ordinary.ID, true, "custom-beyond", "change-approver"), 200)
		requireAccessStatus(t, putAccess(t, adminToken, approver.ID, true, "custom-approver-role"), 200)
		var approvals int
		if err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM v3_effective_role_grants
 WHERE user_id IN ($1,$2) AND module='proposals' AND action='approve' AND assurance='passkey'`, ordinary.ID, approver.ID).Scan(&approvals); err != nil || approvals != 2 {
			t.Fatalf("independent approvals=%d err=%v", approvals, err)
		}
		if err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM v3_effective_role_grants
 WHERE user_id=$1 AND module='proposals' AND action='approve'`, admin.ID).Scan(&approvals); err != nil || approvals != 0 {
			t.Fatalf("admin approvals=%d err=%v", approvals, err)
		}
	})
}
