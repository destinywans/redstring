package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectorsCommandRejectsAmbiguousArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"import"},
		{"import-legacy", "-apply"},
		{"import-legacy", "-apply", "-plan", "p.json"},
		{"import-legacy", "-plan", "p.json"},
		{"import-legacy", "-actor", "ops"},
		{"import-legacy", "-apply", "-plan", "p.json", "-actor", "ops", "-out", "x.json"},
		{"compare", "-apply"},
		{"compare", "extra"},
	} {
		var stderr bytes.Buffer
		if code := runConnectorsCommand(context.Background(), args, &bytes.Buffer{}, &stderr); code != 2 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
	t.Setenv("AIOPS_V3_SOURCE_CATALOG", "/a.json")
	t.Setenv("AIOPS_V3_PROMETHEUS_QUERIES_CONFIG", "/b.json")
	if _, err := loadSourceCatalog(); err == nil {
		t.Fatal("two catalog files accepted")
	}
}

// The operator flow end to end on an isolated schema: dry run, compare,
// apply, install the generated catalog, compare again.
func TestConnectorsCommandImportFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := context.Background()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connectors_command_" + store.ID()
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
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	scoped := dsn + separator + "search_path=" + url.QueryEscape(schema+",public")
	if db, err = pgxpool.New(ctx, scoped); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES('app-checkout','Checkout','checkout','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('app-checkout-dev','app-checkout','dev','Dev','[{"kind":"loki","connector_id":"loki","resource":"lab-checkout"}]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-checkout','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("DATABASE_URL", scoped)
	t.Setenv("AIOPS_V3_INSTANCE_CONFIG", write("instance.json", `{"tier":"nonprod","name":"Lab","domain":"lab.example.invalid","default_env":"dev",
 "environments":[{"id":"dev","label":"Dev","kind":"standing","tone":"dev","clusters":[]}]}`))
	t.Setenv("AIOPS_TELEMETRY_CONFIG", write("telemetry.json", `{"loki":{"url":"http://127.0.0.1:13100","allow_http":true},
 "applications":{"lab-checkout":{"loki_labels":{"app":"checkout"}}}}`))
	t.Setenv("AIOPS_V3_SOURCE_CATALOG", "")
	t.Setenv("AIOPS_V3_PROMETHEUS_QUERIES_CONFIG", "")
	t.Setenv("AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS", "")
	run := func(args ...string) (int, string) {
		var stdout, stderr bytes.Buffer
		code := runConnectorsCommand(ctx, args, &stdout, &stderr)
		return code, stdout.String() + stderr.String()
	}

	planPath := filepath.Join(dir, "plan.json")
	if code, out := run("import-legacy", "-out", planPath); code != 0 {
		t.Fatalf("dry run: %d %s", code, out)
	}
	info, err := os.Stat(planPath)
	raw, _ := os.ReadFile(planPath)
	var plan v3telemetry.Plan
	if err != nil || info.Mode().Perm() != 0o600 || json.Unmarshal(raw, &plan) != nil ||
		plan.Summary.ConnectorsCreated != 1 || plan.Summary.TargetsCreated != 1 || len(plan.Catalog.Scopes) != 1 {
		t.Fatalf("plan file: %v %v %+v", err, info, plan.Summary)
	}
	var connectors int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_connectors`).Scan(&connectors); err != nil || connectors != 0 {
		t.Fatalf("dry run wrote: %d %v", connectors, err)
	}
	if code, out := run("compare"); code != 3 || !strings.Contains(out, `"state": "missing"`) {
		t.Fatalf("compare before import: %d %s", code, out)
	}
	if code, out := run("import-legacy", "-apply", "-plan", planPath, "-actor", "admin@example.invalid"); code != 0 ||
		!strings.Contains(out, `"targets_created": 1`) {
		t.Fatalf("apply: %d %s", code, out)
	}
	// Without the generated catalog the registry has no reviewed scope.
	if code, out := run("compare"); code != 3 || !strings.Contains(out, "scope_missing") {
		t.Fatalf("compare without catalog: %d %s", code, out)
	}
	catalog, _ := json.Marshal(plan.Catalog)
	t.Setenv("AIOPS_V3_SOURCE_CATALOG", write("catalog.json", string(catalog)))
	if code, out := run("compare"); code != 0 || !strings.Contains(out, `"state": "match_not_ready"`) {
		t.Fatalf("compare with catalog: %d %s", code, out)
	}
	if code, _ := run("import-legacy", "-apply", "-plan", planPath, "-actor", "admin@example.invalid"); code != 1 {
		t.Fatal("a stale plan was applied twice")
	}
}

func TestAlertReconcileInterval(t *testing.T) {
	for value, want := range map[string]time.Duration{"": time.Minute, "0": 0, "15": 15 * time.Second, "3600": time.Hour} {
		if got, err := alertReconcileInterval(value); err != nil || got != want {
			t.Fatalf("%q: %v %v", value, got, err)
		}
	}
	for _, value := range []string{"5", "3601", "-1", "1m"} {
		if _, err := alertReconcileInterval(value); err == nil {
			t.Fatalf("%q accepted", value)
		}
	}
}
