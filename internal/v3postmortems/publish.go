package v3postmortems

import (
	"errors"
	"net/http"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// Publish verifies the saved source against the current lifecycle under the
// incident owner's lock. A different human must approve the review.
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
	if err = postmortemReadSession(r.Context(), tx, r); err == nil {
		err = lockPostmortemReadGrants(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		postmortemReadError(w, err)
		return
	}
	var incident, app, env, status, author, kind string
	var investigation *string
	var closeSeq, statusEventID *int64
	err = tx.QueryRow(r.Context(), `SELECT incident_id,application_id,environment_id,status,created_by,
 source_kind,investigation_id,close_event_seq,source_case_status_event_id
 FROM v3_postmortems WHERE id=$1`, id).
		Scan(&incident, &app, &env, &status, &author, &kind, &investigation, &closeSeq, &statusEventID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, env) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem review is unavailable")
		return
	}
	source, err := lockSource(r.Context(), tx, incident)
	if errors.Is(err, pgx.ErrNoRows) {
		postmortemSourceChanged(w)
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem source is unavailable")
		return
	}
	// Match Create's source-then-record lock order. Re-read the record after
	// locking it so a concurrent publish cannot reuse a stale review state.
	err = tx.QueryRow(r.Context(), `SELECT status,created_by,source_kind,investigation_id,
 close_event_seq,source_case_status_event_id FROM v3_postmortems WHERE id=$1 FOR UPDATE`, id).
		Scan(&status, &author, &kind, &investigation, &closeSeq, &statusEventID)
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem review is unavailable")
		return
	}
	if source.app != app || source.env != env || !inScope(scope, source.env) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	user := httpapi.CurrentUser(r)
	if err = s.authorizeTx(r.Context(), tx, user, app, source.team, env, "manage"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Postmortem not found")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		}
		return
	}
	// LockActiveSession already holds the current user row through this
	// transaction. Re-read its persisted identity before recording a review.
	var human bool
	if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human); err != nil {
		httpapi.Error(w, 503, "authorization_unavailable", "Reviewer identity could not be checked")
		return
	}
	if !human {
		httpapi.Error(w, 403, "human_required", "A human reviewer must publish this postmortem")
		return
	}
	if author == user.ID {
		httpapi.Error(w, 403, "self_review_forbidden", "A second reviewer must publish this postmortem")
		return
	}
	if status != "review" {
		httpapi.Error(w, 409, "invalid_state", "Postmortem is not ready for publication")
		return
	}
	if !source.eligible || source.kind != kind || source.closeSeq == nil && closeSeq != nil ||
		source.closeSeq != nil && (closeSeq == nil || *source.closeSeq != *closeSeq) ||
		source.statusEventID == nil && statusEventID != nil ||
		source.statusEventID != nil && (statusEventID == nil || *source.statusEventID != *statusEventID) ||
		kind == "ops" && (investigation == nil || *investigation != source.investigation) {
		postmortemSourceChanged(w)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE v3_postmortems SET status='published',updated_at=now()
 WHERE id=$1 AND status='review'`, id)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'postmortem.published',$3,'succeeded',
 jsonb_build_object('incident_id',$4::text,'environment_id',$5::text,
 'source_kind',$6::text,'source_revision',$7::text))`,
			user.ID, app, id, incident, env, kind, source.revision())
	}
	if err == nil {
		err = postmortemReadSession(r.Context(), tx, r)
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
