package operations

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOperationsInvestigationIndexConcurrentMigration(t *testing.T) {
	if os.Getenv("AIOPS_TESTLAB_INDEX_REGRESSION_ENABLED") != "dedicated-temp-container" {
		t.Skip("explicit disposable testlab index regression opt-in required")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Port != 15461 || cfg.ConnConfig.Database != "aiops_testlab" {
		t.Fatal("dedicated loopback migration database required")
	}
	s := isolatedAutomation(t)
	ctx := t.Context()
	var schema string
	if err = s.DB.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "test_auto_") {
		t.Fatal("isolated schema required")
	}
	var fresh bool
	if err = s.DB.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, pgx.Identifier{schema, "ops_incidents_investigation_scope"}.Sanitize()).Scan(&fresh); err != nil || !fresh {
		t.Fatalf("fresh migration lacks investigation association index: %v", err)
	}
	if _, err = s.DB.Exec(ctx, "DROP INDEX IF EXISTS "+pgx.Identifier{schema, "ops_incidents_investigation_scope"}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, `INSERT INTO ops_incidents(id,source_id,fingerprint,title,severity,alert_status,starts_at) VALUES('index-preserved','synthetic','index-preserved','Preserved','warning','firing',now())`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.Migrate(ctx) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var definition string
	err = s.DB.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname=$1 AND tablename='ops_incidents' AND indexname='ops_incidents_investigation_scope'`, schema).Scan(&definition)
	if err != nil {
		t.Fatal("migration did not install investigation association index:", err)
	}
	if !strings.Contains(definition, "(investigation_id, application_id, environment_id) INCLUDE (id)") {
		t.Fatalf("unexpected index: %s", definition)
	}
	var valid, ready bool
	if err = s.DB.QueryRow(ctx, `SELECT i.indisvalid,i.indisready FROM pg_index i WHERE i.indexrelid=$1::regclass`, pgx.Identifier{schema, "ops_incidents_investigation_scope"}.Sanitize()).Scan(&valid, &ready); err != nil || !valid || !ready {
		t.Fatalf("index not valid/ready: %v", err)
	}
	var count int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM ops_incidents WHERE id='index-preserved' AND fingerprint='index-preserved'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old data changed: count=%d err=%v", count, err)
	}
}
