package v3events

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// ManualTimeline exposes only persisted human notes. It cannot reconstruct the
// complete incident timeline from the legacy incident or investigation data.
func (s *Service) ManualTimeline(w http.ResponseWriter, r *http.Request) {
	if s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	v, err := s.incident(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Event not found")
		return
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	if err = s.scoped(r, v.ApplicationID, v.EnvironmentKey, "view"); err != nil {
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
			httpapi.Error(w, http.StatusNotFound, "not_found", "Event not found")
		} else {
			respondErr(w, err)
		}
		return
	}
	rows, err := s.DB.Query(r.Context(), `SELECT t.created_at,u.name,t.text
		FROM v3_incident_timeline t JOIN users u ON u.id=t.actor_id
		WHERE t.incident_id=$1 ORDER BY t.created_at,t.id LIMIT 501`, v.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	defer rows.Close()
	items := make([]timelineEntry, 0)
	for rows.Next() {
		var created time.Time
		var name, note string
		if err := rows.Scan(&created, &name, &note); err != nil {
			respondErr(w, err)
			return
		}
		if len(items) == 500 {
			httpapi.Error(w, http.StatusServiceUnavailable, "pagination_required", "Manual timeline exceeds the current read limit")
			return
		}
		entry := timelineEntry{Time: created.UTC().Format(time.RFC3339Nano), Text: note}
		entry.Actor.Kind = "person"
		entry.Actor.Name = name
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, struct {
		Items []timelineEntry `json:"items"`
		Scope string          `json:"scope"`
	}{Items: items, Scope: "manual_only"})
}
