package v3audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func isolatedAuditStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops only a random isolated schema")
	}
	ctx := t.Context()
	admin, err := store.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_audit_test_" + store.ID()
	if _, err := admin.DB.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.DB.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuditRegisteredHTTPScopesListAndVerifiedDetail(t *testing.T) {
	s := isolatedAuditStore(t)
	ctx := t.Context()
	password := "isolated-audit-test-password"
	_, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
	 ('audit-admin','audit-admin@example.invalid','Audit Admin','admin',$1),
	 ('audit-viewer','audit-viewer@example.invalid','Audit Viewer','viewer',$1)`, store.PasswordHash(password))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, "audit-admin@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := s.Login(ctx, "audit-viewer@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	var devID, testID, otherID, globalID int64
	for _, row := range []struct {
		id   *int64
		kind string
		name string
		env  string
	}{
		{&devID, "user", "部署变更", "dev"},
		{&testID, "agent", "triage_test", "test"},
		{&otherID, "user", "hidden_other", "other"},
		{&globalID, "system", "workspace_change", ""},
	} {
		changes := `{"environment_id":"` + row.env + `"}`
		if row.id == &devID {
			changes = `{"environment_id":"dev","credential":"private-test-value"}`
		}
		if row.env == "" {
			changes = `{}`
		}
		if err := s.DB.QueryRow(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
		 VALUES('audit-admin',$1,$2,'target','success',$3::jsonb) RETURNING id`, row.kind, row.name, changes).Scan(row.id); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: s}
	api := httpapi.New(s, nil, true)
	New(s.DB, []string{"dev", "test"}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, policy, s.Session,
		func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(authToken, env, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authToken != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: authToken})
		}
		if env != "" {
			r.Header.Set("X-AIOps-Environment", env)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
	}
	assertStatus(request("", "dev", "/api/v1/audit"), http.StatusUnauthorized)
	assertStatus(request(viewerToken, "dev", "/api/v1/audit"), http.StatusForbidden)
	assertStatus(request(token, "other", "/api/v1/audit"), http.StatusUnprocessableEntity)
	w := request(token, "dev", "/api/v1/audit")
	assertStatus(w, http.StatusOK)
	var list struct {
		Items  []entry          `json:"items"`
		Counts map[string]int64 `json:"counts"`
		Volume [][]int64        `json:"volume"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].ID != strconv.FormatInt(globalID, 10) || list.Items[1].ID != strconv.FormatInt(devID, 10) ||
		list.Counts["all"] != 2 || list.Counts["user"] != 1 || list.Counts["system"] != 1 || len(list.Volume) != 7 {
		t.Fatalf("unexpected scoped audit list: %+v", list)
	}
	w = request(token, "dev", "/api/v1/audit?filter=user&q=bushubian")
	assertStatus(w, http.StatusOK)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != strconv.FormatInt(devID, 10) {
		t.Fatalf("filtered audit list=%+v err=%v", list, err)
	}
	w = request(token, "dev", "/api/v1/audit/"+strconv.FormatInt(devID, 10))
	assertStatus(w, http.StatusOK)
	var detail struct {
		ID      string `json:"id"`
		ChainOK bool   `json:"chain_ok"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.ID != strconv.FormatInt(devID, 10) || !detail.ChainOK {
		t.Fatalf("audit detail=%+v err=%v", detail, err)
	}
	if detail.Payload == "" || !json.Valid([]byte(detail.Payload)) || strings.Contains(detail.Payload, "private-test-value") {
		t.Fatalf("invalid redacted payload: %q", detail.Payload)
	}
	assertStatus(request(token, "dev", "/api/v1/audit/"+strconv.FormatInt(testID, 10)), http.StatusNotFound)
	assertStatus(request(token, "dev", "/api/v1/audit/"+strconv.FormatInt(otherID, 10)), http.StatusNotFound)
	assertStatus(request(token, "dev", "/api/v1/audit/invalid"), http.StatusNotFound)
	export := func(authToken, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/audit/export", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: authToken})
		r.Header.Set("X-AIOps-Environment", env)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertStatus(export(token, "all", `{"filter":"user"}`), http.StatusConflict)
	w = export(token, "dev", `{"filter":"user"}`)
	assertStatus(w, http.StatusOK)
	var exported struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &exported); err != nil || !strings.HasPrefix(exported.URL, "/api/v1/audit/exports/") {
		t.Fatalf("export response=%s err=%v", w.Body.String(), err)
	}
	assertStatus(request(viewerToken, "", exported.URL), http.StatusNotFound)
	assertStatus(request(token, "test", exported.URL), http.StatusForbidden)
	w = request(token, "", exported.URL)
	assertStatus(w, http.StatusOK)
	if got := w.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("export content type=%q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "部署变更") || strings.Contains(body, "triage_test") || strings.Contains(body, "hidden_other") || strings.Contains(body, "private-test-value") {
		t.Fatalf("incorrect scoped CSV: %q", body)
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='audit' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(request(token, "dev", "/api/v1/audit"), http.StatusForbidden)
	assertStatus(request(token, "", exported.URL), http.StatusForbidden)
}
