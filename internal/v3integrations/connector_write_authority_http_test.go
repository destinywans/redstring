package v3integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectorTestRejectsManageRevocationWhileWaitingForRow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_test_authority_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','manage','environment','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.CreateUser(ctx, core.User{Email: "authority-viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "authority-viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/-/ready" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors
 (id,environment_id,provider,name,category,endpoint,auth_method,enabled)
 VALUES('locked-probe','dev','prometheus','Locked probe','observe',$1,'none',true)`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", nil).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}

	// Hold the exact permission row. The request may read this grant in its
	// initial policy check, but its final FOR SHARE authority lock must wait.
	lockTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(context.Background())
	var lockedRole string
	if err = lockTx.QueryRow(ctx, `SELECT role_id FROM v3_role_grants
 WHERE role_id='viewer' AND module='connectors' AND action='manage' AND scope_kind='environment' AND scope_id='dev'
 FOR UPDATE`).Scan(&lockedRole); err != nil || lockedRole != "viewer" {
		t.Fatalf("hold connectors.manage grant: role=%s err=%v", lockedRole, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/locked-probe/test", nil)
	request.Header.Set("X-AIOps-Environment", "dev")
	request.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		responses <- response
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err = adminDB.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
	 WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%FROM v3_role_grants%FOR SHARE%'`, schema).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("POST test did not wait on final grant authority lock after probe")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if probes.Load() != 1 {
		t.Fatalf("expected one upstream probe before write, got %d", probes.Load())
	}
	tag, err := lockTx.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id='viewer' AND module='connectors' AND action='manage' AND scope_kind='environment' AND scope_id='dev'`)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("delete locked manage grant: rows=%d err=%v", tag.RowsAffected(), err)
	}
	if err = lockTx.Commit(ctx); err != nil {
		t.Fatalf("commit manage revoke: %v", err)
	}
	select {
	case response := <-responses:
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"forbidden"`) {
			t.Fatalf("revoked POST test: status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("revoked POST test did not return after grant lock release")
	}
	var status string
	var checked bool
	if err = db.QueryRow(ctx, `SELECT status,last_checked_at IS NOT NULL FROM v3_connectors WHERE id='locked-probe'`).Scan(&status, &checked); err != nil || status != "unverified" || checked {
		t.Fatalf("revoked probe persisted state: status=%q checked=%v err=%v", status, checked, err)
	}
	var audits int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='connector.connection.tested' AND target='locked-probe'`).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("revoked probe audited: count=%d err=%v", audits, err)
	}
}
