package actions

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/destinywans/redstring/internal/store"
)

// Migration 173 runs before this module creates its tables, so the tests
// apply it again once the plans exist.
func expireLegacyPlans(t *testing.T, s *Service) {
	t.Helper()
	b, e := os.ReadFile("../../migrations/173_v3_legacy_plans_expire.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Repository.(*PostgresRepository).DB.Exec(context.Background(), string(b)); e != nil {
		t.Fatal(e)
	}
}

type legacyPlans struct {
	approved, queued, succeeded, pending, rejected Plan
	queuedExecution, succeededExecution            Execution
}

func seedLegacyPlans(t *testing.T, s *Service, inv string) legacyPlans {
	t.Helper()
	ctx := context.Background()
	var l legacyPlans
	l.approved = makePlan(t, s, inv)
	if _, e := s.Approve(ctx, l.approved.ID, l.approved.Proposal.Digest, "u"); e != nil {
		t.Fatal(e)
	}
	l.queued = makePlan(t, s, inv)
	l.queuedExecution = approveAndQueue(t, s, l.queued)
	l.succeeded = makePlan(t, s, inv)
	l.succeededExecution = approveAndQueue(t, s, l.succeeded)
	if _, e := s.Repository.(*PostgresRepository).DB.Exec(ctx, "UPDATE executions SET status='succeeded' WHERE id=$1", l.succeededExecution.ID); e != nil {
		t.Fatal(e)
	}
	l.pending = makePlan(t, s, inv)
	l.rejected = makePlan(t, s, inv)
	if _, e := s.Decide(ctx, l.rejected.ID, l.rejected.Proposal.Digest, "rejected", "no", "u"); e != nil {
		t.Fatal(e)
	}
	return l
}

func planDecision(t *testing.T, s *Service, id string) (string, string) {
	t.Helper()
	p, e := s.Plan(context.Background(), id)
	if e != nil {
		t.Fatal(e)
	}
	return p.Decision, p.DecisionReason
}

func expiryAudits(t *testing.T, s *Service) map[string]string {
	t.Helper()
	rows, e := s.Repository.(*PostgresRepository).DB.Query(context.Background(), `SELECT target, application_id, actor_kind, changes->>'previous_execution_status'
		FROM management_audit WHERE action='legacy_action_plan.expired' AND actor_id=''`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var target, app, kind, previous string
		if e = rows.Scan(&target, &app, &kind, &previous); e != nil {
			t.Fatal(e)
		}
		if app != "app" || kind != "system" {
			t.Fatalf("expiry audit for %s: application=%q actor_kind=%q", target, app, kind)
		}
		out[target] = previous
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestLegacyPlansExpireOnV3Instance(t *testing.T) {
	s, _, inv := isolatedActions(t, 4)
	ctx := context.Background()
	l := seedLegacyPlans(t, s, inv)
	if _, e := s.Repository.(*PostgresRepository).DB.Exec(ctx, "INSERT INTO v3_instance_boundary(tier,domain) VALUES('nonprod','test')"); e != nil {
		t.Fatal(e)
	}
	expireLegacyPlans(t, s)

	if decision, reason := planDecision(t, s, l.approved.ID); decision != "expired" || reason != "legacy_plan_expired_registry_cutover" {
		t.Fatalf("unexecuted plan after cutover: decision=%q reason=%q", decision, reason)
	}
	for id, want := range map[string]string{l.queued.ID: "approved", l.succeeded.ID: "approved", l.pending.ID: "pending", l.rejected.ID: "rejected"} {
		if decision, _ := planDecision(t, s, id); decision != want {
			t.Fatalf("plan %s after cutover: decision=%q, want %q", id, decision, want)
		}
	}
	x, e := s.Execution(ctx, l.queuedExecution.ID)
	if e != nil || x.Status != "failed" || x.Error != "legacy_plan_expired_registry_cutover" {
		t.Fatalf("queued execution after cutover: %+v %v", x, e)
	}
	if n := countExecutionEvents(t, s, inv, "execution.failed", l.queuedExecution.ID); n != 1 {
		t.Fatalf("execution.failed events for the queued execution: %d", n)
	}
	if x, e = s.Execution(ctx, l.succeededExecution.ID); e != nil || x.Status != "succeeded" {
		t.Fatalf("finished execution after cutover: %+v %v", x, e)
	}
	if n := countExecutionEvents(t, s, inv, "execution.failed", l.succeededExecution.ID); n != 0 {
		t.Fatalf("execution.failed events for the finished execution: %d", n)
	}
	audits := expiryAudits(t, s)
	if len(audits) != 2 || audits[l.approved.ID] != "none" || audits[l.queued.ID] != "queued" {
		t.Fatalf("expiry audits: %v", audits)
	}

	if _, e = s.Enqueue(ctx, l.approved.ID, "u"); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("enqueueing an expired plan: %v", e)
	}
	if x, e = s.Enqueue(ctx, l.queued.ID, "u"); e != nil || x.ID != l.queuedExecution.ID || x.Status != "failed" {
		t.Fatalf("enqueueing a plan whose queued execution expired: %+v %v", x, e)
	}
	if _, e = s.Approve(ctx, l.approved.ID, l.approved.Proposal.Digest, "u"); e == nil {
		t.Fatal("an expired plan was approved again")
	}
	if _, e = s.Decide(ctx, l.approved.ID, l.approved.Proposal.Digest, "revoked", "late", "u"); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("revoking an expired plan: %v", e)
	}

	expireLegacyPlans(t, s)
	if audits = expiryAudits(t, s); len(audits) != 2 {
		t.Fatalf("a second run audited again: %v", audits)
	}
	if n := countExecutionEvents(t, s, inv, "execution.failed", l.queuedExecution.ID); n != 1 {
		t.Fatalf("a second run added execution.failed events: %d", n)
	}
}

func countExecutionEvents(t *testing.T, s *Service, inv, kind, execution string) int {
	t.Helper()
	var n int
	if e := s.Repository.(*PostgresRepository).DB.QueryRow(context.Background(), "SELECT count(*) FROM events WHERE investigation_id=$1 AND type=$2 AND payload->>'execution_id'=$3", inv, kind, execution).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

func TestLegacyPlansStayOnLegacyInstance(t *testing.T) {
	s, _, inv := isolatedActions(t, 4)
	ctx := context.Background()
	l := seedLegacyPlans(t, s, inv)
	expireLegacyPlans(t, s)

	for id, want := range map[string]string{l.approved.ID: "approved", l.queued.ID: "approved", l.pending.ID: "pending"} {
		if decision, _ := planDecision(t, s, id); decision != want {
			t.Fatalf("plan %s on a legacy instance: decision=%q, want %q", id, decision, want)
		}
	}
	if x, e := s.Execution(ctx, l.queuedExecution.ID); e != nil || x.Status != "queued" {
		t.Fatalf("queued execution on a legacy instance: %+v %v", x, e)
	}
	if audits := expiryAudits(t, s); len(audits) != 0 {
		t.Fatalf("legacy instance audited expiries: %v", audits)
	}
}
