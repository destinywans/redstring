package v3integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConnectorReadsUsePersistedScopedRecords(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connectors_read_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-connector-password"); err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','environment','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,tools,status,enabled)
 VALUES ('metrics','dev','prometheus','监控指标','observe','http://127.0.0.1:9090','none','[{"name":"query_metrics","desc":"Read-only","enabled":true}]','unverified',true)`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,tools,status,enabled)
 VALUES ('code','test','gitlab','GitLab','delivery','https://gitlab.example.invalid','pat','vault://isolated/token#value','[]','error',true)`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,tools,enabled) VALUES
 ('credential-draft','dev','gitlab','Draft','delivery','https://draft.example.invalid','pat','','[{"name":"list_pipelines","enabled":false}]',false),
 ('credential-registered','dev','gitlab','Registered','delivery','https://registered.example.invalid','pat','vault://isolated/token#value','[{"name":"list_pipelines","enabled":true}]',true)`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", nil).Register(api)
	for _, route := range []string{"GET /api/v1/connectors", "GET /api/v1/connectors/{id}"} {
		api.MarkV3Route(route, "connectors", "view")
	}
	api.MarkV3Route("POST /api/v1/connectors/{id}/test", "connectors", "manage")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
	}); err != nil {
		t.Fatal(err)
	}
	request := func(path, env, auth string, languages ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", env)
		language := "zh-CN"
		if len(languages) > 0 {
			language = languages[0]
		}
		r.Header.Set("Accept-Language", language)
		if auth != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: auth})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := request("/api/v1/connectors", "dev", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d %s", w.Code, w.Body.String())
	}
	if w := request("/api/v1/connectors/metrics", "dev", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous detail: %d %s", w.Code, w.Body.String())
	}
	if w := request("/api/v1/connectors", "test", token); w.Code != http.StatusForbidden {
		t.Fatalf("other environment: %d %s", w.Code, w.Body.String())
	}
	listed := request("/api/v1/connectors?category=observe&q=jiankong", "all", token)
	if listed.Code != http.StatusOK {
		t.Fatalf("scoped list: %d %s", listed.Code, listed.Body.String())
	}
	var list struct {
		Items []struct {
			ID, Kind, Meta string
			Tags           []struct{ Label, Tone string }
		} `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "metrics" || list.Items[0].Kind != "指标" || list.Items[0].Meta != "尚未检查连接" || list.Items[0].Tags == nil || len(list.Items[0].Tags) != 0 || list.Counts["unverified"] != 3 || list.Counts["error"] != 0 {
		t.Fatalf("unexpected list: %s", listed.Body.String())
	}
	if w := request("/api/v1/connectors/code", "all", token); w.Code != http.StatusNotFound {
		t.Fatalf("hidden detail: %d %s", w.Code, w.Body.String())
	}
	detail := request("/api/v1/connectors/metrics", "dev", token)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}
	var rawDetail map[string]json.RawMessage
	if err := json.Unmarshal(detail.Body.Bytes(), &rawDetail); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "brand", "name", "kind", "category", "status", "tags", "meta", "scope", "address", "auth", "checked", "services", "queries", "tools"} {
		if _, ok := rawDetail[field]; !ok {
			t.Fatalf("detail missing %s: %s", field, detail.Body.String())
		}
	}
	var got struct {
		Auth struct {
			Brand string `json:"brand"`
			Text  string `json:"text"`
		} `json:"auth"`
		Checked string   `json:"checked"`
		Tools   []tool   `json:"tools"`
		Tags    []any    `json:"tags"`
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Auth.Brand != "" || got.Auth.Text != "无需认证" || got.Checked != "尚未检查" || got.Tools == nil || len(got.Tools) != 0 || got.Tags == nil || len(got.Tags) != 0 || got.Queries == nil || len(got.Queries) != 0 {
		t.Fatalf("unexpected detail: %s", detail.Body.String())
	}
	for _, tc := range []struct {
		id, language, brand, text, checked, meta string
	}{
		{"credential-draft", "zh-CN", "", "个人访问令牌 · 凭据未配置", "尚未检查", "未启用 · 凭据未配置"},
		{"credential-registered", "en-US", "vault", "Personal access token · Vault reference registered", "Never checked", "Connection not tested"},
	} {
		w := request("/api/v1/connectors/"+tc.id, "dev", token, tc.language)
		if w.Code != http.StatusOK {
			t.Fatalf("detail %s: %d %s", tc.id, w.Code, w.Body.String())
		}
		var item struct {
			Status string `json:"status"`
			Meta   string `json:"meta"`
			Auth   struct {
				Brand string `json:"brand"`
				Text  string `json:"text"`
			} `json:"auth"`
			Checked string   `json:"checked"`
			Tags    []any    `json:"tags"`
			Queries []string `json:"queries"`
			Tools   []tool   `json:"tools"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		if item.Status != "unverified" || item.Meta != tc.meta || item.Auth.Brand != tc.brand || item.Auth.Text != tc.text || item.Checked != tc.checked || item.Tags == nil || len(item.Tags) != 0 || item.Queries == nil || len(item.Queries) != 0 || item.Tools == nil || len(item.Tools) != 0 {
			t.Fatalf("unverified connector %s advertised capability: %s", tc.id, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "vault://") {
			t.Fatalf("credential reference leaked from %s", tc.id)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=true,status='ok',last_checked_at=now() WHERE id='credential-draft'`); err != nil {
		t.Fatal(err)
	}
	unusable := request("/api/v1/connectors/credential-draft", "dev", token)
	if unusable.Code != http.StatusOK {
		t.Fatalf("missing-credential detail: %d %s", unusable.Code, unusable.Body.String())
	}
	var state struct {
		Status  string `json:"status"`
		Meta    string `json:"meta"`
		Checked string `json:"checked"`
	}
	if err := json.Unmarshal(unusable.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Status != "unverified" || state.Meta != "凭据未配置" || state.Checked == "" || state.Checked == "尚未检查" {
		t.Fatalf("missing credentials appeared healthy: %s", unusable.Body.String())
	}
	if strings.Contains(detail.Body.String(), "vault://") {
		t.Fatal("credential reference leaked")
	}
	var upstreamHealthy atomic.Bool
	upstreamHealthy.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/-/ready" {
			t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !upstreamHealthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled)
 VALUES('local-probe','dev','prometheus','Local probe','observe',$1,'none',true)`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	probe := func(env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/local-probe/test", nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := probe("dev"); w.Code != http.StatusForbidden {
		t.Fatalf("view-only connection test: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if w := probe("all"); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "env_view_only") {
		t.Fatalf("all-scope connection test: %d %s", w.Code, w.Body.String())
	}
	if w := probe("dev"); w.Code != http.StatusOK {
		t.Fatalf("authorized connection test: %d %s", w.Code, w.Body.String())
	}
	var status string
	var checked bool
	if err := db.QueryRow(ctx, `SELECT status,last_checked_at IS NOT NULL FROM v3_connectors WHERE id='local-probe'`).Scan(&status, &checked); err != nil || status != "ok" || !checked {
		t.Fatalf("persisted connection check: status=%q checked=%v err=%v", status, checked, err)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='connector.connection.tested' AND target='local-probe' AND outcome='success'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("connection check audit: count=%d err=%v", audits, err)
	}
	upstreamHealthy.Store(false)
	if w := probe("dev"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "connector_test_failed") {
		t.Fatalf("failed upstream connection test: %d %s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT status,last_checked_at IS NOT NULL FROM v3_connectors WHERE id='local-probe'`).Scan(&status, &checked); err != nil || status != "error" || !checked {
		t.Fatalf("persisted failed connection check: status=%q checked=%v err=%v", status, checked, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='connector.connection.tested' AND target='local-probe' AND outcome='failure'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("failed check audit: count=%d err=%v", audits, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors'`); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/connectors", "dev", token); w.Code != http.StatusForbidden {
		t.Fatalf("revoked grant: %d %s", w.Code, w.Body.String())
	}
	if w := request("/api/v1/connectors/metrics", "dev", token); w.Code != http.StatusForbidden {
		t.Fatalf("revoked detail grant: %d %s", w.Code, w.Body.String())
	}
}
