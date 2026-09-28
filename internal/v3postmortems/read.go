package v3postmortems

import (
	"errors"
	"net/http"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Record is a persisted postmortem list item. It intentionally omits the
// dashboard's untracked cause categories and action completion metrics.
type Record struct {
	ID          string `json:"id"`
	IncidentID  string `json:"incident_id"`
	Title       string `json:"title"`
	Severity    string `json:"severity"`
	Resolved    string `json:"resolved"`
	Duration    string `json:"duration"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	ActionCount int    `json:"action_count"`
}

const recordsSQL = `SELECT pm.id,pm.incident_id,pm.status,pm.created_at,pm.created_by,
 i.application_id,COALESCE(a.owner_team_id,''),e.key,i.title,i.severity,i.starts_at,closed.created_at,
 jsonb_array_length(pm.actions)
 FROM v3_postmortems pm
 JOIN ops_incidents i ON i.id=pm.incident_id AND i.application_id=pm.application_id
   AND i.investigation_id=pm.investigation_id
 JOIN applications a ON a.id=pm.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=pm.application_id
   AND e.key=pm.environment_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=pm.application_id AND ve.environment_id=pm.environment_id
 JOIN events closed ON closed.seq=pm.close_event_seq AND closed.investigation_id=pm.investigation_id
   AND closed.type='investigation.closed'
 WHERE pm.environment_id=ANY($1::text[]) AND ($2::text='all' OR pm.status=$2)
   AND ($3::timestamptz IS NULL OR (pm.created_at,pm.id)<($3::timestamptz,$4::text))
 ORDER BY pm.created_at DESC,pm.id DESC LIMIT 256`

// Records exposes only fields backed by durable incident and postmortem data.
// The dashboard's PostmortemSummary contract remains separate and unavailable.
func (s *Service) Records(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "all"
	}
	if tab != "all" && tab != "draft" && tab != "review" && tab != "published" {
		httpapi.Error(w, 422, "invalid_tab", "Select all, draft, review, or published")
		return
	}
	type row struct {
		record                 Record
		app, team, env, author string
		start, closed          time.Time
		created                time.Time
	}
	items := make([]Record, 0)
	user := httpapi.CurrentUser(r)
	var cursor *time.Time
	cursorID := ""
	for {
		rows, err := s.DB.Query(r.Context(), recordsSQL, scope.EnvironmentIDs, tab, cursor, cursorID)
		if err != nil {
			httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem records unavailable")
			return
		}
		// Authorization may use this pool, so release the query first.
		batch := make([]row, 0, 256)
		for rows.Next() {
			var v row
			if err = rows.Scan(&v.record.ID, &v.record.IncidentID, &v.record.Status, &v.created, &v.author,
				&v.app, &v.team, &v.env, &v.record.Title, &v.record.Severity, &v.start, &v.closed,
				&v.record.ActionCount); err != nil {
				break
			}
			batch = append(batch, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem records unavailable")
			return
		}
		for _, v := range batch {
			if err = s.authorize(r.Context(), user, v.app, v.team, v.env, "view"); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
				return
			}
			if v.record.Status != "published" && v.author != user.ID {
				if err = s.authorizeModule(r.Context(), user, v.app, v.team, v.env, "knowledge", "manage"); err != nil {
					if errors.Is(err, store.ErrForbidden) {
						continue
					}
					httpapi.Error(w, 503, "authorization_unavailable", "Postmortem review access could not be checked")
					return
				}
			}
			parsed, valid := severity(v.record.Severity)
			if !valid {
				httpapi.Error(w, 503, "source_unavailable", "Incident severity cannot be represented by the V3 contract")
				return
			}
			v.record.Severity = parsed
			v.record.Resolved = v.closed.UTC().Format(time.RFC3339)
			v.record.Duration = duration(v.start, v.closed)
			v.record.CreatedAt = v.created.UTC().Format(time.RFC3339)
			items = append(items, v.record)
			if len(items) > 1000 {
				httpapi.Error(w, 413, "scope_too_large", "Postmortem record collection exceeds the unpaged V3 response limit")
				return
			}
		}
		if len(batch) < 256 {
			break
		}
		last := batch[len(batch)-1]
		cursor = &last.created
		cursorID = last.record.ID
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}
