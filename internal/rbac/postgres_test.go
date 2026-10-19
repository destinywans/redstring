package rbac

import (
	"context"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
)

func isolatedDB(t *testing.T) *store.Store { return isolatedDBSeeded(t, true) }

func isolatedDBSeeded(t *testing.T, seed bool) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("invalid test database configuration")
	}
	schema := "rbac_test_" + store.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error("isolated schema cleanup failed")
		}
	})
	// Deliberately establish pre-RBAC data to exercise the real migration.
	_, e = db.Exec(ctx, "CREATE TABLE schema_migrations(name text PRIMARY KEY)")
	if e != nil {
		t.Fatal(e)
	}
	entries, _ := migrations.Files.ReadDir(".")
	for _, f := range entries {
		if !strings.HasSuffix(f.Name(), ".sql") || f.Name() >= "014" {
			continue
		}
		raw, _ := migrations.Files.ReadFile(f.Name())
		if _, e = db.Exec(ctx, string(raw)); e != nil {
			t.Fatal(f.Name(), e)
		}
		if _, e = db.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", f.Name()); e != nil {
			t.Fatal(e)
		}
	}
	if seed {
		_, e = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,department) VALUES('a','a@test.invalid','Admin','admin','unused',''),('u','u@test.invalid','Operator','operator','unused','Dept 2'),('v','v@test.invalid','Viewer','viewer','unused','');
 INSERT INTO users(id,email,name,role,password_hash,disabled,source,external_id) VALUES('mock','mock@test.invalid','Mock','admin','',true,'settings_demo','mock');
 INSERT INTO teams(id,name) VALUES('d1','Dept 1'),('d2','Dept 2'); INSERT INTO team_members VALUES('d1','u'),('d2','u');
 INSERT INTO applications(id,key,name) VALUES('app1','app1','One'),('app2','app2','Two');
 INSERT INTO application_grants VALUES('app1','team','d1','operate');
 INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('r1','host','Host One','manual'),('r2','host','Host Two','manual');`)
		if e != nil {
			t.Fatal(e)
		}
	}
	st := &store.Store{DB: db}
	if e = st.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return st
}
