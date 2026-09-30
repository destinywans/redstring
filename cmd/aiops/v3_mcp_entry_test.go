package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConfiguredV3MCPCatalogAndReviewRoutes(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_entry_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
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
	if err := st.Bootstrap(ctx, "mcp-entry-admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "mcp-entry@example.invalid", Name: "MCP manager", Role: "viewer"}, "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, viewer.Email, "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','application','app'),
 ('viewer','connectors','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES('app','app','App','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES('app-dev','app','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "entry-fixture", Version: "1.2.3"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "read_app", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			t.Error("discovery executed a tool")
			return &mcp.CallToolResult{}, nil, nil
		})
	server.AddResource(&mcp.Resource{URI: "test://app/service", Name: "service"}, nil)
	server.AddPrompt(&mcp.Prompt{Name: "summary"}, nil)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "token"), []byte("local-token"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIOPS_MCP_CREDENTIAL_ROOT", root)
	record := aiextensions.Record{ID: "mcp-1", ApplicationID: "app", Kind: "mcp", Title: "App MCP",
		Maintainer: "platform", Enabled: true, Version: 1, UpdatedAt: time.Now().UTC(),
		MCP: &aiextensions.MCPConfig{Endpoint: remote.URL, AllowHTTP: true, CredentialRef: "token",
			Bindings: []aiextensions.Binding{{Name: "read_app", Tool: "read_app", ReadOnlyConfirmed: true, Arguments: map[string]any{}}}}}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES('mcp-1','app','mcp',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','dev',id FROM users WHERE email='mcp-entry-admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	runner := &v3BindingRunner{}
	api := httpapi.New(st, runner, true)
	instance := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}
	if err := configureV3(ctx, st, runner, api, nil, nil, &v3Instance{}, instance, operations.Config{}); err != nil {
		t.Fatal(err)
	}
	entry := httptest.NewServer(api)
	defer entry.Close()
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		var input io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, entry.URL+path, input)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-AIOps-Environment", "dev")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		resp, err := entry.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("%s %s = %d %s", method, path, resp.StatusCode, data)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	base := "/api/v1/mcp-servers/mcp-1"
	call(http.MethodPost, base+"/discover", nil)
	detail := call(http.MethodGet, base, nil)
	if detail["version"] != "1.2.3" || detail["calls"] != nil {
		t.Fatalf("formal detail version/calls: %+v", detail)
	}
	counts, ok := detail["counts"].(map[string]any)
	if !ok || counts["tools"] != float64(1) || counts["resources"] != float64(1) || counts["prompts"] != float64(1) {
		t.Fatalf("formal discovery did not use full catalog: %+v", detail)
	}
	for _, secret := range []string{remote.URL, "local-token", root} {
		encoded, _ := json.Marshal(detail)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("detail disclosed private MCP data: %s", secret)
		}
	}
	candidate := call(http.MethodGet, base+"/review-candidate", nil)
	tools, ok := candidate["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("review candidate: %+v", candidate)
	}
	tool := tools[0].(map[string]any)
	review := map[string]any{"snapshot_id": candidate["snapshot_id"],
		"registration_version": candidate["registration_version"], "registration_hash": candidate["registration_hash"],
		"tools_hash": candidate["tools_hash"], "decisions": []map[string]any{{
			"name": "read_app", "tool_hash": tool["tool_hash"], "decision": "allow_read_only"}}}
	call(http.MethodPost, base+"/review", review)
	change := map[string]any{"snapshot_id": candidate["snapshot_id"],
		"registration_version": candidate["registration_version"], "registration_hash": candidate["registration_hash"],
		"tools_hash": candidate["tools_hash"], "tool_hash": tool["tool_hash"], "expected_revision": 1, "enabled": true}
	call(http.MethodPatch, base+"/tools/read_app", change)
	final := call(http.MethodGet, base, nil)
	state, ok := final["review"].(map[string]any)
	if !ok || state["reviewed"] != true || state["enabled_count"] != float64(1) {
		t.Fatalf("formal review/toggle did not persist: %+v", final)
	}
}
