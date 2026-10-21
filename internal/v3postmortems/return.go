package v3postmortems

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// Return sends a postmortem in review back to draft with the reviewer's
// reason (PA-128). The reviewer is whoever could publish it: a human with
// knowledge.manage who is not the author. The reason is kept in the audit row
// and shown on the draft until it is submitted again.
func (s *Service) Return(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !bounded(id, 128) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&in)
	reason := strings.TrimSpace(in.Reason)
	if err != nil || decoder.Decode(new(any)) != io.EOF || reason == "" || utf8.RuneCountInString(reason) > 2000 {
		httpapi.Error(w, 422, "invalid_return", "Send the reason for returning it, 1-2000 characters")
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
	v, err := readSavedPostmortem(r.Context(), tx, id, false)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, v.env) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem review is unavailable")
		return
	}
	// The application row is only shared, as in Publish; then the record.
	team, err := postmortemTeam(r.Context(), tx, v.app)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Postmortem not found")
		return
	}
	if err == nil {
		v, err = readSavedPostmortem(r.Context(), tx, id, true)
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem review is unavailable")
		return
	}
	user := httpapi.CurrentUser(r)
	if err = s.authorizeTx(r.Context(), tx, user, v.app, team, v.env, "manage"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Postmortem not found")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		}
		return
	}
	var human bool
	if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human); err != nil {
		httpapi.Error(w, 503, "authorization_unavailable", "Reviewer identity could not be checked")
		return
	}
	if !human {
		httpapi.Error(w, 403, "human_required", "A human reviewer must return this postmortem")
		return
	}
	if v.author == user.ID {
		httpapi.Error(w, 403, "self_review_forbidden", "A second reviewer must return this postmortem")
		return
	}
	if v.Status != "review" {
		httpapi.Error(w, 409, "invalid_state", "Only a postmortem in review can be returned")
		return
	}
	var updated time.Time
	err = tx.QueryRow(r.Context(), `UPDATE v3_postmortems SET status='draft',revision=revision+1,updated_at=now()
 WHERE id=$1 AND status='review' RETURNING revision,updated_at`, id).Scan(&v.Revision, &updated)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'postmortem.returned',$3,'succeeded',jsonb_build_object('incident_id',$4::text,'environment_id',$5::text,
 'status','draft','revision',$6::bigint,'reason',$7::text))`,
			user.ID, v.app, id, v.IncidentID, v.env, v.Revision, reason)
	}
	if err == nil {
		err = postmortemReadSession(r.Context(), tx, r)
	}
	if errors.Is(err, store.ErrSessionRevoked) {
		postmortemReadError(w, err)
		return
	}
	if err == nil {
		v.Status, v.UpdatedAt = "draft", updated.UTC().Format(time.RFC3339)
		err = describe(r.Context(), tx, &v)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not return postmortem")
		return
	}
	w.Header().Set("ETag", postmortemETag(v.Revision))
	httpapi.WriteJSON(w, 200, v.Detail)
}
