package v3events

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// authorizeAlertTx binds the request to the incident's current persisted
// application, environment, owner team, session, and V3 grants. All locks are
// held through the caller's commit; authorization must be the final decision
// after any wait for the incident row.
func (s *Service) authorizeAlertTx(r *http.Request, tx pgx.Tx, v incidentRecord, action string) error {
	if tx == nil || s.AuthorizeTx == nil {
		return store.ErrForbidden
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || (action != "view" && (len(scope.EnvironmentIDs) != 1 || scope.All)) {
		return store.ErrNotFound
	}

	var environment, ownerTeam string
	err := tx.QueryRow(r.Context(), `SELECT e.key,COALESCE(a.owner_team_id,'')
		FROM applications a
		JOIN application_environments e ON e.application_id=a.id AND e.id=$2 AND NOT e.archived
		JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=e.key
		WHERE a.id=$1 AND NOT a.archived
		FOR SHARE OF a,e,ve`, v.ApplicationID, v.EnvironmentID).Scan(&environment, &ownerTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	selected := false
	for _, id := range scope.EnvironmentIDs {
		selected = selected || id == environment
	}
	if environment != v.EnvironmentKey || !selected {
		return store.ErrNotFound
	}

	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	user := httpapi.CurrentUser(r)
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		return err
	}
	if action == "change" {
		var human bool
		if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human); err != nil {
			return err
		}
		if !human {
			return store.ErrForbidden
		}
	}
	// An existing matching grant row is not enough: a new role assignment can
	// become a phantom, and a revoke can commit after a plain SELECT. These
	// short SHARE locks serialize both changes with the final policy decision.
	if _, err = tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return err
	}
	err = s.AuthorizeTx(r.Context(), tx, user, v.ApplicationID, environment, action)
	if !errors.Is(err, store.ErrForbidden) || ownerTeam == "" || s.AuthorizeTeamTx == nil {
		return err
	}
	return s.AuthorizeTeamTx(r.Context(), tx, user, ownerTeam, environment, action)
}

func respondAlertAuthorityError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return
	}
	respondErr(w, err)
}
