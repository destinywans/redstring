package v3mcp

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
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestMCPMainRoutesRealCatalogReviewAndToggleIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_main_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.MaxConns = 4
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = aiextensions.New(db, "").Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	manager, err := st.CreateUser(ctx, core.User{Email: "manager@example.invalid", Name: "Manager", Role: "viewer"}, "isolated-manager-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, manager.Email, "isolated-manager-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','application','app'),
 ('viewer','connectors','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES ('app','app','App','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES ('app-dev','app','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app','dev')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "fixture-service", Version: "2.4.1"}, &mcp.ServerOptions{PageSize: 1})
	for _, tool := range []struct {
		name     string
		readOnly bool
	}{{"read_app", true}, {"write_app", false}} {
		mcp.AddTool(server, &mcp.Tool{Name: tool.name, Description: "fixture", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: tool.readOnly}},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				t.Error("discovery executed a tool")
				return &mcp.CallToolResult{}, nil, nil
			})
	}
	server.AddResource(&mcp.Resource{URI: "test://app/service", Name: "service", Description: "resource"}, nil)
	server.AddPrompt(&mcp.Prompt{Name: "summary", Description: "prompt"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	var mu sync.Mutex
	methods := map[string]int{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-only-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Body != nil {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<10))
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &request) == nil {
				mu.Lock()
				methods[request.Method]++
				mu.Unlock()
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	credentialRoot := t.TempDir()
	if err = os.Chmod(credentialRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(credentialRoot, "fixture-token"), []byte("fixture-only-token"), 0600); err != nil {
		t.Fatal(err)
	}
	record := aiextensions.Record{ID: "mcp-1", ApplicationID: "app", Kind: "mcp", Title: "App MCP",
		Maintainer: "platform", Enabled: true, Version: 1, UpdatedAt: time.Now().UTC(),
		MCP: &aiextensions.MCPConfig{Endpoint: remote.URL, AllowHTTP: true, CredentialRef: "fixture-token",
			Bindings: []aiextensions.Binding{{Name: "read_app", Tool: "read_app", ReadOnlyConfirmed: true, Arguments: map[string]any{}}}},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES('mcp-1','app','mcp',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(db, policy)
	service.DiscoverCatalog = func(ctx context.Context, c aiextensions.MCPConfig) (aiextensions.MCPCatalog, error) {
		return aiextensions.DiscoverCatalog(ctx, credentialRoot, c)
	}
	service.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/mcp-servers/mcp-1"
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var input io.Reader
		if body != nil {
			encoded, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			input = bytes.NewReader(encoded)
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, input).WithContext(callCtx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		if method == http.MethodGet && strings.HasSuffix(path, "/review-candidate") {
			api.ServeHTTP(&candidateDeadlineRecorder{w}, r)
		} else {
			api.ServeHTTP(w, r)
		}
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var result map[string]any
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatalf("invalid JSON %d: %v %s", w.Code, e, w.Body.String())
		}
		return result
	}
	object := func(v any) map[string]any {
		t.Helper()
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("expected object, got %T: %v", v, v)
		}
		return m
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if e := db.QueryRow(ctx, sql).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	before := call(http.MethodGet, "/api/v1/mcp-servers", nil)
	if before.Code != 200 {
		t.Fatalf("pre-discovery list: %d %s", before.Code, before.Body.String())
	}
	items, ok := decode(before)["items"].([]any)
	if !ok || len(items) != 1 || object(items[0])["discovery"] != nil || object(items[0])["call_metrics"] != nil {
		t.Fatalf("pre-discovery list fabricated state: %s", before.Body.String())
	}
	before = call(http.MethodGet, base, nil)
	if before.Code != 200 {
		t.Fatalf("pre-discovery detail: %d %s", before.Code, before.Body.String())
	}
	if counts := object(decode(before)["counts"]); counts["tools"] != nil || counts["resources"] != nil || counts["prompts"] != nil {
		t.Fatalf("unknown counts fabricated as zero: %s", before.Body.String())
	}
	if w := call(http.MethodPost, base+"/review", nil); w.Code != 422 {
		t.Fatalf("unversioned review accepted: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, base+"/discover", nil); w.Code != 200 {
		t.Fatalf("real MCP discovery: %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	if methods["tools/list"] < 2 || methods["resources/list"] != 1 || methods["prompts/list"] != 1 ||
		methods["tools/call"] != 0 || methods["resources/read"] != 0 || methods["prompts/get"] != 0 {
		t.Fatalf("unexpected upstream methods: %+v", methods)
	}
	mu.Unlock()
	listed := call(http.MethodGet, "/api/v1/mcp-servers", nil)
	if listed.Code != 200 {
		t.Fatalf("discovered list: %d %s", listed.Code, listed.Body.String())
	}
	items, ok = decode(listed)["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("discovered list item missing: %s", listed.Body.String())
	}
	item := object(items[0])
	discovery := object(item["discovery"])
	if discovery["server_name"] != "fixture-service" || discovery["server_version"] != "2.4.1" ||
		object(discovery["tools"])["count"] != float64(2) || object(discovery["resources"])["count"] != float64(1) ||
		object(discovery["prompts"])["count"] != float64(1) || item["call_metrics"] != nil {
		t.Fatalf("unsourced list fields: %s", listed.Body.String())
	}
	detailed := call(http.MethodGet, base, nil)
	if detailed.Code != 200 {
		t.Fatalf("discovered detail: %d %s", detailed.Code, detailed.Body.String())
	}
	view := decode(detailed)
	if view["version"] != "2.4.1" || view["calls"] != nil || object(view["counts"])["tools"] != float64(2) ||
		object(view["counts"])["resources"] != float64(1) || object(view["counts"])["prompts"] != float64(1) {
		t.Fatalf("detail counts/version/calls: %s", detailed.Body.String())
	}
	resources, ok := view["resources"].([]any)
	if !ok || len(resources) != 1 || object(resources[0])["uri"] != "test://app/service" {
		t.Fatalf("resource metadata: %s", detailed.Body.String())
	}
	prompts, ok := view["prompts"].([]any)
	if !ok || len(prompts) != 1 || object(prompts[0])["name"] != "summary" {
		t.Fatalf("prompt metadata: %s", detailed.Body.String())
	}
	for _, secret := range []string{remote.URL, "fixture-token", "fixture-only-token"} {
		if strings.Contains(listed.Body.String(), secret) || strings.Contains(detailed.Body.String(), secret) {
			t.Fatalf("secret disclosed: %s", secret)
		}
	}
	candidateResponse := call(http.MethodGet, base+"/review-candidate", nil)
	if candidateResponse.Code != 200 {
		t.Fatalf("candidate: %d %s", candidateResponse.Code, candidateResponse.Body.String())
	}
	candidate := decode(candidateResponse)
	tools, ok := candidate["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("candidate tools: %s", candidateResponse.Body.String())
	}
	hashes := map[string]string{}
	for _, v := range tools {
		tool := object(v)
		name, _ := tool["name"].(string)
		hashes[name], _ = tool["tool_hash"].(string)
	}
	if len(hashes["read_app"]) != 64 || len(hashes["write_app"]) != 64 {
		t.Fatalf("candidate hashes: %s", candidateResponse.Body.String())
	}
	review := map[string]any{"snapshot_id": candidate["snapshot_id"], "registration_version": candidate["registration_version"],
		"registration_hash": candidate["registration_hash"], "tools_hash": candidate["tools_hash"],
		"decisions": []map[string]any{{"name": "read_app", "tool_hash": hashes["read_app"], "decision": "allow_read_only"},
			{"name": "write_app", "tool_hash": hashes["write_app"], "decision": "deny"}},
	}
	wrongHash := map[string]any{}
	for k, v := range review {
		wrongHash[k] = v
	}
	wrongHash["registration_hash"] = strings.Repeat("0", 64)
	if w := call(http.MethodPost, base+"/review", wrongHash); w.Code != 409 || count(`SELECT count(*) FROM v3_mcp_tool_reviews`) != 0 {
		t.Fatalf("stale review accepted: %d %s", w.Code, w.Body.String())
	}
	unsafe := map[string]any{}
	for k, v := range review {
		unsafe[k] = v
	}
	unsafe["decisions"] = []map[string]any{{"name": "read_app", "tool_hash": hashes["read_app"], "decision": "allow_read_only"},
		{"name": "write_app", "tool_hash": hashes["write_app"], "decision": "allow_read_only"}}
	if w := call(http.MethodPost, base+"/review", unsafe); w.Code != 422 || count(`SELECT count(*) FROM v3_mcp_tool_reviews`) != 0 {
		t.Fatalf("non-read-only tool allowed: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, base+"/review", review); w.Code != 200 {
		t.Fatalf("main review: %d %s", w.Code, w.Body.String())
	}
	if count(`SELECT count(*) FROM v3_mcp_tool_reviews`) != 1 || count(`SELECT count(*) FROM v3_mcp_tool_review_decisions`) != 2 ||
		count(`SELECT count(*) FROM management_audit WHERE action='v3.mcp.tool_version_reviewed'`) != 1 {
		t.Fatal("review and audit did not persist")
	}
	change := map[string]any{"snapshot_id": candidate["snapshot_id"], "registration_version": candidate["registration_version"],
		"registration_hash": candidate["registration_hash"], "tools_hash": candidate["tools_hash"],
		"tool_hash": hashes["read_app"], "expected_revision": 1, "enabled": true}
	missing := map[string]any{}
	for k, v := range change {
		if k != "registration_version" {
			missing[k] = v
		}
	}
	if w := call(http.MethodPatch, base+"/tools/read_app", missing); w.Code != 409 {
		t.Fatalf("unversioned toggle accepted: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPatch, base+"/tools/read_app", change); w.Code != 200 {
		t.Fatalf("main toggle: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPatch, base+"/tools/read_app", change); w.Code != 409 ||
		count(`SELECT count(*) FROM management_audit WHERE action='v3.mcp.reviewed_tool_changed'`) != 1 {
		t.Fatalf("stale revision accepted: %d %s", w.Code, w.Body.String())
	}
	final := call(http.MethodGet, base, nil)
	if final.Code != 200 {
		t.Fatalf("reviewed detail: %d %s", final.Code, final.Body.String())
	}
	state := decode(final)
	if object(state["review"])["reviewed"] != true || object(state["review"])["enabled_count"] != float64(1) ||
		state["calls"] != nil || object(state["counts"])["resources"] != float64(1) {
		t.Fatalf("reviewed detail inconsistent: %s", final.Body.String())
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPatch, base+"/tools/read_app", change); w.Code != 404 ||
		count(`SELECT count(*) FROM management_audit WHERE action='v3.mcp.reviewed_tool_changed'`) != 1 {
		t.Fatalf("revoked manager changed tool: %d %s", w.Code, w.Body.String())
	}
}
