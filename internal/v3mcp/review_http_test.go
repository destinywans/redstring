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

	"github.com/destinywans/redstring/internal/aiextensions"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type reviewReadAuthorizationBarrier struct {
	policy  *rbac.V3Policy
	reached chan struct{}
	release chan struct{}
}

type candidateDeadlineRecorder struct{ *httptest.ResponseRecorder }

func (*candidateDeadlineRecorder) SetWriteDeadline(time.Time) error { return nil }

// This fixture is independent of the write-race scenarios in the other test. Both
// revocations must commit before the paused candidate read continues, so a
// prior write-path branch cannot silently skip the disclosure assertions.
func TestMCPReviewCandidateRevocationHTTPIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_read_race_" + store.ID()
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
	_, token, err := st.Login(ctx, manager.Email, "isolated-manager-password")
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
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(db, policy)
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		return []aiextensions.ToolInfo{{Name: "read_app", Description: "sensitive remote description",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"privateField": map[string]any{"type": "string"}}}}}, nil
	}
	service.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, nil).WithContext(callCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		if method == http.MethodGet && strings.HasSuffix(path, "/review-candidate") {
			api.ServeHTTP(&candidateDeadlineRecorder{w}, r)
		} else {
			api.ServeHTTP(w, r)
		}
		return w
	}
	base := "/api/v1/mcp-servers/mcp-1"
	if w := call(http.MethodPost, base+"/discover"); w.Code != http.StatusOK {
		t.Fatalf("discover read candidate: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, base+"/review-candidate"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "sensitive remote description") || !strings.Contains(w.Body.String(), "privateField") {
		t.Fatalf("expected sensitive candidate baseline: %d %s", w.Code, w.Body.String())
	}
	runRevocation := func(label, query string, args ...any) {
		t.Helper()
		barrier := &reviewReadAuthorizationBarrier{policy: policy, reached: make(chan struct{}, 1), release: make(chan struct{})}
		service.Access = barrier
		defer func() {
			select {
			case <-barrier.release:
			default:
				close(barrier.release)
			}
			service.Access = policy
		}()
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() { response <- call(http.MethodGet, base+"/review-candidate") }()
		select {
		case <-barrier.reached:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: candidate did not reach post-authorization barrier", label)
		}
		deleteCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		tag, err := adminDB.Exec(deleteCtx, query, args...)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("%s: revocation did not commit before candidate resumed: tag=%v err=%v", label, tag, err)
		}
		close(barrier.release)
		var result *httptest.ResponseRecorder
		select {
		case result = <-response:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: candidate did not finish after barrier release", label)
		}
		if result.Code != http.StatusNotFound || strings.Contains(result.Body.String(), "sensitive remote description") ||
			strings.Contains(result.Body.String(), "privateField") {
			t.Fatalf("%s: committed revocation leaked tool declaration: status=%d body=%s", label, result.Code, result.Body.String())
		}
	}
	runRevocation("connector grant", `DELETE FROM `+quoted+`.v3_role_grants
 WHERE role_id='viewer' AND module='connectors' AND action='manage'`)
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','application','app')`); err != nil {
		t.Fatal(err)
	}
	runRevocation("session logout", `DELETE FROM `+quoted+`.sessions WHERE token_hash=$1`, store.Hash(token))
}

