package v3configsync

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3canvas"
)

// This opt-in test uses only the disposable WAN-124 PostgreSQL container. Each
// instance gets the complete production migration sequence in its own schema.
func formalPromotionStore(t *testing.T, dsn string) *store.Store {
	t.Helper()
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Extensions are database-wide. Require the caller to prepare pgcrypto in
	// public of the disposable database; this test only creates and drops its
	// own random schemas.
	var extensionSchema string
	if err := admin.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e
		JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&extensionSchema); err != nil || extensionSchema != "public" {
		admin.Close()
		t.Fatal("preinstall pgcrypto in public of the disposable test database")
	}
	schema := "wan124_formal_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &store.Store{DB: db}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("full Store.Migrate for %s: %v", schema, err)
	}
	return s
}

func TestConfigPromotionFullFormalSchemasRoundTrip(t *testing.T) {
	if os.Getenv("AIOPS_TEST_WAN124_FORMAL_PG") != "dedicated-temp-container" {
		t.Skip("requires explicit WAN-124 isolated PostgreSQL opt-in")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/wan124test" {
		t.Fatal("TEST_DATABASE_URL must target the dedicated loopback wan124test database")
	}
	source := formalPromotionStore(t, dsn)
	production := formalPromotionStore(t, dsn)
	ctx := t.Context()
	// Users, roles and sessions use the real schema and effective-grant view.
	if _, err := source.DB.Exec(ctx, `
INSERT INTO users(id,email,name,role,password_hash)
 VALUES('owner','owner@example.invalid','Owner','viewer','unused');
INSERT INTO rbac_roles(id,name) VALUES('promotion-owner','Promotion Owner');
INSERT INTO rbac_user_roles(user_id,role_id) VALUES('owner','promotion-owner');
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('promotion-owner','environment','view','environment','test'),
 ('promotion-owner','canvas','change','environment','test');
INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name,nodes,edges)
 VALUES('CT-source','owner','test','plan','Safe plan',
 '[{"id":"node-1","x":16,"y":140,"w":260,"icon":"layers","tone":"info","title":"节点 1","sub":"","blocks":[]}]','[]');`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at)
 VALUES($1,'owner',now()+interval '1 hour')`, store.Hash("owner-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := production.DB.Exec(ctx, `
INSERT INTO users(id,email,name,role,password_hash)
 VALUES('admin','admin@example.invalid','Admin','admin','unused');`); err != nil {
		t.Fatal(err)
	}
	if _, err := production.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at)
 VALUES($1,'admin',now()+interval '1 hour')`, store.Hash("admin-token")); err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepository{}
	nonprodHTTP := configServer(t, source, repo, "nonprod")
	prodHTTP := configServer(t, production, repo, "production")
	exported := configRequest(nonprodHTTP, "POST", "/api/v1/promotions", "test", "owner-token",
		`{"kind":"templates","id":"CT-source"}`)
	if exported.Code != 200 {
		t.Fatalf("source export: %d %s", exported.Code, exported.Body.String())
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_config_exports`) != 0 {
		t.Fatal("source export appeared in production schema")
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_canvas_templates WHERE id='CT-source'`) != 0 {
		t.Fatal("private source template appeared in production schema")
	}
	repo.merged = true // fixture stands for the separately reviewed merged MR.
	for attempt := 0; attempt < 2; attempt++ {
		imported := configRequest(prodHTTP, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", "")
		if imported.Code != 200 || !strings.Contains(imported.Body.String(), `"status":"imported"`) {
			t.Fatalf("production import %d: %d %s", attempt, imported.Code, imported.Body.String())
		}
	}
	if promotionCount(t, production.DB, `SELECT count(*) FROM v3_config_imports`) != 1 ||
		promotionCount(t, production.DB, `SELECT count(*) FROM management_audit WHERE action='v3.config_import.completed'`) != 1 ||
		promotionCount(t, source.DB, `SELECT count(*) FROM v3_config_imports`) != 0 {
		t.Fatal("import ledger/audit not idempotent or crossed schema boundary")
	}
	var exportID string
	if err := source.DB.QueryRow(ctx, `SELECT id FROM v3_config_exports WHERE object_id='CT-source'`).Scan(&exportID); err != nil {
		t.Fatal(err)
	}
	// A new pool and service must recover the imported system template from PG.
	freshPool, err := pgxpool.NewWithConfig(ctx, production.DB.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(freshPool.Close)
	freshProd := &store.Store{DB: freshPool}
	recovered := configServer(t, freshProd, repo, "production")
	v3canvas.New(freshProd).Register(recovered)
	templates := configRequest(recovered, "GET", "/api/v1/canvas-templates", "prod", "admin-token", "")
	if templates.Code != 200 || !strings.Contains(templates.Body.String(), "CTP-"+exportID) ||
		!strings.Contains(templates.Body.String(), `"name":"Safe plan"`) {
		t.Fatalf("fresh production template read: %d %s", templates.Code, templates.Body.String())
	}
	if promotionCount(t, freshPool, `SELECT count(*) FROM v3_config_imports`) != 1 {
		t.Fatal("new production pool did not recover import ledger")
	}
}
