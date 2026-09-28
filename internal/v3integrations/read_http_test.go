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
	request := func(path, env, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.Header.Set("Accept-Language", "zh-CN")
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
		} `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "metrics" || list.Items[0].Kind != "指标" || list.Items[0].Meta != "尚未检查连接" || list.Counts["unverified"] != 1 || list.Counts["error"] != 0 {
		t.Fatalf("unexpected list: %s", listed.Body.String())
	}
	if w := request("/api/v1/connectors/code", "all", token); w.Code != http.StatusNotFound {
		t.Fatalf("hidden detail: %d %s", w.Code, w.Body.String())
	}
	detail := request("/api/v1/connectors/metrics", "dev", token)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}
	var got struct {
		Auth struct {
			Brand string `json:"brand"`
			Text  string `json:"text"`
		} `json:"auth"`
		Checked string `json:"checked"`
		Tools   []tool `json:"tools"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Auth.Brand != "" || got.Auth.Text != "无需认证" || got.Checked != "尚未检查" || len(got.Tools) != 1 || got.Tools[0].Desc != "查询指标" {
		t.Fatalf("unexpected detail: %s", detail.Body.String())
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
}
