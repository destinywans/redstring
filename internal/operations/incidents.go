package operations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	Fingerprint string            `json:"fingerprint"`
}
type AlertEnvelope struct {
	Alerts []Alert `json:"alerts"`
}

const incidentColumns = "id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,suspected,assignee_id,investigation_id,summary,starts_at,ends_at,updated_at"

func scanIncident(row pgx.Row) (Incident, error) {
	var v Incident
	e := row.Scan(&v.ID, &v.SourceID, &v.ApplicationID, &v.EnvironmentID, &v.Fingerprint, &v.Title, &v.Severity, &v.Status, &v.AlertStatus, &v.Suspected, &v.AssigneeID, &v.InvestigationID, &v.Summary, &v.StartsAt, &v.EndsAt, &v.UpdatedAt)
	return v, store.ClassifyError(e)
}
func (s *Service) incident(ctx context.Context, id string) (Incident, error) {
	return scanIncident(s.DB.QueryRow(ctx, "SELECT "+incidentColumns+" FROM ops_incidents WHERE id=$1", id))
}
func (s *Service) incidentAccess(ctx context.Context, u core.User, v Incident, permission string) error {
	if v.ApplicationID == "" {
		if u.Role == "admin" {
			return nil
		}
		return store.ErrNotFound
	}
	return s.API.AuthorizeApplication(ctx, u, v.ApplicationID, permission)
}
func (s *Service) Incidents(ctx context.Context, u core.User, app, env string) ([]Incident, error) {
	ids, e := s.API.VisibleApplicationIDs(ctx, u)
	if e != nil {
		return nil, e
	}
	rows, e := s.DB.Query(ctx, "SELECT "+incidentColumns+" FROM ops_incidents WHERE (application_id=ANY($1::text[]) OR ($2 AND application_id='')) AND ($3='' OR application_id=$3) AND ($4='' OR environment_id=$4) ORDER BY updated_at DESC LIMIT 200", ids, u.Role == "admin", app, env)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		v, e := scanIncident(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) webhook(w http.ResponseWriter, r *http.Request) {
	var source *Source
	for i := range s.Config.Sources {
		if s.Config.Sources[i].ID == r.PathValue("source") {
			source = &s.Config.Sources[i]
		}
	}
	if source == nil {
		httpapi.Error(w, 404, "not_found", "Unknown alert source")
		return
	}
	token, e := os.ReadFile(source.TokenFile)
	if e != nil {
		httpapi.Error(w, 503, "source_unavailable", "Alert source authentication unavailable")
		return
	}
	expected := strings.TrimSpace(string(token))
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(expected) < 24 || subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
		httpapi.Error(w, 401, "unauthorized", "Invalid alert source token")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var input AlertEnvelope
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Alerts) == 0 || len(input.Alerts) > 200 {
		httpapi.Error(w, 422, "invalid_alerts", "Expected 1 to 200 alerts")
		return
	}
	for _, a := range input.Alerts {
		if a.Fingerprint == "" || len(a.Fingerprint) > 256 || a.StartsAt.IsZero() || (a.Status != "firing" && a.Status != "resolved") || (a.Status == "resolved" && (a.EndsAt.IsZero() || a.EndsAt.Before(a.StartsAt))) {
			httpapi.Error(w, 422, "invalid_alert", "Fingerprint, start time and valid status are required")
			return
		}
	}
	// Each alert is independently idempotent, so retrying a partially accepted
	// envelope is safe after a database failure.
	for _, a := range input.Alerts {
		if e = s.Ingest(r.Context(), *source, a); e != nil {
			httpapi.Error(w, 503, "unavailable", "Alert persistence failed; retry the envelope")
			return
		}
	}
	httpapi.WriteJSON(w, 202, map[string]int{"accepted": len(input.Alerts)})
}
func (s *Service) mapping(ctx context.Context, source Source, labels map[string]string) (string, string) {
	matches := []Mapping{}
	for _, m := range source.Mappings {
		ok := true
		for k, v := range m.Labels {
			if labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, m)
		}
	}
	if len(matches) != 1 {
		return "", ""
	}
	m := matches[0]
	app, e := s.environment(ctx, m.EnvironmentID)
	if e != nil || app != m.ApplicationID {
		return "", ""
	}
	return app, m.EnvironmentID
}
func (s *Service) Ingest(ctx context.Context, source Source, a Alert) error {
	app, env := s.mapping(ctx, source, a.Labels)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	title := short(a.Labels["alertname"], 300)
	if title == "" {
		title = "Alert"
	}
	summary := short(a.Annotations["summary"], 1000)
	severity := short(a.Labels["severity"], 32)
	if severity == "" {
		severity = "warning"
	}
	id := store.ID()
	var ends *time.Time
	if a.Status == "resolved" {
		ends = &a.EndsAt
	}
	_, e = tx.Exec(ctx, "INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,summary,starts_at,ends_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(source_id,fingerprint,starts_at) DO NOTHING", id, source.ID, app, env, a.Fingerprint, title, severity, a.Status, summary, a.StartsAt, ends)
	if e != nil {
		return e
	}
	v, e := scanIncident(tx.QueryRow(ctx, "SELECT "+incidentColumns+" FROM ops_incidents WHERE source_id=$1 AND fingerprint=$2 AND starts_at=$3 FOR UPDATE", source.ID, a.Fingerprint, a.StartsAt))
	if e != nil {
		return e
	}
	b, _ := json.Marshal(a)
	tag, e := tx.Exec(ctx, "INSERT INTO ops_alert_occurrences(incident_id,status,payload_hash) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", v.ID, a.Status, store.Hash(string(b)))
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	// A late firing webhook for the same occurrence can never reopen its resolved
	// alert state. A genuinely new occurrence has a different startsAt identity.
	state := v.AlertStatus
	if state != "resolved" {
		state = a.Status
	}
	if _, e = tx.Exec(ctx, "UPDATE ops_incidents SET alert_status=$2,ends_at=CASE WHEN $2='resolved' THEN COALESCE(ends_at,$3) ELSE NULL END,updated_at=now() WHERE id=$1", v.ID, state, ends); e != nil {
		return e
	}
	v.AlertStatus = state
	eventKey := "incident:" + v.ID + ":" + state
	body := "AIOps Canvas · " + v.Title + "\n告警状态：" + state + "；业务恢复仍需验证。"
	if e = s.enqueueNotification(ctx, tx, v, eventKey, body); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Investigation admission and linkage commit together: duplicate button clicks,
// webhook retries and automatic initial analysis cannot create orphan duplicates.
func (s *Service) CreateInvestigation(ctx context.Context, id string, u core.User) (Incident, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Incident{}, e
	}
	defer tx.Rollback(ctx)
	v, e := scanIncident(tx.QueryRow(ctx, "SELECT "+incidentColumns+" FROM ops_incidents WHERE id=$1 FOR UPDATE", id))
	if e != nil {
		return v, e
	}
	if e = s.incidentAccess(ctx, u, v, "operate"); e != nil {
		return v, e
	}
	if v.EnvironmentID == "" {
		return v, store.ErrInvalidReference
	}
	if v.InvestigationID != "" {
		return v, nil
	}
	if _, e = s.environment(ctx, v.EnvironmentID); e != nil {
		return v, e
	}
	inv, bid := store.ID(), store.ID()
	if _, e = tx.Exec(ctx, "INSERT INTO investigations(id,application_id,environment_id,title) VALUES($1,$2,$3,$4)", inv, v.ApplicationID, v.EnvironmentID, v.Title); e != nil {
		return v, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO branches(id,investigation_id,title) VALUES($1,$2,'Main investigation')", bid, inv); e != nil {
		return v, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES($1,0,'[]','{\"kind\":\"incident\"}')", bid); e != nil {
		return v, e
	}
	v.InvestigationID = inv
	v.Status = "investigating"
	if _, e = tx.Exec(ctx, "UPDATE ops_incidents SET investigation_id=$2,status='investigating',updated_at=now() WHERE id=$1", id, inv); e != nil {
		return v, e
	}
	payload, _ := json.Marshal(map[string]string{"incident_id": id, "investigation_id": inv})
	if _, e = tx.Exec(ctx, "INSERT INTO events(investigation_id,type,payload) VALUES($1,'investigation.created',$2)", inv, payload); e != nil {
		return v, e
	}
	return v, tx.Commit(ctx)
}
func (s *Service) claimIncident(ctx context.Context, id string, u core.User, assignee string) (Incident, error) {
	v, e := s.incident(ctx, id)
	if e != nil {
		return v, e
	}
	if e = s.incidentAccess(ctx, u, v, "operate"); e != nil {
		return v, e
	}
	if assignee != "" && assignee != u.ID && u.Role != "admin" {
		return v, store.ErrInvalidReference
	}
	if assignee != "" {
		if _, e = s.actor(ctx, assignee, v.ApplicationID); e != nil {
			return v, e
		}
	}
	_, e = s.DB.Exec(ctx, "UPDATE ops_incidents SET assignee_id=$2,updated_at=now() WHERE id=$1", id, assignee)
	if e != nil {
		return v, e
	}
	return s.incident(ctx, id)
}
func respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		httpapi.WriteJSON(w, 200, v)
		return
	}
	if errors.Is(e, store.ErrConflict) {
		httpapi.Error(w, 409, "conflict", "State changed; refresh and retry")
		return
	}
	if errors.Is(e, store.ErrInvalidReference) {
		httpapi.Error(w, 422, "invalid_input", "Invalid registered scope or settings")
		return
	}
	httpapi.AuthorizationError(w, e)
}
