package v3mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestMCPReadsAndBindingUseSingleConnectionWithoutPartialLists(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_reader_" + store.ID()
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
	if err := aiextensions.New(db, "").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','application','app'),
 ('viewer','connectors','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES
 ('app','app','App','dev',false),('hidden','hidden','Hidden','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('app-dev','app','dev','Dev',false),('hidden-dev','hidden','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app','dev'),('hidden','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	record := aiextensions.Record{
		ID: "mcp-1", ApplicationID: "app", Kind: "mcp", Title: "App MCP",
		Maintainer: "platform", Enabled: true, Version: 1, UpdatedAt: time.Now().UTC(),
		MCP: &aiextensions.MCPConfig{Endpoint: "https://private.example.invalid/mcp", CredentialRef: "secret-ref",
			Bindings: []aiextensions.Binding{{Name: "read_app", Tool: "read_app", ReadOnlyConfirmed: true, Arguments: map[string]any{}}}},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES('mcp-1','app','mcp',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, policy).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, nil).WithContext(callCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/v1/mcp-servers", "/api/v1/mcp-servers/mcp-1"} {
		w := request(http.MethodGet, path)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"complete_v3_dto":false`) ||
			strings.Contains(w.Body.String(), "secret-ref") || strings.Contains(w.Body.String(), "private.example.invalid") {
			t.Fatalf("read %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := request(http.MethodPut, "/api/v1/mcp-servers/mcp-1/environment-binding"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"changed":false`) {
		t.Fatalf("idempotent binding: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("revoked list: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers/mcp-1"); w.Code != http.StatusNotFound {
		t.Fatalf("revoked detail: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','connectors','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record)
 SELECT 'extra-'||g,'hidden','mcp',1,
 jsonb_build_object('id','extra-'||g,'application_id','hidden','kind','mcp',
  'title','Hidden','mcp',jsonb_build_object('bindings','[]'::jsonb))
 FROM generate_series(1,1000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'extra-'||g,'hidden','dev',(SELECT id FROM users WHERE email='admin@example.invalid')
 FROM generate_series(1,1000) g`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"id":"mcp-1"`) || strings.Contains(w.Body.String(), `"id":"extra-`) {
		t.Fatalf("hidden records blocked visible list: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','connectors','view','application','hidden')`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, "/api/v1/mcp-servers"); w.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(w.Body.String(), `"code":"scope_too_large"`) || strings.Contains(w.Body.String(), `"items"`) {
		t.Fatalf("truncated list: %d %s", w.Code, w.Body.String())
	}
}
