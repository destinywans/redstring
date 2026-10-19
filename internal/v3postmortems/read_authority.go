package v3postmortems

import (
	"context"
	"errors"
	"net/http"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

func postmortemReadSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	return store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID)
}

func postmortemReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return
	}
	httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Postmortem access could not be checked")
}

// The effective V3 grants view depends on the current account's assigned
// roles, department roles and their grant rows. Hold those source rows during
// the read so revocation cannot commit between authorization and response.
func lockPostmortemReadGrants(ctx context.Context, tx pgx.Tx, userID string) error {
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

func (s *Service) beginPostmortemRead(w http.ResponseWriter, r *http.Request, ctx context.Context) (pgx.Tx, bool) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "postmortems_unavailable", "Postmortem evidence is unavailable")
		return nil, false
	}
	if err = postmortemReadSession(ctx, tx, r); err == nil {
		err = lockPostmortemReadGrants(ctx, tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		postmortemReadError(w, err)
		return nil, false
	}
	return tx, true
}
