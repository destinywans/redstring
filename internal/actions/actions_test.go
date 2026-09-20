package actions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/store"
)

type fakeConnector struct {
	executed, reconciled, observed atomic.Int32
	plan                           connectors.RollbackPlan
	observedWindow                 atomic.Int64
	executeErr                     error
}

func (f *fakeConnector) ProposeRollback(context.Context, string) (connectors.RollbackPlan, error) {
	return f.plan, nil
}
func (f *fakeConnector) Execute(_ context.Context, _ connectors.RollbackPlan, op string) (connectors.ExecutionResult, error) {
	f.executed.Add(1)
	return connectors.ExecutionResult{State: "applied", OperationID: op, Generation: 2}, f.executeErr
}
func (f *fakeConnector) Reconcile(_ context.Context, _ connectors.RollbackPlan, op string) (connectors.ExecutionResult, error) {
	f.reconciled.Add(1)
	return connectors.ExecutionResult{State: "applied", OperationID: op, Generation: 2}, nil
}
func (f *fakeConnector) ObservePlan(_ context.Context, _ connectors.RollbackPlan, window time.Duration) (connectors.RecoveryResult, error) {
	f.observed.Add(1)
	f.observedWindow.Store(int64(window))
	return connectors.RecoveryResult{Recovered: true, Samples: 4, Requests: 25}, nil
}
func (f *fakeConnector) Snapshot(context.Context) (connectors.Snapshot, error) {
	var d connectors.Deployment
	d.Metadata.UID = f.plan.UID
	d.Metadata.Generation = 2
	d.Spec.Replicas = 1
	d.Status.ObservedGeneration = 2
	d.Status.Replicas = 1
	d.Status.AvailableReplicas = 1
	d.Status.UpdatedReplicas = 1
	return connectors.Snapshot{Deployment: d, TemplateDigest: f.plan.TargetTemplateDigest}, nil
}

