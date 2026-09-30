package v3knowledge

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Pin the role assignment and grant rows used by a Runbook write until commit.
// This serializes concurrent grant removal with the final authorization read.
func lockRunbookGrantSources(ctx context.Context, tx pgx.Tx, userID string) error {
	roles := map[string]bool{}
	for _, query := range []string{
		`SELECT role_id FROM rbac_user_roles WHERE user_id=$1 FOR SHARE`,
		`SELECT dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id WHERE u.id=$1 FOR SHARE OF dr`,
	} {
		rows, err := tx.Query(ctx, query, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			roles[id] = true
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	for _, query := range []string{
		`SELECT id FROM rbac_roles WHERE id=ANY($1::text[]) FOR SHARE`,
		`SELECT role_id FROM v3_role_grants WHERE role_id=ANY($1::text[]) FOR SHARE`,
	} {
		rows, err := tx.Query(ctx, query, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
