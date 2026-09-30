package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"net/url"
	"os"
	"testing"
	"time"
)

func isolated(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; tests create an isolated random schema")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	s, e := New(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(ctx, "INSERT INTO applications(id,key,name) VALUES('app','app','Test'); INSERT INTO application_environments(id,application_id,key,name) VALUES('app-default','app','test','Test')"); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestPasswordHash(t *testing.T) {
	h := PasswordHash("correct horse battery")
	if !CheckPassword("correct horse battery", h) || CheckPassword("wrong", h) || CheckPassword("x", "malformed") {
		t.Fatal("password verification")
	}
}
func TestPostgresContextAndLeaseInvariants(t *testing.T) {
	s := isolated(t)
	ctx := context.Background()
	inv, e := s.CreateInvestigation(ctx, "app", "investigation")
	if e != nil {
		t.Fatal(e)
	}
	root := inv.Branches[0]
	run, e := s.Enqueue(ctx, root.ID, "parent question", "model-a", "request-1")
	if e != nil {
		t.Fatal(e)
	}
	dup, e := s.Enqueue(ctx, root.ID, "parent question", "model-a", "request-1")
	if e != nil || dup.ID != run.ID {
		t.Fatalf("idempotency %v %+v", e, dup)
	}
	if _, e = s.Enqueue(ctx, root.ID, "changed", "model-a", "request-1"); !errors.Is(e, ErrConflict) {
		t.Fatalf("request content reuse accepted: %v", e)
	}
	if _, e = s.Enqueue(ctx, root.ID, "another", "model-a", "request-2"); !errors.Is(e, ErrConflict) {
		t.Fatalf("concurrent run accepted: %v", e)
	}
	claim, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, claim, "parent answer", nil); e != nil {
		t.Fatal(e)
	}
	child, e := s.Fork(ctx, root.ID, "child", nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(child.Messages) != 2 {
		t.Fatalf("fork history %d", len(child.Messages))
	}
	if _, e = s.Enqueue(ctx, child.ID, "child question", "model-b", "child-req"); e != nil {
		t.Fatal(e)
	}
	claim, e = s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, claim, "child answer", nil); e != nil {
		t.Fatal(e)
	}
	fresh, e := s.Branch(ctx, root.ID)
	if e != nil || len(fresh.Messages) != 2 {
		t.Fatalf("child contaminated parent %+v %v", fresh, e)
	}
	preview, e := s.Preview(ctx, root.ID, child.ID)
	if e != nil || len(preview.Additions) != 2 {
		t.Fatalf("preview %+v %v", preview, e)
	}
	if _, e = s.Enqueue(ctx, root.ID, "parent changed", "model-b", "root-req2"); e != nil {
		t.Fatal(e)
	}
	claim, e = s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, claim, "new answer", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Merge(ctx, root.ID, preview.ID); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale merge accepted %v", e)
	}
	preview, e = s.Preview(ctx, root.ID, child.ID)
	if e != nil {
		t.Fatal(e)
	}
	merged, e := s.Merge(ctx, root.ID, preview.ID)
	if e != nil || len(merged.Messages) != 6 {
		t.Fatalf("merge result %v %+v", e, merged)
	}
	if _, e = s.Merge(ctx, root.ID, preview.ID); !errors.Is(e, ErrConflict) {
		t.Fatalf("preview reusable %v", e)
	}
	if _, e = s.Enqueue(ctx, root.ID, "lease test", "model-a", "lease-req"); e != nil {
		t.Fatal(e)
	}
	old, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(ctx, "UPDATE runs SET lease_until=now()-interval '1 second' WHERE id=$1", old.Run.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, old, "late result", nil); !errors.Is(e, ErrLease) {
		t.Fatalf("expired lease accepted %v", e)
	}
	newClaim, e := s.Claim(ctx)
	if e != nil || newClaim.Token == old.Token || newClaim.Run.Attempt != 2 {
		t.Fatalf("reclaim %v %+v", e, newClaim)
	}
	if e = s.AppendDelta(ctx, old, "late delta"); !errors.Is(e, ErrLease) {
		t.Fatalf("stale delta accepted %v", e)
	}
	if _, e = s.Cancel(ctx, newClaim.Run.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, newClaim, "cancelled result", nil); !errors.Is(e, ErrLease) {
		t.Fatalf("cancel fencing %v", e)
	}
	events, e := s.Events(ctx, inv.ID, 0)
	if e != nil || len(events) < 10 {
		t.Fatalf("events %d %v", len(events), e)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatal("event ordering")
		}
	}
	tail, e := s.Events(ctx, inv.ID, events[len(events)-2].Seq)
	if e != nil || len(tail) != 1 {
		t.Fatalf("cursor replay %v %d", e, len(tail))
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatalf("repeat migrations %v", e)
	}
}

