package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/destinywans/redstring/internal/actions"
	"github.com/destinywans/redstring/internal/aiextensions"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	// Match main's prerequisite: the V3 execution read projection references
	// the recovery ledger even though V3 does not start its legacy workers.
	if err := actions.New(db, nil, "dev").Migrate(ctx); err != nil {
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
	var toolCalls atomic.Int32
	addRead := func(description string) {
		mcp.AddTool(server, &mcp.Tool{Name: "read_app", Description: description, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
				toolCalls.Add(1)
				if string(req.Params.Arguments) != "{}" {
					t.Errorf("model arguments reached MCP: %s", req.Params.Arguments)
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture: two ready replicas"}}}, nil, nil
			})
	}
	addRead("Read the fixture app")
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
	var info v3Instance
	if err := json.Unmarshal([]byte(`{"tier":"nonprod","domain":"entry.example.invalid","environments":[{"id":"dev"}]}`), &info); err != nil {
		t.Fatal(err)
	}
	registrationConfig, err := json.Marshal(v3MCPRegistrationFile{Tier: info.Tier, InstanceID: info.Domain,
		Bindings: []v3MCPRegistrationBinding{{ApplicationID: "app", EnvironmentID: "dev", Endpoint: remote.URL, AllowHTTP: true, CredentialRef: "token", ReadTools: []string{"read_app"}}}})
	if err != nil {
		t.Fatal(err)
	}
	registrationPath := filepath.Join(root, "registration-bindings.json")
	if err := os.WriteFile(registrationPath, registrationConfig, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIOPS_V3_MCP_REGISTRATION_BINDINGS", registrationPath)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Tools []struct {
				Function struct{ Name string }
			}
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		delta := map[string]any{}
		finish := "stop"
		var query string
		var observation bool
		for _, message := range input.Messages {
			if message.Role == "user" && message.Content == "request invalid scope" {
				query = "another-app"
			}
			if message.Role == "tool" && strings.Contains(message.Content, "fixture: two ready replicas") {
				observation = true
			}
		}
		if observation {
			delta["content"] = "The MCP source reports two ready replicas."
		} else {
			if len(input.Tools) != 1 || !strings.HasPrefix(input.Tools[0].Function.Name, "mcp_") {
				t.Error("model did not receive exactly one pinned MCP choice")
				return
			}
			args, _ := json.Marshal(map[string]string{"query": query})
			delta["tool_calls"] = []map[string]any{{
				"index": 0, "id": "call-mcp", "type": "function",
				"function": map[string]string{"name": input.Tools[0].Function.Name, "arguments": string(args)},
			}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer model.Close()
	runner, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "mcp-fixture-model", Kind: "openai", Model: "fixture", BaseURL: model.URL}}, HTTPClient: model.Client()})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIOPS_V3_TOOL_MODELS", "mcp-fixture-model")
	api := httpapi.New(st, runner, true)
	instance := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}
	if err := configureV3(ctx, st, runner, api, nil, &info, instance, operations.Config{}); err != nil {
		t.Fatal(err)
	}
	entry := httptest.NewServer(api)
	defer entry.Close()
	callAs := func(token, method, path string, body any, want int) map[string]any {
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
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		resp, err := entry.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s = %d %s", method, path, resp.StatusCode, data)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		want := http.StatusOK
		if method == http.MethodPost && path == "/api/v1/mcp-servers" {
			want = http.StatusCreated
		}
		return callAs(session, method, path, body, want)
	}
	created := call(http.MethodPost, "/api/v1/mcp-servers", map[string]any{
		"application_id": "app", "title": "App MCP", "maintainer": "platform",
		"endpoint": remote.URL, "allow_http": true, "credential_ref": "token",
	})
	id, ok := created["id"].(string)
	if !ok || id == "" || created["execution_available"] != false || created["reviewed"] != false {
		t.Fatalf("new registration did not preserve pending state: %+v", created)
	}
	base := "/api/v1/mcp-servers/" + id
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
	if toolCalls.Load() != 0 {
		t.Fatal("registration, discovery or review executed a tool")
	}
	_, adminToken, err := st.Login(ctx, "mcp-entry-admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	admin := func(method, path string, input any, status int) map[string]any {
		t.Helper()
		return callAs(adminToken, method, "/api/v1"+path, input, status)
	}
	agent := "/agents/mcp-entry-agent"
	admin("POST", "/agents", map[string]any{"id": "mcp-entry-agent", "application_id": "app", "name": "MCP integration"}, 201)
	admin("POST", agent+"/versions", map[string]any{"version": "v1", "artifact_ref": "git:synthetic-mcp"}, 201)
	prompt := admin("POST", "/prompts", map[string]any{"agent_id": "mcp-entry-agent", "name": "MCP integration prompt", "text": "Read the fixture status."}, 201)["id"].(string)
	choices := admin("GET", agent+"/execution-mcp-tools", nil, 200)["items"].([]any)
	if len(choices) != 1 {
		t.Fatalf("unexpected tool choices: %v", choices)
	}
	alias := choices[0].(map[string]any)["id"].(string)
	for _, secret := range []string{remote.URL, "local-token", root} {
		raw, _ := json.Marshal(choices)
		if strings.Contains(string(raw), secret) {
			t.Fatal("tool choices disclosed private transport configuration")
		}
	}
	config := map[string]any{"model_id": "mcp-fixture-model", "prompt_id": prompt, "prompt_version": 1,
		"tool_names": []string{"mcp_" + strings.Repeat("0", 32)}, "max_model_calls": 3, "max_tool_calls": 1,
		"max_input_bytes": 1024, "max_output_bytes": 1024, "max_output_tokens": 64, "timeout_seconds": 10}
	admin("PUT", agent+"/versions/v1/execution", config, 422)
	config["tool_names"] = []string{alias}
	admin("PUT", agent+"/versions/v1/execution", config, 200)
	admin("PUT", "/agents/environment-limit", map[string]any{"max_level": "L1", "expected_max_level": "L0"}, 200)
	admin("PATCH", agent+"/level", map[string]any{"level": "L1", "expected_revision": 1}, 200)
	admin("POST", agent+"/rollout", map[string]any{"to": "v1", "percent": 100, "expected_revision": 2,
		"override": true, "override_reason": "Synthetic integration"}, 200)
	run := admin("POST", agent+"/runs", map[string]any{"input": "check fixture"}, 200)
	runPath := "/agent-runs/" + run["id"].(string)
	if run["status"] != "success" || run["output"] != "The MCP source reports two ready replicas." || toolCalls.Load() != 1 {
		t.Fatalf("MCP did not execute in real governed run: %+v calls=%d trace=%+v", run, toolCalls.Load(), admin("GET", runPath+"/trace", nil, 200))
	}
	result := admin("GET", runPath+"/result", nil, 200)
	evidence := result["mcp_evidence"].([]any)
	if len(evidence) != 1 || !strings.Contains(evidence[0].(map[string]any)["content"].(string), "fixture: two ready replicas") {
		t.Fatalf("evidence missing from private result: %+v", evidence)
	}
	trace, _ := json.Marshal(admin("GET", runPath+"/trace", nil, 200))
	if !strings.Contains(string(trace), alias) || strings.Contains(string(trace), "fixture: two ready replicas") {
		t.Fatalf("tool span missing or contains private evidence: %s", trace)
	}
	if invalid := admin("POST", agent+"/runs", map[string]any{"input": "request invalid scope"}, 200); invalid["status"] == "success" || toolCalls.Load() != 1 {
		t.Fatalf("model modified fixed scope: %+v calls=%d", invalid, toolCalls.Load())
	}
	addRead("Provider changed the declaration")
	if drift := admin("POST", agent+"/runs", map[string]any{"input": "check fixture"}, 200); drift["status"] == "success" || toolCalls.Load() != 1 {
		t.Fatalf("changed declaration ran without approval: %+v calls=%d", drift, toolCalls.Load())
	}
	change["expected_revision"], change["enabled"] = 2, false
	call(http.MethodPatch, base+"/tools/read_app", change)
	if disabled := admin("GET", agent+"/execution-mcp-tools", nil, 200)["items"].([]any); len(disabled) != 0 {
		t.Fatal("disabled tool remained selectable")
	}
	admin("POST", agent+"/runs", map[string]any{"input": "check fixture"}, 409)
	change["expected_revision"], change["enabled"] = 3, true
	call(http.MethodPatch, base+"/tools/read_app", change)
	reenabled := admin("GET", agent+"/execution-mcp-tools", nil, 200)["items"].([]any)
	if len(reenabled) != 1 || reenabled[0].(map[string]any)["id"] == alias {
		t.Fatal("old identity resurrected after re-enable")
	}
	admin("POST", agent+"/runs", map[string]any{"input": "check fixture"}, 409)
}
