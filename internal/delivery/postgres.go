package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

type PostgresRepository struct{ DB *pgxpool.Pool }

func (p *PostgresRepository) Migrate(ctx context.Context) error {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018996)"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS delivery_pipelines (
 id text PRIMARY KEY, application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL, version bigint NOT NULL,
 FOREIGN KEY(environment_id,application_id) REFERENCES application_environments(id,application_id));
CREATE TABLE IF NOT EXISTS delivery_versions (
 pipeline_id text NOT NULL REFERENCES delivery_pipelines(id), version bigint NOT NULL,
 snapshot jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(pipeline_id,version));
CREATE TABLE IF NOT EXISTS delivery_runs (
 id text PRIMARY KEY, pipeline_id text NOT NULL, version bigint NOT NULL,
 application_id text NOT NULL REFERENCES applications(id), environment_id text NOT NULL,
 provider text NOT NULL, status text NOT NULL, external_id text NOT NULL DEFAULT '',
 snapshot jsonb NOT NULL, projection jsonb NOT NULL DEFAULT '{}',
 created_by text NOT NULL REFERENCES users(id), idempotency_key text NOT NULL, request_digest text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(pipeline_id,version) REFERENCES delivery_versions(pipeline_id,version),
 FOREIGN KEY(environment_id,application_id) REFERENCES application_environments(id,application_id),
 UNIQUE(application_id,idempotency_key));
CREATE TABLE IF NOT EXISTS delivery_commands (
 id text PRIMARY KEY, run_id text NOT NULL REFERENCES delivery_runs(id), kind text NOT NULL,
 job_id bigint NOT NULL DEFAULT 0, status text NOT NULL DEFAULT 'pending', actor_id text NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL, request_digest text NOT NULL, error text NOT NULL DEFAULT '',
 lease_token text, lease_until timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(run_id,idempotency_key));
