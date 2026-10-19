package v3conversation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This regression executes the production COUNT, including its authorization
// UNION and correlated incident lookup. Milliseconds are diagnostic evidence,
// not a portable acceptance threshold.
func TestConversationListInvestigationPlan(t *testing.T) {
	if os.Getenv("AIOPS_TESTLAB_INDEX_REGRESSION_ENABLED") != "dedicated-temp-container" {
		t.Skip("explicit disposable testlab index regression opt-in required")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Port != 15461 || cfg.ConnConfig.Database != "aiops_testlab" {
		t.Fatal("dedicated loopback migration database required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := "wan161_list_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, e := admin.Exec(c, "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ops := operations.New(db, st, nil, nil, operations.Config{})
	if err = ops.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO users(id,email,name,role,password_hash) VALUES('plan-owner','plan@example.invalid','Plan Owner','viewer','unused')`,
		`INSERT INTO rbac_roles(id,name) VALUES('plan-reader','Plan Reader')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES('plan-owner','plan-reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('plan-reader','canvas','view','environment','test')`,
		`INSERT INTO teams(id,name) VALUES('plan-team','Plan Team')`,
		`INSERT INTO team_members(team_id,user_id) VALUES('plan-team','plan-owner')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES('plan-app','Plan App','plan-app','plan-team'),('other-app','Other App','other-app','plan-team')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('plan-env','plan-app','test','Test'),('other-env','plan-app','staging','Staging')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('plan-app','test'),('plan-app','staging')`,
		`INSERT INTO applications(id,name,key) SELECT 'load-app-'||n,'Synthetic service '||n,'load-app-'||n FROM generate_series(1,1000) n`,
		`INSERT INTO application_environments(id,application_id,key,name) SELECT 'load-env-'||n,'load-app-'||n,'test','Test' FROM generate_series(1,1000) n`,
		`INSERT INTO v3_application_environments(application_id,environment_id) SELECT 'load-app-'||n,'test' FROM generate_series(1,1000) n`,
		`INSERT INTO investigations(id,application_id,environment_id,title,status,created_by) SELECT 'INV-plan-'||n,'plan-app','plan-env','Plan investigation '||n,'open','plan-owner' FROM generate_series(1,16) n`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at) SELECT 'INC-unlinked-'||n,'plan-source','load-app-'||((n-1)%1000+1),'load-env-'||((n-1)%1000+1),'fp-'||n,repeat('synthetic ',80),'warning','firing',now() FROM generate_series(1,100000) n`,
		`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,investigation_id,starts_at) VALUES
 ('INC-one','plan-source','plan-app','plan-env','one','One','warning','firing','INV-plan-2',now()),
 ('INC-two-a','plan-source','plan-app','plan-env','two-a','Two','warning','firing','INV-plan-3',now()),
 ('INC-two-b','plan-source','plan-app','plan-env','two-b','Two','warning','firing','INV-plan-3',now()),
 ('INC-foreign-app','plan-source','other-app','plan-env','foreign-app','Foreign','warning','firing','INV-plan-4',now()),
 ('INC-foreign-env','plan-source','plan-app','other-env','foreign-env','Foreign','warning','firing','INV-plan-5',now())`,
		`ANALYZE ops_incidents`,
		`ANALYZE applications`,
		`ANALYZE application_environments`,
		`ANALYZE v3_application_environments`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate an existing pre-fix Operations table in this isolated schema only.
	if _, err = db.Exec(ctx, "DROP INDEX IF EXISTS "+quoted+".ops_incidents_investigation_scope"); err != nil {
		t.Fatal(err)
	}
	countSQL := filteredOwnedConversationSources + ` SELECT
 (SELECT count(*) FROM owned WHERE is_mine AND status IS DISTINCT FROM 'archived'),
 (SELECT count(*) FROM owned WHERE is_team AND status IS DISTINCT FROM 'archived'),
 (SELECT count(*) FROM owned WHERE is_triage AND status IS DISTINCT FROM 'archived'),
 (SELECT count(*) FROM owned WHERE is_mine AND status='archived'),
 (SELECT count(*) FROM filtered)`
	args := []any{"plan-owner", []string{"test"}, "mine", "", "", "", []string{}}
	explain := func(label string) map[string]any {
		t.Helper()
		tx, e := db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if _, e = tx.Exec(ctx, `SET LOCAL statement_timeout='20s'`); e != nil {
			t.Fatal(e)
		}
		var jit string
		var threshold float64
		if e = tx.QueryRow(ctx, `SELECT current_setting('jit'),current_setting('jit_above_cost')::float8`).Scan(&jit, &threshold); e != nil {
			t.Fatal(e)
		}
		if jit != "on" || threshold != 100000 {
			t.Fatal("regression requires unchanged PostgreSQL default JIT settings")
		}
		var raw []byte
		if e = tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,TIMING OFF,FORMAT JSON) "+countSQL, args...).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var plans []map[string]any
		if e = json.Unmarshal(raw, &plans); e != nil || len(plans) != 1 {
			t.Fatal("invalid plan")
		}
		dir := os.Getenv("AIOPS_TESTLAB_PLAN_EVIDENCE")
		if dir != "" {
			clean := filepath.Clean(dir)
			if !filepath.IsAbs(clean) || filepath.Base(filepath.Dir(clean)) != "testlab" {
				t.Fatal("private testlab evidence directory required")
			}
			if e = os.MkdirAll(clean, 0700); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(clean, schema+"-"+label+".json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
		}
		t.Logf("%s cost=%v execution_ms=%v jit=%v", label, plans[0]["Plan"].(map[string]any)["Total Cost"], plans[0]["Execution Time"], plans[0]["JIT"] != nil)
		return plans[0]
	}
	before := explain("before")
	if err = ops.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	after := explain("after")
	var oldLoops, newScans, indexLoops float64
	var walk func(any, bool)
	walk = func(v any, old bool) {
		switch x := v.(type) {
		case map[string]any:
			if !old && x["Index Name"] == "ops_incidents_investigation_scope" {
				loops, _ := x["Actual Loops"].(float64)
				indexLoops += loops
			}
			if x["Relation Name"] == "ops_incidents" {
				loops, _ := x["Actual Loops"].(float64)
				if x["Node Type"] == "Seq Scan" {
					if old {
						oldLoops += loops
					} else {
						newScans += loops
					}
				}
			}
			for _, child := range x {
				walk(child, old)
			}
		case []any:
			for _, child := range x {
				walk(child, old)
			}
		}
	}
	walk(before, true)
	walk(after, false)
	if oldLoops != 16 {
		t.Fatalf("pre-index plan did not exercise 16 correlated full scans: loops=%v", oldLoops)
	}
	if newScans != 0 || indexLoops != 16 {
		t.Fatalf("migration failed to remove repeated incident scans: seq_loops=%v index_loops=%v", newScans, indexLoops)
	}
	beforeCost := before["Plan"].(map[string]any)["Total Cost"].(float64)
	afterCost := after["Plan"].(map[string]any)["Total Cost"].(float64)
	if beforeCost <= 100000 || afterCost >= 100000 || after["JIT"] != nil {
		t.Fatalf("default JIT trigger not removed: before=%v after=%v jit=%v", beforeCost, afterCost, after["JIT"] != nil)
	}
	var mine, team, triage, archived, total int
	if err = db.QueryRow(ctx, countSQL, args...).Scan(&mine, &team, &triage, &archived, &total); err != nil {
		t.Fatal(err)
	}
	if mine != 16 || team != 16 || triage != 0 || archived != 0 || total != 16 {
		t.Fatalf("counts changed: %d/%d/%d/%d/%d", mine, team, triage, archived, total)
	}
	rows, err := db.Query(ctx, filteredOwnedConversationSources+` SELECT id,incident_id FROM filtered ORDER BY id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var incident *string
		if err = rows.Scan(&id, &incident); err != nil {
			t.Fatal(err)
		}
		if id == "INV-plan-2" {
			if incident == nil || *incident != "INC-one" {
				t.Fatal("single exact-scope association missing")
			}
		} else if incident != nil {
			t.Fatalf("ambiguous or cross-scope incident leaked for %s", id)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
