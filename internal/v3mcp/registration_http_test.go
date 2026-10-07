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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestMCPRegistrationHTTPIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_registration_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
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
	actor, err := st.CreateUser(ctx, core.User{Email: "manager@example.invalid", Name: "Manager", Role: "viewer"}, "isolated-manager-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, actor.Email, "isolated-manager-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','environment','view','environment','dev'),('viewer','connectors','view','application','app'),('viewer','connectors','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES ('app','app','App','dev',false),('foreign','foreign','Foreign','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES ('app-dev','app','dev','Dev',false),('foreign-dev','foreign','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app','dev'),('foreign','dev')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	service := New(db, policy)
	api := httpapi.New(st, nil, true)
	discoveryCalls := 0
	service.DiscoverTools = func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error) {
		discoveryCalls++
		return []aiextensions.ToolInfo{{Name: "read_app", ReadOnlyHint: true, InputSchema: map[string]any{"type": "object"}}}, nil
	}
	service.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(&candidateDeadlineRecorder{w}, r)
		return w
	}
	body := `{"application_id":"app","title":"App MCP","maintainer":"platform","endpoint":"https://private.example.invalid/mcp","credential_ref":"private-ref"}`
	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if w := call("POST", "/api/v1/mcp-servers", body, "dev"); w.Code != 503 || !strings.Contains(w.Body.String(), "mcp_registration_endpoint_unavailable") || discoveryCalls != 0 || count("ai_extensions") != 0 {
		t.Fatalf("missing deployment admission accepted registration: %d %s", w.Code, w.Body.String())
	}
	service.AuthorizeRegistrationEndpoint = func(_ context.Context, app, env string, config aiextensions.MCPConfig) error {
		if app != "app" || env != "dev" || config.Endpoint != "https://private.example.invalid/mcp" || config.CredentialRef != "private-ref" || config.AllowHTTP {
			return errors.New("not admitted")
		}
		return nil
	}
	admission := service.AuthorizeRegistrationEndpoint
	if w := call("POST", "/api/v1/mcp-servers", strings.Replace(body, "private.example.invalid", "untrusted.example.invalid", 1), "dev"); w.Code != 503 || count("ai_extensions") != 0 {
		t.Fatalf("untrusted deployment transport admitted: %d %s", w.Code, w.Body.String())
	}
	for _, extra := range []string{`,"environment_id":"prod"`, `,"bindings":[]`, `,"token":"secret"`, `,"reviewed":true`, `,"enabled":true`} {
		if w := call("POST", "/api/v1/mcp-servers", strings.TrimSuffix(body, "}")+extra+"}", "dev"); w.Code != 422 {
			t.Fatalf("untrusted body accepted: %d %s", w.Code, w.Body.String())
		}
	}
	absent := call("POST", "/api/v1/mcp-servers", strings.Replace(body, `"app"`, `"absent"`, 1), "dev")
	foreign := call("POST", "/api/v1/mcp-servers", strings.Replace(body, `"app"`, `"foreign"`, 1), "dev")
	if absent.Code != 404 || foreign.Code != 404 || absent.Body.String() != foreign.Body.String() {
		t.Fatalf("application existence leak: %d %s / %d %s", absent.Code, absent.Body.String(), foreign.Code, foreign.Body.String())
	}
	if w := call("POST", "/api/v1/mcp-servers", body, "all"); w.Code != 409 {
		t.Fatalf("all write accepted: %d %s", w.Code, w.Body.String())
	}
	if count("ai_extensions") != 0 || count("ai_extension_revisions") != 0 || count("v3_mcp_environment_bindings") != 0 {
		t.Fatal("failed create left records")
	}
	// Pause after policy admission. Session/grant/application revocations must wait
	// for registration and audit to commit, rather than race a stale policy read.
	barrier := &reviewReadAuthorizationBarrier{policy: policy, reached: make(chan struct{}, 1), release: make(chan struct{})}
	service.Access = barrier
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- call("POST", "/api/v1/mcp-servers", body, "dev") }()
	select {
	case <-barrier.reached:
	case <-time.After(5 * time.Second):
		close(barrier.release)
		t.Fatal("registration did not reach authority barrier")
	}
	for _, statement := range []string{
		`DELETE FROM ` + quoted + `.sessions WHERE user_id='` + actor.ID + `'`,
		`DELETE FROM ` + quoted + `.v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`,
		`UPDATE ` + quoted + `.applications SET archived=true WHERE id='app'`,
	} {
		tx, e := admin.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, `SET LOCAL lock_timeout='150ms'`); e == nil {
			_, e = tx.Exec(ctx, statement)
		}
		_ = tx.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(e, &pgErr) || pgErr.Code != "55P03" {
			close(barrier.release)
			t.Fatalf("authority revoke not pinned: %v", e)
		}
	}
	close(barrier.release)
	var w *httptest.ResponseRecorder
	select {
	case w = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not complete")
	}
	service.Access = policy
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID                 string `json:"id"`
		EnvironmentID      string `json:"environment_id"`
		Reviewed           bool   `json:"reviewed"`
		ExecutionAvailable bool   `json:"execution_available"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.EnvironmentID != "dev" || created.Reviewed || created.ExecutionAvailable || discoveryCalls != 0 {
		t.Fatalf("registration implied runtime action: %s", w.Body.String())
	}
	for _, path := range []string{"/api/v1/mcp-servers", "/api/v1/mcp-servers/" + created.ID} {
		response := call("GET", path, "", "dev")
		if response.Code != 200 || !strings.Contains(response.Body.String(), created.ID) {
			t.Fatalf("new registration unreadable: %d %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "private.example.invalid") || strings.Contains(response.Body.String(), "private-ref") {
			t.Fatal("read response exposed transport secrets")
		}
	}
	if count("ai_extensions") != 1 || count("ai_extension_revisions") != 1 || count("v3_mcp_environment_bindings") != 1 || count("v3_mcp_tool_reviews") != 0 {
		t.Fatal("registration persistence or trust boundary incorrect")
	}
	var audit string
	if err = db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE action='v3.mcp.registered' AND target=$1`, created.ID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(audit, "private") || strings.Contains(w.Body.String(), "private") {
		t.Fatal("registration/audit leaked transport config")
	}
	// Discover/review/enable the newly created registration via the actual V3 handlers.
	service.AuthorizeRegistrationEndpoint = nil
	w = call("POST", "/api/v1/mcp-servers/"+created.ID+"/discover", "", "dev")
	if w.Code != 503 || discoveryCalls != 0 {
		t.Fatalf("revoked deployment admission contacted endpoint: %d %s", w.Code, w.Body.String())
	}
	if read := call("GET", "/api/v1/mcp-servers/"+created.ID, "", "dev"); read.Code != 200 {
		t.Fatalf("deployment admission hid stored registration: %d %s", read.Code, read.Body.String())
	}
	service.AuthorizeRegistrationEndpoint = admission
	for _, method := range []string{"DELETE", "PUT"} {
		if rebound := call(method, "/api/v1/mcp-servers/"+created.ID+"/environment-binding", "", "dev"); rebound.Code != 200 {
			t.Fatalf("pending registration cannot rebind: %d %s", rebound.Code, rebound.Body.String())
		}
	}
	w = call("POST", "/api/v1/mcp-servers/"+created.ID+"/discover", "", "dev")
	if w.Code != 200 || discoveryCalls != 1 {
		t.Fatalf("new registration cannot discover: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/mcp-servers/"+created.ID+"/review-candidate", "", "dev")
	if w.Code != 200 {
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
	if err = json.Unmarshal(w.Body.Bytes(), &candidate); err != nil || len(candidate.Tools) != 1 {
		t.Fatalf("invalid candidate: %v %s", err, w.Body.String())
	}
	review, _ := json.Marshal(map[string]any{"snapshot_id": candidate.SnapshotID, "registration_version": candidate.RegistrationVersion, "registration_hash": candidate.RegistrationHash, "tools_hash": candidate.ToolsHash, "decisions": []map[string]string{{"name": "read_app", "tool_hash": candidate.Tools[0].ToolHash, "decision": "allow_read_only"}}})
	w = call("POST", "/api/v1/mcp-servers/"+created.ID+"/review", string(review), "dev")
	if w.Code != 200 {
		t.Fatalf("review: %d %s", w.Code, w.Body.String())
	}
	toggle, _ := json.Marshal(map[string]any{"snapshot_id": candidate.SnapshotID, "registration_version": candidate.RegistrationVersion, "registration_hash": candidate.RegistrationHash, "tools_hash": candidate.ToolsHash, "tool_hash": candidate.Tools[0].ToolHash, "expected_revision": 1, "enabled": true})
	w = call("PATCH", "/api/v1/mcp-servers/"+created.ID+"/tools/read_app", string(toggle), "dev")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"execution_available":false`) {
		t.Fatalf("toggle escaped execution boundary: %d %s", w.Code, w.Body.String())
	}
	var source string
	if err = db.QueryRow(ctx, `SELECT record->>'registration_source' FROM ai_extensions WHERE id=$1`, created.ID).Scan(&source); err != nil || source != "v3" {
		t.Fatalf("lifecycle dropped deployment-admission identity: %v %q", err, source)
	}
	service.AuthorizeRegistrationEndpoint = nil
	if again := call("POST", "/api/v1/mcp-servers/"+created.ID+"/discover", "", "dev"); again.Code != 503 || discoveryCalls != 1 {
		t.Fatalf("reviewed registration bypassed revoked deployment admission: %d %s", again.Code, again.Body.String())
	}
	service.AuthorizeRegistrationEndpoint = admission
	if _, err = db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT reject_registration CHECK(action<>'v3.mcp.registered') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	w = call("POST", "/api/v1/mcp-servers", body, "dev")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "audit_unavailable") || count("ai_extensions") != 1 || count("ai_extension_revisions") != 1 || count("v3_mcp_environment_bindings") != 1 {
		t.Fatalf("audit failure left partial registration: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT reject_registration`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", "/api/v1/mcp-servers", body, "dev"); w.Code != 404 || count("ai_extensions") != 1 {
		t.Fatalf("revoked grant registered: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(session)); err != nil {
		t.Fatal(err)
	}
	if w = call("POST", "/api/v1/mcp-servers", body, "dev"); w.Code != 401 {
		t.Fatalf("revoked session registered: %d %s", w.Code, w.Body.String())
	}
}