CREATE INDEX IF NOT EXISTS delivery_commands_pending ON delivery_commands(status,created_at);
CREATE INDEX IF NOT EXISTS delivery_runs_scope ON delivery_runs(application_id,environment_id,created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS delivery_runs_gitlab_identity ON delivery_runs ((snapshot->'definition'->>'source_id'),(snapshot->'definition'->>'project_id'),external_id) WHERE provider='gitlab' AND external_id<>'';
CREATE TABLE IF NOT EXISTS delivery_events (
 sequence bigserial PRIMARY KEY, run_id text NOT NULL REFERENCES delivery_runs(id),
 type text NOT NULL, actor_id text NOT NULL DEFAULT '', payload jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS delivery_events_run ON delivery_events(run_id,sequence);
CREATE TABLE IF NOT EXISTS delivery_webhook_inbox (
 source_id text NOT NULL, event_id text NOT NULL, project_id bigint NOT NULL, external_id text NOT NULL,
 received_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(source_id,event_id));
CREATE TABLE IF NOT EXISTS delivery_gates(run_id text NOT NULL REFERENCES delivery_runs(id),node_id text NOT NULL,plan_id text NOT NULL,execution_id text NOT NULL DEFAULT '',PRIMARY KEY(run_id,node_id));
CREATE TABLE IF NOT EXISTS delivery_retention(run_id text PRIMARY KEY REFERENCES delivery_runs(id), retain_until timestamptz NOT NULL, held boolean NOT NULL DEFAULT false, reason text NOT NULL DEFAULT '', updated_by text NOT NULL, reclaim_requested_at timestamptz);
CREATE TABLE IF NOT EXISTS delivery_references(run_id text NOT NULL REFERENCES delivery_runs(id), reference_id text NOT NULL, reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(run_id,reference_id));
`)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *PostgresRepository) SavePipeline(ctx context.Context, in Pipeline, expected int64) (Pipeline, error) {
	if err := ValidateDefinition(in.Definition); err != nil {
		return Pipeline{}, err
	}
	if in.Name == "" || len(in.Name) > 200 || in.ApplicationID == "" || in.EnvironmentID == "" || in.CreatedBy == "" {
		return Pipeline{}, ErrInvalid
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	defer tx.Rollback(ctx)
	if in.ID == "" {
		if expected != 0 {
			return Pipeline{}, store.ErrConflict
		}
		in.ID = store.ID()
		in.Version = 1
		_, err = tx.Exec(ctx, "INSERT INTO delivery_pipelines(id,application_id,environment_id,version) VALUES($1,$2,$3,1)", in.ID, in.ApplicationID, in.EnvironmentID)
	} else {
		var app, env string
		var version int64
		err = tx.QueryRow(ctx, "SELECT application_id,environment_id,version FROM delivery_pipelines WHERE id=$1 FOR UPDATE", in.ID).Scan(&app, &env, &version)
		if err == nil && (app != in.ApplicationID || env != in.EnvironmentID || version != expected) {
			return Pipeline{}, store.ErrConflict
		}
		in.Version = version + 1
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE delivery_pipelines SET version=$2 WHERE id=$1", in.ID, in.Version)
		}
	}
	if err != nil {
		return Pipeline{}, err
	}
	in.Digest = hash(in.Definition)
	in.CreatedAt = time.Now().UTC()
	b, err := json.Marshal(in)
	if err != nil {
		return Pipeline{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO delivery_versions(pipeline_id,version,snapshot) VALUES($1,$2,$3)", in.ID, in.Version, b); err != nil {
		return Pipeline{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'delivery.pipeline.saved',$3,'success',$4)", in.CreatedBy, in.ApplicationID, in.ID, b); err != nil {
		return Pipeline{}, err
	}
	return in, tx.Commit(ctx)
}
func (p *PostgresRepository) Pipeline(ctx context.Context, id string, version int64) (Pipeline, error) {
	var raw []byte
	var out Pipeline
	err := p.DB.QueryRow(ctx, `SELECT v.snapshot FROM delivery_versions v JOIN delivery_pipelines p ON p.id=v.pipeline_id WHERE p.id=$1 AND v.version=CASE WHEN $2::bigint=0 THEN p.version ELSE $2::bigint END`, id, version).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
func (p *PostgresRepository) Versions(ctx context.Context, id string) ([]Pipeline, error) {
	rows, err := p.DB.Query(ctx, "SELECT snapshot FROM delivery_versions WHERE pipeline_id=$1 ORDER BY version DESC LIMIT 100", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pipeline{}
	for rows.Next() {
		var raw []byte
		var v Pipeline
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (p *PostgresRepository) Pipelines(ctx context.Context, apps []string, env string) ([]Pipeline, error) {
	rows, err := p.DB.Query(ctx, `SELECT v.snapshot FROM delivery_pipelines p JOIN delivery_versions v ON v.pipeline_id=p.id AND v.version=p.version WHERE p.application_id=ANY($1) AND ($2='' OR p.environment_id=ANY(string_to_array($2,','))) ORDER BY v.created_at DESC LIMIT 200`, apps, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pipeline{}
	for rows.Next() {
		var raw []byte
		var v Pipeline
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func eventTx(ctx context.Context, tx pgx.Tx, run, app, kind, actor string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO delivery_events(run_id,type,actor_id,payload) VALUES($1,$2,$3,$4)", run, kind, actor, b); err != nil {
		return err
	}
	actorKind := "user"
	if actor == "" {
		actorKind = "system"
	}
	_, err = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,$2,$3,$4,$5,'recorded',$6)", actor, actorKind, app, kind, run, b)
	return err
}
func (p *PostgresRepository) CreateRun(ctx context.Context, pipe Pipeline, actor, key string) (Run, error) {
	if key == "" || len(key) > 200 || actor == "" {
		return Run{}, ErrInvalid
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	request := hash(struct {
		Pipeline string
		Version  int64
	}{pipe.ID, pipe.Version})
	id := store.ID()
	snapshot, _ := json.Marshal(pipe)
	tag, err := tx.Exec(ctx, `INSERT INTO delivery_runs(id,pipeline_id,version,application_id,environment_id,provider,status,snapshot,created_by,idempotency_key,request_digest) VALUES($1,$2,$3,$4,$5,$6,'queued',$7,$8,$9,$10) ON CONFLICT(application_id,idempotency_key) DO NOTHING`, id, pipe.ID, pipe.Version, pipe.ApplicationID, pipe.EnvironmentID, pipe.Definition.Provider, snapshot, actor, key, request)
	if err != nil {
		return Run{}, err
	}
	var existing, want string
	if err = tx.QueryRow(ctx, "SELECT id,request_digest FROM delivery_runs WHERE application_id=$1 AND idempotency_key=$2", pipe.ApplicationID, key).Scan(&existing, &want); err != nil {
		return Run{}, err
	}
	if want != request {
		return Run{}, store.ErrConflict
	}
	if tag.RowsAffected() > 0 {
		_, err = tx.Exec(ctx, "INSERT INTO delivery_commands(id,run_id,kind,actor_id,idempotency_key,request_digest) VALUES($1,$2,'submit',$3,'initial-submit',$4)", store.ID(), id, actor, request)
		if err == nil {
			err = eventTx(ctx, tx, id, pipe.ApplicationID, "delivery.run.queued", actor, map[string]any{"version": pipe.Version, "digest": pipe.Digest})
		}
		if err != nil {
			return Run{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return p.GetRun(ctx, existing)
}

const runColumns = `id,pipeline_id,version,application_id,environment_id,provider,status,external_id,snapshot,projection,created_by,created_at,updated_at`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var snapshot, projection []byte
	err := row.Scan(&r.ID, &r.PipelineID, &r.Version, &r.ApplicationID, &r.EnvironmentID, &r.Provider, &r.Status, &r.ExternalID, &snapshot, &projection, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(snapshot, &r.Snapshot)
	}
	if err == nil {
		err = json.Unmarshal(projection, &r.Projection)
	}
	return r, err
}
func (p *PostgresRepository) GetRun(ctx context.Context, id string) (Run, error) {
	return scanRun(p.DB.QueryRow(ctx, "SELECT "+runColumns+" FROM delivery_runs WHERE id=$1", id))
}
func (p *PostgresRepository) Runs(ctx context.Context, apps []string, env string) ([]Run, error) {
	rows, err := p.DB.Query(ctx, "SELECT "+runColumns+" FROM delivery_runs WHERE application_id=ANY($1) AND ($2='' OR environment_id=ANY(string_to_array($2,','))) ORDER BY created_at DESC LIMIT 100", apps, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		v, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (p *PostgresRepository) Active(ctx context.Context) ([]Run, error) {
	rows, err := p.DB.Query(ctx, "SELECT "+runColumns+" FROM delivery_runs WHERE status IN('queued','pending','running','waiting','cancel_requested') ORDER BY updated_at LIMIT 50")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		v, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (p *PostgresRepository) Enqueue(ctx context.Context, run Run, kind string, job int64, actor, key string) (Command, error) {
	if key == "" || len(key) > 200 {
		return Command{}, ErrInvalid
	}
	switch kind {
	case "cancel", "retry", "play", "reconcile":
	default:
		return Command{}, ErrInvalid
	}
	if kind == "play" && job <= 0 {
		return Command{}, ErrInvalid
	}
	if kind != "play" && job != 0 {
		return Command{}, ErrInvalid
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return Command{}, err
	}
	defer tx.Rollback(ctx)
	var current string
	if err = tx.QueryRow(ctx, "SELECT status FROM delivery_runs WHERE id=$1 FOR UPDATE", run.ID).Scan(&current); err != nil {
		return Command{}, err
	}
	id := store.ID()
	digest := hash(struct {
		Kind string
		Job  int64
	}{kind, job})
	tag, err := tx.Exec(ctx, "INSERT INTO delivery_commands(id,run_id,kind,job_id,actor_id,idempotency_key,request_digest) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(run_id,idempotency_key) DO NOTHING", id, run.ID, kind, job, actor, key, digest)
	if err != nil {
		return Command{}, err
	}
	var out Command
	var want string
	err = tx.QueryRow(ctx, "SELECT id,run_id,kind,job_id,status,actor_id,error,created_at,request_digest FROM delivery_commands WHERE run_id=$1 AND idempotency_key=$2", run.ID, key).Scan(&out.ID, &out.RunID, &out.Kind, &out.JobID, &out.Status, &out.ActorID, &out.Error, &out.CreatedAt, &want)
	if err != nil {
		return Command{}, err
	}
	if want != digest {
		return Command{}, store.ErrConflict
	}
	if tag.RowsAffected() > 0 {
		if kind == "cancel" {
			if terminal(current) {
				return Command{}, store.ErrConflict
			}
			if _, err = tx.Exec(ctx, "UPDATE delivery_runs SET status='cancel_requested',updated_at=now() WHERE id=$1", run.ID); err != nil {
				return Command{}, err
			}
		}
		if kind == "retry" && current != "failed" && current != "cancelled" {
			return Command{}, store.ErrConflict
		}
		if err = eventTx(ctx, tx, run.ID, run.ApplicationID, "delivery.command.queued", actor, map[string]any{"command_id": id, "kind": kind, "job_id": job}); err != nil {
			return Command{}, err
		}
	}
	return out, tx.Commit(ctx)
}
func (p *PostgresRepository) Claim(ctx context.Context) (Command, error) {
	var c Command
	token := store.ID()
	err := p.DB.QueryRow(ctx, `WITH next AS (SELECT c.id,c.status FROM delivery_commands c JOIN delivery_runs r ON r.id=c.run_id WHERE (c.status='pending' OR (c.status='executing' AND c.lease_until<now())) AND NOT EXISTS(SELECT 1 FROM delivery_commands busy WHERE busy.run_id=c.run_id AND busy.id<>c.id AND busy.status='executing' AND busy.lease_until>now()) ORDER BY c.created_at FOR UPDATE OF c,r SKIP LOCKED LIMIT 1) UPDATE delivery_commands c SET status='executing',lease_token=$1,lease_until=now()+interval '90 seconds' FROM next WHERE c.id=next.id RETURNING c.id,c.run_id,c.kind,c.job_id,c.actor_id,c.created_at,next.status='executing'`, token).Scan(&c.ID, &c.RunID, &c.Kind, &c.JobID, &c.ActorID, &c.CreatedAt, &c.Recovery)
	c.LeaseToken = token
	c.Status = "executing"
	return c, err
}
func projectTx(ctx context.Context, tx pgx.Tx, id string, projection Projection) error {
	var app string
	var status string
	var previous []byte
	if err := tx.QueryRow(ctx, "SELECT application_id,projection,status FROM delivery_runs WHERE id=$1 FOR UPDATE", id).Scan(&app, &previous, &status); err != nil {
		return err
	}
	var old Projection
	_ = json.Unmarshal(previous, &old)
	if status == "cancel_requested" && !terminal(projection.Status) {
		projection.Status = "cancel_requested"
	}
	projection.ObservedAt = time.Now().UTC()
	body, _ := json.Marshal(projection)
	_, err := tx.Exec(ctx, "UPDATE delivery_runs SET status=$2,external_id=CASE WHEN $3='' THEN external_id ELSE $3 END,projection=$4,updated_at=now() WHERE id=$1", id, projection.Status, projection.ExternalID, body)
	if err != nil {
		return err
	}
	old.ObservedAt = time.Time{}
	compare := projection
	compare.ObservedAt = time.Time{}
	if hash(old) != hash(compare) {
		return eventTx(ctx, tx, id, app, "delivery.run.observed", "", projection)
	}
	return nil
}
func (p *PostgresRepository) Project(ctx context.Context, id string, projection Projection) error {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = projectTx(ctx, tx, id, projection); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p *PostgresRepository) Finish(ctx context.Context, c Command, projection *Projection, problem string) error {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	status := "completed"
	if problem != "" {
		status = "needs_attention"
	}
	tag, err := tx.Exec(ctx, "UPDATE delivery_commands SET status=$3,error=$4,lease_until=NULL,lease_token=NULL WHERE id=$1 AND lease_token=$2 AND lease_until>now()", c.ID, c.LeaseToken, status, problem)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return store.ErrLease
	}
	if projection != nil {
		if err = projectTx(ctx, tx, c.RunID, *projection); err != nil {
			return err
		}
	}
	var app string
	if err = tx.QueryRow(ctx, "SELECT application_id FROM delivery_runs WHERE id=$1", c.RunID).Scan(&app); err != nil {
		return err
	}
	if err = eventTx(ctx, tx, c.RunID, app, "delivery.command."+status, c.ActorID, map[string]any{"command_id": c.ID, "kind": c.Kind, "message": problem}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p *PostgresRepository) Events(ctx context.Context, id string, after int64) ([]Event, error) {
	rows, err := p.DB.Query(ctx, "SELECT sequence,run_id,type,actor_id,payload,created_at FROM delivery_events WHERE run_id=$1 AND sequence>$2 ORDER BY sequence LIMIT 200", id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Sequence, &e.RunID, &e.Type, &e.ActorID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (p *PostgresRepository) Commands(ctx context.Context, id string) ([]Command, error) {
	rows, err := p.DB.Query(ctx, "SELECT id,run_id,kind,job_id,status,actor_id,error,created_at FROM delivery_commands WHERE run_id=$1 ORDER BY created_at DESC LIMIT 100", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Command{}
	for rows.Next() {
		var c Command
		if err = rows.Scan(&c.ID, &c.RunID, &c.Kind, &c.JobID, &c.Status, &c.ActorID, &c.Error, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (p *PostgresRepository) RecordWebhook(ctx context.Context, source, event string, project int64, external string) (bool, error) {
	tag, err := p.DB.Exec(ctx, "INSERT INTO delivery_webhook_inbox(source_id,event_id,project_id,external_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", source, event, project, external)
	return tag.RowsAffected() > 0, err
}
func (p *PostgresRepository) GitLabRuns(ctx context.Context, source string, project int64, external string) ([]Run, error) {
	rows, err := p.DB.Query(ctx, "SELECT "+runColumns+" FROM delivery_runs WHERE provider='gitlab' AND external_id=$3 AND snapshot->'definition'->>'source_id'=$1 AND (snapshot->'definition'->>'project_id')::bigint=$2", source, project, external)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *PostgresRepository) ImportRun(ctx context.Context, pipe Pipeline, projection Projection, actor, key string) (Run, error) {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback(ctx)
	id := store.ID()
	snapshot, _ := json.Marshal(pipe)
	raw, _ := json.Marshal(projection)
	_, err = tx.Exec(ctx, `INSERT INTO delivery_runs(id,pipeline_id,version,application_id,environment_id,provider,status,external_id,snapshot,projection,created_by,idempotency_key,request_digest) VALUES($1,$2,$3,$4,$5,'gitlab',$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`, id, pipe.ID, pipe.Version, pipe.ApplicationID, pipe.EnvironmentID, projection.Status, projection.ExternalID, snapshot, raw, actor, key, hash(projection.ExternalID))
	if err != nil {
		return Run{}, err
	}
	var found string
	if err = tx.QueryRow(ctx, "SELECT id FROM delivery_runs WHERE provider='gitlab' AND snapshot->'definition'->>'source_id'=$1 AND snapshot->'definition'->>'project_id'=$2 AND external_id=$3", pipe.Definition.SourceID, fmt.Sprint(pipe.Definition.ProjectID), projection.ExternalID).Scan(&found); err != nil {
		return Run{}, err
	}
	if found == id {
		if err = eventTx(ctx, tx, id, pipe.ApplicationID, "delivery.run.imported", actor, projection); err != nil {
			return Run{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Run{}, err
	}
	return p.GetRun(ctx, found)
}
