package v3knowledgesources

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

func isolatedSourcesDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates an isolated random schema")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_sources_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
CREATE TABLE applications(id text PRIMARY KEY,owner_team_id text,archived boolean NOT NULL DEFAULT false);
CREATE TABLE v3_application_environments(application_id text NOT NULL,environment_id text NOT NULL);
CREATE TABLE application_environments(application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false);
CREATE TABLE v3_effective_role_grants(user_id text NOT NULL,module text NOT NULL,action text NOT NULL,scope_kind text NOT NULL,scope_id text NOT NULL);
CREATE TABLE knowledge_documents(id text PRIMARY KEY,application_id text,title text NOT NULL,metadata jsonb NOT NULL,lifecycle text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
INSERT INTO applications(id,owner_team_id) VALUES('app','team');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','test');
INSERT INTO application_environments(application_id,key) VALUES('app','test');
INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES
('viewer','environment','view','environment','test'),
('viewer','knowledge','view','application','app');
INSERT INTO knowledge_documents(id,application_id,title,metadata,lifecycle) VALUES
('visible','app','Visible','{"visibility":"application","environment_ids":["test"]}','published'),
('team','app','Team','{"visibility":"application","environment_ids":["test"],"team_id":"team"}','published'),
('wrong-team','app','Wrong team','{"visibility":"application","environment_ids":["test"],"team_id":"other"}','published'),
('empty-team','app','Empty team','{"visibility":"application","environment_ids":["test"],"team_id":""}','published'),
('malformed-team','app','Malformed team','{"visibility":"application","environment_ids":["test"],"team_id":123}','published'),
('unknown-env','app','Unknown environment','{"visibility":"application"}','published'),
('malformed-env','app','Malformed environment','{"visibility":"application","environment_ids":["test",null]}','published'),
('unverified-source','app','Unverified source','{"visibility":"application","environment_ids":["test"],"source_registration_id":"source"}','published'),
('workspace',NULL,'Workspace','{"visibility":"workspace","environment_ids":["test"]}','published'),
('app-less-private',NULL,'Private','{"visibility":"application","environment_ids":["test"]}','published'),
('app-less-team',NULL,'Team without app','{"visibility":"workspace","environment_ids":["test"],"team_id":"team"}','published');
`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func documentIDs(docs []document) map[string]bool {
	ids := make(map[string]bool, len(docs))
	for _, doc := range docs {
		ids[doc.ID] = true
	}
	return ids
}

func TestPGObservedDocumentsCurrentScopeBeforeLimit(t *testing.T) {
	db := isolatedSourcesDB(t)
	ctx := context.Background()
	// Earlier hidden rows must not consume the bounded candidate result.
	if _, err := db.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,metadata,lifecycle,created_at)
SELECT 'hidden-'||n,'app','Hidden', '{"visibility":"application","environment_ids":["test"],"team_id":"other"}'::jsonb,'published',now()+interval '1 day'
FROM generate_series(1,2100) n`); err != nil {
		t.Fatal(err)
	}
	repo := pgRepository{db}
	initial, err := repo.Documents(ctx, "viewer", []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial) != 1 || initial[0].ID != "visible" {
		t.Fatalf("unexpected initial candidates: %#v", initial)
	}
	if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET lifecycle='draft' WHERE id='visible'`); err != nil {
		t.Fatal(err)
	}
	if current, err := repo.CurrentDocuments(ctx, "viewer", []string{"test"}, []string{"visible"}); err != nil || len(current) != 0 {
		t.Fatalf("unpublished candidate survived final check: %#v, %v", current, err)
	}
	if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET lifecycle='published' WHERE id='visible'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET metadata='{"visibility":"application","environment_ids":["prod"]}' WHERE id='visible'`); err != nil {
		t.Fatal(err)
	}
	if current, err := repo.CurrentDocuments(ctx, "viewer", []string{"test"}, []string{"visible"}); err != nil || len(current) != 0 {
		t.Fatalf("moved candidate survived final check: %#v, %v", current, err)
	}
	if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET metadata='{"visibility":"application","environment_ids":["test"]}' WHERE id='visible'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE module='knowledge'`); err != nil {
		t.Fatal(err)
	}
	if current, err := repo.CurrentDocuments(ctx, "viewer", []string{"test"}, []string{"visible"}); err != nil || len(current) != 0 {
		t.Fatalf("revoked candidate survived final check: %#v, %v", current, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id)
VALUES('viewer','knowledge','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	check := func(want map[string]bool) {
		t.Helper()
		docs, err := repo.Documents(ctx, "viewer", []string{"test"})
		if err != nil {
			t.Fatal(err)
		}
		got := documentIDs(docs)
		if len(got) != len(want) {
			t.Fatalf("visible IDs = %v, want %v", got, want)
		}
		for id := range want {
			if !got[id] {
				t.Fatalf("visible IDs = %v, want %v", got, want)
			}
		}
	}
	check(map[string]bool{"visible": true})
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES
('viewer','knowledge','view','team','team')`); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{"visible": true, "team": true})
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='other' WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{"visible": true})
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='team' WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES
('viewer','knowledge','view','environment','test')`); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{"visible": true, "team": true, "workspace": true})
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE module='knowledge' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	// The environment grant is intentionally broad, as in the shared V3
	// predicate. It authorizes a team-scoped document in this environment.
	check(map[string]bool{"visible": true, "team": true, "workspace": true})
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE module='knowledge' AND scope_kind='environment'`); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{"visible": true})
	if _, err := db.Exec(ctx, `UPDATE application_environments SET archived=true WHERE application_id='app'`); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{})
	if _, err := db.Exec(ctx, `UPDATE application_environments SET archived=false WHERE application_id='app'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='app'`); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{})
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=false WHERE id='app';
DELETE FROM v3_application_environments WHERE application_id='app'`); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{})
}
