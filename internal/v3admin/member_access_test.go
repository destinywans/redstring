package v3admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func accessTestMatrix() []matrixRow {
	rows := make([]matrixRow, 0, len(matrixCatalog))
	for _, entry := range matrixCatalog {
		cells := make([]int, 5)
		if entry.Code == "conversation" || entry.Code == "services" {
			cells[0] = 1
		}
		rows = append(rows, matrixRow{Resource: entry.Code, Grants: cells})
	}
	return rows
}
func TestAccessRequestValidation(t *testing.T) {
	for _, body := range []string{`{"enabled":true,"enabled":false}`, `{"role_revisions":{"role":"1","role":"2"}}`, `{"initial_password":"not-allowed"}`, `{} {}`, `null`, `[]`} {
		var in memberAccessInput
		err := decodeAccess(httptest.NewRecorder(), httptest.NewRequest("PUT", "/", strings.NewReader(body)), &in)
		if err == nil {
			err = validateAccessInput(&in)
		}
		if err == nil {
			t.Fatalf("accepted ambiguous/incomplete body %s", body)
		}
	}
	enabled := true
	in := memberAccessInput{ExpectedRevision: strings.Repeat("a", 64), RoleIDs: []string{"custom-a"}, RoleRevisions: map[string]string{"custom-a": "1"}, Enabled: &enabled}
	if err := validateAccessInput(&in); err != nil {
		t.Fatal(err)
	}
	in.RoleRevisions["custom-a"] = "01"
	if err := validateAccessInput(&in); err == nil {
		t.Fatal("accepted noncanonical role version")
	}
}
func TestCreateRoleExplicitScope(t *testing.T) {
	in := createRoleInput{ID: "custom-dev", Name: " Dev readers ", Scopes: []accessScope{{Kind: "environment", ID: "dev"}}, Matrix: accessTestMatrix()}
	grants, err := normalizeRoleInput(&in, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if in.Name != "Dev readers" || len(grants) != 3 {
		t.Fatalf("role projection: %#v %#v", in, grants)
	}
	for _, g := range grants {
		if g.ScopeKind != "environment" || g.ScopeID != "dev" {
			t.Fatalf("scope widened: %#v", g)
		}
	}
	in.Scopes[0].ID = "prod"
	if _, err := normalizeRoleInput(&in, "dev"); err == nil {
		t.Fatal("accepted environment outside request")
	}
	in.Scopes = []accessScope{{Kind: "global", ID: "*"}}
	if _, err := normalizeRoleInput(&in, "dev"); err != nil {
		t.Fatal(err)
	}
	in.ID = "admin"
	if _, err := normalizeRoleInput(&in, "dev"); err == nil {
		t.Fatal("accepted builtin namespace")
	}
}

func accessHTTPFixture(t *testing.T) (*Service, core.User, string, http.Handler) {
	t.Helper()
	s, actor, token := invitationAuditFixture(t)
	p := &rbac.V3Policy{Store: s.Store}
	api := httpapi.New(s.Store, nil, true)
	s.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, p, s.Store.Session, func(ctx context.Context, u core.User, m, a, e string) error {
		return p.Authorize(ctx, u, rbac.V3Decision{Module: m, Action: a, EnvironmentID: e})
	}); err != nil {
		t.Fatal(err)
	}
	return s, actor, token, api
}
func accessRequest(t *testing.T, h http.Handler, method, path, env, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set("X-AIOps-Environment", env)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func requireAccessStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d wanted %d: %s", w.Code, status, w.Body.String())
	}
}
func accessSnapshotHTTP(t *testing.T, h http.Handler, id, token string) memberAccess {
	t.Helper()
	w := accessRequest(t, h, "GET", "/api/v1/members/"+id+"/access", "dev", token, nil)
	requireAccessStatus(t, w, 200)
	var v memberAccess
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestRoleCreationMemberAccessHTTPPostgres(t *testing.T) {
	s, actor, token, h := accessHTTPFixture(t)
	ctx := t.Context()
	in := createRoleInput{ID: "custom-dev-read", Name: "Dev readers", Scopes: []accessScope{{Kind: "environment", ID: "dev"}}, Matrix: accessTestMatrix()}
	requireAccessStatus(t, accessRequest(t, h, "POST", "/api/v1/roles", "all", token, in), 409)
	requireAccessStatus(t, accessRequest(t, h, "POST", "/api/v1/roles", "dev", token, in), 201)
	requireAccessStatus(t, accessRequest(t, h, "POST", "/api/v1/roles", "dev", token, in), 200)
	changed := in
	changed.Name = "Different"
	requireAccessStatus(t, accessRequest(t, h, "POST", "/api/v1/roles", "dev", token, changed), 409)
	var count int
	if err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.role.created' AND target=$1`, in.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("create exactly once: %d %v", count, err)
	}
	id, code, _, err := s.prepareInvitation(ctx, actor, token, "dev", invitationInput{Email: "access-invite@testlab.invalid", Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.confirmInvitationDelivery(ctx, actor, token, "dev", id); err != nil {
		t.Fatal(err)
	}
	if err = s.acceptInvitation(ctx, invitationAcceptance{Code: code, Name: "Access invite", Password: "synthetic-access-password"}); err != nil {
		t.Fatal(err)
	}
	user, userToken, err := s.Store.Login(ctx, "access-invite@testlab.invalid", "synthetic-access-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: s.Store}
	decision := rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: "dev"}
	if err = policy.Authorize(ctx, user, decision); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("invitation unexpectedly authorized: %v", err)
	}
	before := accessSnapshotHTTP(t, h, user.ID, token)
	enabled := true
	input := memberAccessInput{ExpectedRevision: before.Revision, RoleIDs: []string{in.ID}, RoleRevisions: map[string]string{in.ID: "1"}, Enabled: &enabled}
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+user.ID+"/access", "dev", token, input), 200)
	if err = policy.Authorize(ctx, user, decision); err != nil {
		t.Fatalf("assigned member cannot use conversation: %v", err)
	}
	decision.EnvironmentID = "test"
	if err = policy.Authorize(ctx, user, decision); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("cross-env access: %v", err)
	}
	if _, err = s.Store.Session(ctx, userToken); err == nil {
		t.Fatal("old user session survived grant change")
	}
	user, userToken, err = s.Store.Login(ctx, "access-invite@testlab.invalid", "synthetic-access-password")
	if err != nil {
		t.Fatalf("authorized member cannot log in: %v", err)
	}
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+user.ID+"/access", "dev", token, input), 409)
	after := accessSnapshotHTTP(t, h, user.ID, token)
	input.ExpectedRevision = after.Revision
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+user.ID+"/access", "dev", token, input), 200)
	if _, err = s.Store.Session(ctx, userToken); err != nil {
		t.Fatal("idempotent no-op revoked valid session", err)
	}
	if err = s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.member.access.updated' AND target=$1`, user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("update exactly once: %d %v", count, err)
	}
	input.RoleRevisions[in.ID] = "2"
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+user.ID+"/access", "dev", token, input), 409)
	input.RoleRevisions[in.ID] = "1"
	// A failing audit must undo associations, account state and session deletion.
	if _, err = s.Store.DB.Exec(ctx, `CREATE FUNCTION reject_access_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='v3.member.access.updated' THEN RAISE EXCEPTION 'synthetic access audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_access_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_access_audit()`); err != nil {
		t.Fatal(err)
	}
	enabled = false
	input.RoleIDs = []string{}
	input.RoleRevisions = map[string]string{}
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+user.ID+"/access", "dev", token, input), 503)
	rollback := accessSnapshotHTTP(t, h, user.ID, token)
	if rollback.Revision != after.Revision {
		t.Fatal("audit failure leaked mutation")
	}
	if _, err = s.Store.Session(ctx, userToken); err != nil {
		t.Fatal("audit rollback lost session", err)
	}
	if _, err = s.Store.DB.Exec(ctx, `DROP TRIGGER reject_access_audit ON management_audit`); err != nil {
		t.Fatal(err)
	}
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+user.ID+"/access", "dev", token, input), 200)
	if _, err = s.Store.Session(ctx, userToken); err == nil {
		t.Fatal("disabled session remains valid")
	}
	disabled := accessSnapshotHTTP(t, h, user.ID, token)
	if disabled.Enabled || len(disabled.Roles) != 1 || disabled.Roles[0].Origin != "legacy" {
		t.Fatalf("legacy binding lost: %#v", disabled)
	}
}

