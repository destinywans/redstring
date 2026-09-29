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
	"github.com/wans/aiops-canvas/internal/settings"
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
	err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, policy, s.Session,
		func(ctx context.Context, user core.User, module, action, environmentID string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environmentID})
		})
	if err != nil {
		t.Fatal(err)
	}
	workspaceTag := ""
	requestWithLanguage := func(method, token, env, language, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/workspace", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		if method == http.MethodPut && workspaceTag != "" {
			r.Header.Set("If-Match", workspaceTag)
		}
		if language != "" {
			r.Header.Set("Accept-Language", language)
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	request := func(method, token, env, body string) *httptest.ResponseRecorder {
		return requestWithLanguage(method, token, env, "", body)
	}
	if w := request(http.MethodGet, "", "dev", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d %s", w.Code, w.Body.String())
	}
	initial := request(http.MethodGet, viewerToken, "dev", "")
	if initial.Code != http.StatusOK {
		t.Fatalf("viewer read: %d %s", initial.Code, initial.Body.String())
	}
	workspaceTag = initial.Header().Get("ETag")
	if workspaceTag == "" {
		t.Fatal("GET did not expose workspace revision ETag")
	}
	initialTag := workspaceTag
	var original workspace
	if err := json.Unmarshal(initial.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(initial.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "slug", "timezone", "locale", "week_start", "retention_days", "data_region", "brand"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("workspace response missing %s: %s", field, initial.Body.String())
		}
	}
	if len(fields) != 8 {
		t.Fatalf("workspace response has unexpected fields: %s", initial.Body.String())
	}
	if w := request(http.MethodGet, viewerToken, "all", ""); w.Code != http.StatusOK || w.Body.String() != initial.Body.String() {
		t.Fatalf("all read with one authorized environment: %d %s", w.Code, w.Body.String())
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
	for _, invalid := range []string{
		strings.Replace(string(payload), `"logo":null,`, "", 1),
		strings.Replace(string(payload), `"brand":{"name":`, `"brand":{"name":"duplicate","name":`, 1),
		strings.Replace(string(payload), `"data_region":"local"`, `"data_region":null`, 1),
	} {
		if w := request(http.MethodPut, adminToken, "dev", invalid); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("missing, duplicate or null field accepted: %d %s", w.Code, w.Body.String())
		}
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
	for _, invalid := range []string{`{}`, `null`, `{"name":"bad","unknown":true}`, `{"name":"a","name":"b"}`} {
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
	for _, change := range []struct {
		name   string
		mutate func(*workspace)
	}{
		{"data region", func(v *workspace) { v.DataRegion = "elsewhere" }},
		{"brand logo", func(v *workspace) { logo := "other-logo"; v.Brand.Logo = &logo }},
		{"brand pending", func(v *workspace) { v.Brand.Pending = !v.Brand.Pending }},
	} {
		tampered := updated
		change.mutate(&tampered)
		body, _ := json.Marshal(tampered)
		if w := request(http.MethodPut, adminToken, "dev", string(body)); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("immutable %s: %d %s", change.name, w.Code, w.Body.String())
		}
	}
	if w := requestWithLanguage(http.MethodPut, adminToken, "dev", "zh-CN", `{"unknown":true}`); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "工作区字段缺失、重复或无效") {
		t.Fatalf("localized unknown-field error: %d %s", w.Code, w.Body.String())
	}
	if w := requestWithLanguage(http.MethodPut, adminToken, "dev", "en-US", `{"unknown":true}`); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "Missing, duplicate, or invalid workspace field") {
		t.Fatalf("English unknown-field error: %d %s", w.Code, w.Body.String())
	}
	saved := request(http.MethodPut, adminToken, "dev", string(payload))
	if saved.Code != http.StatusOK {
		t.Fatalf("admin write: %d %s", saved.Code, saved.Body.String())
	}
	if saved.Header().Get("ETag") == workspaceTag {
		t.Fatalf("workspace revision did not advance: %s", workspaceTag)
	}
	workspaceTag = saved.Header().Get("ETag")
	for _, tc := range []struct {
		tag  string
		code int
	}{
		{"", http.StatusPreconditionRequired},
		{`W/` + workspaceTag, http.StatusBadRequest},
		{"*", http.StatusBadRequest},
		{workspaceTag + "," + workspaceTag, http.StatusBadRequest},
		{initialTag, http.StatusPreconditionFailed},
	} {
		workspaceTag = tc.tag
		if w := request(http.MethodPut, adminToken, "dev", string(payload)); w.Code != tc.code {
			t.Fatalf("If-Match %q: got %d %s, want %d", tc.tag, w.Code, w.Body.String(), tc.code)
		}
	}
	workspaceTag = saved.Header().Get("ETag")
	got := request(http.MethodGet, viewerToken, "dev", "")
	if got.Code != http.StatusOK || got.Body.String() != saved.Body.String() {
		t.Fatalf("read-after-write: %d %s; saved=%s", got.Code, got.Body.String(), saved.Body.String())
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.workspace.updated' AND target='default' AND outcome='success'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	// Legacy settings changes the same revision even when the visible name is
	// unchanged. A V3 editor holding the earlier ETag must not overwrite it.
	legacyRevision, ok := parseWorkspaceIfMatch(workspaceTag)
	if !ok {
		t.Fatalf("invalid saved ETag: %q", workspaceTag)
	}
	if _, err := settings.New(s, "").UpdateWorkspace(store.WithPrincipal(ctx, core.User{ID: "one", Role: "admin"}), settings.WorkspacePatch{Revision: legacyRevision, Name: &updated.Name}); err != nil {
		t.Fatalf("legacy workspace write: %v", err)
	}
	if w := request(http.MethodPut, adminToken, "dev", string(payload)); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale V3 write after legacy update: %d %s", w.Code, w.Body.String())
	}
	refreshed := request(http.MethodGet, adminToken, "dev", "")
	if refreshed.Code != http.StatusOK || refreshed.Body.String() != saved.Body.String() || refreshed.Header().Get("ETag") == workspaceTag {
		t.Fatalf("legacy revision/read mismatch: %d %s etag=%q", refreshed.Code, refreshed.Body.String(), refreshed.Header().Get("ETag"))
	}
	workspaceTag = refreshed.Header().Get("ETag")
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
	if _, err := s.DB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='workspace' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, viewerToken, "dev", ""); w.Code != http.StatusForbidden {
		t.Fatalf("viewer without workspace.view read settings: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.DB.Exec(ctx, `UPDATE workspace_settings SET v3_slug='Invalid Slug' WHERE id='default'`); err != nil {
		t.Fatal(err)
	}
	if w := requestWithLanguage(http.MethodGet, adminToken, "dev", "zh-CN", ""); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "工作区设置记录缺失或损坏") {
		t.Fatalf("corrupt stored workspace read: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, adminToken, "dev", string(payload)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("corrupt stored workspace write: %d %s", w.Code, w.Body.String())
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM workspace_settings WHERE id='default'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, adminToken, "dev", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing workspace read: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, adminToken, "dev", string(payload)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing workspace write: %d %s", w.Code, w.Body.String())
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
	_, _, err = New(s, nil).putWorkspace(ctx, core.User{ID: "one", Role: "admin"}, "dev", 1, input)
	if err != store.ErrForbidden {
		t.Fatalf("revoked grant: got %v, want forbidden", err)
	}
	var name string
	if err := s.DB.QueryRow(ctx, `SELECT name FROM workspace_settings WHERE id='default'`).Scan(&name); err != nil || name == input.Name {
		t.Fatalf("revoked write persisted: name=%q err=%v", name, err)
	}
}

func TestWorkspaceReadWaitsForViewRevocationBeforeReturningSettings(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-workspace-reader-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='two'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, "two@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(s, nil, true)
	New(s, nil).Register(api)
	api.MarkV3Route("GET /api/v1/workspace", "workspace", "view")
	policy := &rbac.V3Policy{Store: s}
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, s.Session,
		func(ctx context.Context, user core.User, module, action, environmentID string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environmentID})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/workspace", nil).WithContext(ctx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.Header.Set("Accept-Language", "en-US")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := request(ctx); w.Code != http.StatusOK {
		t.Fatalf("initial workspace read: %d %s", w.Code, w.Body.String())
	}
	var applicationName string
	if err := s.DB.QueryRow(ctx, `SELECT current_setting('application_name')`).Scan(&applicationName); err != nil || applicationName == "" {
		t.Fatalf("isolated application name: %q, %v", applicationName, err)
	}
	writer, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(context.Background())
	if _, err := writer.Exec(ctx, `SELECT pg_advisory_xact_lock(752018998)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='workspace' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- request(readCtx) }()
	// The route gate sees the old committed grant. The handler must then wait
	// behind the revocation and check authority again inside its read transaction.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting int
		if err := s.DB.QueryRow(readCtx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
 WHERE l.locktype='advisory' AND NOT l.granted AND l.mode='ShareLock' AND l.objid::bigint=$1 AND l.objsubid=1
 AND a.application_name=$2`, int64(752018998), applicationName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case w := <-result:
			t.Fatalf("workspace read completed before revocation: %d %s", w.Code, w.Body.String())
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("workspace read did not queue behind view revocation")
		}
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "Permission to view workspace settings denied") || strings.Contains(w.Body.String(), `"name":`) {
			t.Fatalf("revoked reader received workspace settings: %d %s", w.Code, w.Body.String())
		}
	case <-readCtx.Done():
		t.Fatal("workspace read remained blocked after revocation committed")
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
		_, _, writeErr := New(s, nil).putWorkspace(ctx, core.User{ID: "one", Role: "admin"}, "dev", 1, input)
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
