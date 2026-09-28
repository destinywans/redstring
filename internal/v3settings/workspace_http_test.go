package v3settings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestWorkspaceAuthenticatedHTTPPersistenceAndAudit(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-workspace-test-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id IN ('one','two')`, store.PasswordHash(password)); err != nil {
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
	err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, allowPreferencesEnvironment{}, s.Session,
		func(_ context.Context, user core.User, module, action, _ string) error {
			if module != "workspace" {
				t.Errorf("unexpected policy module: %s", module)
				return store.ErrForbidden
			}
			if action == "manage" && user.ID != "one" {
				return store.ErrForbidden
			}
			return nil
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
		t.Fatalf("viewer write: %d %s", w.Code, w.Body.String())
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
}
