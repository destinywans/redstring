package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3alertrules"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func enableWorkerEntryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "enable_entry_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.MaxConns = 4
	db, err = pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Readiness must work against the same complete migration set as main,
	// rather than a hand-made schema that repeats the mistaken table name.
	if err := (&store.Store{DB: db}).Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

type enableWorkerEntrySource struct{ t *testing.T }

func (s enableWorkerEntrySource) List(context.Context, string) ([]v3alertrules.ObservedRule, error) {
	s.t.Error("local readiness or an empty worker queue must not query a provider")
	return nil, errors.New("unexpected provider call")
}

func enableWorkerEntry(t *testing.T, db *pgxpool.Pool) *v3AlertRuleEnableWorker {
	t.Helper()
	return newV3AlertRuleEnableWorker(&v3alertrules.WriteService{
		DB: db, Access: &rbac.V3Policy{Store: &store.Store{DB: db}},
		Reader:         &v3alertrules.Service{Source: enableWorkerEntrySource{t}},
		EnableApproval: &v3alertrules.AlertRuleApprovalAuthority{},
		// With no queued actions the concrete applier is never invoked. No
		// GitLab, Argo or Prometheus endpoint is configured by this fixture.
		EnableApplier: &v3alertrules.GitOpsAlertRuleApplier{},
	}, map[string]bool{"test": true, "disabled": false})
}

func TestV3AlertRuleEnableWorkerReadyUsesMigratedQueue(t *testing.T) {
	db := enableWorkerEntryDB(t)
	w := enableWorkerEntry(t, db)
	if err := w.Ready(t.Context(), "test"); err == nil {
		t.Fatal("a stopped worker must not accept new enable requests")
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("empty migrated queue worker exit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("worker did not stop after cancellation")
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for !w.running.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := w.Ready(t.Context(), "test"); err != nil {
		t.Fatalf("running worker on complete production migrations must be ready: %v", err)
	}
	for _, env := range []string{"", "all", "disabled", "other"} {
		if err := w.Ready(t.Context(), env); err == nil {
			t.Errorf("unconfigured environment %q accepted", env)
		}
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if err := w.Ready(canceled, "test"); err == nil {
		t.Fatal("canceled readiness probe must not report ready")
	}
}

func TestV3AlertRuleEnableWorkerReadyRequiresQueueTables(t *testing.T) {
	db := enableWorkerEntryDB(t)
	w := enableWorkerEntry(t, db)
	// Probe a running-state snapshot directly, without executing a broken
	// queue. Each missing table still has to fail closed, not return true.
	w.running.Store(true)
	for _, table := range []string{
		"v3_proposals", "v3_proposal_actions", "v3_proposal_approval_decisions",
		"v3_proposal_approval_revocations", "v3_alert_rule_enable_executions",
	} {
		t.Run(table, func(t *testing.T) {
			if err := w.Ready(t.Context(), "test"); err != nil {
				t.Fatalf("complete migrated queue: %v", err)
			}
			original := pgx.Identifier{table}.Sanitize()
			missing := pgx.Identifier{table + "_unavailable"}.Sanitize()
			if _, err := db.Exec(t.Context(), "ALTER TABLE "+original+" RENAME TO "+missing); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec(context.Background(), "ALTER TABLE "+missing+" RENAME TO "+original); err != nil {
					t.Error(err)
				}
			})
			err := w.Ready(t.Context(), "test")
			var unavailable *pgconn.PgError
			if !errors.As(err, &unavailable) || unavailable.Code != "42P01" {
				t.Fatalf("missing %s must reject with undefined-table root cause: %v", table, err)
			}
		})
	}
}
