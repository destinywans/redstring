package aiextensions

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGRepository struct{ DB *pgxpool.Pool }

func New(db *pgxpool.Pool, credentialRoot string) *Service {
	return &Service{Repository: &PGRepository{DB: db}, CredentialRoot: credentialRoot}
}
func (s *Service) Migrate(ctx context.Context) error {
	return s.Repository.(*PGRepository).Migrate(ctx)
}
func (p *PGRepository) Migrate(ctx context.Context) error {
	tx, e := p.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(752018993)`); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS ai_extensions(id text PRIMARY KEY,application_id text REFERENCES applications(id),kind text NOT NULL CHECK(kind IN ('mcp','skill','prompt')),version integer NOT NULL,record jsonb NOT NULL,updated_at timestamptz NOT NULL DEFAULT now());
 CREATE INDEX IF NOT EXISTS ai_extensions_scope ON ai_extensions(application_id,kind);
 CREATE TABLE IF NOT EXISTS ai_extension_revisions(id text NOT NULL REFERENCES ai_extensions(id),version integer NOT NULL,record jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(id,version));
 ALTER TABLE ai_extensions ALTER COLUMN application_id DROP NOT NULL;
 DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='ai_extensions'::regclass AND conname='ai_extensions_workspace_scope') THEN
 ALTER TABLE ai_extensions ADD CONSTRAINT ai_extensions_workspace_scope CHECK (application_id IS NOT NULL OR kind IN ('skill','prompt'));
 END IF;
 END $$;`)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (p *PGRepository) List(ctx context.Context, app, kind string) ([]Record, error) {
	rows, e := p.DB.Query(ctx, "SELECT record FROM ai_extensions WHERE application_id IS NOT DISTINCT FROM NULLIF($1, '') AND kind=$2 ORDER BY updated_at DESC LIMIT 200", app, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var raw []byte
		var r Record
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (p *PGRepository) Get(ctx context.Context, id string, v int) (Record, error) {
	var raw []byte
	var e error
	if v == 0 {
		e = p.DB.QueryRow(ctx, "SELECT record FROM ai_extensions WHERE id=$1", id).Scan(&raw)
	} else {
		e = p.DB.QueryRow(ctx, "SELECT record FROM ai_extension_revisions WHERE id=$1 AND version=$2", id, v).Scan(&raw)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return Record{}, store.ErrNotFound
	}
	if e != nil {
		return Record{}, e
	}
	var r Record
	e = json.Unmarshal(raw, &r)
	return r, e
}
func (p *PGRepository) Save(ctx context.Context, r Record, expected int) (Record, error) {
	tx, e := p.DB.Begin(ctx)
	if e != nil {
		return Record{}, e
	}
	defer tx.Rollback(ctx)
	if e = tx.QueryRow(ctx, "SELECT now()").Scan(&r.UpdatedAt); e != nil {
		return Record{}, e
	}
	raw, e := json.Marshal(r)
	if e != nil {
		return Record{}, e
	}
	if expected == 0 {
		_, e = tx.Exec(ctx, "INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES($1,NULLIF($2, ''),$3,$4,$5)", r.ID, r.ApplicationID, r.Kind, r.Version, raw)
	} else {
		var n int
		e = tx.QueryRow(ctx, "UPDATE ai_extensions SET version=$2,record=$3,updated_at=now() WHERE id=$1 AND version=$4 AND application_id IS NOT DISTINCT FROM NULLIF($5, '') AND kind=$6 RETURNING version", r.ID, r.Version, raw, expected, r.ApplicationID, r.Kind).Scan(&n)
		if errors.Is(e, pgx.ErrNoRows) {
			return Record{}, store.ErrConflict
		}
	}
	if e != nil {
		return Record{}, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO ai_extension_revisions(id,version,record) VALUES($1,$2,$3)", r.ID, r.Version, raw); e != nil {
		return Record{}, e
	}
	// Audit only identities/versions; never include endpoint credentials or prompt text.
	changes, _ := json.Marshal(map[string]any{"kind": r.Kind, "version": r.Version, "enabled": r.Enabled, "content_hash": r.ContentHash})
	if _, e = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'ai_extension.saved',$3,'success',$4)", store.Principal(ctx).ID, r.ApplicationID, r.ID, changes); e != nil {
		return Record{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Record{}, e
	}
	return r, nil
}
