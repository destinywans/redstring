package deployment

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"strings"
)

func (s *Service) Migrate(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018997)"); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS deployment_plans(
 id text PRIMARY KEY,application_id text NOT NULL REFERENCES applications(id),environment_id text NOT NULL,target_id text NOT NULL,
 request_key text NOT NULL UNIQUE,digest text NOT NULL,snapshot jsonb NOT NULL,status text NOT NULL DEFAULT 'pending',
 created_by text NOT NULL REFERENCES users(id),approved_by text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(environment_id,application_id) REFERENCES application_environments(id,application_id));
 CREATE TABLE IF NOT EXISTS deployment_executions(
 id text PRIMARY KEY,plan_id text NOT NULL UNIQUE REFERENCES deployment_plans(id),status text NOT NULL DEFAULT 'queued',
 result jsonb NOT NULL DEFAULT '{}',cancel_requested boolean NOT NULL DEFAULT false,created_by text NOT NULL REFERENCES users(id),
 lease_token text,lease_until timestamptz,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS deployment_environment_locks(environment_id text PRIMARY KEY,execution_id text NOT NULL UNIQUE);
 CREATE INDEX IF NOT EXISTS deployment_claim ON deployment_executions(status,created_at);
 CREATE TABLE IF NOT EXISTS deployment_events(sequence bigserial PRIMARY KEY,execution_id text NOT NULL REFERENCES deployment_executions(id),
 type text NOT NULL,payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());`)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func audit(ctx context.Context, tx pgx.Tx, actor, app, action, target string, changes any) error {
	raw, e := json.Marshal(changes)
	if e != nil {
		return e
	}
	outcome := "success"
	if strings.HasSuffix(action, ".failed") {
		outcome = "failed"
	}
	if strings.HasSuffix(action, ".needs_attention") {
		outcome = "unknown"
	}
	_, e = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,$3,$4,$5,$6)", actor, app, action, target, outcome, raw)
	return e
}
