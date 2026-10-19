package v3events

import (
	"errors"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// appendCaseTimeline continues a human-confirmed incident without mutating
// the incoming alert occurrences linked to it.
func (s *Service) appendCaseTimeline(w http.ResponseWriter, r *http.Request, tx pgx.Tx, v caseRecord, note string) {
	if !validCase(v) {
		respondIncidentDetailError(w, store.ErrInvalidReference)
		return
	}
	identity := incidentRecord{ApplicationID: v.ApplicationID, EnvironmentID: v.EnvironmentID, EnvironmentKey: v.EnvironmentKey}
	if err := s.authorizeAlertTx(r, tx, identity, "change"); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	user := httpapi.CurrentUser(r)
	id := store.ID()
	var created time.Time
	err := tx.QueryRow(r.Context(), `INSERT INTO v3_incident_timeline(id,incident_id,actor_id,text)
		VALUES($1,$2,$3,$4) RETURNING created_at`, id, v.ID, user.ID, note).Scan(&created)
	if err != nil {
		respondErr(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE v3_incident_cases SET updated_at=$2 WHERE id=$1`, v.ID, created); err != nil {
		respondErr(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
		VALUES($1,'user',$2,'incident.timeline.append',$3,'succeeded',jsonb_build_object('entry_id',$4::text,'environment_id',$5::text))`,
		user.ID, v.ApplicationID, v.ID, id, v.EnvironmentKey); err != nil {
		respondErr(w, err)
		return
	}
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	entry := timelineEntry{Time: created.UTC().Format(time.RFC3339Nano), Text: note}
	entry.Actor.Kind = "person"
	entry.Actor.Name = user.Name
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 201, entry)
}

// caseManualTimeline returns false only when the ID is not a V3 case, so the
// historical alert-backed manual timeline reader may resolve it instead.
func (s *Service) caseManualTimeline(w http.ResponseWriter, r *http.Request) bool {
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondErr(w, err)
		return true
	}
	defer tx.Rollback(r.Context())
	v, err := scanCase(tx.QueryRow(r.Context(), caseSelect+` WHERE c.id=$1 FOR SHARE OF c`, r.PathValue("id")))
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(r.Context())
		return false
	}
	if err != nil {
		respondIncidentDetailError(w, err)
		return true
	}
	identity := incidentRecord{ApplicationID: v.ApplicationID, EnvironmentID: v.EnvironmentID, EnvironmentKey: v.EnvironmentKey}
	if err := s.authorizeAlertTx(r, tx, identity, "view"); err != nil {
		respondIncidentDetailError(w, err)
		return true
	}
	if !validCase(v) {
		respondIncidentDetailError(w, store.ErrInvalidReference)
		return true
	}
	notes, err := readIncidentManualTimeline(r, tx, v.ID)
	if err != nil {
		respondIncidentDetailError(w, err)
		return true
	}
	items := make([]timelineEntry, 0, len(notes))
	for _, note := range notes {
		if note.Actor.Kind != "human" {
			respondIncidentDetailError(w, store.ErrInvalidReference)
			return true
		}
		entry := timelineEntry{Time: note.Time, Text: note.Text}
		entry.Actor.Kind = "person"
		entry.Actor.Name = note.Actor.Name
		items = append(items, entry)
	}
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return true
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, struct {
		Items []timelineEntry `json:"items"`
		Scope string          `json:"scope"`
	}{Items: items, Scope: "manual_only"})
	return true
}
