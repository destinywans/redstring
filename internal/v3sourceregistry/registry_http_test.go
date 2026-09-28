package v3sourceregistry

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
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type revokingAccess struct {
	policy *rbac.V3Policy
	revoke func() error
}

func (a *revokingAccess) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	return a.policy.Authorize(ctx, user, decision)
}

func (a *revokingAccess) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, decision rbac.V3Decision) error {
	if decision.Action == "manage" && a.revoke != nil {
		revoke := a.revoke
		a.revoke = nil
		if err := revoke(); err != nil {
			return err
		}
	}
	return a.policy.AuthorizeWithTx(ctx, tx, user, decision)
}

func TestSourceRegistrySingleConnectionAndRevokedGrants(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_source_reader_" + store.ID()
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
 ('viewer','knowledge','view','application','app'),
 ('viewer','knowledge','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES
 ('app','app','App','dev',false),('hidden','hidden','Hidden','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('app-dev','app','dev','Dev',false),('hidden-dev','hidden','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app','dev'),('hidden','dev')`,
		`INSERT INTO v3_knowledge_source_registrations
 (id,environment_id,application_id,name,source_type,source_url,secret_ref,spaces,schedule,chunk,inherit_acl,created_by)
 SELECT 'visible-source','dev','app','Visible','web','https://wiki.example.invalid','vault:kv/read',
 '[]'::jsonb,'manual','heading',true,id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO v3_knowledge_source_registrations
 (id,environment_id,application_id,name,source_type,source_url,secret_ref,spaces,schedule,chunk,inherit_acl,created_by)
 SELECT 'hidden-source','dev','hidden','Hidden','web','https://hidden.example.invalid','vault:kv/read',
 '[]'::jsonb,'manual','heading',true,id FROM users WHERE email='admin@example.invalid'`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	access := &revokingAccess{policy: policy}
	api := httpapi.New(st, nil, true)
	New(db, access).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, "/api/v1/knowledge-sources/registrations", strings.NewReader(body)).WithContext(callCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodGet, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"visible-source"`) ||
		strings.Contains(w.Body.String(), "hidden-source") || strings.Contains(w.Body.String(), "vault:kv/read") {
		t.Fatalf("initial list: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		Items []item `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("unexpected visible registry: %v %s", err, w.Body.String())
	}
	createBody := `{"name":"New source","type":"web","url":"https://wiki.example.invalid",` +
		`"secret_ref":"vault:kv/read","spaces":[],"schedule":"manual","chunk":"heading",` +
		`"inherit_acl":true,"application_id":"app"}`
	if w := request(http.MethodPost, createBody); w.Code != http.StatusCreated ||
		!strings.Contains(w.Body.String(), `"registration_status":"unverified"`) {
		t.Fatalf("create with grant: %d %s", w.Code, w.Body.String())
	}
	revokeDB, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer revokeDB.Close()
	// Revoke after create has checked the application row, immediately before
	// its transaction-scoped manage decision. The separate pool commits the
	// change while the request transaction still holds its one connection.
	access.revoke = func() error {
		_, err := revokeDB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='manage'`)
		return err
	}
	if w := request(http.MethodPost, createBody); w.Code != http.StatusForbidden {
		t.Fatalf("create after manage revocation: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_registrations WHERE application_id='app'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unexpected registration count after revocation: %d %v", count, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("list after view revocation: %d %s", w.Code, w.Body.String())
	}
}
