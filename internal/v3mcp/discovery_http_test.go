package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
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

func TestValidateDiscoveryRejectsUntrustedMalformedDeclarations(t *testing.T) {
	valid := func() []aiextensions.ToolInfo {
		return []aiextensions.ToolInfo{{Name: "read_status", Description: "Status", ReadOnlyHint: true,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"service": map[string]any{"type": "string"}}}}}
	}
	if data, err := validateDiscovery(valid()); err != nil || !strings.Contains(string(data), `"read_only_hint":true`) {
		t.Fatalf("valid observation: %s, %v", data, err)
	}
	precise, err := validateDiscovery([]aiextensions.ToolInfo{{Name: "read_big_number",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"const":9007199254740993}}}`)}})
	if err != nil || !strings.Contains(string(precise), "9007199254740993") {
		t.Fatalf("schema number changed during snapshot normalization: %s, %v", precise, err)
	}
	ordered := append(valid(), aiextensions.ToolInfo{Name: "a_first", InputSchema: map[string]any{"type": "object"}})
	reversed := []aiextensions.ToolInfo{ordered[1], ordered[0]}
	forwardJSON, forwardErr := validateDiscovery(ordered)
	reverseJSON, reverseErr := validateDiscovery(reversed)
	if forwardErr != nil || reverseErr != nil || string(forwardJSON) != string(reverseJSON) ||
		digestBytes(forwardJSON) != digestBytes(reverseJSON) {
		t.Fatalf("tool order changed snapshot evidence: %s %s %v %v", forwardJSON, reverseJSON, forwardErr, reverseErr)
	}
	for _, tc := range []struct {
		name  string
		tools []aiextensions.ToolInfo
	}{
		{"duplicate", append(valid(), valid()...)},
		{"invalid name", []aiextensions.ToolInfo{{Name: "../secret", InputSchema: map[string]any{"type": "object"}}}},
		{"missing schema", []aiextensions.ToolInfo{{Name: "read", InputSchema: nil}}},
		{"nonobject schema", []aiextensions.ToolInfo{{Name: "read", InputSchema: map[string]any{"type": "string"}}}},
		{"oversize description", []aiextensions.ToolInfo{{Name: "read", Description: strings.Repeat("x", 2001), InputSchema: map[string]any{"type": "object"}}}},
		{"oversize schema", []aiextensions.ToolInfo{{Name: "read", InputSchema: map[string]any{"type": "object", "description": strings.Repeat("x", 16<<10)}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateDiscovery(tc.tools); err == nil {
				t.Fatal("untrusted invalid declaration accepted")
			}
		})
	}
}

func TestDiscoverySnapshotHTTPBoundedScopeAndRaces(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_discovery_" + store.ID()
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
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	// This order catches migrations that incorrectly depend on ai_extensions.
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
	outsider, err := st.CreateUser(ctx, core.User{Email: "outsider@example.invalid", Name: "Outsider", Role: "viewer"}, "isolated-outsider-password")
	if err != nil {
		t.Fatal(err)
	}
	_, outsiderSession, err := st.Login(ctx, outsider.Email, "isolated-outsider-password")
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
		`INSERT INTO rbac_roles(id,name) VALUES ('mcp-outsider','MCP Outsider')`,
		`DELETE FROM rbac_user_roles WHERE user_id=(SELECT id FROM users WHERE email='outsider@example.invalid')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) SELECT id,'mcp-outsider' FROM users WHERE email='outsider@example.invalid'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('mcp-outsider','environment','view','environment','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	record := aiextensions.Record{ID: "mcp-1", ApplicationID: "app", Kind: "mcp", Title: "App MCP",
		Maintainer: "platform", Enabled: true, Version: 1, UpdatedAt: time.Now().UTC(),
		MCP: &aiextensions.MCPConfig{Endpoint: "https://private.example.invalid/mcp", CredentialRef: "private-ref",
			Bindings: []aiextensions.Binding{{Name: "read_app", Tool: "read_app", ReadOnlyConfirmed: true, Arguments: map[string]any{}}}},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record)
 VALUES('mcp-1','app','mcp',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	bind := func() {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
			t.Fatal(err)
		}
	}
	bind()
	if _, err := db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record)
 VALUES('mcp-malformed','app','mcp',1,'{"id":"mcp-malformed","application_id":"app","kind":"mcp","version":1,"enabled":true,"title":"Bad","mcp":{"endpoint":"http://invalid","bindings":[]}}'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-malformed','app','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(db, policy)
	service.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	requestAs := func(token, env, id string) *httptest.ResponseRecorder {
		t.Helper()
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/mcp-servers/"+id+"/discover", nil).WithContext(callCtx)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	request := func(env string) *httptest.ResponseRecorder { return requestAs(session, env, "mcp-1") }
	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	validTools := func() []aiextensions.ToolInfo {
		return []aiextensions.ToolInfo{{Name: "read_app", Description: "untrusted declaration", ReadOnlyHint: true,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}}
	}
	called := 0
	service.DiscoverTools = func(_ context.Context, config aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		called++
		if config.CredentialRef != "private-ref" || config.Endpoint != "https://private.example.invalid/mcp" {
			t.Fatalf("unexpected bound config: %+v", config)
		}
		return validTools(), nil
	}
	var hiddenBody string
	for _, id := range []string{"mcp-1", "mcp-malformed", "mcp-absent"} {
		w := requestAs(outsiderSession, "dev", id)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Fatalf("unauthorized %s exposed target status: %d %s", id, w.Code, w.Body.String())
		}
		if hiddenBody != "" && hiddenBody != w.Body.String() {
			t.Fatalf("unauthorized targets have distinguishable responses: %q vs %q", hiddenBody, w.Body.String())
		}
		hiddenBody = w.Body.String()
	}
	if called != 0 {
		t.Fatal("unauthorized target reached network discovery")
	}
	if w := requestAs(session, "dev", "mcp-malformed"); w.Code != http.StatusServiceUnavailable || called != 0 {
		t.Fatalf("authorized malformed registration reached discovery: %d %s", w.Code, w.Body.String())
	}
	w := request("dev")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"trust":"untrusted_observation"`) ||
		!strings.Contains(w.Body.String(), `"health_verified":false`) || strings.Contains(w.Body.String(), "private-ref") ||
		strings.Contains(w.Body.String(), "private.example.invalid") || strings.Contains(w.Body.String(), "untrusted declaration") ||
		count("v3_mcp_discovery_snapshots") != 1 || count("management_audit") < 1 {
		t.Fatalf("observation response/audit: %d %s", w.Code, w.Body.String())
	}
	var savedVersion int
	var savedHash, toolsHash string
	var toolsJSON []byte
	if err := db.QueryRow(ctx, `SELECT registration_version,registration_hash,tools_hash,tools
 FROM v3_mcp_discovery_snapshots WHERE extension_id='mcp-1'`).Scan(&savedVersion, &savedHash, &toolsHash, &toolsJSON); err != nil {
		t.Fatal(err)
	}
	if savedVersion != 1 || len(savedHash) != 64 || toolsHash != digestBytes(toolsJSON) ||
		!strings.Contains(string(toolsJSON), "untrusted declaration") {
		t.Fatalf("incomplete snapshot: %d %s %s %s", savedVersion, savedHash, toolsHash, toolsJSON)
	}
	var auditChanges []byte
	if err := db.QueryRow(ctx, `SELECT changes FROM management_audit
 WHERE action='v3.mcp.discovery_observed' AND target='mcp-1'`).Scan(&auditChanges); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(auditChanges), toolsHash) ||
		strings.Contains(string(auditChanges), "private-ref") ||
		strings.Contains(string(auditChanges), "private.example.invalid") {
		t.Fatalf("audit missing hash or exposing credential: %s", auditChanges)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_mcp_discovery_snapshots`); err == nil {
		t.Fatal("historical snapshot was mutable")
	}
	checkFailure := func(label string, expected, auditDelta int) {
		t.Helper()
		priorAudit := count("management_audit")
		w := request("dev")
		if w.Code != expected || count("v3_mcp_discovery_snapshots") != 1 || count("management_audit") != priorAudit+auditDelta ||
			strings.Contains(w.Body.String(), "private-ref") || strings.Contains(w.Body.String(), "private.example.invalid") {
			t.Fatalf("%s: %d %s", label, w.Code, w.Body.String())
		}
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		return nil, errors.New("private-ref https://private.example.invalid/mcp")
	}
	checkFailure("upstream error", http.StatusBadGateway, 1)
	var failureChanges []byte
	if err := db.QueryRow(ctx, `SELECT changes FROM management_audit WHERE action='v3.mcp.discovery_failed'
 ORDER BY id DESC LIMIT 1`).Scan(&failureChanges); err != nil ||
		!strings.Contains(string(failureChanges), `"failure_kind": "upstream"`) ||
		strings.Contains(string(failureChanges), "private-ref") {
		t.Fatalf("unsafe failure audit: %s, %v", failureChanges, err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		return append(validTools(), validTools()...), nil
	}
	checkFailure("invalid remote declaration", http.StatusBadGateway, 1)
	if err := db.QueryRow(ctx, `SELECT changes FROM management_audit WHERE action='v3.mcp.discovery_failed'
 ORDER BY id DESC LIMIT 1`).Scan(&failureChanges); err != nil ||
		!strings.Contains(string(failureChanges), `"failure_kind": "invalid_declaration"`) {
		t.Fatalf("invalid declaration audit missing kind: %s, %v", failureChanges, err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
			t.Fatal(err)
		}
		return validTools(), nil
	}
	checkFailure("grant revoked after network", http.StatusForbidden, 0)
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','application','app')`); err != nil {
		t.Fatal(err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='environment'`); err != nil {
			t.Fatal(err)
		}
		return validTools(), nil
	}
	checkFailure("environment grant revoked after network", http.StatusForbidden, 0)
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(session)); err != nil {
			t.Fatal(err)
		}
		return validTools(), nil
	}
	checkFailure("session logged out after network", http.StatusNotFound, 0)
	_, session, err = st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `DELETE FROM v3_mcp_environment_bindings WHERE extension_id='mcp-1'`); err != nil {
			t.Fatal(err)
		}
		return validTools(), nil
	}
	checkFailure("binding removed after network", http.StatusConflict, 0)
	bind()
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `DELETE FROM v3_mcp_environment_bindings WHERE extension_id='mcp-1'`); err != nil {
			t.Fatal(err)
		}
		bind()
		return validTools(), nil
	}
	checkFailure("same-scope binding replaced after network", http.StatusConflict, 0)
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `UPDATE ai_extensions SET record=jsonb_set(record,'{title}','"Changed"'::jsonb)
 WHERE id='mcp-1'`); err != nil {
			t.Fatal(err)
		}
		return validTools(), nil
	}
	checkFailure("same-version record edit", http.StatusConflict, 0)
	if _, err := db.Exec(ctx, `UPDATE ai_extensions SET record=$1 WHERE id='mcp-1'`, raw); err != nil {
		t.Fatal(err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `UPDATE ai_extensions SET record=jsonb_set(record,'{enabled}','false'::jsonb)
 WHERE id='mcp-1'`); err != nil {
			t.Fatal(err)
		}
		return validTools(), nil
	}
	checkFailure("registration disabled after network", http.StatusConflict, 0)
	if _, err := db.Exec(ctx, `UPDATE ai_extensions SET record=$1 WHERE id='mcp-1'`, raw); err != nil {
		t.Fatal(err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if _, err := db.Exec(ctx, `UPDATE ai_extensions SET version=2 WHERE id='mcp-1'`); err != nil {
			t.Fatal(err)
		}
		return validTools(), nil
	}
	checkFailure("relational version changed", http.StatusConflict, 0)
	if _, err := db.Exec(ctx, `UPDATE ai_extensions SET version=1 WHERE id='mcp-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE ai_extensions SET version=2 WHERE id='mcp-1'`); err != nil {
		t.Fatal(err)
	}
	preMismatchCalls := 0
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		preMismatchCalls++
		return validTools(), nil
	}
	checkFailure("relational JSON version mismatch before network", http.StatusServiceUnavailable, 0)
	if preMismatchCalls != 0 {
		t.Fatal("inconsistent persisted version reached network")
	}
	if _, err := db.Exec(ctx, `UPDATE ai_extensions SET version=1 WHERE id='mcp-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT deny_discovery_observation
 CHECK (action <> 'v3.mcp.discovery_observed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		called++
		return validTools(), nil
	}
	checkFailure("audit rollback", http.StatusServiceUnavailable, 0)
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT deny_discovery_observation`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT deny_discovery_failure
 CHECK (action <> 'v3.mcp.discovery_failed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		return nil, errors.New("private-ref upstream error")
	}
	checkFailure("failure audit rollback", http.StatusServiceUnavailable, 0)
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT deny_discovery_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	priorCalls := called
	w = request("dev")
	if w.Code != http.StatusNotFound || called != priorCalls {
		t.Fatalf("pre-network grant check: %d %s", w.Code, w.Body.String())
	}
	if w = request("all"); w.Code != http.StatusConflict {
		t.Fatalf("all-environment write: %d %s", w.Code, w.Body.String())
	}
}
