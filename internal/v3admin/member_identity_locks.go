package v3admin

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// memberIdentityLocks serializes invitation account creation with account,
// association, and role-matrix writers in their established lock order.
func memberIdentityLocks(ctx context.Context, tx pgx.Tx, exclusive bool) error {
	query := `SELECT pg_advisory_xact_lock_shared($1)`
	if exclusive {
		query = `SELECT pg_advisory_xact_lock($1)`
	}
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, query, key); err != nil {
			return err
		}
	}
	return nil
}
