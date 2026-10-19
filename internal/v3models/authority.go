package v3models

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// lockModelAuthority pins the actor, live session and the grant rows used by
// the final permission witness. Revocation that wins the lock is seen before
// the observation read/write; later revocation waits for this transaction.
func lockModelAuthority(ctx context.Context, tx pgx.Tx, r *http.Request, environment, action string) error {
	if action != "view" && action != "manage" {
		return store.ErrForbidden
	}
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			return err
		}
	}
	actorID := httpapi.CurrentUser(r).ID
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
	before, err := modelActorRoles(ctx, tx, actorID, false)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM rbac_roles WHERE id=ANY($1::text[])
 ORDER BY id FOR SHARE`, before)
	if err != nil {
		return err
	}
	locked := 0
	for rows.Next() {
		var ignored string
		if err = rows.Scan(&ignored); err != nil {
			break
		}
		locked++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if locked != len(before) {
		return store.ErrForbidden
	}
	after, err := modelActorRoles(ctx, tx, actorID, true)
	if err != nil {
		return err
	}
	if !slices.Equal(before, after) {
		return store.ErrForbidden
	}
	rows, err = tx.Query(ctx, `SELECT role_id,module,action,scope_kind,scope_id FROM v3_role_grants
 WHERE role_id=ANY($1::text[])
 AND ((module='environment' AND action='view')
   OR (module='models' AND action=$2))
 ORDER BY role_id,module,action,scope_kind,scope_id FOR SHARE`, before, action)
	if err != nil {
		return err
	}
	var environmentView, modelAllowed bool
	for rows.Next() {
		var roleID, module, grantAction, scopeKind, scopeID string
		if err = rows.Scan(&roleID, &module, &grantAction, &scopeKind, &scopeID); err != nil {
			break
		}
		if !((scopeKind == "global" && scopeID == "*") || (scopeKind == "environment" && scopeID == environment)) {
			continue
		}
		if module == "environment" && grantAction == "view" {
			environmentView = true
		}
		if module == "models" && grantAction == action {
			modelAllowed = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return store.ErrSessionRevoked
	}
	if err = store.LockActiveSession(ctx, tx, cookie.Value, actorID); err != nil {
		return err
	}
	if !environmentView || !modelAllowed {
		return store.ErrForbidden
	}
	return nil
}

func modelActorRoles(ctx context.Context, tx pgx.Tx, actorID string, lock bool) ([]string, error) {
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
