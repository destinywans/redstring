package v3integrations

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// lockConnectorAuthority pins the actor, role assignments, relevant grants and
// live session while a connector write is in flight. A revoke committed
// while the write waited for a connector row is visible to the following policy check;
// a later revoke waits for this transaction to commit or roll back.
// The final permission witness must come from the locked grant rows: a new
// role assignment inserted after the assignment scan cannot authorize this
// write without also being part of its locked witness. Role rows precede
// assignment rows to match the role-management lock order.
func lockConnectorAuthority(ctx context.Context, tx pgx.Tx, r *http.Request, environment string) error {
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
	before, err := connectorActorRoles(ctx, tx, actorID, false)
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
	after, err := connectorActorRoles(ctx, tx, actorID, true)
	if err != nil {
		return err
	}
	if !slices.Equal(before, after) {
		return store.ErrForbidden
	}
	rows, err = tx.Query(ctx, `SELECT role_id,module,action,scope_kind,scope_id FROM v3_role_grants
 WHERE role_id=ANY($1::text[])
 AND ((module='environment' AND action='view')
   OR (module='connectors' AND action='manage'))
 ORDER BY role_id,module,action,scope_kind,scope_id FOR SHARE`, before)
	if err != nil {
		return err
	}
	var environmentView, connectorManage bool
	for rows.Next() {
		var roleID, module, action, scopeKind, scopeID string
		if err = rows.Scan(&roleID, &module, &action, &scopeKind, &scopeID); err != nil {
			break
		}
		if !((scopeKind == "global" && scopeID == "*") || (scopeKind == "environment" && scopeID == environment)) {
			continue
		}
		if module == "environment" && action == "view" {
			environmentView = true
		}
		if module == "connectors" && action == "manage" {
			connectorManage = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	// Account writers update users before deleting sessions. Keep the same
	// user-before-session order so account disable cannot deadlock this write.
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return store.ErrForbidden
	}
	var sessionUserID string
	err = tx.QueryRow(ctx, `SELECT user_id FROM sessions
 WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp()
 FOR SHARE`, store.Hash(cookie.Value), actorID).Scan(&sessionUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	if !environmentView || !connectorManage {
		return store.ErrForbidden
	}
	return nil
}

func connectorActorRoles(ctx context.Context, tx pgx.Tx, actorID string, lock bool) ([]string, error) {
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

func rollbackConnectorWrite(tx pgx.Tx) {
	// The request context may be canceled while a lock wait is in progress.
	// Release any acquired rows with an independent bounded cleanup context.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
