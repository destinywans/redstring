package actions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// Decisions serialize with Enqueue on the immutable plan. Revocation is only
// accepted BEFORE an execution exists; it cannot undo an already queued write.
func (s *PostgresRepository) Decide(ctx context.Context, id, digest, decision, reason, user string) (Plan, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Plan{}, e
	}
	defer tx.Rollback(ctx)
	var want, inv string
	if e = tx.QueryRow(ctx, "SELECT digest,investigation_id FROM action_plans WHERE id=$1 FOR UPDATE", id).Scan(&want, &inv); e != nil {
		return Plan{}, e
	}
	if digest != want {
		return Plan{}, store.ErrConflict
	}
	var exists bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM executions WHERE plan_id=$1)", id).Scan(&exists); e != nil {
		return Plan{}, e
	}
	if exists {
		return Plan{}, store.ErrConflict
	}
	var current string
	if e = tx.QueryRow(ctx, "SELECT COALESCE((SELECT decision FROM approvals WHERE plan_id=$1),'pending')", id).Scan(&current); e != nil {
		return Plan{}, e
	}
	if current == decision {
		return s.planAfterCommit(ctx, tx, id)
	}
	if (decision == "rejected" && current != "pending") || (decision == "revoked" && current != "approved") {
		return Plan{}, store.ErrConflict
	}
	_, e = tx.Exec(ctx, "INSERT INTO approvals(plan_id,digest,approved_by,decision,reason) VALUES($1,$2,$3,$4,$5) ON CONFLICT(plan_id) DO UPDATE SET decision=excluded.decision,reason=excluded.reason,approved_by=excluded.approved_by,created_at=now()", id, digest, user, decision, reason)
	if e != nil {
		return Plan{}, e
	}
	if e = event(ctx, tx, inv, "action."+decision, map[string]string{"plan_id": id, "digest": digest, "decided_by": user, "reason": reason}); e != nil {
		return Plan{}, e
	}
	return s.planAfterCommit(ctx, tx, id)
}
func (s *PostgresRepository) planAfterCommit(ctx context.Context, tx pgx.Tx, id string) (Plan, error) {
	if e := tx.Commit(ctx); e != nil {
		return Plan{}, e
	}
	return s.Plan(ctx, id)
}
func (s *PostgresRepository) Report(ctx context.Context, inv string) (Report, error) {
	var r Report
	var b []byte
	e := s.DB.QueryRow(ctx, "SELECT report FROM investigation_reports WHERE investigation_id=$1", inv).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &r)
	}
	return r, e
}
func (s *PostgresRepository) CloseInvestigation(ctx context.Context, inv, execution, summary, user string) (Report, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Report{}, e
	}
	defer tx.Rollback(ctx)
	var app, status string
	if e = tx.QueryRow(ctx, "SELECT application_id,status FROM investigations WHERE id=$1 FOR NO KEY UPDATE", inv).Scan(&app, &status); e != nil {
		return Report{}, e
	}
	var prior []byte
	e = tx.QueryRow(ctx, "SELECT report FROM investigation_reports WHERE investigation_id=$1", inv).Scan(&prior)
	if e == nil {
		var r Report
		if e = json.Unmarshal(prior, &r); e != nil {
			return r, e
		}
		if r.ExecutionID != execution {
			return Report{}, store.ErrConflict
		}
		return r, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return Report{}, e
	}
	var active bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM runs r JOIN branches b ON b.id=r.branch_id WHERE b.investigation_id=$1 AND r.status IN('queued','running'))", inv).Scan(&active); e != nil {
		return Report{}, e
	}
	if active {
		return Report{}, store.ErrConflict
	}
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM executions x JOIN action_plans p ON p.id=x.plan_id WHERE p.investigation_id=$1 AND x.status IN('queued','executing','verifying'))", inv).Scan(&active); e != nil {
		return Report{}, e
	}
	if active {
		return Report{}, store.ErrConflict
	}
	r := Report{InvestigationID: inv, ApplicationID: app, ExecutionID: execution, Summary: summary, ClosedBy: user, ClosedAt: time.Now().UTC(), Evidence: []core.Evidence{}}
	var proposal []byte
	e = tx.QueryRow(ctx, "SELECT p.id,p.proposal,a.approved_by,x.result FROM executions x JOIN action_plans p ON p.id=x.plan_id JOIN approvals a ON a.plan_id=p.id WHERE x.id=$1 AND p.investigation_id=$2 AND x.status='succeeded' AND a.decision='approved' AND NOT EXISTS(SELECT 1 FROM executions newer JOIN action_plans np ON np.id=newer.plan_id WHERE np.investigation_id=$2 AND newer.id<>x.id AND newer.updated_at>=x.updated_at)", execution, inv).Scan(&r.PlanID, &proposal, &r.ApprovedBy, &r.Recovery)
	if errors.Is(e, pgx.ErrNoRows) {
		return r, store.ErrConflict
	}
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(proposal, &r.Proposal); e != nil {
		return r, e
	}
	rows, e := tx.Query(ctx, "SELECT e.id,e.branch_id,e.kind,e.title,e.content,e.source,e.created_at FROM evidence e JOIN branches b ON b.id=e.branch_id WHERE b.investigation_id=$1 ORDER BY e.created_at,e.id", inv)
	if e != nil {
		return r, e
	}
	for rows.Next() {
		var v core.Evidence
		if e = rows.Scan(&v.ID, &v.BranchID, &v.Kind, &v.Title, &v.Content, &v.Source, &v.CreatedAt); e != nil {
			rows.Close()
			return r, e
		}
		r.Evidence = append(r.Evidence, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return r, e
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return r, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO investigation_reports(investigation_id,execution_id,report) VALUES($1,$2,$3)", inv, execution, raw); e != nil {
		return r, e
	}
	if _, e = tx.Exec(ctx, "UPDATE investigations SET status='closed',updated_at=now() WHERE id=$1", inv); e != nil {
		return r, e
	}
	if e = event(ctx, tx, inv, "investigation.closed", map[string]string{"execution_id": execution, "closed_by": user}); e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}
