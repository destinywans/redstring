package v3mcp

import (
	"bytes"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// These routes store human review metadata only. In particular, successful
// review or enablement must never turn the untrusted observation into an Agent
// execution grant or make the public, unfenced routes available.
func TestMCPVersionReviewHTTPIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_review_" + store.ID()
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
	insertReached := make(chan struct{}, 1)
	toggleReached := make(chan struct{}, 1)
	cfg.ConnConfig.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		if notice.Message == "mcp_review_insert_barrier" {
			select {
			case insertReached <- struct{}{}:
			default:
			}
		} else if notice.Message == "mcp_toggle_update_barrier" {
			select {
			case toggleReached <- struct{}{}:
			default:
			}
		}
	}
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
	manager, err := st.CreateUser(ctx, core.User{Email: "manager@example.invalid", Name: "Manager", Role: "viewer"}, "isolated-manager-password")
	if err != nil {
		t.Fatal(err)
	}
	_, managerSession, err := st.Login(ctx, manager.Email, "isolated-manager-password")
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
		`INSERT INTO rbac_roles(id,name) VALUES ('review-outsider','Review Outsider')`,
		`DELETE FROM rbac_user_roles WHERE user_id=(SELECT id FROM users WHERE email='outsider@example.invalid')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) SELECT id,'review-outsider' FROM users WHERE email='outsider@example.invalid'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('review-outsider','environment','view','environment','dev')`,
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
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(db, policy)
	service.DiscoverTools = func(_ context.Context, config aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		if config.CredentialRef != "private-ref" {
			t.Fatal("wrong MCP registration reached discovery")
		}
		return []aiextensions.ToolInfo{{Name: "read_app", Description: "untrusted remote text", ReadOnlyHint: true,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}}, nil
	}
	service.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	call := func(token, env, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var input *bytes.Reader
		if body == nil {
			input = bytes.NewReader(nil)
		} else {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(encoded)
		}
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, input).WithContext(callCtx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	base := "/api/v1/mcp-servers/mcp-1"
	managerCall := func(method, path string, body any) *httptest.ResponseRecorder {
		return call(managerSession, "dev", method, path, body)
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("invalid JSON response %d: %v: %s", w.Code, err, w.Body.String())
		}
		return result
	}
	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	countAudit := func(action string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action=$1`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, path := range []string{base + "/review-candidate", base + "/review-version", base + "/reviewed-tools/read_app"} {
		method := http.MethodGet
		var body any
		if strings.HasSuffix(path, "/review-version") {
			method = http.MethodPost
			body = map[string]any{"snapshot_id": "unknown", "registration_version": 1,
				"registration_hash": strings.Repeat("0", 64), "tools_hash": strings.Repeat("0", 64),
				"decisions": []any{}}
		} else if strings.Contains(path, "/reviewed-tools/") {
			method = http.MethodPatch
			body = map[string]any{"snapshot_id": "unknown", "tools_hash": strings.Repeat("0", 64),
				"tool_hash": strings.Repeat("0", 64), "expected_revision": 1, "enabled": true}
		}
		w := call(outsiderSession, "dev", method, path, body)
		if w.Code != http.StatusNotFound {
			t.Fatalf("unauthorized %s leaked target: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := call(managerSession, "all", http.MethodGet, base+"/review-candidate", nil); w.Code != http.StatusConflict {
		t.Fatalf("all-environment candidate: %d %s", w.Code, w.Body.String())
	}
	if w := managerCall(http.MethodGet, base+"/review-candidate", nil); w.Code == http.StatusOK {
		t.Fatal("candidate existed before discovery")
	}
	if w := managerCall(http.MethodPost, base+"/discover", nil); w.Code != http.StatusOK {
		t.Fatalf("discover: %d %s", w.Code, w.Body.String())
	}
	w := managerCall(http.MethodGet, base+"/review-candidate", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("candidate: %d %s", w.Code, w.Body.String())
	}
	candidate := decode(w)
	tools, ok := candidate["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("candidate tools: %v", candidate)
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "read_app" || tool["tool_hash"] == "" || tool["enabled"] != false || candidate["reviewed"] != false ||
		candidate["trust"] != "untrusted_observation" || strings.Contains(w.Body.String(), "private-ref") || strings.Contains(w.Body.String(), "private.example.invalid") {
		t.Fatalf("unsafe candidate: %s", w.Body.String())
	}
	snapshotID, _ := candidate["snapshot_id"].(string)
	toolsHash, _ := candidate["tools_hash"].(string)
	toolHash, _ := tool["tool_hash"].(string)
	version, _ := candidate["registration_version"].(float64)
	registrationHash, _ := candidate["registration_hash"].(string)
	if snapshotID == "" || len(toolsHash) != 64 || len(toolHash) != 64 || version != 1 || len(registrationHash) != 64 {
		t.Fatalf("missing candidate identity: %v", candidate)
	}
	reviewBody := map[string]any{
		"snapshot_id": snapshotID, "registration_version": int(version), "registration_hash": registrationHash,
		"tools_hash": toolsHash, "decisions": []map[string]any{{"name": "read_app", "tool_hash": toolHash, "decision": "allow_read_only"}},
	}
	if w := managerCall(http.MethodPost, base+"/review-version", map[string]any{
		"snapshot_id": snapshotID, "registration_version": int(version), "registration_hash": registrationHash,
		"tools_hash": toolsHash, "decisions": []map[string]any{{"name": "read_app", "tool_hash": strings.Repeat("0", 64), "decision": "allow_read_only"}},
	}); w.Code == http.StatusOK || count("v3_mcp_tool_reviews") != 0 {
		t.Fatalf("mismatched tool hash accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, manager.ID); err != nil {
		t.Fatal(err)
	}
	priorReviewAudit := countAudit("v3.mcp.tool_version_reviewed")
	w = managerCall(http.MethodPost, base+"/review-version", reviewBody)
	if w.Code == http.StatusOK || count("v3_mcp_tool_reviews") != 0 ||
		countAudit("v3.mcp.tool_version_reviewed") != priorReviewAudit {
		t.Fatalf("machine identity submitted human review: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE users SET v3_identity_kind='human' WHERE id=$1`, manager.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE rbac_roles SET v3_kind='machine' WHERE id='viewer'`); err != nil {
		t.Fatal(err)
	}
	w = managerCall(http.MethodPost, base+"/review-version", reviewBody)
	if w.Code == http.StatusOK || count("v3_mcp_tool_reviews") != 0 ||
		countAudit("v3.mcp.tool_version_reviewed") != priorReviewAudit {
		t.Fatalf("human identity with machine role submitted human review: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE rbac_roles SET v3_kind='system' WHERE id='viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT deny_mcp_review
 CHECK (action <> 'v3.mcp.tool_version_reviewed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	w = managerCall(http.MethodPost, base+"/review-version", reviewBody)
	if w.Code != http.StatusServiceUnavailable || count("v3_mcp_tool_reviews") != 0 || count("v3_mcp_tool_review_decisions") != 0 {
		t.Fatalf("review persisted without audit: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT deny_mcp_review`); err != nil {
		t.Fatal(err)
	}
	w = managerCall(http.MethodPost, base+"/review-version", reviewBody)
	if w.Code != http.StatusOK {
		t.Fatalf("review: %d %s", w.Code, w.Body.String())
	}
	if got := decode(w); got["reviewed"] != true || got["enabled_count"] != float64(0) {
		t.Fatalf("review response granted tools prematurely: %v", got)
	}
	var ownerApp, ownerEnv, storedHash, storedToolHash, decision, actorID string
	var enabled bool
	var revision int
	if err := db.QueryRow(ctx, `SELECT r.application_id,r.environment_id,r.registration_hash,d.tool_hash,d.decision,
 d.enabled,d.revision,r.reviewed_by FROM v3_mcp_tool_reviews r
 JOIN v3_mcp_tool_review_decisions d ON d.snapshot_id=r.snapshot_id
 WHERE r.snapshot_id=$1 AND d.tool_name='read_app'`, snapshotID).
		Scan(&ownerApp, &ownerEnv, &storedHash, &storedToolHash, &decision, &enabled, &revision, &actorID); err != nil {
		t.Fatal(err)
	}
	if ownerApp != "app" || ownerEnv != "dev" || storedHash != registrationHash || storedToolHash != toolHash ||
		decision != "allow_read_only" || enabled || actorID != manager.ID {
		t.Fatalf("incorrect durable review: app=%s env=%s hash=%s tool=%s decision=%s enabled=%t revision=%d actor=%s",
			ownerApp, ownerEnv, storedHash, storedToolHash, decision, enabled, revision, actorID)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_mcp_tool_reviews SET application_id='other' WHERE snapshot_id=$1`, snapshotID); err == nil {
		t.Fatal("review owner was mutable")
	}
	if _, err := db.Exec(ctx, `UPDATE v3_mcp_tool_review_decisions SET decision='deny' WHERE snapshot_id=$1`, snapshotID); err == nil {
		t.Fatal("review decision was mutable")
	}
	var auditChanges []byte
	if err := db.QueryRow(ctx, `SELECT changes FROM management_audit WHERE action='v3.mcp.tool_version_reviewed' AND target='mcp-1'`).Scan(&auditChanges); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(auditChanges), snapshotID) || !strings.Contains(string(auditChanges), toolsHash) ||
		strings.Contains(string(auditChanges), "private-ref") || strings.Contains(string(auditChanges), "untrusted remote text") {
		t.Fatalf("unsafe review audit: %s", auditChanges)
	}
	if w := managerCall(http.MethodPost, base+"/review", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unfenced main review opened: %d %s", w.Code, w.Body.String())
	}
	if w := managerCall(http.MethodPatch, base+"/tools/read_app", map[string]any{"enabled": true}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unfenced main tool route opened: %d %s", w.Code, w.Body.String())
	}
	changeBody := func(rev int, state bool) map[string]any {
		return map[string]any{"snapshot_id": snapshotID, "tools_hash": toolsHash, "tool_hash": toolHash,
			"expected_revision": rev, "enabled": state}
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT deny_mcp_toggle
 CHECK (action <> 'v3.mcp.reviewed_tool_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	w = managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", changeBody(revision, true))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("toggle succeeded without audit: %d %s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT enabled,revision FROM v3_mcp_tool_review_decisions
 WHERE snapshot_id=$1 AND tool_name='read_app'`, snapshotID).Scan(&enabled, &revision); err != nil || enabled {
		t.Fatalf("toggle persisted without audit: enabled=%t revision=%d err=%v", enabled, revision, err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT deny_mcp_toggle`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, manager.ID); err != nil {
		t.Fatal(err)
	}
	priorToggleAudit := countAudit("v3.mcp.reviewed_tool_changed")
	w = managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", changeBody(revision, true))
	if w.Code == http.StatusOK || countAudit("v3.mcp.reviewed_tool_changed") != priorToggleAudit {
		t.Fatalf("machine identity enabled reviewed tool: %d %s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT enabled,revision FROM v3_mcp_tool_review_decisions
 WHERE snapshot_id=$1 AND tool_name='read_app'`, snapshotID).Scan(&enabled, &revision); err != nil || enabled {
		t.Fatalf("machine toggle persisted: enabled=%t revision=%d err=%v", enabled, revision, err)
	}
	if _, err := db.Exec(ctx, `UPDATE users SET v3_identity_kind='human' WHERE id=$1`, manager.ID); err != nil {
		t.Fatal(err)
	}
	w = managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", changeBody(revision, true))
	if w.Code != http.StatusOK {
		t.Fatalf("enable reviewed tool: %d %s", w.Code, w.Body.String())
	}
	newRevision, ok := decode(w)["revision"].(float64)
	if !ok || int(newRevision) <= revision {
		t.Fatalf("revision did not advance: %s", w.Body.String())
	}
	w = managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", changeBody(revision, false))
	if w.Code != http.StatusConflict {
		t.Fatalf("stale revision changed tool: %d %s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT enabled,revision FROM v3_mcp_tool_review_decisions
 WHERE snapshot_id=$1 AND tool_name='read_app'`, snapshotID).Scan(&enabled, &revision); err != nil || !enabled || revision != int(newRevision) {
		t.Fatalf("stale CAS changed state: enabled=%t revision=%d err=%v", enabled, revision, err)
	}
	if err := db.QueryRow(ctx, `SELECT changes FROM management_audit WHERE action='v3.mcp.reviewed_tool_changed'
 ORDER BY id DESC LIMIT 1`).Scan(&auditChanges); err != nil || !strings.Contains(string(auditChanges), snapshotID) {
		t.Fatalf("tool change audit missing identity: %s %v", auditChanges, err)
	}
	if w := managerCall(http.MethodPost, base+"/discover", nil); w.Code != http.StatusOK {
		t.Fatalf("rediscover: %d %s", w.Code, w.Body.String())
	}
	w = managerCall(http.MethodGet, base+"/review-candidate", nil)
	if w.Code != http.StatusOK || decode(w)["reviewed"] != false {
		t.Fatalf("new snapshot inherited review: %d %s", w.Code, w.Body.String())
	}
	newCandidate := decode(w)
	if newCandidate["snapshot_id"] == snapshotID {
		t.Fatalf("rediscovery did not advance snapshot: %s", w.Body.String())
	}
	if newCandidate["tools_hash"] != toolsHash {
		t.Fatalf("test requires the same tool digest across discoveries: %s", w.Body.String())
	}
	if w := managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", changeBody(revision, false)); w.Code != http.StatusConflict {
		t.Fatalf("old approval survived new snapshot: %d %s", w.Code, w.Body.String())
	}
	newTools, ok := newCandidate["tools"].([]any)
	if !ok || len(newTools) != 1 {
		t.Fatalf("new candidate tools: %v", newCandidate)
	}
	newTool, ok := newTools[0].(map[string]any)
	if !ok {
		t.Fatalf("new candidate tool: %v", newTools[0])
	}
	denyBody := map[string]any{
		"snapshot_id": newCandidate["snapshot_id"], "registration_version": int(version),
		"registration_hash": registrationHash, "tools_hash": newCandidate["tools_hash"],
		"decisions": []map[string]any{{"name": "read_app", "tool_hash": newTool["tool_hash"], "decision": "deny"}},
	}
	w = managerCall(http.MethodPost, base+"/review-version", denyBody)
	if w.Code != http.StatusOK {
		t.Fatalf("deny reviewed tool: %d %s", w.Code, w.Body.String())
	}
	w = managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", map[string]any{
		"snapshot_id": newCandidate["snapshot_id"], "tools_hash": newCandidate["tools_hash"],
		"tool_hash": newTool["tool_hash"], "expected_revision": 1, "enabled": true,
	})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "mcp_tool_denied") {
		t.Fatalf("denied tool enabled: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, base + "/review-candidate", nil},
		{http.MethodPost, base + "/review-version", denyBody},
		{http.MethodPatch, base + "/reviewed-tools/read_app", changeBody(revision, false)},
	} {
		if w := managerCall(tc.method, tc.path, tc.body); w.Code != http.StatusNotFound {
			t.Fatalf("revoked manager accessed %s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','application','app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE ai_extensions SET record=jsonb_set(record,'{title}','"Changed"'::jsonb)
 WHERE id='mcp-1'`); err != nil {
		t.Fatal(err)
	}
	if w := managerCall(http.MethodGet, base+"/review-candidate", nil); w.Code == http.StatusOK {
		t.Fatalf("changed registration retained candidate: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE ai_extensions SET record=$1 WHERE id='mcp-1'`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_mcp_environment_bindings WHERE extension_id='mcp-1'`); err != nil {
		t.Fatal(err)
	}
	bind()
	if w := managerCall(http.MethodGet, base+"/review-candidate", nil); w.Code == http.StatusOK {
		t.Fatalf("replaced binding retained candidate: %d %s", w.Code, w.Body.String())
	}
	// A corrupt or partially written review header must not make even its
	// present allow decision effective when another discovered tool is missing.
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		return []aiextensions.ToolInfo{
			{Name: "read_app", InputSchema: map[string]any{"type": "object"}},
			{Name: "read_other", InputSchema: map[string]any{"type": "object"}},
		}, nil
	}
	if w := managerCall(http.MethodPost, base+"/discover", nil); w.Code != http.StatusOK {
		t.Fatalf("discover two tools: %d %s", w.Code, w.Body.String())
	}
	w = managerCall(http.MethodGet, base+"/review-candidate", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("two-tool candidate: %d %s", w.Code, w.Body.String())
	}
	partial := decode(w)
	partialTools, ok := partial["tools"].([]any)
	if !ok || len(partialTools) != 2 {
		t.Fatalf("two-tool candidate declarations: %s", w.Body.String())
	}
	var readAppHash string
	for _, item := range partialTools {
		tool, ok := item.(map[string]any)
		if ok && tool["name"] == "read_app" {
			readAppHash, _ = tool["tool_hash"].(string)
		}
	}
	if readAppHash == "" {
		t.Fatalf("read_app missing from two-tool candidate: %s", w.Body.String())
	}
	var bindingInstanceID string
	if err := db.QueryRow(ctx, `SELECT binding_instance_id FROM v3_mcp_environment_bindings
 WHERE extension_id='mcp-1' AND environment_id='dev'`).Scan(&bindingInstanceID); err != nil {
		t.Fatal(err)
	}
	partialID, _ := partial["snapshot_id"].(string)
	partialToolsHash, _ := partial["tools_hash"].(string)
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_tool_reviews
 (snapshot_id,extension_id,binding_instance_id,application_id,environment_id,
 registration_version,registration_hash,tools_hash,reviewed_by)
 VALUES($1,'mcp-1',$2,'app','dev',$3,$4,$5,$6)`, partialID, bindingInstanceID,
		int(version), registrationHash, partialToolsHash, manager.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_tool_review_decisions
 (snapshot_id,tool_name,tool_hash,decision,updated_by)
 VALUES($1,'read_app',$2,'allow_read_only',$3)`, partialID, readAppHash, manager.ID); err != nil {
		t.Fatal(err)
	}
	priorToggleAudit = countAudit("v3.mcp.reviewed_tool_changed")
	w = managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", map[string]any{
		"snapshot_id": partialID, "tools_hash": partialToolsHash, "tool_hash": readAppHash,
		"expected_revision": 1, "enabled": true,
	})
	if w.Code != http.StatusServiceUnavailable || countAudit("v3.mcp.reviewed_tool_changed") != priorToggleAudit {
		t.Fatalf("incomplete review enabled a tool: %d %s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT enabled FROM v3_mcp_tool_review_decisions
 WHERE snapshot_id=$1 AND tool_name='read_app'`, partialID).Scan(&enabled); err != nil || enabled {
		t.Fatalf("incomplete review changed switch: enabled=%t err=%v", enabled, err)
	}

	// Pause exactly after the service's transaction-local object authorization.
	// The revoker uses another connection. If its DELETE commits while this
	// transaction is paused, the later review write must fail closed.
	if w := managerCall(http.MethodPost, base+"/discover", nil); w.Code != http.StatusOK {
		t.Fatalf("discover concurrency candidate: %d %s", w.Code, w.Body.String())
	}
	w = managerCall(http.MethodGet, base+"/review-candidate", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("concurrency candidate: %d %s", w.Code, w.Body.String())
	}
	concurrentCandidate := decode(w)
	concurrentTools, ok := concurrentCandidate["tools"].([]any)
	if !ok || len(concurrentTools) != 2 {
		t.Fatalf("concurrency candidate tools: %s", w.Body.String())
	}
	concurrentDecisions := make([]map[string]any, 0, len(concurrentTools))
	for _, item := range concurrentTools {
		tool, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("invalid concurrency tool: %v", item)
		}
		concurrentDecisions = append(concurrentDecisions, map[string]any{
			"name": tool["name"], "tool_hash": tool["tool_hash"], "decision": "allow_read_only",
		})
	}
	concurrentReview := map[string]any{
		"snapshot_id":          concurrentCandidate["snapshot_id"],
		"registration_version": concurrentCandidate["registration_version"],
		"registration_hash":    concurrentCandidate["registration_hash"],
		"tools_hash":           concurrentCandidate["tools_hash"],
		"decisions":            concurrentDecisions,
	}
	if _, err := db.Exec(ctx, `CREATE FUNCTION mcp_review_insert_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
  RAISE NOTICE 'mcp_review_insert_barrier';
  PERFORM pg_advisory_xact_lock(8675309);
  RETURN NEW;
 END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TRIGGER mcp_review_insert_barrier
 BEFORE INSERT ON v3_mcp_tool_reviews FOR EACH ROW EXECUTE FUNCTION mcp_review_insert_barrier()`); err != nil {
		t.Fatal(err)
	}
	lockTx, err := adminDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = lockTx.Rollback(context.Background())
		}
	}()
	if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(8675309)`); err != nil {
		t.Fatal(err)
	}
	priorConcurrentReviewAudit := countAudit("v3.mcp.tool_version_reviewed")
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- managerCall(http.MethodPost, base+"/review-version", concurrentReview) }()
	select {
	case <-insertReached:
	case <-time.After(5 * time.Second):
		t.Fatal("review did not reach post-human-check insert barrier")
	}
	type revokeResult struct {
		committed bool
		err       error
	}
	revokeManage := func() revokeResult {
		revokeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		tx, err := adminDB.Begin(revokeCtx)
		if err != nil {
			return revokeResult{err: err}
		}
		defer tx.Rollback(revokeCtx)
		if _, err = tx.Exec(revokeCtx, `SET LOCAL lock_timeout = '500ms'`); err == nil {
			_, err = tx.Exec(revokeCtx, `DELETE FROM `+quoted+`.v3_role_grants
 WHERE role_id='viewer' AND module='connectors' AND action='manage'`)
		}
		if err == nil {
			err = tx.Commit(revokeCtx)
		}
		return revokeResult{committed: err == nil, err: err}
	}
	revoked := make(chan revokeResult, 1)
	go func() { revoked <- revokeManage() }()
	var revoke revokeResult
	select {
	case revoke = <-revoked:
	case <-time.After(3 * time.Second):
		t.Fatal("role-grant revoke did not finish within its bounded context")
	}
	if revoke.err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(revoke.err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("unexpected revoke failure: %v", revoke.err)
		}
	}
	if err := lockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	lockHeld = false
	var reviewResponse *httptest.ResponseRecorder
	select {
	case reviewResponse = <-response:
	case <-time.After(5 * time.Second):
		t.Fatal("review did not finish after authorization barrier release")
	}
	t.Logf("revoke committed=%t err=%v; review status=%d", revoke.committed, revoke.err, reviewResponse.Code)
	if revoke.committed {
		var persisted int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_mcp_tool_reviews WHERE snapshot_id=$1`,
			concurrentCandidate["snapshot_id"]).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		auditCount := countAudit("v3.mcp.tool_version_reviewed")
		if reviewResponse.Code == http.StatusOK || persisted != 0 || auditCount != priorConcurrentReviewAudit {
			t.Fatalf("revocation committed before review, but write succeeded: status=%d body=%s persisted=%d audit_delta=%d",
				reviewResponse.Code, reviewResponse.Body.String(), persisted, auditCount-priorConcurrentReviewAudit)
		}
	} else if reviewResponse.Code != http.StatusOK {
		t.Fatalf("revoke was blocked until review committed, but review failed: %d %s",
			reviewResponse.Code, reviewResponse.Body.String())
	}
	if revoke.committed {
		return // No reviewed switch exists when the review correctly fails.
	}
	var concurrentReadAppHash string
	for _, item := range concurrentTools {
		tool := item.(map[string]any)
		if tool["name"] == "read_app" {
			concurrentReadAppHash, _ = tool["tool_hash"].(string)
		}
	}
	if concurrentReadAppHash == "" {
		t.Fatal("concurrent read_app hash is missing")
	}
	if _, err := db.Exec(ctx, `CREATE FUNCTION mcp_toggle_update_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
  RAISE NOTICE 'mcp_toggle_update_barrier';
  PERFORM pg_advisory_xact_lock(8675310);
  RETURN NEW;
 END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TRIGGER mcp_toggle_update_barrier
 BEFORE UPDATE ON v3_mcp_tool_review_decisions FOR EACH ROW EXECUTE FUNCTION mcp_toggle_update_barrier()`); err != nil {
		t.Fatal(err)
	}
	toggleLockTx, err := adminDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	toggleLockHeld := true
	defer func() {
		if toggleLockHeld {
			_ = toggleLockTx.Rollback(context.Background())
		}
	}()
	if _, err := toggleLockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(8675310)`); err != nil {
		t.Fatal(err)
	}
	priorConcurrentToggleAudit := countAudit("v3.mcp.reviewed_tool_changed")
	toggleResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		toggleResponse <- managerCall(http.MethodPatch, base+"/reviewed-tools/read_app", map[string]any{
			"snapshot_id": concurrentCandidate["snapshot_id"], "tools_hash": concurrentCandidate["tools_hash"],
			"tool_hash": concurrentReadAppHash, "expected_revision": 1, "enabled": true,
		})
	}()
	select {
	case <-toggleReached:
	case <-time.After(5 * time.Second):
		t.Fatal("tool switch did not reach post-human-check update barrier")
	}
	toggleRevoked := make(chan revokeResult, 1)
	go func() { toggleRevoked <- revokeManage() }()
	var toggleRevoke revokeResult
	select {
	case toggleRevoke = <-toggleRevoked:
	case <-time.After(3 * time.Second):
		t.Fatal("role-grant revoke during toggle did not finish within its bounded context")
	}
	if toggleRevoke.err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(toggleRevoke.err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("unexpected toggle revoke failure: %v", toggleRevoke.err)
		}
	}
	if err := toggleLockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	toggleLockHeld = false
	var toggleResult *httptest.ResponseRecorder
	select {
	case toggleResult = <-toggleResponse:
	case <-time.After(5 * time.Second):
		t.Fatal("tool switch did not finish after update barrier release")
	}
	var finalEnabled bool
	if err := db.QueryRow(ctx, `SELECT enabled FROM v3_mcp_tool_review_decisions
 WHERE snapshot_id=$1 AND tool_name='read_app'`, concurrentCandidate["snapshot_id"]).Scan(&finalEnabled); err != nil {
		t.Fatal(err)
	}
	toggleAuditDelta := countAudit("v3.mcp.reviewed_tool_changed") - priorConcurrentToggleAudit
	t.Logf("toggle revoke committed=%t err=%v; status=%d", toggleRevoke.committed, toggleRevoke.err, toggleResult.Code)
	if toggleRevoke.committed {
		if toggleResult.Code == http.StatusOK || finalEnabled || toggleAuditDelta != 0 {
			t.Fatalf("revocation committed before tool update, but switch succeeded: status=%d body=%s enabled=%t audit_delta=%d",
				toggleResult.Code, toggleResult.Body.String(), finalEnabled, toggleAuditDelta)
		}
	} else if toggleResult.Code != http.StatusOK || !finalEnabled || toggleAuditDelta != 1 {
		t.Fatalf("revoke was blocked until tool update committed, but switch failed: status=%d body=%s enabled=%t audit_delta=%d",
			toggleResult.Code, toggleResult.Body.String(), finalEnabled, toggleAuditDelta)
	}
}
