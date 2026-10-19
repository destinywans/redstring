package v3mcp

import (
	"context"
	"errors"
	"slices"
	"sort"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// lockReviewerGrantSources pins the current actor and the existing rows from
// which V3 environment admission and connectors.manage can derive. A revoke
// that wins first is visible to the following policy reads; a later revoke
// waits until this review/switch transaction commits. These are actor-scoped
// row locks, not global role-management locks.
//
// Legacy association writers lock a role before deleting its assignments.
// Keep that role-before-assignment order here to avoid a lock cycle with them.
func lockReviewerGrantSources(ctx context.Context, tx pgx.Tx, actorID string) error {
	if err := lockReviewerAccount(ctx, tx, actorID); err != nil {
		return err
	}
	return lockReviewerRoleGrantSources(ctx, tx, actorID)
}

func lockReviewerAccount(ctx context.Context, tx pgx.Tx, actorID string) error {
	var activeID string
	err := tx.QueryRow(ctx, `SELECT id FROM users
 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo'
 FOR SHARE`, actorID).Scan(&activeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	return nil
}

func lockReviewerRoleGrantSources(ctx context.Context, tx pgx.Tx, actorID string) error {
	before, err := reviewerRoleIDs(ctx, tx, actorID, false)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM rbac_roles WHERE id=ANY($1::text[])
 ORDER BY id FOR SHARE`, before)
	if err != nil {
		return err
	}
	lockedRoles := 0
	for rows.Next() {
		var ignored string
		if err = rows.Scan(&ignored); err != nil {
			break
		}
		lockedRoles++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if lockedRoles != len(before) {
		return store.ErrForbidden
	}
	after, err := reviewerRoleIDs(ctx, tx, actorID, true)
	if err != nil {
		return err
	}
	if !slices.Equal(before, after) {
		return store.ErrForbidden
	}
	rows, err = tx.Query(ctx, `SELECT role_id FROM v3_role_grants
 WHERE role_id=ANY($1::text[])
 AND ((module='environment' AND action='view')
   OR (module='connectors' AND action IN ('view','manage')))
 ORDER BY role_id,module,action,scope_kind,scope_id FOR SHARE`, before)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ignored string
		if err = rows.Scan(&ignored); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return err
}

func reviewerRoleIDs(ctx context.Context, tx pgx.Tx, actorID string, lock bool) ([]string, error) {
	statements := []string{
		`SELECT role_id FROM rbac_user_roles WHERE user_id=$1 ORDER BY role_id`,
		`SELECT dr.role_id FROM users u JOIN rbac_department_roles dr
 ON dr.department_id=u.department_id WHERE u.id=$1 ORDER BY dr.role_id`,
	}
	if lock {
		statements[0] += ` FOR SHARE`
		statements[1] += ` FOR SHARE OF dr`
	}
	roles := map[string]bool{}
	for _, statement := range statements {
		rows, err := tx.Query(ctx, statement, actorID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var roleID string
			if err = rows.Scan(&roleID); err != nil {
				break
			}
			roles[roleID] = true
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
