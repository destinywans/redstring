package v3settings

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

func TestWorkspaceAuthenticatedHTTPPersistenceAndAudit(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-workspace-test-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id IN ('one','two')`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE users SET role='admin' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','workspace','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := s.Login(ctx, "two@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(s, nil, true)
	New(s, nil).Register(api)
	api.MarkV3Route("GET /api/v1/workspace", "workspace", "view")
	api.MarkV3Route("PUT /api/v1/workspace", "workspace", "manage")
	policy := &rbac.V3Policy{Store: s}
	err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, s.Session,
		func(ctx context.Context, user core.User, module, action, environmentID string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environmentID})
		})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, token, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/workspace", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "", "dev", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d %s", w.Code, w.Body.String())
	}
	initial := request(http.MethodGet, viewerToken, "dev", "")
	if initial.Code != http.StatusOK {
		t.Fatalf("viewer read: %d %s", initial.Code, initial.Body.String())
	}
	var original workspace
	if err := json.Unmarshal(initial.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Name = "研发工作区"
	updated.Slug = "development"
	updated.Locale = "en-US"
	updated.WeekStart = "sunday"
	updated.RetentionDays = 180
	payload, err := json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPut, viewerToken, "dev", string(payload)); w.Code != http.StatusForbidden {
		t.Fatalf("environment-scoped workspace manager changed instance settings: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','workspace','manage','global','*')`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodPut, viewerToken, "dev", string(payload)); w.Code != http.StatusForbidden {
		t.Fatalf("delegated global manager without platform-admin role changed workspace: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, adminToken, "all", string(payload)); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "env_view_only") {
		t.Fatalf("all write: %d %s", w.Code, w.Body.String())
	}
	for _, invalid := range []string{`{}`, `null`, `{"name":"bad","unknown":true}`} {
		if w := request(http.MethodPut, adminToken, "dev", invalid); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid body %s: %d %s", invalid, w.Code, w.Body.String())
		}
	}
	tampered := updated
	tampered.Brand.Name = "other"
	tamperedJSON, _ := json.Marshal(tampered)
	if w := request(http.MethodPut, adminToken, "dev", string(tamperedJSON)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("immutable brand: %d %s", w.Code, w.Body.String())
	}
	saved := request(http.MethodPut, adminToken, "dev", string(payload))
	if saved.Code != http.StatusOK {
		t.Fatalf("admin write: %d %s", saved.Code, saved.Body.String())
	}
	got := request(http.MethodGet, viewerToken, "dev", "")
	if got.Code != http.StatusOK || got.Body.String() != saved.Body.String() {
		t.Fatalf("read-after-write: %d %s; saved=%s", got.Code, got.Body.String(), saved.Body.String())
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.workspace.updated' AND target='default' AND outcome='success'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	if _, err := s.DB.Exec(ctx, `CREATE FUNCTION fail_workspace_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.workspace.updated' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `CREATE TRIGGER fail_workspace_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION fail_workspace_audit()`); err != nil {
		t.Fatal(err)
	}
	changed := updated
	changed.Name = "Must roll back"
	changedJSON, _ := json.Marshal(changed)
	if w := request(http.MethodPut, adminToken, "dev", string(changedJSON)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, adminToken, "dev", ""); w.Code != http.StatusOK || w.Body.String() != saved.Body.String() {
		t.Fatalf("workspace changed after audit failure: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceWriteRechecksRevokedManageGrantInTransaction(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	if _, err := s.DB.Exec(ctx, `UPDATE users SET role='admin' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	input, err := New(s, nil).getWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input.Name = "Should not persist"
	if _, err := s.DB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='workspace' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	_, err = New(s, nil).putWorkspace(ctx, core.User{ID: "one", Role: "admin"}, "dev", input)
	if err != store.ErrForbidden {
		t.Fatalf("revoked grant: got %v, want forbidden", err)
	}
	var name string
	if err := s.DB.QueryRow(ctx, `SELECT name FROM workspace_settings WHERE id='default'`).Scan(&name); err != nil || name == input.Name {
		t.Fatalf("revoked write persisted: name=%q err=%v", name, err)
	}
}

func TestWorkspaceWriteWaitsForMatrixRevocationBeforeCheckingAuthority(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	if _, err := s.DB.Exec(ctx, `UPDATE users SET role='admin' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	input, err := New(s, nil).getWorkspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	input.Name = "Revoked before write"
	writer, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	// saveMatrix takes this same lock before changing V3 role grants.
	if _, err := writer.Exec(ctx, `SELECT pg_advisory_xact_lock(752018998)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='workspace' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, writeErr := New(s, nil).putWorkspace(ctx, core.User{ID: "one", Role: "admin"}, "dev", input)
		done <- writeErr
	}()
	select {
	case err := <-done:
		t.Fatalf("workspace write finished before revocation committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrForbidden) {
			t.Fatalf("revoked grant allowed write: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workspace write remained blocked after revocation committed")
	}
	var name string
	if err := s.DB.QueryRow(ctx, `SELECT name FROM workspace_settings WHERE id='default'`).Scan(&name); err != nil || name == input.Name {
		t.Fatalf("workspace changed despite revoked grant: name=%q err=%v", name, err)
	}
}