func TestMemberAccessSyncedActivationAndLastPlatformAdminPostgres(t *testing.T) {
	s, actor, token, h := accessHTTPFixture(t)
	ctx := t.Context()
	in := createRoleInput{ID: "custom-synced", Name: "Synced readers", Scopes: []accessScope{{Kind: "environment", ID: "dev"}}, Matrix: accessTestMatrix()}
	requireAccessStatus(t, accessRequest(t, h, "POST", "/api/v1/roles", "dev", token, in), 201)
	if _, err := s.Store.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,source,external_id,disabled) VALUES('synced','synced@testlab.invalid','Synced','viewer','','feishu','synthetic-feishu-member',true)`); err != nil {
		t.Fatal(err)
	}
	synced := accessSnapshotHTTP(t, h, "synced", token)
	enabled := true
	input := memberAccessInput{ExpectedRevision: synced.Revision, RoleIDs: []string{in.ID}, RoleRevisions: map[string]string{in.ID: "1"}, Enabled: &enabled}
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/synced/access", "dev", token, input), 200)
	synced = accessSnapshotHTTP(t, h, "synced", token)
	if !synced.Enabled || synced.LoginReady {
		t.Fatalf("activation fabricated authentication: %#v", synced)
	}
	// Last-platform-admin checks must include direct associations, not just users.role.
	other, err := s.Store.CreateUser(ctx, core.User{Email: "last-direct@testlab.invalid", Name: "Direct admin", Role: "viewer"}, "synthetic-direct-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'admin'),($1,'custom-synced')`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(ctx, `UPDATE users SET disabled=true WHERE id=$1`, actor.ID); err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := s.Store.Login(ctx, other.Email, "synthetic-direct-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := accessSnapshotHTTP(t, h, other.ID, otherToken)
	input = memberAccessInput{ExpectedRevision: snapshot.Revision, RoleIDs: []string{in.ID}, RoleRevisions: map[string]string{in.ID: "1"}, Enabled: &enabled}
	w := accessRequest(t, h, "PUT", "/api/v1/members/"+other.ID+"/access", "dev", otherToken, input)
	requireAccessStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), "last_administrator") {
		t.Fatalf("wrong invariant: %s", w.Body.String())
	}
}

