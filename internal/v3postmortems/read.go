package v3postmortems

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Record is a persisted postmortem list item. It intentionally omits the
// dashboard's untracked cause categories and action completion metrics.
type Record struct {
	ID             string   `json:"id"`
	IncidentID     string   `json:"incident_id"`
	Title          string   `json:"title"`
	Severity       string   `json:"severity"`
	Resolved       string   `json:"resolved"`
	Duration       string   `json:"duration"`
	SourceRevision string   `json:"source_revision"`
	UnknownFields  []string `json:"unknown_fields,omitempty"`
	Status         string   `json:"status"`
	CreatedAt      string   `json:"created_at"`
	ActionCount    int      `json:"action_count"`
}

const recordsSQL = postmortemSources + ` SELECT pm.id,pm.incident_id,pm.status,pm.created_at,pm.created_by,
 s.application_id,s.team_id,s.environment_id,s.title,s.severity,s.started_at,
 CASE WHEN pm.source_kind='ops' THEN saved_close.created_at ELSE saved_case.created_at END,
 pm.source_kind,pm.close_event_seq,pm.source_case_status_event_id,jsonb_array_length(pm.actions)
 ` + recordSourceFrom + `
 WHERE ` + validRecordedSource + ` AND pm.environment_id=ANY($1::text[])
 AND ($2::text='all' OR pm.status=$2)
 AND ($3::timestamptz IS NULL OR (pm.created_at,pm.id)<($3::timestamptz,$4::text))
 ORDER BY pm.created_at DESC,pm.id DESC LIMIT 256`

func (v Record) MarshalJSON() ([]byte, error) {
	type recordJSON struct {
		ID             string   `json:"id"`
		IncidentID     string   `json:"incident_id"`
		Title          string   `json:"title"`
		Severity       string   `json:"severity"`
		Resolved       string   `json:"resolved"`
		Duration       any      `json:"duration"`
		Status         string   `json:"status"`
		CreatedAt      string   `json:"created_at"`
		ActionCount    int      `json:"action_count"`
		SourceRevision string   `json:"source_revision"`
		UnknownFields  []string `json:"unknown_fields,omitempty"`
	}
	out := recordJSON{ID: v.ID, IncidentID: v.IncidentID, Title: v.Title, Severity: v.Severity,
		Resolved: v.Resolved, Duration: v.Duration, Status: v.Status, CreatedAt: v.CreatedAt,
		ActionCount: v.ActionCount, SourceRevision: v.SourceRevision, UnknownFields: v.UnknownFields}
	if len(v.UnknownFields) > 0 {
		out.Duration = nil
	}
	return json.Marshal(out)
}

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
		record                  Record
		app, team, env, author  string
		start                   *time.Time
		closed                  time.Time
		kind                    string
		closeSeq, statusEventID *int64
		created                 time.Time
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
				&v.kind, &v.closeSeq, &v.statusEventID, &v.record.ActionCount); err != nil {
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
			v.record.SourceRevision = sourceRevision(v.kind, v.closeSeq, v.statusEventID)
			if v.start != nil {
				v.record.Duration = duration(*v.start, v.closed)
			} else {
				v.record.UnknownFields = []string{"duration"}
			}
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
