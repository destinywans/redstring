package store

import (
	"context"
	"errors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
)

func platformDatabase(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("isolated database configuration invalid")
	}
	schema := "test_platform_" + ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if e != nil {
			t.Error("isolated schema cleanup failed")
		}
	})
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal("invalid isolated database configuration")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, e = pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal("isolated database unavailable")
	}
	s := &Store{DB: db}
	if _, e = db.Exec(ctx, "CREATE TABLE schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		t.Fatal(e)
	}
	files, e := migrations.Files.ReadDir(".")
	if e != nil {
		t.Fatal(e)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".sql") || file.Name() >= "006" {
			continue
		}
		raw, e := migrations.Files.ReadFile(file.Name())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, string(raw)); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, "INSERT INTO schema_migrations(name) VALUES($1)", file.Name()); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.Exec(ctx, `INSERT INTO applications(id,name,environment) VALUES('legacy','Old','prod');INSERT INTO investigations(id,application_id,title) VALUES('old-investigation','legacy','Old incident')`); e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPlatformFoundationPostgres(t *testing.T) {
	s := platformDatabase(t)
	base := context.Background()
	if _, e := s.DB.Exec(base, `INSERT INTO users(id,email,name,role,password_hash) VALUES('admin','admin@example.invalid','Admin','admin','unused'),('operator','op@example.invalid','Operator','operator','unused'),('viewer','view@example.invalid','Viewer','viewer','unused')`); e != nil {
		t.Fatal(e)
	}
	admin := core.User{ID: "admin", Role: "admin"}
	operator := core.User{ID: "operator", Role: "operator"}
	viewer := core.User{ID: "viewer", Role: "viewer"}
	ctx := WithPrincipal(base, admin)
	old, e := s.Investigation(ctx, "old-investigation")
	if e != nil || old.EnvironmentID != "legacy-default" {
		t.Fatalf("migration lost legacy environment: %+v %v", old, e)
	}
	if e = s.AuthorizeApplication(ctx, operator, "legacy", "view"); !errors.Is(e, ErrForbidden) {
		t.Fatalf("legacy grant should be explicit: %v", e)
	}
	team, e := s.SaveTeam(ctx, core.Team{Name: "SRE"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SetTeamMembers(ctx, team.ID, []string{operator.ID, viewer.ID}); e != nil {
		t.Fatal(e)
	}
	if e = s.SetGrants(ctx, "legacy", []core.Grant{{SubjectType: "team", SubjectID: team.ID, Permission: "operate"}}); e != nil {
		t.Fatal(e)
	}
	// Current application authorization is derived from exact infrastructure
	// grants. Preserve separate read/change roles and department revocation.
	if _, e = s.DB.Exec(ctx, `INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('foundation-host','host','Foundation host','manual');
INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES('foundation-host','legacy','legacy-default');
INSERT INTO rbac_roles(id,name) VALUES('foundation-view','Foundation view'),('foundation-change','Foundation change');
INSERT INTO rbac_permissions(id,name) VALUES('foundation-view','Foundation view'),('foundation-change','Foundation change');
INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES('foundation-view','resource','foundation-host','view'),('foundation-change','resource','foundation-host','update');
INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES('foundation-view','foundation-view'),('foundation-change','foundation-change');
INSERT INTO rbac_user_roles(user_id,role_id) VALUES('operator','foundation-change'),('admin','foundation-view'),('admin','foundation-change')`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(ctx, `INSERT INTO rbac_department_roles(department_id,role_id) VALUES($1,'foundation-view')`, team.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeApplication(ctx, operator, "legacy", "operate"); e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeApplication(ctx, viewer, "legacy", "view"); e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeApplication(ctx, viewer, "legacy", "operate"); !errors.Is(e, ErrForbidden) {
		t.Fatalf("viewer role ceiling bypassed: %v", e)
	}
	second, e := s.RegisterApplication(ctx, core.Application{Name: "Second", Key: "second", Environment: "test"})
	if e != nil {
		t.Fatal(e)
	}
	visible, e := s.VisibleApplicationIDs(ctx, operator)
	if e != nil || len(visible) != 1 || visible[0] != "legacy" {
		t.Fatalf("list scope leaked: %v %v", visible, e)
	}
	if e = s.AuthorizeApplication(ctx, operator, second.ID, "view"); !errors.Is(e, ErrForbidden) {
		t.Fatal("cross-application read allowed")
	}
	if e = s.SetTeamMembers(ctx, team.ID, []string{viewer.ID}); e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeApplication(ctx, operator, "legacy", "view"); !errors.Is(e, ErrForbidden) {
		t.Fatal("revocation not immediate")
	}
	if e = s.SetGrants(ctx, "legacy", []core.Grant{{SubjectType: "user", SubjectID: operator.ID, Permission: "operate"}}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES('operator','foundation-view')`); e != nil {
		t.Fatal(e)
	}
	env, e := s.SaveEnvironment(ctx, core.Environment{ApplicationID: "legacy", Key: "staging", Name: "Staging"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateInvestigation(ctx, "legacy", "ambiguous"); !errors.Is(e, ErrInvalidReference) {
		t.Fatalf("ambiguous environment accepted: %v", e)
	}
	inv, e := s.CreateInvestigation(WithEnvironment(ctx, env.ID), "legacy", "specific")
	if e != nil || inv.EnvironmentID != env.ID {
		t.Fatalf("environment identity: %+v %v", inv, e)
	}
	stale := env
	env.Name = "Staging updated"
	env, e = s.SaveEnvironment(ctx, env)
	if e != nil || env.BindingRevision != 1 {
		t.Fatalf("display name must not invalidate the binding: %+v %v", env, e)
	}
	env.Key = "staging-updated"
	env, e = s.SaveEnvironment(ctx, env)
	if e != nil || env.BindingRevision != 2 {
		t.Fatalf("binding revision: %+v %v", env, e)
	}
	if _, e = s.SaveEnvironment(ctx, stale); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale update accepted: %v", e)
	}
	app, e := s.Application(ctx, "legacy")
	if e != nil {
		t.Fatal(e)
	}
	app.Archived = true
	if _, e = s.UpdateApplication(ctx, app); e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeApplication(ctx, operator, "legacy", "operate"); !errors.Is(e, ErrForbidden) {
		t.Fatal("archived operation allowed")
	}
	if e = s.AuthorizeApplication(ctx, operator, "legacy", "view"); e != nil {
		t.Fatal("archived history unavailable")
	}
	if _, e = s.Investigation(ctx, inv.ID); e != nil {
		t.Fatal("archived history deleted")
	}
	app.Archived = false
	if _, e = s.UpdateApplication(ctx, app); e != nil {
		t.Fatal(e)
	}
	if e = s.AuthorizeApplication(ctx, operator, "legacy", "operate"); e != nil {
		t.Fatal("restore failed")
	}
	audit, e := s.AuditEntries(ctx)
	if e != nil || len(audit) < 8 {
		t.Fatalf("audit missing: %d %v", len(audit), e)
	}
}