func TestPostgresConcurrentBranchCommitEvents(t *testing.T) {
	s := isolated(t)
	ctx := context.Background()
	inv, e := s.CreateInvestigation(ctx, "app", "parallel")
	if e != nil {
		t.Fatal(e)
	}
	b1 := inv.Branches[0]
	b2, e := s.Fork(ctx, b1.ID, "parallel sibling", nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, bid := range []string{b1.ID, b2.ID} {
		if _, e = s.Enqueue(ctx, bid, "question", "model-a", "parallel-req"); e != nil {
			t.Fatal(e)
		}
	}
	a, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 2)
	go func() { done <- s.Finish(ctx, a, "answer a", nil) }()
	go func() { done <- s.Finish(ctx, b, "answer b", nil) }()
	for range 2 {
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	}
	detail, e := s.Investigation(ctx, inv.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, branch := range detail.Branches {
		if len(branch.Messages) != 2 {
			t.Fatal("branch result isolation")
		}
	}
	before, e := s.Events(ctx, inv.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	last := before[len(before)-1].Seq
	tx1, e := s.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx1.Rollback(ctx)
	if e = event(ctx, tx1, inv.ID, b1.ID, "test.first", nil); e != nil {
		t.Fatal(e)
	}
	tx2, e := s.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx2.Rollback(ctx)
	started := make(chan struct{})
	go func() {
		close(started)
		err := event(ctx, tx2, inv.ID, b2.ID, "test.second", nil)
		if err == nil {
			err = tx2.Commit(ctx)
		}
		done <- err
	}()
	<-started
	if e = tx1.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	after, e := s.Events(ctx, inv.ID, last)
	if e != nil || len(after) != 2 || after[0].Type != "test.first" || after[1].Type != "test.second" {
		t.Fatalf("commit order %v %+v", e, after)
	}
}

func TestFrozenEvidenceAndRetryScope(t *testing.T) {
	s := isolated(t)
	ctx := context.Background()
	inv, e := s.CreateInvestigation(ctx, "app", "evidence")
	if e != nil {
		t.Fatal(e)
	}
	bid := inv.Branches[0].ID
	if _, e = s.Enqueue(ctx, bid, "inspect", "model-a", "evidence-1"); e != nil {
		t.Fatal(e)
	}
	c, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SaveEvidence(ctx, c, core.Evidence{Kind: "logs", Title: "parent", Content: "original evidence", Source: "test://parent"}); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, c, "summary only", nil); e != nil {
		t.Fatal(e)
	}
	child, e := s.Fork(ctx, bid, "child", nil)
	if e != nil {
		t.Fatal(e)
	}
	sibling, e := s.Fork(ctx, bid, "sibling", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Enqueue(ctx, sibling.ID, "secret sibling", "model-a", "sibling-1"); e != nil {
		t.Fatal(e)
	}
	sc, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.SaveEvidence(ctx, sc, core.Evidence{Kind: "logs", Title: "sibling", Content: "must not inherit", Source: "test://sibling"}); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, sc, "sibling answer", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Enqueue(ctx, child.ID, "continue with other model", "model-b", "child-evidence-1"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(ctx, "UPDATE evidence SET content='mutated after snapshot' WHERE run_id=$1", c.Run.ID); e != nil {
		t.Fatal(e)
	}
	next, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.FrozenEvidence) != 1 || next.FrozenEvidence[0].Content != "original evidence" {
		t.Fatalf("not frozen/exact scope %+v", next.FrozenEvidence)
	}
	if e = s.SaveEvidence(ctx, next, core.Evidence{Kind: "logs", Title: "retry artifact", Content: "committed before crash", Source: "test://attempt"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec(ctx, "UPDATE runs SET lease_until=now()-interval '1 second' WHERE id=$1", next.Run.ID); e != nil {
		t.Fatal(e)
	}
	recovered, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	artifacts, e := s.RetryEvidence(ctx, recovered)
	if e != nil || len(artifacts) != 1 || artifacts[0].Content != "committed before crash" || len(recovered.FrozenEvidence) != 1 {
		t.Fatalf("recovery evidence %v %+v %+v", e, recovered, artifacts)
	}
}

func TestBranchLockDoesNotDeadlockEvidenceFK(t *testing.T) {
	s := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	inv, e := s.CreateInvestigation(ctx, "app", "lock order")
	if e != nil {
		t.Fatal(e)
	}
	bid := inv.Branches[0].ID
	if _, e = s.Enqueue(ctx, bid, "inspect", "model-a", "lock-request"); e != nil {
		t.Fatal(e)
	}
	c, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if _, e = branch(ctx, tx, bid, true); e != nil {
		t.Fatal(e)
	}
	if e = s.AppendDelta(ctx, c, "draft"); e != nil {
		t.Fatalf("branch lock blocks event FK: %v", e)
	}
	if e = s.SaveEvidence(ctx, c, core.Evidence{Kind: "test", Title: "read", Content: "data", Source: "test://"}); e != nil {
		t.Fatalf("branch lock blocks evidence FK: %v", e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Cancel(ctx, c.Run.ID); e != nil {
		t.Fatal(e)
	}
}
