package v3alertrules

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func workerWithConnectFailure(t *testing.T, failure error) (*WriteService, *atomic.Int32) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://localhost/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		calls.Add(1)
		return failure
	}
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return &WriteService{DB: db, Access: &recoveryAccess{}, Reader: &Service{Source: fakeSource{}},
		EnableApproval: &recoveryApproval{}, EnableApplier: &recoveryApplier{}}, calls
}

// A queue read interrupted by an upstream connection must not permanently
// terminate the independent worker. Cancellation must interrupt its backoff.
func TestEnableWorkerRetriesTransientQueueReadAndCancelsBackoff(t *testing.T) {
	s, calls := workerWithConnectFailure(t, io.ErrUnexpectedEOF)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.RunEnableWorker(ctx) }()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for calls.Load() < 2 {
		select {
		case err := <-done:
			t.Fatalf("worker exited on transient queue read: %v", err)
		case <-deadline.C:
			t.Fatal("worker never retried a transient queue read")
		case <-tick.C:
		}
	}
	if calls.Load() > 3 {
		t.Fatalf("retry storm: %d attempts", calls.Load())
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker backoff did not observe cancellation")
	}
}

func TestEnableWorkerDoesNotRetryBrokenSchema(t *testing.T) {
	s, calls := workerWithConnectFailure(t, &pgconn.PgError{Code: "42P01", Message: "test missing queue relation"})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := s.RunEnableWorker(ctx); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("broken schema must fail promptly: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("broken schema retried %d times", calls.Load())
	}
}

func TestEnableWorkerRecoversConnectionAndProcessesOriginalQueue(t *testing.T) {
	db := enableRecoveryDB(t)
	f := seedEnableRecovery(t, db, true, false, "pending_review")
	// Exact GitOps sync with an as-yet absent Prometheus rule must commit
	// awaiting_provider, proving the recovered pass completed persistence.
	mergeRevision := strings.Repeat("f", 40)
	f.applier.observation = AlertRuleDeploymentObservation{State: "synced", MergeRevision: mergeRevision,
		SyncedRevision: mergeRevision, ContentDigest: f.intent.Deployment.ContentDigest}
	cfg := db.Config()
	var connections atomic.Int32
	cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
		if connections.Add(1) == 1 {
			return io.ErrUnexpectedEOF
		}
		return nil
	}
	reconnected, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reconnected.Close)
	f.s.DB = reconnected
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.s.RunEnableWorker(ctx) }()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	committed := false
	for !committed {
		select {
		case err := <-done:
			t.Fatalf("queue worker exited before recovery: %v", err)
		case <-timer.C:
			t.Fatal("recovered queue did not process the original proposal")
		case <-tick.C:
			if f.applier.observeCalls.Load() > 0 {
				if err := db.QueryRow(t.Context(), `SELECT state='awaiting_provider' AND updated_at>clock_timestamp()-interval '10 seconds' FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, f.id).Scan(&committed); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker stop: %v", err)
	}
	if connections.Load() < 2 {
		t.Fatal("test never reconnected")
	}
	if f.applier.prepareCalls.Load() != 0 || f.applier.planCalls.Load() != 0 {
		t.Fatal("read-only recovery issued a remote write")
	}
}
