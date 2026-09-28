package v3postmortems

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Publish completes a human review. A draft's author cannot approve their own
// report, and only a report tied to the original verified close can advance.
func (s *Service) Publish(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !bounded(id, 128) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem review is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	var incident, investigation, app, team, env, status, author string
	var validClose bool
	err = tx.QueryRow(r.Context(), `SELECT pm.incident_id,pm.investigation_id,pm.application_id,COALESCE(a.owner_team_id,''),
 pm.environment_id,pm.status,pm.created_by,
 (i.status='resolved' AND inv.status='closed' AND i.investigation_id=pm.investigation_id
  AND EXISTS(SELECT 1 FROM events ev WHERE ev.seq=pm.close_event_seq
   AND ev.investigation_id=pm.investigation_id AND ev.type='investigation.closed'))
 FROM v3_postmortems pm
 JOIN ops_incidents i ON i.id=pm.incident_id AND i.application_id=pm.application_id
 JOIN investigations inv ON inv.id=pm.investigation_id AND inv.application_id=pm.application_id
 JOIN applications a ON a.id=pm.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=pm.application_id
  AND e.key=pm.environment_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=pm.application_id AND ve.environment_id=pm.environment_id
 WHERE pm.id=$1 FOR UPDATE OF pm,i,inv`, id).
		Scan(&incident, &investigation, &app, &team, &env, &status, &author, &validClose)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, env) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem review is unavailable")
		return
	}
	user := httpapi.CurrentUser(r)
	if err = s.authorizeTx(r.Context(), tx, user, app, team, env, "manage"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Postmortem not found")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		}
		return
	}
	if author == user.ID {
		httpapi.Error(w, 403, "self_review_forbidden", "A second reviewer must publish this postmortem")
		return
	}
	if status != "review" || !validClose {
		httpapi.Error(w, 409, "invalid_state", "Postmortem is not ready for publication")
		return
	}
	// Hold the report row through commit so a concurrent deletion cannot remove
	// the evidence after this check and before publication.
	var reportInvestigation string
	err = tx.QueryRow(r.Context(), `SELECT investigation_id FROM investigation_reports
 WHERE investigation_id=$1 FOR SHARE`, investigation).Scan(&reportInvestigation)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "invalid_state", "Postmortem is not ready for publication")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem review is unavailable")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE v3_postmortems SET status='published',updated_at=now()
 WHERE id=$1 AND status='review'`, id)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'postmortem.published',$3,'succeeded',
  jsonb_build_object('incident_id',$4::text,'environment_id',$5::text))`,
			user.ID, app, id, incident, env)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not publish postmortem")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]string{"id": id, "status": "published"})
}
