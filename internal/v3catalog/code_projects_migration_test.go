package v3catalog

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// moveCodeBindings runs the data part of migration 174 again: the migration
// itself ran before these rows existed.
func moveCodeBindings(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	b, err := os.ReadFile("../../migrations/174_v3_service_code_projects.sql")
	if err != nil {
		t.Fatal(err)
	}
	statement := string(b)
	start := strings.Index(statement, "DO $$")
	if start < 0 {
		t.Fatal("migration 174 has no data block")
	}
	if _, err = db.Exec(context.Background(), statement[start:]); err != nil {
		t.Fatal(err)
	}
}

// D20: on a database bound to a V3 instance, migration 174 moves a stored
// GitLab code binding that names an enrolled project target into
// v3_service_code_projects, drops every code binding from the stored
// bindings and audits each one; a legacy database keeps its bindings.
func TestMigration174MovesCodeBindings(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_code_projects_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, core.User{Email: "editor@example.invalid", Name: "Editor", Role: "viewer"}, "isolated-editor-password")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// matched: a GitLab binding with its enrolled target, next to a metrics
	// binding. unmatched: a GitLab project no target names, and a GitHub one.
	exec(`INSERT INTO applications(id,key,name) VALUES('matched','matched','Matched'),('unmatched','unmatched','Unmatched')`)
	exec(`INSERT INTO application_environments(id,application_id,key,name,bindings,binding_revision) VALUES
 ('matched-dev','matched','dev','Dev','[{"kind":"prometheus","connector_id":"prometheus","resource":"job=api"},{"kind":"GitLab","connector_id":"gitlab-1","resource":"42"}]',3),
 ('unmatched-dev','unmatched','dev','Dev','[{"kind":"gitlab","connector_id":"gitlab-1","resource":"77"},{"kind":"github","connector_id":"gh","resource":"org/repo"}]',1)`)
	exec(`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('matched','dev'),('unmatched','dev')`)
	exec(`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools)
 VALUES('gitlab-1','dev','gitlab','GitLab','delivery','https://gitlab.example.invalid','pat','vault://kv/gitlab#token',true,'[]')`)
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-matched','matched','dev','gitlab-1','project','42',$1)`, user.ID)

	// Not bound to a V3 instance: nothing moves.
	moveCodeBindings(t, db)
	var rows int
	var stored string
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM v3_service_code_projects),
 (SELECT bindings::text FROM application_environments WHERE id='matched-dev')`).Scan(&rows, &stored); err != nil ||
		rows != 0 || !strings.Contains(stored, `"42"`) {
		t.Fatalf("legacy database changed: rows=%d bindings=%s err=%v", rows, stored, err)
	}

	exec(`INSERT INTO v3_instance_boundary(tier,domain) VALUES('nonprod','test')`)
	moveCodeBindings(t, db)
	var target, source string
	var confirmedBy *string
	if err := db.QueryRow(ctx, `SELECT target_id,source,confirmed_by FROM v3_service_code_projects
 WHERE application_id='matched' AND environment_id='dev'`).Scan(&target, &source, &confirmedBy); err != nil ||
		target != "t-matched" || source != "migrated_binding" || confirmedBy != nil {
		t.Fatalf("moved code project: target=%q source=%q by=%v err=%v", target, source, confirmedBy, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_service_code_projects`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("code projects: %d %v", rows, err)
	}
	for id, want := range map[string]string{
		"matched-dev":   `[{"kind": "prometheus", "resource": "job=api", "connector_id": "prometheus"}]`,
		"unmatched-dev": `[]`,
	} {
		var revision int64
		if err := db.QueryRow(ctx, `SELECT bindings::text,binding_revision FROM application_environments WHERE id=$1`, id).Scan(&stored, &revision); err != nil ||
			stored != want {
			t.Fatalf("%s stored bindings: %s %v", id, stored, err)
		}
	}
	audits := map[string]int{}
	auditRows, err := db.Query(ctx, `SELECT action FROM management_audit WHERE action LIKE 'v3.service_code_project.%' AND actor_kind='system'`)
	if err != nil {
		t.Fatal(err)
	}
	for auditRows.Next() {
		var action string
		if err := auditRows.Scan(&action); err != nil {
			t.Fatal(err)
		}
		audits[action]++
	}
	auditRows.Close()
	if audits["v3.service_code_project.migrated"] != 1 || audits["v3.service_code_project.not_migrated"] != 2 {
		t.Fatalf("migration audits: %v", audits)
	}
	// The target's removal removes the code project, and both raise the
	// mapping revision.
	var before, after int64
	if err := db.QueryRow(ctx, `SELECT revision FROM v3_service_target_revisions WHERE application_id='matched' AND environment_id='dev'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM v3_connector_targets WHERE id='t-matched'`)
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM v3_service_code_projects),
 (SELECT revision FROM v3_service_target_revisions WHERE application_id='matched' AND environment_id='dev')`).Scan(&rows, &after); err != nil ||
		rows != 0 || after <= before {
		t.Fatalf("target removal: rows=%d revision %d -> %d err=%v", rows, before, after, err)
	}
	// A code project must point at a project target of the same service.
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-other','unmatched','dev','gitlab-1','project','78',$1)`, user.ID)
	if _, err := db.Exec(ctx, `INSERT INTO v3_service_code_projects(application_id,environment_id,target_id,source)
 VALUES('matched','dev','t-other','migrated_binding')`); err == nil {
		t.Fatal("a code project took another service's target")
	}
}
