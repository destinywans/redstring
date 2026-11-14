package v3events

import (
	"errors"
	"net/http"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	investigateChange    = "investigate:change"
	legacyTimelineChange = "timeline:change"
)

// admitAlertScope checks the same persisted application/team scope as the
// final alert policy without acquiring any row or grant-source locks. Keep the
// final locked authorization in the caller: this check only permits waiting
// for a target row and cannot authorize a response or mutation.
func (s *Service) admitAlertScope(r *http.Request, tx pgx.Tx, v incidentRecord, ownerTeam string, actions ...string) error {
	if s.AuthorizeTx == nil {
		return store.ErrForbidden
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		return store.ErrNotFound
	}
	selected := false
	for _, env := range scope.EnvironmentIDs {
		selected = selected || env == v.EnvironmentKey
	}
	if !selected {
		return store.ErrNotFound
	}
	user := httpapi.CurrentUser(r)
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		return err
	}
	for _, action := range actions {
		if action == legacyTimelineChange {
			// The legacy alert-backed timeline write uses app scope only.
			return s.scopedTx(r, tx, v.ApplicationID, v.EnvironmentKey, "change")
		}
		investigate := action == investigateChange
		if investigate {
			action = "change"
		}
		if action != "view" {
			if scope.All || len(scope.EnvironmentIDs) != 1 {
				return store.ErrNotFound
			}
			var human bool
			err := tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !human {
				if investigate {
					return errInvestigationForbidden
				}
				return store.ErrForbidden
			}
			if err != nil {
				return err
			}
		}
		err := s.AuthorizeTx(r.Context(), tx, user, v.ApplicationID, v.EnvironmentKey, action)
		if errors.Is(err, store.ErrForbidden) && ownerTeam != "" && s.AuthorizeTeamTx != nil {
			err = s.AuthorizeTeamTx(r.Context(), tx, user, ownerTeam, v.EnvironmentKey, action)
		}
		if investigate && err == nil {
			err = s.authorizeCanvasActionTx(r, tx, v.ApplicationID, v.EnvironmentKey, ownerTeam, "change")
		}
		if investigate && errors.Is(err, store.ErrForbidden) {
			return errInvestigationForbidden
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) lockAdmittedAlert(r *http.Request, tx pgx.Tx, id string, write, includeSuspected bool, actions ...string) (incidentRecord, error) {
	var v incidentRecord
	var team string
	err := tx.QueryRow(r.Context(), `SELECT i.application_id,i.environment_id,e.key,COALESCE(a.owner_team_id,'')
 FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 WHERE i.id=$1 AND ($2 OR NOT i.suspected)`, id, includeSuspected).Scan(&v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey, &team)
	if err != nil {
		return incidentRecord{}, err
	}
	if err := s.admitAlertScope(r, tx, v, team, actions...); err != nil {
		return incidentRecord{}, err
	}
	lock := " FOR SHARE OF i"
	if write {
		lock = " FOR UPDATE OF i"
	}
	return scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1 AND ($2 OR NOT i.suspected)
 AND i.application_id=$3 AND i.environment_id=$4 AND e.key=$5`+lock,
		id, includeSuspected, v.ApplicationID, v.EnvironmentID, v.EnvironmentKey))
}

func (s *Service) lockAdmittedCase(r *http.Request, tx pgx.Tx, id string, write bool, actions ...string) (caseRecord, error) {
	var v incidentRecord
	var team string
	err := tx.QueryRow(r.Context(), `SELECT c.application_id,c.environment_id,e.key,COALESCE(a.owner_team_id,'')
 FROM v3_incident_cases c
 JOIN application_environments e ON e.id=c.environment_id AND e.application_id=c.application_id
   AND e.key=c.environment_key AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=c.application_id AND ve.environment_id=e.key
 JOIN applications a ON a.id=c.application_id AND NOT a.archived
 WHERE c.id=$1`, id).Scan(&v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey, &team)
	if err != nil {
		return caseRecord{}, err
	}
	if err := s.admitAlertScope(r, tx, v, team, actions...); err != nil {
		return caseRecord{}, err
	}
	lock := " FOR SHARE OF c"
	if write {
		lock = " FOR UPDATE OF c"
	}
	result, err := scanCase(tx.QueryRow(r.Context(), caseSelect+` WHERE c.id=$1
 AND c.application_id=$2 AND c.environment_id=$3 AND c.environment_key=$4`+lock,
		id, v.ApplicationID, v.EnvironmentID, v.EnvironmentKey))
	if errors.Is(err, pgx.ErrNoRows) {
		// The case existed during admission. Do not fall through to an alert
		// with the same ID when its scope changed while waiting.
		return caseRecord{}, store.ErrNotFound
	}
	return result, err
}
