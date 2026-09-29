package v3alertmanager

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// inhibitionFinalAuthority locks the currently selected application, session,
// and role sources in one snapshot before a preview or draft response. A
// rule crosses alert identities, so an app/team grant is insufficient here.
func inhibitionFinalAuthority(r *http.Request, s *Service, env string, expected Application) error {
	db, access := routeTestAuthority(s.Catalog, s.Access)
	if db == nil || access == nil {
		return errors.New("inhibition authority snapshot unavailable")
	}
	tx, err := db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	if err := lockInhibitionAuthority(r, tx, access, env, expected); err != nil {
		return err
	}
	return tx.Commit(r.Context())
}

func lockInhibitionAuthority(r *http.Request, tx pgx.Tx, access routeTxAccess, env string, expected Application) error {
	ctx := r.Context()
	var current Application
	err := tx.QueryRow(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,'')
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND ve.environment_id=$2 AND NOT a.archived FOR SHARE OF a,ve,e`, expected.ID, env).
		Scan(&current.ID, &current.EnvironmentID, &current.OwnerTeamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && current != expected {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	user := httpapi.CurrentUser(r)
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,rbac_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return err
	}
	for _, action := range []string{"view", "manage"} {
		if err = access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "silences", Action: action, EnvironmentID: env}); err != nil {
			return err
		}
	}
	var administrator, unexpired bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$1 AND g.module='silences' AND g.action='manage'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR
      (g.scope_kind='environment' AND g.scope_id=$2)))`, user.ID, env).Scan(&administrator)
	if err != nil {
		return err
	}
	if !administrator {
		return store.ErrForbidden
	}
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions WHERE token_hash=$1 AND user_id=$2`,
		store.Hash(cookie.Value), user.ID).Scan(&unexpired)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !unexpired {
		return store.ErrSessionRevoked
	}
	return err
}

func writeInhibitionAuthorityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Inhibition access changed")
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, http.StatusNotFound, "application_not_found", "Bound application is unavailable")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_scope_changed", "Inhibition ownership or authorization changed")
	}
}
