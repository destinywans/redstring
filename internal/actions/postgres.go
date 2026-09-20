package actions

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/store"
)

type PostgresRepository struct{ DB *pgxpool.Pool }

func New(db *pgxpool.Pool, c Connector, app string) *Service {
	return NewWithRepository(&PostgresRepository{DB: db}, c, app)
}
func (s *PostgresRepository) Application(ctx context.Context, inv string) (string, error) {
	var app string
	e := s.DB.QueryRow(ctx, "SELECT application_id FROM investigations WHERE id=$1", inv).Scan(&app)
	return app, e
}
func (s *PostgresRepository) Renew(ctx context.Context, c Lease) error {
	tag, e := s.DB.Exec(ctx, "UPDATE executions SET lease_until=now()+interval '30 seconds' WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND status IN('executing','verifying')", c.ID, c.Token)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return store.ErrLease
	}
	return nil
}
func (s *PostgresRepository) CreatePlan(ctx context.Context, inv string, proposal connectors.RollbackPlan, user string) (Plan, error) {
	b, e := json.Marshal(proposal)
	if e != nil {
		return Plan{}, e
	}
	id := store.ID()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Plan{}, e
	}
	defer tx.Rollback(ctx)
	var invState string
	if e = tx.QueryRow(ctx, "SELECT status FROM investigations WHERE id=$1 FOR NO KEY UPDATE", inv).Scan(&invState); e != nil {
		return Plan{}, e
	}
	if invState == "closed" {
		return Plan{}, store.ErrConflict
	}
	if _, e = tx.Exec(ctx, "INSERT INTO action_plans(id,investigation_id,proposal,digest,created_by) VALUES($1,$2,$3,$4,$5)", id, inv, b, proposal.Digest, user); e != nil {
		return Plan{}, e
	}
	if e = event(ctx, tx, inv, "action.proposed", map[string]any{"plan_id": id, "digest": proposal.Digest}); e != nil {
		return Plan{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Plan{}, e
	}
	return s.Plan(ctx, id)
}
func (s *PostgresRepository) Migrate(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018993)"); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS action_plans(id text PRIMARY KEY,investigation_id text NOT NULL REFERENCES investigations(id),proposal jsonb NOT NULL,digest text NOT NULL,created_by text NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS approvals(plan_id text PRIMARY KEY REFERENCES action_plans(id),digest text NOT NULL,approved_by text NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS executions(id text PRIMARY KEY,plan_id text UNIQUE NOT NULL REFERENCES action_plans(id),status text NOT NULL CHECK(status IN('queued','executing','verifying','succeeded','failed','needs_attention')),attempt integer NOT NULL DEFAULT 0,lease_token text,lease_until timestamptz,error text NOT NULL DEFAULT '',result jsonb NOT NULL DEFAULT '{}',created_by text NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
 ALTER TABLE approvals ADD COLUMN IF NOT EXISTS decision text NOT NULL DEFAULT 'approved';
 ALTER TABLE approvals ADD COLUMN IF NOT EXISTS reason text NOT NULL DEFAULT '';
 CREATE TABLE IF NOT EXISTS investigation_reports(investigation_id text PRIMARY KEY REFERENCES investigations(id),execution_id text NOT NULL REFERENCES executions(id),report jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());`)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}

const planSelect = `SELECT p.id,p.investigation_id,p.created_by,p.created_at,CASE WHEN a.decision='approved' THEN a.approved_by ELSE '' END,CASE WHEN a.decision='approved' THEN a.created_at END,p.proposal,COALESCE(a.decision,'pending'),COALESCE(a.approved_by,''),COALESCE(a.reason,''),a.created_at FROM action_plans p LEFT JOIN approvals a ON a.plan_id=p.id `

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	var b []byte
	e := row.Scan(&p.ID, &p.InvestigationID, &p.CreatedBy, &p.CreatedAt, &p.ApprovedBy, &p.ApprovedAt, &b, &p.Decision, &p.DecisionBy, &p.DecisionReason, &p.DecidedAt)
	if e == nil {
		e = json.Unmarshal(b, &p.Proposal)
	}
	return p, e
}
func (s *PostgresRepository) Plan(ctx context.Context, id string) (Plan, error) {
	return scanPlan(s.DB.QueryRow(ctx, planSelect+" WHERE p.id=$1", id))
}
func (s *PostgresRepository) Plans(ctx context.Context, inv string) ([]Plan, error) {
	rows, e := s.DB.Query(ctx, planSelect+" WHERE p.investigation_id=$1 ORDER BY p.created_at", inv)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		p, e := scanPlan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *PostgresRepository) Approve(ctx context.Context, id, digest, user string) (Plan, error) {
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
	var decision string
	if e = tx.QueryRow(ctx, "SELECT COALESCE((SELECT decision FROM approvals WHERE plan_id=$1),'pending')", id).Scan(&decision); e != nil {
		return Plan{}, e
	}
	if decision != "pending" && decision != "approved" {
		return Plan{}, store.ErrConflict
	}
	tag, e := tx.Exec(ctx, "INSERT INTO approvals(plan_id,digest,approved_by) VALUES($1,$2,$3) ON CONFLICT(plan_id) DO NOTHING", id, digest, user)
	if e != nil {
		return Plan{}, e
	}
	if tag.RowsAffected() > 0 {
		if e = event(ctx, tx, inv, "action.approved", map[string]any{"plan_id": id, "approved_by": user, "digest": digest}); e != nil {
			return Plan{}, e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return Plan{}, e
	}
	return s.Plan(ctx, id)
}
func (s *PostgresRepository) Enqueue(ctx context.Context, id, user string) (Execution, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Execution{}, e
	}
	defer tx.Rollback(ctx)
	var digest, inv string
	if e = tx.QueryRow(ctx, "SELECT digest,investigation_id FROM action_plans WHERE id=$1 FOR UPDATE", id).Scan(&digest, &inv); e != nil {
		return Execution{}, e
	}
	var existing string
	if err := tx.QueryRow(ctx, "SELECT id FROM executions WHERE plan_id=$1", id).Scan(&existing); err == nil {
		if err = tx.Commit(ctx); err != nil {
			return Execution{}, err
		}
		return s.Execution(ctx, existing)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Execution{}, err
	}
	var invState string
	if e = tx.QueryRow(ctx, "SELECT status FROM investigations WHERE id=$1 FOR NO KEY UPDATE", inv).Scan(&invState); e != nil {
		return Execution{}, e
	}
	if invState == "closed" {
		return Execution{}, store.ErrConflict
	}
	var approved bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM approvals WHERE plan_id=$1 AND digest=$2 AND decision='approved')", id, digest).Scan(&approved); e != nil {
		return Execution{}, e
	}
	if !approved {
		return Execution{}, store.ErrConflict
	}
	eid := store.ID()
	tag, e := tx.Exec(ctx, "INSERT INTO executions(id,plan_id,status,created_by) VALUES($1,$2,'queued',$3) ON CONFLICT(plan_id) DO NOTHING", eid, id, user)
	if e != nil {
		return Execution{}, e
	}
	if tag.RowsAffected() > 0 {
		if e = event(ctx, tx, inv, "execution.queued", map[string]any{"execution_id": eid, "plan_id": id}); e != nil {
			return Execution{}, e
		}
	}
	if e = tx.QueryRow(ctx, "SELECT id FROM executions WHERE plan_id=$1", id).Scan(&eid); e != nil {
		return Execution{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Execution{}, e
	}
	return s.Execution(ctx, eid)
}
func (s *PostgresRepository) ExecutionForPlan(ctx context.Context, id string) (Execution, error) {
	var x Execution
	e := s.DB.QueryRow(ctx, "SELECT id,plan_id,status,attempt,error,result,created_at,updated_at FROM executions WHERE plan_id=$1", id).Scan(&x.ID, &x.PlanID, &x.Status, &x.Attempt, &x.Error, &x.Result, &x.CreatedAt, &x.UpdatedAt)
	return x, e
}

func (s *PostgresRepository) Execution(ctx context.Context, id string) (Execution, error) {
	var x Execution
	e := s.DB.QueryRow(ctx, "SELECT id,plan_id,status,attempt,error,result,created_at,updated_at FROM executions WHERE id=$1", id).Scan(&x.ID, &x.PlanID, &x.Status, &x.Attempt, &x.Error, &x.Result, &x.CreatedAt, &x.UpdatedAt)
	return x, e
}
func event(ctx context.Context, tx pgx.Tx, inv, kind string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "SELECT id FROM investigations WHERE id=$1 FOR NO KEY UPDATE", inv); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO events(investigation_id,type,payload) VALUES($1,$2,$3)", inv, kind, b)
	return e
}

func (s *PostgresRepository) Claim(ctx context.Context) (Lease, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Lease{}, e
	}
	defer tx.Rollback(ctx)
	var c Lease
	e = tx.QueryRow(ctx, "SELECT id,plan_id,status,attempt FROM executions WHERE status='queued' OR (status IN('executing','verifying') AND lease_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&c.ID, &c.PlanID, &c.Previous, &c.Attempt)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, ErrNoWork
	}
	if e != nil {
		return c, e
	}
	c.Token = store.ID()
	c.Attempt++
	if _, e = tx.Exec(ctx, "UPDATE executions SET status='executing',attempt=$2,lease_token=$3,lease_until=now()+interval '30 seconds',updated_at=now() WHERE id=$1", c.ID, c.Attempt, c.Token); e != nil {
		return c, e
	}
	p, e := scanPlan(tx.QueryRow(ctx, planSelect+" WHERE p.id=$1", c.PlanID))
	if e != nil {
		return c, e
	}
	if e = event(ctx, tx, p.InvestigationID, "execution.started", map[string]any{"execution_id": c.ID, "attempt": c.Attempt, "resuming": c.Previous != "queued"}); e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}
func (s *PostgresRepository) Transition(ctx context.Context, c Lease, status, message string, result any) error {
	b, e := json.Marshal(result)
	if e != nil {
		return e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, "UPDATE executions SET status=$3,error=$4,result=$5,updated_at=now() WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND status IN('executing','verifying')", c.ID, c.Token, status, message, b)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return store.ErrLease
	}
	p, e := scanPlan(tx.QueryRow(ctx, planSelect+" WHERE p.id=$1", c.PlanID))
	if e != nil {
		return e
	}
	if e = event(ctx, tx, p.InvestigationID, "execution."+status, map[string]any{"execution_id": c.ID, "result": result, "error": message}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *PostgresRepository) ExecutionActor(ctx context.Context, id string) (string, error) {
	var actor string
	e := s.DB.QueryRow(ctx, "SELECT created_by FROM executions WHERE id=$1", id).Scan(&actor)
	return actor, e
}