func isolatedActions(t *testing.T, maxConns int32) (*Service, *fakeConnector, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated random schema")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("cannot configure test database")
	}
	schema := "test_actions_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error("test schema cleanup failed")
		}
	})
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal("cannot configure test pool")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = maxConns
	pool, e = pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	st := &store.Store{DB: pool}
	if e = st.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES('u','test@example.invalid','Test','operator','unused'); INSERT INTO applications(id,key,name) VALUES('app','app','Test'); INSERT INTO application_environments(id,application_id,key,name) VALUES('app-default','app','test','Test')"); e != nil {
		t.Fatal(e)
	}
	inv, e := st.CreateInvestigation(ctx, "app", "Test rollback")
	if e != nil {
		t.Fatal(e)
	}
	f := &fakeConnector{plan: connectors.RollbackPlan{Namespace: "test", Deployment: "demo", UID: "uid-test", ExpectedTemplateDigest: "old", ExpectedSpecDigest: "old-spec", TargetTemplate: json.RawMessage(`{"spec":{"containers":[{"name":"app","image":"test:v1"}]}}`), TargetTemplateDigest: "new-template", TargetRevision: "1", ObservationSeconds: 180, Digest: "approved-exact-digest"}}
	s := New(pool, f, "app")
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return s, f, inv.ID
}
func makePlan(t *testing.T, s *Service, inv string) Plan {
	t.Helper()
	p, e := s.Create(context.Background(), inv, "1", "u")
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func approveAndQueue(t *testing.T, s *Service, p Plan) Execution {
	t.Helper()
	if _, e := s.Approve(context.Background(), p.ID, p.Proposal.Digest, "u"); e != nil {
		t.Fatal(e)
	}
	x, e := s.Enqueue(context.Background(), p.ID, "u")
	if e != nil {
		t.Fatal(e)
	}
	return x
}
func countEvents(t *testing.T, s *Service, inv, kind string) int {
	t.Helper()
	var n int
	if e := s.Repository.(*PostgresRepository).DB.QueryRow(context.Background(), "SELECT count(*) FROM events WHERE investigation_id=$1 AND type=$2", inv, kind).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestPostgresActionsApprovalDigestAndConcurrentIdempotency(t *testing.T) {
	s, f, inv := isolatedActions(t, 5)
	p := makePlan(t, s, inv)
	ctx := context.Background()
	if _, e := s.Enqueue(ctx, p.ID, "u"); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("unapproved enqueue accepted: %v", e)
	}
	if _, e := s.Approve(ctx, p.ID, "different-digest", "u"); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("wrong digest accepted: %v", e)
	}
	if _, e := s.claim(ctx); !errors.Is(e, ErrNoWork) {
		t.Fatalf("unapproved execution claimable: %v", e)
	}
	if f.executed.Load() != 0 || f.observed.Load() != 0 {
		t.Fatal("action reached connector before approval")
	}
	const callers = 8
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.Approve(ctx, p.ID, p.Proposal.Digest, "u"); e != nil {
				errs <- e
				return
			}
			x, e := s.Enqueue(ctx, p.ID, "u")
			if e != nil {
				errs <- e
				return
			}
			ids <- x.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("multiple executions created")
		}
	}
	if first == "" {
		t.Fatal("no execution")
	}
	if n := countEvents(t, s, inv, "action.approved"); n != 1 {
		t.Fatalf("approval audit count %d", n)
	}
	if n := countEvents(t, s, inv, "execution.queued"); n != 1 {
		t.Fatalf("enqueue audit count %d", n)
	}
	c, e := s.claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	s.perform(ctx, c)
	x, e := s.Execution(ctx, first)
	if e != nil || x.Status != "succeeded" {
		t.Fatalf("execution %+v %v", x, e)
	}
	if f.executed.Load() != 1 || f.reconciled.Load() != 0 || f.observed.Load() != 1 || time.Duration(f.observedWindow.Load()) != 3*time.Minute {
		t.Fatalf("unexpected connector counts execute=%d reconcile=%d observe=%d window=%s", f.executed.Load(), f.reconciled.Load(), f.observed.Load(), time.Duration(f.observedWindow.Load()))
	}
	if _, e = s.claim(ctx); !errors.Is(e, ErrNoWork) {
		t.Fatal("completed execution claimable again")
	}
	if countEvents(t, s, inv, "execution.succeeded") != 1 {
		t.Fatal("success audit missing")
	}
}
func TestPostgresActionsExpiredLeaseReconcilesAndFencesOldWorker(t *testing.T) {
	s, f, inv := isolatedActions(t, 3)
	ctx := context.Background()
	p := makePlan(t, s, inv)
	x := approveAndQueue(t, s, p)
	old, e := s.claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Repository.(*PostgresRepository).DB.Exec(ctx, "UPDATE executions SET lease_until=now()-interval '1 second' WHERE id=$1", x.ID); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Token == old.Token || fresh.Attempt != 2 || fresh.Previous != "executing" {
		t.Fatalf("bad recovery claim %+v", fresh)
	}
	if e = s.transition(ctx, old, "succeeded", "", nil); !errors.Is(e, store.ErrLease) {
		t.Fatalf("stale lease committed: %v", e)
	}
	s.perform(ctx, fresh)
	got, e := s.Execution(ctx, x.ID)
	if e != nil || got.Status != "succeeded" {
		t.Fatalf("%+v %v", got, e)
	}
	if f.executed.Load() != 0 || f.reconciled.Load() != 1 || f.observed.Load() != 1 {
		t.Fatalf("recovery repeated write: execute=%d reconcile=%d observe=%d", f.executed.Load(), f.reconciled.Load(), f.observed.Load())
	}
}
func TestPostgresActionsSingleConnectionClaimAndTransition(t *testing.T) {
	s, _, inv := isolatedActions(t, 1)
	p := makePlan(t, s, inv)
	approveAndQueue(t, s, p)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, e := s.claim(ctx)
	if e != nil {
		t.Fatalf("single-connection claim: %v", e)
	}
	if e = s.transition(ctx, c, "needs_attention", "test terminal transition", nil); e != nil {
		t.Fatalf("single-connection transition: %v", e)
	}
}
func TestPostgresActionsAmbiguousWriteReconcilesWithoutSecondWrite(t *testing.T) {
	s, f, inv := isolatedActions(t, 3)
	f.executeErr = connectors.ErrAmbiguous
	p := makePlan(t, s, inv)
	x := approveAndQueue(t, s, p)
	ctx := context.Background()
	c, e := s.claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	s.perform(ctx, c)
	got, e := s.Execution(ctx, x.ID)
	if e != nil || got.Status != "succeeded" || f.executed.Load() != 1 || f.reconciled.Load() != 1 {
		t.Fatalf("ambiguous action %+v err=%v execute=%d reconcile=%d", got, e, f.executed.Load(), f.reconciled.Load())
	}
}
