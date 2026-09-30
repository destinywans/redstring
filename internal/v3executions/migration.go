package v3executions

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

// Migrate requires the legacy actions repository migration to have created
// executions and investigation_reports. It does not run the migration itself.
func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	if db == nil {
		return errors.New("execution storage is unavailable")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018994)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) Migrate(ctx context.Context) error {
	if s == nil {
		return errors.New("execution storage is unavailable")
	}
	return Migrate(ctx, s.DB)
}
