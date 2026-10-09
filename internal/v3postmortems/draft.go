package v3postmortems

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Draft uses only observed lifecycle evidence. A case without Canvas has no
// investigation report or measured duration to copy into a review draft.
func (s *Service) Draft(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("incident"))
	if !bounded(id, 128) {
		httpapi.Error(w, 422, "invalid_incident", "Select a resolved incident")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, ok := s.beginPostmortemRead(w, r, ctx)
	if !ok {
		return
	}
	defer tx.Rollback(ctx)
	var kind, app, team, env, title, rawSeverity, summary, investigation string
	var started, closed *time.Time
	var closeSeq, statusEventID *int64
	err := tx.QueryRow(ctx, postmortemSources+` SELECT s.source_kind,s.application_id,s.team_id,s.environment_id,
 s.title,s.severity,s.summary,s.started_at,s.resolved_at,s.investigation_id,s.close_event_seq,s.status_event_id
 FROM source s WHERE s.incident_id=$1 AND s.status='resolved' AND s.eligible AND s.resolved_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM v3_postmortems pm WHERE pm.incident_id=s.incident_id)`, id).
		Scan(&kind, &app, &team, &env, &title, &rawSeverity, &summary, &started, &closed, &investigation, &closeSeq, &statusEventID)
	if errors.Is(err, pgx.ErrNoRows) {
		// PA-65: point to the postmortem already saved for this incident.
		existing, existingErr := s.visibleExistingPostmortem(ctx, tx, r, scope, id)
		switch {
		case existingErr == nil:
			postmortemExists(w, existing)
		case errors.Is(existingErr, pgx.ErrNoRows), errors.Is(existingErr, store.ErrForbidden):
			httpapi.Error(w, 404, "not_found", "Incident is not eligible for a postmortem draft")
		default:
			httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem evidence is unavailable")
		}
		return
	}
	if err == nil && !inScope(scope, env) {
		httpapi.Error(w, 404, "not_found", "Incident is not eligible for a postmortem draft")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem evidence is unavailable")
		return
	}
	if err = s.authorizeTx(ctx, tx, httpapi.CurrentUser(r), app, team, env, "view"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Incident is not eligible for a postmortem draft")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		}
		return
	}
	sev, valid := severity(rawSeverity)
	if !valid || closed == nil || kind == "ops" && (started == nil || closeSeq == nil || closed.Before(*started)) {
		httpapi.Error(w, 503, "source_unavailable", "Incident source cannot be represented by the V3 contract")
		return
	}
	var rows pgx.Rows
	if kind == "ops" {
		rows, err = tx.Query(ctx, `SELECT type,created_at FROM events
 WHERE investigation_id=$1 AND created_at >= $2
 AND (created_at,seq) <= ($3::timestamptz,$4::bigint)
 ORDER BY created_at,seq LIMIT 201`, investigation, started, closed, closeSeq)
	} else {
		rows, err = tx.Query(ctx, `SELECT 'incident.'||to_status,created_at FROM v3_incident_case_status_events
 WHERE case_id=$1 AND id<=$2 ORDER BY id LIMIT 201`, id, statusEventID)
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Incident timeline is unavailable")
		return
	}
	timeline := make([]map[string]string, 0)
	for rows.Next() {
		var kind string
		var at time.Time
		if err = rows.Scan(&kind, &at); err != nil {
			break
		}
		tone := "info"
		if kind == "investigation.closed" || kind == "incident.resolved" {
			tone = "ok"
		}
		timeline = append(timeline, map[string]string{"at": at.UTC().Format(time.RFC3339), "text": kind, "tone": tone})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Incident timeline is unavailable")
		return
	}
	if len(timeline) > 200 {
		httpapi.Error(w, 413, "timeline_too_large", "Incident timeline exceeds the draft limit")
		return
	}
	if err = postmortemReadSession(ctx, tx, r); err != nil {
		postmortemReadError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem evidence is unavailable")
		return
	}
	incident := map[string]any{"id": id, "title": title, "severity": sev, "window": nil, "duration": nil}
	missing := []string{"verified_impact", "root_cause", "action_recommendations", "war_room_messages"}
	if started != nil {
		incident["window"] = started.UTC().Format(time.RFC3339) + " – " + closed.UTC().Format(time.RFC3339)
		incident["duration"] = duration(*started, *closed)
	} else {
		missing = append(missing, "incident.window", "incident.duration", "investigation_report", "evidence_count")
	}
	result := map[string]any{
		"incident": incident,
		"sections": []Section{{Key: "summary", Title: "Incident summary", Text: summary}, {Key: "impact", Title: "Impact", Text: ""}, {Key: "root", Title: "Root cause", Text: ""}},
		"timeline": timeline, "actions": []Action{}, "source_revision": sourceRevision(kind, closeSeq, statusEventID),
		"draft_mode": "evidence_only", "complete_v3_dto": false, "partial": true, "missing": missing,
	}
	if kind == "ops" {
		result["source_close_event_seq"] = *closeSeq
	}
	httpapi.WriteJSON(w, 200, result)
}