func TestMemberAccessAuthorityAndLastManagerPostgres(t *testing.T) {
	s, actor, token, h := accessHTTPFixture(t)
	ctx := t.Context()
	manager, err := s.Store.CreateUser(ctx, core.User{Email: "member-only@testlab.invalid", Name: "Member manager", Role: "viewer"}, "synthetic-manager-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(ctx, `INSERT INTO rbac_roles(id,name) VALUES('custom-member-manager','Member manager'); INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('custom-member-manager','environment','view','global','*'),('custom-member-manager','members','manage','global','*')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'custom-member-manager')`, manager.ID); err != nil {
		t.Fatal(err)
	}
	_, managerToken, err := s.Store.Login(ctx, manager.Email, "synthetic-manager-password")
	if err != nil {
		t.Fatal(err)
	}
	requireAccessStatus(t, accessRequest(t, h, "GET", "/api/v1/members/"+actor.ID+"/access", "dev", managerToken, nil), 403)
	// A global V3 manager cannot remove the final legacy platform administrator.
	if _, err = s.Store.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('custom-member-manager','roles','manage','global','*')`); err != nil {
		t.Fatal(err)
	}
	target := accessSnapshotHTTP(t, h, actor.ID, token)
	disabled := false
	input := memberAccessInput{ExpectedRevision: target.Revision, RoleIDs: []string{}, RoleRevisions: map[string]string{}, Enabled: &disabled}
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+actor.ID+"/access", "dev", managerToken, input), 403)
	// Make manager a platform admin through a department, then attempt to remove
	// its last V3 role-manager grant while the original admin lacks roles/manage.
	if _, err = s.Store.DB.Exec(ctx, `INSERT INTO teams(id,name) VALUES('admin-team','Admin team'); INSERT INTO rbac_department_roles(department_id,role_id) VALUES('admin-team','admin'); DELETE FROM v3_role_grants WHERE role_id='admin' AND module='roles' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.DB.Exec(ctx, `UPDATE users SET department_id='admin-team' WHERE id=$1`, manager.ID); err != nil {
		t.Fatal(err)
	}
	own := accessSnapshotHTTP(t, h, manager.ID, managerToken)
	enabled := true
	input = memberAccessInput{ExpectedRevision: own.Revision, RoleIDs: []string{}, RoleRevisions: map[string]string{}, Enabled: &enabled}
	requireAccessStatus(t, accessRequest(t, h, "PUT", "/api/v1/members/"+manager.ID+"/access", "dev", managerToken, input), 409)
	// The inherited admin role must remain readable and unchanged after rollback.
	after := accessSnapshotHTTP(t, h, manager.ID, managerToken)
	if after.Revision != own.Revision {
		t.Fatal("last-manager rejection changed inherited grants")
	}
}

// A role write admitted by middleware must still fail when its exact session
// or its authority is revoked while it waits for the shared identity lock.
func TestRoleCreationRechecksAfterLockPostgres(t *testing.T) {
	for _, which := range []string{"session", "grant"} {
		t.Run(which, func(t *testing.T) {
			s, actor, token, h := accessHTTPFixture(t)
			ctx := t.Context()
			blocker, err := s.Store.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
				t.Fatal(err)
			}
			in := createRoleInput{ID: "custom-revoked", Name: "Revoked create", Scopes: []accessScope{{Kind: "environment", ID: "dev"}}, Matrix: accessTestMatrix()}
			body, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("POST", "/api/v1/roles", strings.NewReader(string(body)))
			request.Header.Set("X-AIOps-Environment", "dev")
			request.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); h.ServeHTTP(w, request); done <- w }()
			// Inspect real lock waiting rather than trusting a sleep to order requests.
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				if err = s.Store.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("request did not wait on identity lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			expected := 403
			if which == "session" {
				expected = 401
				_, err = blocker.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1 AND user_id=$2`, store.Hash(token), actor.ID)
			} else {
				_, err = blocker.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='roles' AND action='manage'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				requireAccessStatus(t, w, expected)
			case <-time.After(5 * time.Second):
				t.Fatal("blocked role write did not return")
			}
			var count int
			if err = s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM rbac_roles WHERE id='custom-revoked'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("revoked role write persisted: %d %v", count, err)
			}
		})
	}
}
