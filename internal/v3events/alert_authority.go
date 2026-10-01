package v3events

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// listAuthorityGuard belongs to one top-level read transaction and request.
// It remembers held locks, never authorization results. Both list helpers make
// a new guard on every invocation, including the policy's second transaction;
// none of these callers releases locks through savepoints.
type listAuthorityGuard struct {
	tx                      pgx.Tx
	request                 *http.Request
	actorID, tokenHash      string
	sessionHeld, grantsHeld bool
}

func newListAuthorityGuard(r *http.Request, tx pgx.Tx) *listAuthorityGuard {
	g := &listAuthorityGuard{tx: tx, request: r, actorID: httpapi.CurrentUser(r).ID}
	if cookie, err := r.Cookie("aiops_session"); err == nil {
		g.tokenHash = store.Hash(cookie.Value)
	}
	return g
}

func (g *listAuthorityGuard) matches(r *http.Request, tx pgx.Tx) bool {
	cookie, err := r.Cookie("aiops_session")
	return err == nil && g.tx == tx && g.request == r && g.actorID != "" &&
		g.actorID == httpapi.CurrentUser(r).ID && g.tokenHash == store.Hash(cookie.Value)
}

// authorizeAlertTx binds the request to the incident's current persisted
// application, environment, owner team, session, and V3 grants. All locks are
// held through the caller's commit; authorization must be the final decision
// after any wait for the incident row.
func (s *Service) authorizeAlertTx(r *http.Request, tx pgx.Tx, v incidentRecord, action string) error {
	return s.authorizeAlertWithGuard(r, tx, v, action, nil)
}

// A non-nil guard is private to the two read helpers. The ordinary authority
// path, including all writes, takes the original locks on every call.
func (s *Service) authorizeAlertWithGuard(r *http.Request, tx pgx.Tx, v incidentRecord, action string, guard *listAuthorityGuard) error {
	if guard != nil && (action != "view" || !guard.matches(r, tx)) {
		return store.ErrForbidden
	}
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
	if guard != nil && guard.sessionHeld {
		// App/environment locks above can wait. Row locks prevent revocation but
		// not natural expiry, so every reused proof still checks the current DB clock.
		if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
			return err
		}
	} else {
		if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
			return err
		}
		if guard != nil {
			guard.sessionHeld = true
		}
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
	if guard == nil || !guard.grantsHeld {
		if _, err = tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
			return err
		}
		if guard != nil {
			guard.grantsHeld = true
		}
	}
	authorize := s.AuthorizeTx
	if guard != nil && s.AuthorizeListTx != nil {
		authorize = s.AuthorizeListTx
	}
	err = authorize(r.Context(), tx, user, v.ApplicationID, environment, action)
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
