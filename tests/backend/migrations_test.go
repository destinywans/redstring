package backend_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/migrations"
)

func migrationNames(t *testing.T) []string {
	t.Helper()
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range entries {
		if strings.HasSuffix(f.Name(), ".sql") {
			names = append(names, f.Name())
		}
	}
	return names
}

func assertLedger(t *testing.T, s *store.Store) {
	t.Helper()
	rows, err := s.DB.Query(t.Context(), `SELECT name FROM schema_migrations ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := migrationNames(t)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatal("migration ledger does not track every complete filename exactly once")
	}
}

func TestMigrationFreshRepeatAndConcurrentStartup(t *testing.T) {
	f := readFixture(t, false)
	s, _ := isolatedStore(t, f)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Migrate(t.Context()) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertLedger(t, s)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLedger(t, s)
}

// This is a synthetic upgrade fixture, not a copy of a real old business DB.
// It executes the actual embedded files preceding the 151/152 lifecycle batch.
func TestMigrationSyntheticUpgradePreservesUnknownHistoricalEvidence(t *testing.T) {
	f := readFixture(t, false)
	s, _ := isolatedStore(t, f)
	ctx := t.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TABLE schema_migrations(name text PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, name := range migrationNames(t) {
		n, err := strconv.Atoi(name[:3])
		if err != nil {
			t.Fatal(err)
		}
		if n >= 151 {
			continue
		}
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("old fixture migration %s: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('historical-owner','history@example.invalid','History','viewer','unused');
 INSERT INTO applications(id,key,name,environment) VALUES('historical-app','historical-app','History','test');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('historical-env','historical-app','test','Test');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('historical-app','test');
 INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,status,actor_id,request_key,request_hash)
 VALUES('INC-historical','historical-app','historical-env','test','Old resolved without event','P2','resolved','historical-owner','historical-key',repeat('a',64));`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertLedger(t, s)
	var revision int64
	var unknown bool
	if err := s.DB.QueryRow(ctx, `SELECT status_revision,resolved_status_event_id IS NULL AND resolved_at IS NULL FROM v3_incident_cases WHERE id='INC-historical'`).Scan(&revision, &unknown); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || !unknown {
		t.Fatal("upgrade invented a verified closing event or lost historical data")
	}
}

func TestMigrationFailureRollsBackWholeBatch(t *testing.T) {
	f := readFixture(t, false)
	s, schema := isolatedStore(t, f)
	// A conflicting preexisting relation forces the real initial migration to
	// fail. The transaction must leave neither a partial batch nor its ledger.
	if _, err := s.DB.Exec(t.Context(), `CREATE TABLE users(collision text)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err == nil {
		t.Fatal("conflicting schema unexpectedly migrated")
	}
	var ledger, partial bool
	if err := s.DB.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL,to_regclass($2) IS NOT NULL`, schema+".schema_migrations", schema+".applications").Scan(&ledger, &partial); err != nil {
		t.Fatal(err)
	}
	if ledger || partial {
		t.Fatal("failed migration left a partial schema/ledger")
	}
	if _, err := s.DB.Exec(t.Context(), `DROP TABLE users`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertLedger(t, s)
}

func TestMigrationRealBackupRestore(t *testing.T) {
	f := readFixture(t, false)
	if f.PostgresContainer == "" || f.Owner == "" {
		t.Fatal("owned disposable PostgreSQL container identity required for backup proof")
	}
	// Verify the generated ownership label before running commands in a named
	// container. Never fall back to another local PostgreSQL instance.
	raw, err := exec.CommandContext(t.Context(), "docker", "inspect", f.PostgresContainer).Output()
	if err != nil {
		t.Fatal("test PostgreSQL container unavailable")
	}
	var inspected []struct {
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal(raw, &inspected) != nil || len(inspected) != 1 {
		t.Fatal("invalid container ownership response")
	}
	if inspected[0].Config.Labels["aiops.testlab.owner"] != f.Owner {
		t.Fatal("PostgreSQL container lacks the generated testlab ownership label")
	}
	s, schema := isolatedStore(t, f)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(t.Context(), `CREATE TABLE backup_probe(id integer PRIMARY KEY,body text NOT NULL);INSERT INTO backup_probe VALUES(1,'synthetic exact backup payload')`); err != nil {
		t.Fatal(err)
	}
	dump := exec.CommandContext(t.Context(), "docker", "exec", f.PostgresContainer, "pg_dump", "-U", "testlab", "-d", "aiops_testlab", "--format=custom", "--no-owner", "--no-privileges", "--schema="+schema)
	var stderr bytes.Buffer
	dump.Stderr = &stderr
	archive, err := dump.Output()
	if err != nil || len(archive) == 0 {
		t.Fatal("real isolated schema backup failed")
	}
	if _, err := s.DB.Exec(t.Context(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	restore := exec.CommandContext(t.Context(), "docker", "exec", "-i", f.PostgresContainer, "pg_restore", "-U", "testlab", "-d", "aiops_testlab", "--exit-on-error", "--no-owner", "--no-privileges")
	restore.Stdin = bytes.NewReader(archive)
	if err := restore.Run(); err != nil {
		t.Fatal("real isolated schema restore failed")
	}
	var body string
	if err := s.DB.QueryRow(context.Background(), `SELECT body FROM backup_probe WHERE id=1`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "synthetic exact backup payload" {
		t.Fatal("backup restore altered persisted bytes")
	}
	assertLedger(t, s)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
}
