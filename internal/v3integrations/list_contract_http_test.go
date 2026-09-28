package v3integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConnectorListCompleteScopedCountsAndPersistedProbeState(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_list_" + store.ID()
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
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-list-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','environment','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,tools,status,last_checked_at,enabled) VALUES
 ('ok','dev','prometheus','Unique health connector','observe','http://127.0.0.1:9090','none','[]','ok',now(),true),
 ('failed','dev','loki','Loki','observe','http://127.0.0.1:3100','none','[]','error',now(),true),
 ('hidden','test','grafana','Grafana','observe','http://127.0.0.1:3000','none','[]','ok',now(),true)`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method)
 SELECT 'dev-'||n,'dev','prometheus','Probe '||lpad(n::text,3,'0'),'observe','http://127.0.0.1:'||(10000+n),'none'
 FROM generate_series(1,202) n`,
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
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(path, env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.Header.Set("Accept-Language", "zh-CN")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := func(w *httptest.ResponseRecorder) struct {
		Items      []struct{ ID, Status, Meta string } `json:"items"`
		Counts     map[string]int                      `json:"counts"`
		Categories []struct {
			ID    string
			Count int
		} `json:"categories"`
	} {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var result struct {
			Items      []struct{ ID, Status, Meta string } `json:"items"`
			Counts     map[string]int                      `json:"counts"`
			Categories []struct {
				ID    string
				Count int
			} `json:"categories"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	readDetail := func() (string, string, string) {
		t.Helper()
		w := request("/api/v1/connectors/ok", "dev")
		if w.Code != 200 {
			t.Fatalf("connector detail: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			Status  string `json:"status"`
			Meta    string `json:"meta"`
			Checked string `json:"checked"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Status, result.Meta, result.Checked
	}
	got := read(request("/api/v1/connectors", "all"))
	if len(got.Items) != 204 || got.Counts["ok"] != 1 || got.Counts["error"] != 1 || got.Counts["unverified"] != 202 {
		t.Fatalf("incomplete or unauthorized connector counts: items=%d counts=%v", len(got.Items), got.Counts)
	}
	if len(got.Categories) == 0 || got.Categories[0].ID != "all" || got.Categories[0].Count != 204 {
		t.Fatalf("incomplete category counts: %+v", got.Categories)
	}
	for _, item := range got.Items {
		if item.ID == "hidden" {
			t.Fatal("other environment leaked into list")
		}
		if item.ID == "ok" && (item.Status != "ok" || !strings.Contains(item.Meta, "已检查 ")) {
			t.Fatalf("persisted successful probe was not projected: %+v", item)
		}
		if item.ID == "failed" && (item.Status != "error" || !strings.Contains(item.Meta, "连接检查失败 ")) {
			t.Fatalf("persisted failed probe was not projected: %+v", item)
		}
	}
	status, meta, checked := readDetail()
	if status != "ok" || !strings.Contains(meta, "已检查 ") || checked == "" {
		t.Fatalf("fresh detail status=%q meta=%q checked=%q", status, meta, checked)
	}
	filtered := read(request("/api/v1/connectors?category=observe&q=unique", "dev"))
	if len(filtered.Items) != 1 || filtered.Items[0].ID != "ok" || filtered.Counts["unverified"] != 202 || filtered.Categories[0].Count != 204 {
		t.Fatalf("filtered items changed global visible counts: items=%d counts=%v categories=%v", len(filtered.Items), filtered.Counts, filtered.Categories)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET last_checked_at=now()-interval '16 minutes' WHERE id='ok'`); err != nil {
		t.Fatal(err)
	}
	stale := read(request("/api/v1/connectors?category=observe&q=unique", "dev"))
	if len(stale.Items) != 1 || stale.Items[0].Status != "unverified" || !strings.Contains(stale.Items[0].Meta, "已过期") || stale.Counts["ok"] != 0 || stale.Counts["unverified"] != 203 {
		t.Fatalf("stale probe was treated as current health: items=%v counts=%v", stale.Items, stale.Counts)
	}
	status, meta, staleChecked := readDetail()
	if status != "unverified" || !strings.Contains(meta, "已过期") || staleChecked == "" || staleChecked == checked {
		t.Fatalf("stale detail status=%q meta=%q checked=%q", status, meta, staleChecked)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET status='ok',last_checked_at=NULL WHERE id='ok'`); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/connectors", "dev"); w.Code != 503 || !strings.Contains(w.Body.String(), "connector_state_unavailable") {
		t.Fatalf("unproven healthy state was accepted: %d %s", w.Code, w.Body.String())
	}
	if w := request("/api/v1/connectors/ok", "dev"); w.Code != 503 || !strings.Contains(w.Body.String(), "connector_state_unavailable") {
		t.Fatalf("unproven detail state was accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET status='unverified' WHERE id='ok'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method)
 SELECT 'dev-'||n,'dev','prometheus','Probe '||lpad(n::text,4,'0'),'observe','http://127.0.0.1:'||(10000+n),'none'
 FROM generate_series(203,1000) n`); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/connectors", "dev"); w.Code != 413 || !strings.Contains(w.Body.String(), "scope_too_large") {
		t.Fatalf("oversize unpaged collection: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors'`); err != nil {
		t.Fatal(err)
	}
	if w := request("/api/v1/connectors", "dev"); w.Code != 403 {
		t.Fatalf("revoked connector permission: %d %s", w.Code, w.Body.String())
	}
}
