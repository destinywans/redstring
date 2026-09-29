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

// Draft is an evidence-only starting point. It intentionally leaves impact,
// root cause and actions for a human to establish; no model is called here.
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
	var app, team, env, investigation, title, rawSeverity, summary string
	var closeSeq int64
	var started, closed time.Time
	err := tx.QueryRow(ctx, `SELECT i.application_id,COALESCE(a.owner_team_id,''),e.key,
 i.investigation_id,i.title,i.severity,i.summary,i.starts_at,closed.seq,closed.created_at
 FROM ops_incidents i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 JOIN investigations inv ON inv.id=i.investigation_id AND inv.application_id=i.application_id AND inv.status='closed'
 JOIN investigation_reports report ON report.investigation_id=inv.id
 JOIN LATERAL (SELECT ev.seq,ev.created_at FROM events ev
   WHERE ev.investigation_id=inv.id AND ev.type='investigation.closed' AND ev.created_at>=i.starts_at
   ORDER BY ev.created_at,ev.seq LIMIT 1) closed ON true
 WHERE i.id=$1 AND i.status='resolved'
   AND NOT EXISTS(SELECT 1 FROM v3_postmortems pm WHERE pm.incident_id=i.id)
 FOR SHARE OF i,a,e,ve,inv,report`, id).
		Scan(&app, &team, &env, &investigation, &title, &rawSeverity, &summary, &started, &closeSeq, &closed)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, env) {
		httpapi.Error(w, 404, "not_found", "Incident is not eligible for a postmortem draft")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem evidence is unavailable")
		return
	}
	var lockedCloseSeq int64
	if err = tx.QueryRow(ctx, `SELECT seq FROM events
 WHERE seq=$1 AND investigation_id=$2 AND type='investigation.closed' FOR SHARE`, closeSeq, investigation).Scan(&lockedCloseSeq); err != nil {
		httpapi.Error(w, 503, "source_unavailable", "Closing event could not be retained in the draft snapshot")
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
	if !valid {
		httpapi.Error(w, 503, "source_unavailable", "Incident severity cannot be represented by the V3 contract")
		return
	}
	if closed.Before(started) {
		httpapi.Error(w, 503, "source_unavailable", "Incident closing evidence precedes its start")
		return
	}
	rows, err := tx.Query(ctx, `SELECT type,created_at FROM events
 WHERE investigation_id=$1 AND created_at >= $2
   AND (created_at,seq) <= ($3::timestamptz,$4::bigint)
 ORDER BY created_at,seq LIMIT 201`, investigation, started, closed, closeSeq)
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
		if kind == "investigation.closed" {
			tone = "ok"
		}
		timeline = append(timeline, map[string]string{
			"at": at.UTC().Format(time.RFC3339), "text": kind, "tone": tone,
		})
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
	httpapi.WriteJSON(w, 200, map[string]any{
		"incident": map[string]string{
			"id": id, "title": title, "severity": sev,
			"window":   started.UTC().Format(time.RFC3339) + " – " + closed.UTC().Format(time.RFC3339),
			"duration": duration(started, closed),
		},
		"sections": []Section{
			{Key: "summary", Title: "Incident summary", Text: summary},
			{Key: "impact", Title: "Impact", Text: ""},
			{Key: "root", Title: "Root cause", Text: ""},
		},
		"timeline":               timeline,
		"actions":                []Action{},
		"source_close_event_seq": closeSeq,
		"draft_mode":             "evidence_only",
		"complete_v3_dto":        false,
		"partial":                true,
		"missing":                []string{"verified_impact", "root_cause", "action_recommendations", "war_room_messages"},
	})
}
