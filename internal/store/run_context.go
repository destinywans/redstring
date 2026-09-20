package store

import (
	"context"
	"encoding/json"
)

func (s *Store) SaveRunContext(ctx context.Context, c Claim, manifest json.RawMessage) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = fence(ctx, tx, c); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "INSERT INTO run_context_manifests(attempt_id,run_id,manifest) VALUES($1,$2,$3) ON CONFLICT(attempt_id) DO NOTHING", c.Token, c.Run.ID, manifest)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) RunContext(ctx context.Context, id string) (json.RawMessage, error) {
	var raw []byte
	e := s.DB.QueryRow(ctx, "SELECT manifest FROM run_context_manifests WHERE run_id=$1 ORDER BY created_at DESC LIMIT 1", id).Scan(&raw)
	return raw, ClassifyError(e)
}