func (b *reviewReadAuthorizationBarrier) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, decision rbac.V3Decision) error {
	if err := b.policy.AuthorizeWithTx(ctx, tx, user, decision); err != nil {
		return err
	}
	if decision.Module == "connectors" && decision.Action == "manage" {
		select {
		case b.reached <- struct{}{}:
		default:
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
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
		if method == http.MethodGet && strings.HasSuffix(path, "/review-candidate") {
			api.ServeHTTP(&candidateDeadlineRecorder{w}, r)
		} else {
			api.ServeHTTP(w, r)
		}
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
	if w := managerCall(http.MethodPost, base+"/review", nil); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unversioned main review accepted: %d %s", w.Code, w.Body.String())
	}
	if w := managerCall(http.MethodPatch, base+"/tools/read_app", map[string]any{"enabled": true}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unversioned main tool route accepted: %d %s", w.Code, w.Body.String())
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
			{Name: "read_app", ReadOnlyHint: true, InputSchema: map[string]any{"type": "object"}},
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
	unsafeDecisions := make([]map[string]any, 0, len(partialTools))
	for _, item := range partialTools {
		tool := item.(map[string]any)
		unsafeDecisions = append(unsafeDecisions, map[string]any{"name": tool["name"], "tool_hash": tool["tool_hash"], "decision": "allow_read_only"})
	}
	unsafeReview := map[string]any{"snapshot_id": partial["snapshot_id"], "registration_version": partial["registration_version"],
		"registration_hash": partial["registration_hash"], "tools_hash": partial["tools_hash"], "decisions": unsafeDecisions}
	if response := managerCall(http.MethodPost, base+"/review", unsafeReview); response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), "mcp_tool_not_read_only") {
		t.Fatalf("write-capable declaration accepted as read-only: %d %s", response.Code, response.Body.String())
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
		decision := "deny"
		if tool["read_only_hint"] == true {
			decision = "allow_read_only"
		}
		concurrentDecisions = append(concurrentDecisions, map[string]any{
			"name": tool["name"], "tool_hash": tool["tool_hash"], "decision": decision,
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

// Account row contention pauses the write after middleware has resolved the
// cookie, before the transaction-local session check. The second barrier is
// after that check and verifies logout waits through the write and audit.
func TestMCPReviewWriteSessionLogoutIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_session_race_" + store.ID()
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
	login := func() string {
		t.Helper()
		_, token, err := st.Login(ctx, manager.Email, "isolated-manager-password")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := login()
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
	if _, err := db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by)
 SELECT 'mcp-1','app','dev',id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(db, policy)
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		return []aiextensions.ToolInfo{{Name: "read_app", Description: "read only", ReadOnlyHint: true,
			InputSchema: map[string]any{"type": "object"}}}, nil
	}
	service.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/mcp-servers/mcp-1"
	call := func(token, method, path string, body any) *httptest.ResponseRecorder {
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, path, bytes.NewReader(payload)).WithContext(callCtx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		if method == http.MethodGet && strings.HasSuffix(path, "/review-candidate") {
			api.ServeHTTP(&candidateDeadlineRecorder{w}, r)
		} else {
			api.ServeHTTP(w, r)
		}
		return w
	}
	if w := call(token, http.MethodPost, base+"/discover", nil); w.Code != http.StatusOK {
		t.Fatalf("discover: %d %s", w.Code, w.Body.String())
	}
	w := call(token, http.MethodGet, base+"/review-candidate", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("candidate: %d %s", w.Code, w.Body.String())
	}
	var candidate struct {
		SnapshotID          string `json:"snapshot_id"`
		RegistrationVersion int    `json:"registration_version"`
		RegistrationHash    string `json:"registration_hash"`
		ToolsHash           string `json:"tools_hash"`
		Tools               []struct {
			Name     string `json:"name"`
			ToolHash string `json:"tool_hash"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &candidate); err != nil || len(candidate.Tools) != 1 {
		t.Fatalf("candidate tools: %v %s", err, w.Body.String())
	}
	reviewBody := map[string]any{
		"snapshot_id": candidate.SnapshotID, "registration_version": candidate.RegistrationVersion,
		"registration_hash": candidate.RegistrationHash, "tools_hash": candidate.ToolsHash,
		"decisions": []map[string]any{{"name": candidate.Tools[0].Name,
			"tool_hash": candidate.Tools[0].ToolHash, "decision": "allow_read_only"}},
	}
	toggleBody := map[string]any{
		"snapshot_id": candidate.SnapshotID, "tools_hash": candidate.ToolsHash,
		"tool_hash": candidate.Tools[0].ToolHash, "expected_revision": 1, "enabled": true,
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	await := func(label string, signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not reach barrier", label)
		}
	}
	logoutMustWait := func(token string) {
		t.Helper()
		lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		tx, err := adminDB.Begin(lockCtx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(lockCtx)
		if _, err := tx.Exec(lockCtx, `SET LOCAL lock_timeout = '300ms'`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(lockCtx, `DELETE FROM `+quoted+`.sessions WHERE token_hash=$1`, store.Hash(token))
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("logout should wait on review session lock: %v", err)
		}
	}
	logoutAfterWrite := func(token string) {
		t.Helper()
		tag, err := adminDB.Exec(ctx, `DELETE FROM `+quoted+`.sessions WHERE token_hash=$1`, store.Hash(token))
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("logout after write did not commit: tag=%v err=%v", tag, err)
		}
		var exists bool
		if err := adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+quoted+`.sessions WHERE token_hash=$1)`,
			store.Hash(token)).Scan(&exists); err != nil || exists {
			t.Fatalf("session survived committed logout: exists=%t err=%v", exists, err)
		}
	}
	for _, tc := range []struct {
		name, method, path string
		body               any
		wantReview         int
		wantToggle         int
	}{
		{"review", http.MethodPost, base + "/review-version", reviewBody, 0, 0},
		{"toggle", http.MethodPatch, base + "/reviewed-tools/read_app", toggleBody, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logoutTx, err := adminDB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer logoutTx.Rollback(context.Background())
			if _, err := logoutTx.Exec(ctx, `SELECT id FROM `+quoted+`.users WHERE id=$1 FOR UPDATE`, manager.ID); err != nil {
				t.Fatal(err)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- call(token, tc.method, tc.path, tc.body) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				err := adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE wait_event_type='Lock' AND query LIKE '%NOT disabled AND deleted_at IS NULL%')`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s did not reach write account lock", tc.name)
				}
				time.Sleep(10 * time.Millisecond)
			}
			tag, err := logoutTx.Exec(ctx, `DELETE FROM `+quoted+`.sessions WHERE token_hash=$1`, store.Hash(token))
			if err != nil || tag.RowsAffected() != 1 {
				t.Fatalf("logout did not delete session: tag=%v err=%v", tag, err)
			}
			if err := logoutTx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := <-result
			if w.Code != http.StatusNotFound ||
				count(`SELECT count(*) FROM v3_mcp_tool_reviews`) != tc.wantReview ||
				count(`SELECT count(*) FROM management_audit WHERE action='v3.mcp.tool_version_reviewed'`) != tc.wantReview ||
				count(`SELECT count(*) FROM management_audit WHERE action='v3.mcp.reviewed_tool_changed'`) != tc.wantToggle ||
				count(`SELECT count(*) FROM v3_mcp_tool_review_decisions WHERE enabled`) != 0 {
				t.Fatalf("revocation-first write: status=%d body=%s", w.Code, w.Body.String())
			}
			token = login()
			if tc.name == "review" {
				if w := call(token, tc.method, tc.path, tc.body); w.Code != http.StatusOK {
					t.Fatalf("baseline review: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"toggle", http.MethodPatch, base + "/reviewed-tools/read_app", toggleBody},
	} {
		t.Run(tc.name+"-write-first", func(t *testing.T) {
			barrier := &reviewReadAuthorizationBarrier{policy: policy, reached: make(chan struct{}, 1), release: make(chan struct{})}
			service.Access = barrier
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- call(token, tc.method, tc.path, tc.body) }()
			await(tc.name+" authorization", barrier.reached)
			logoutMustWait(token)
			close(barrier.release)
			w := <-result
			service.Access = policy
			if w.Code != http.StatusOK ||
				count(`SELECT count(*) FROM management_audit WHERE action='v3.mcp.reviewed_tool_changed'`) != 1 {
				t.Fatalf("write-first toggle: %d %s", w.Code, w.Body.String())
			}
			logoutAfterWrite(token)
			token = login()
		})
	}
	// A new discovery version gives the review route a fresh write while the
	// preceding toggle remains available as the separate switch case.
	if w := call(token, http.MethodPost, base+"/discover", nil); w.Code != http.StatusOK {
		t.Fatalf("discover second review: %d %s", w.Code, w.Body.String())
	}
	w = call(token, http.MethodGet, base+"/review-candidate", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("second review candidate: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &candidate); err != nil || len(candidate.Tools) != 1 {
		t.Fatalf("second review candidate tools: %v %s", err, w.Body.String())
	}
	reviewBody = map[string]any{
		"snapshot_id": candidate.SnapshotID, "registration_version": candidate.RegistrationVersion,
		"registration_hash": candidate.RegistrationHash, "tools_hash": candidate.ToolsHash,
		"decisions": []map[string]any{{"name": candidate.Tools[0].Name,
			"tool_hash": candidate.Tools[0].ToolHash, "decision": "allow_read_only"}},
	}
	barrier := &reviewReadAuthorizationBarrier{policy: policy, reached: make(chan struct{}, 1), release: make(chan struct{})}
	service.Access = barrier
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- call(token, http.MethodPost, base+"/review-version", reviewBody) }()
	await("review authorization", barrier.reached)
	logoutMustWait(token)
	close(barrier.release)
	w = <-result
	service.Access = policy
	if w.Code != http.StatusOK || count(`SELECT count(*) FROM v3_mcp_tool_reviews`) != 2 ||
		count(`SELECT count(*) FROM management_audit WHERE action='v3.mcp.tool_version_reviewed'`) != 2 {
		t.Fatalf("write-first review: %d %s", w.Code, w.Body.String())
	}
	logoutAfterWrite(token)
}
