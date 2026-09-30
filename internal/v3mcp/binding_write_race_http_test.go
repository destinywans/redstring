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

func TestMCPEnvironmentBindingPinsSessionAndGrantThroughAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mcp_binding_race_" + store.ID()
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
	cfg.MaxConns = 1
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
	var fixtureDigest string
	if err = db.QueryRow(ctx, `SELECT encode(digest('fixture','sha256'),'hex')`).Scan(&fixtureDigest); err != nil {
		t.Fatalf("isolated audit digest unavailable: %v", err)
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
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','manage','application','app')`,
		`INSERT INTO teams(id,name) VALUES('owner','Owner')`,
		`INSERT INTO applications(id,key,name,environment,owner_team_id,archived)
 VALUES('app','app','App','dev','owner',false),('foreign','foreign','Foreign','dev',NULL,false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived)
 VALUES('app-dev','app','dev','Dev',false),('foreign-dev','foreign','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('foreign','dev')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	record := aiextensions.Record{ID: "mcp-1", ApplicationID: "app", Kind: "mcp", Title: "MCP", Maintainer: "platform", Enabled: true, Version: 1,
		MCP: &aiextensions.MCPConfig{Endpoint: "https://mcp.example.invalid/mcp", Bindings: []aiextensions.Binding{{Name: "read", Tool: "read", ReadOnlyConfirmed: true, Arguments: map[string]any{}}}}}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES('mcp-1','app','mcp',1,$1)`, raw); err != nil {
		t.Fatal(err)
	}
	foreignRecord := record
	foreignRecord.ID, foreignRecord.ApplicationID = "mcp-foreign", "foreign"
	foreignRaw, err := json.Marshal(foreignRecord)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES('mcp-foreign','foreign','mcp',1,$1)`, foreignRaw); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	service := New(db, policy)
	api := httpapi.New(st, nil, true)
	service.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/mcp-servers/mcp-1/environment-binding"
	callTarget := func(method, target string) *httptest.ResponseRecorder {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, target, nil).WithContext(callCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	call := func(method string) *httptest.ResponseRecorder { return callTarget(method, path) }
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		absent := callTarget(method, "/api/v1/mcp-servers/mcp-absent/environment-binding")
		foreign := callTarget(method, "/api/v1/mcp-servers/mcp-foreign/environment-binding")
		if absent.Code != http.StatusNotFound || foreign.Code != http.StatusNotFound || absent.Body.String() != foreign.Body.String() {
			t.Fatalf("binding target existence leaked: %s absent=%d %s foreign=%d %s", method, absent.Code, absent.Body.String(), foreign.Code, foreign.Body.String())
		}
	}
	count := func(table, where string) int {
		t.Helper()
		var value int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE "+where).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	assertRevocationBlocked := func(statement string, args ...any) {
		t.Helper()
		tx, err := adminDB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='250ms'`); err == nil {
			_, err = tx.Exec(ctx, statement, args...)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			_ = tx.Rollback(context.Background())
			t.Fatalf("revocation committed while binding transaction was authorized: %v", err)
		}
		_ = tx.Rollback(context.Background())
	}
	runPaused := func(method string, revocations ...func()) *httptest.ResponseRecorder {
		t.Helper()
		barrier := &reviewReadAuthorizationBarrier{policy: policy, reached: make(chan struct{}, 1), release: make(chan struct{})}
		service.Access = barrier
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- call(method) }()
		select {
		case <-barrier.reached:
		case <-time.After(5 * time.Second):
			close(barrier.release)
			service.Access = policy
			t.Fatal("binding did not reach post-authorization barrier")
		}
		for _, revoke := range revocations {
			revoke()
		}
		close(barrier.release)
		var response *httptest.ResponseRecorder
		select {
		case response = <-result:
		case <-time.After(5 * time.Second):
			service.Access = policy
			t.Fatal("binding did not finish after barrier release")
		}
		service.Access = policy
		return response
	}
	managerGrant := `DELETE FROM ` + quoted + `.v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage' AND scope_kind='application'`
	if w := runPaused(http.MethodPut, func() { assertRevocationBlocked(managerGrant) }, func() {
		assertRevocationBlocked(`DELETE FROM `+quoted+`.sessions WHERE token_hash=$1`, store.Hash(session))
	}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) || count("v3_mcp_environment_bindings", "extension_id='mcp-1'") != 1 ||
		count("management_audit", "action='v3.mcp.environment_bound'") != 1 {
		t.Fatalf("bound write/audit failed under pinned authority: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','team','owner')`); err != nil {
		t.Fatal(err)
	}
	teamGrant := `DELETE FROM ` + quoted + `.v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage' AND scope_kind='team'`
	if w := runPaused(http.MethodDelete, func() { assertRevocationBlocked(teamGrant) }, func() {
		assertRevocationBlocked(`DELETE FROM `+quoted+`.sessions WHERE token_hash=$1`, store.Hash(session))
	}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) || count("v3_mcp_environment_bindings", "extension_id='mcp-1'") != 0 ||
		count("management_audit", "action='v3.mcp.environment_unbound'") != 1 {
		t.Fatalf("team-scoped unbind/audit failed under pinned authority: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT deny_mcp_binding_audit
 CHECK (action<>'v3.mcp.environment_bound') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPut); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), `"code":"audit_unavailable"`) ||
		count("v3_mcp_environment_bindings", "extension_id='mcp-1'") != 0 ||
		count("management_audit", "action='v3.mcp.environment_bound'") != 1 {
		t.Fatalf("failed audit committed binding: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT deny_mcp_binding_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPut); w.Code != http.StatusNotFound || count("v3_mcp_environment_bindings", "extension_id='mcp-1'") != 0 {
		t.Fatalf("revoked team grant still bound MCP: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(session)); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPut); w.Code != http.StatusUnauthorized || count("v3_mcp_environment_bindings", "extension_id='mcp-1'") != 0 {
		t.Fatalf("logged-out session still bound MCP: %d %s", w.Code, w.Body.String())
	}
}
