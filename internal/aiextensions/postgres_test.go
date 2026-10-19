package aiextensions

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresVersionCASAuditAndReload(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated random schema only")
	}
	ctx := context.Background()
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal("invalid test DSN")
	}
	if cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost" {
		t.Fatal("only explicitly local test PostgreSQL is accepted")
	}
	admin, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := "test_aiextensions_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	base := &store.Store{DB: db}
	if e = base.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('user','extensions@example.invalid','Extension test','admin','unused'); INSERT INTO applications(id,name) VALUES('app-a','Synthetic extension app')`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `INSERT INTO application_environments(id,application_id,key,name) VALUES('app-a-test','app-a','test','Test');
INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('extension-host','host','Extension host','manual');
INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES('extension-host','app-a','app-a-test');
INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES('resource-view','resource','extension-host','view'),('resource-update','resource','extension-host','update');`); e != nil {
		t.Fatal(e)
	}
	s := New(db, "")
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal("repeat migration", e)
	}
	s.Authorize = base.AuthorizeApplication
	ctx = store.WithPrincipal(ctx, core.User{ID: "user", Role: "admin"})
	general, e := s.Save(ctx, Record{Kind: "skill", Title: "Workspace skill", Body: "general instructions", Maintainer: "owner", Enabled: true})
	if e != nil {
		t.Fatal("save workspace record", e)
	}
	general.Body = "revised instructions"
	general, e = s.Save(ctx, general)
	if e != nil || general.Version != 2 {
		t.Fatal("update workspace record", e)
	}
	workspaceItems, e := s.ListWorkspace(ctx, "skill")
	if e != nil || len(workspaceItems) != 1 || workspaceItems[0].ID != general.ID {
		t.Fatal("list workspace records", e)
	}
	appItems, e := s.List(ctx, "app-a", "skill")
	if e != nil || len(appItems) != 0 {
		t.Fatal("workspace records leaked into application list", e)
	}
	if _, e = db.Exec(ctx, `INSERT INTO ai_extensions(id,kind,version,record) VALUES('invalid-general-mcp','mcp',1,'{}')`); e == nil {
		t.Fatal("database allowed workspace MCP")
	}
	old, e := s.Save(ctx, Record{ApplicationID: "app-a", Kind: "prompt", Title: "Version test", Body: "first", Maintainer: "owner", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	next := old
	next.Body = "second"
	next, e = s.Save(ctx, next)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Save(ctx, old); !errors.Is(e, store.ErrConflict) {
		t.Fatal("CAS", e)
	}
	reloaded := New(db, "")
	reloaded.Authorize = base.AuthorizeApplication
	got, e := reloaded.Get(ctx, old.ID, 1)
	if e != nil || got.Body != "first" || got.ContentHash != old.ContentHash {
		t.Fatal("revision reload", got, e)
	}
	got, e = reloaded.Get(ctx, old.ID, 0)
	if e != nil || got.Body != "second" || got.Version != 2 {
		t.Fatal("current reload", got, e)
	}
	var revisions, audits int
	_ = db.QueryRow(ctx, "SELECT count(*) FROM ai_extension_revisions WHERE id=$1", old.ID).Scan(&revisions)
	_ = db.QueryRow(ctx, "SELECT count(*) FROM management_audit WHERE action='ai_extension.saved' AND target=$1", old.ID).Scan(&audits)
	if revisions != 2 || audits != 2 {
		t.Fatal("atomic versions/audit", revisions, audits)
	}
	if _, e = db.Exec(ctx, `INSERT INTO application_environments(id,application_id,key,name) VALUES('env','app-a','lab','Lab')`); e != nil {
		t.Fatal(e)
	}
	composition, e := s.Compose(ctx, Composition{ApplicationID: "app-a", References: []Reference{{ID: old.ID, Version: 1}}, Question: "Investigate"})
	if e != nil {
		t.Fatal(e)
	}
	inv, e := base.CreateInvestigation(store.WithEnvironment(ctx, "env"), "app-a", "Extension persistence")
	if e != nil {
		t.Fatal(e)
	}
	// Queue only inside the disposable schema; no worker/model is started.
	if _, e = base.Enqueue(ctx, inv.Branches[0].ID, composition.Content, "isolated-test-model", "extension-version-test"); e != nil {
		t.Fatal(e)
	}
	read, e := (&store.Store{DB: db}).Investigation(ctx, inv.ID)
	if e != nil || len(read.Branches[0].Messages) != 1 || read.Branches[0].Messages[0].Content != composition.Content {
		t.Fatal("versioned composition not preserved in product messages", e)
	}
}
