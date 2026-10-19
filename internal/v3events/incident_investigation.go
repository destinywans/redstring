package v3events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// OpenInvestigation answers POST /incidents/{id}/investigation (PA-88): it
// reuses the investigation already linked to the incident, or creates one
// for the incident's service and environment, titled after the incident and
// with the same structure as an investigation created on the canvas, and
// links it back. Seeing the incident needs its events.view (404 otherwise);
// opening writes (the investigation, the alert's status), so it also needs
// events.change, like starting one from an alert group, and is refused with
// 403 before anything is written (PA-252: the V3 route skips the legacy
// "operator" role, so this handler is the only check). Without a model_id no model is called; starting the AI with one is
// the canvas's job, so a model_id is refused rather than ignored. The
// response is the incident as GET /incidents/{id} returns it.
func (s *Service) OpenInvestigation(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	var in struct {
		ModelID string `json:"model_id"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.ModelID != "" {
		httpapi.Error(w, http.StatusUnprocessableEntity, "initial_analysis_unavailable",
			"Open the investigation without a model; ask the AI from the investigation canvas")
		return
	}
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Event not found")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = s.openInvestigationTx(r, tx, id); err != nil {
		if errors.Is(err, errInvestigationForbidden) {
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Insufficient permission")
			return
		}
		respondIncidentDetailError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	s.Incident(w, r)
}

func (s *Service) openInvestigationTx(r *http.Request, tx pgx.Tx, id string) error {
	ctx := r.Context()
	user := httpapi.CurrentUser(r)
	caseValue, err := scanCase(tx.QueryRow(ctx, caseSelect+` WHERE c.id=$1 FOR UPDATE OF c`, id))
	if err == nil {
		record := incidentRecord{ApplicationID: caseValue.ApplicationID, EnvironmentID: caseValue.EnvironmentID, EnvironmentKey: caseValue.EnvironmentKey}
		if err = s.authorizeAlertTx(r, tx, record, "view"); err != nil {
			return err
		}
		if err = s.authorizeInvestigationStart(r, tx, record); err != nil {
			return err
		}
		if !validCase(caseValue) {
			return store.ErrInvalidReference
		}
		if caseValue.InvestigationID != nil {
			return linkedInvestigation(ctx, tx, *caseValue.InvestigationID, caseValue.ApplicationID, caseValue.EnvironmentID)
		}
		goal := caseValue.Title
		if caseValue.Impact != nil {
			goal = *caseValue.Impact
		}
		investigation, err := createIncidentInvestigation(ctx, tx, user, id, caseValue.ApplicationID, caseValue.EnvironmentID,
			caseValue.EnvironmentKey, caseValue.Title, goal, caseValue.CreatedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE v3_incident_cases SET investigation_id=$2,updated_at=greatest(now(),created_at) WHERE id=$1`, id, investigation)
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	v, err := scanIncident(tx.QueryRow(ctx, incidentSelect+` WHERE i.id=$1 FOR UPDATE OF i`, id))
	if err != nil {
		return err
	}
	if err = s.authorizeAlertTx(r, tx, v, "view"); err != nil {
		return err
	}
	if err = s.authorizeInvestigationStart(r, tx, v); err != nil {
		return err
	}
	if v.ApplicationID == "" || v.EnvironmentID == "" || v.EnvironmentKey == "" || strings.TrimSpace(v.Title) == "" || v.StartsAt.IsZero() {
		return store.ErrInvalidReference
	}
	if v.InvestigationID != "" {
		return linkedInvestigation(ctx, tx, v.InvestigationID, v.ApplicationID, v.EnvironmentID)
	}
	goal := strings.TrimSpace(v.Summary)
	if goal == "" {
		goal = v.Title
	}
	investigation, err := createIncidentInvestigation(ctx, tx, user, id, v.ApplicationID, v.EnvironmentID, v.EnvironmentKey,
		v.Title, goal, v.StartsAt)
	if err != nil {
		return err
	}
	// A resolved alert keeps its status; an open one is now investigated.
	_, err = tx.Exec(ctx, `UPDATE ops_incidents SET investigation_id=$2,
 status=CASE WHEN status='open' THEN 'investigating' ELSE status END,updated_at=now() WHERE id=$1`, id, investigation)
	return err
}

// errInvestigationForbidden: the viewer may see the incident but not change it.
var errInvestigationForbidden = errors.New("opening an investigation needs events.change")

// authorizeInvestigationStart requires events.change on the incident's
// application and environment, after its events.view has been checked.
func (s *Service) authorizeInvestigationStart(r *http.Request, tx pgx.Tx, v incidentRecord) error {
	err := s.authorizeAlertTx(r, tx, v, "change")
	if errors.Is(err, store.ErrForbidden) {
		return errInvestigationForbidden
	}
	return err
}

// linkedInvestigation checks that an incident's existing investigation is
// still its application's and environment's before it is reused.
func linkedInvestigation(ctx context.Context, tx pgx.Tx, id, app, env string) error {
	var investigationApp string
	var investigationEnv *string
	err := tx.QueryRow(ctx, `SELECT application_id,environment_id FROM investigations WHERE id=$1 FOR SHARE`, id).
		Scan(&investigationApp, &investigationEnv)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (investigationApp != app || investigationEnv == nil || *investigationEnv != env)) {
		return store.ErrInvalidReference
	}
	return err
}

// createIncidentInvestigation creates an investigation the canvas opens like
// one created there (an INV- ID, a main branch with an empty context), and
// records that it came from the incident.
func createIncidentInvestigation(ctx context.Context, tx pgx.Tx, user core.User, incidentID, app, envRow, envKey, title, goal string,
	alertStartedAt time.Time) (string, error) {
	if user.ID == "" {
		return "", store.ErrForbidden
	}
	title, goal = clip(strings.TrimSpace(title), 300), clip(strings.TrimSpace(goal), 2000)
	id, root := "INV-"+store.ID(), store.ID()
	if _, err := tx.Exec(ctx, `INSERT INTO investigations(id,application_id,environment_id,title,goal,created_by)
 VALUES($1,$2,$3,$4,$5,$6)`, id, app, envRow, title, goal, user.ID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO branches(id,investigation_id,title) VALUES($1,$2,'主调查')`, root, id); err != nil {
		return "", err
	}
	provenance, _ := json.Marshal(map[string]string{"kind": "incident", "incident_id": incidentID})
	if _, err := tx.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 VALUES($1,0,'[]'::jsonb,$2::jsonb)`, root, provenance); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]string{"id": id, "incident_id": incidentID,
		"alert_started_at": alertStartedAt.UTC().Format(time.RFC3339Nano)})
	if _, err := tx.Exec(ctx, `INSERT INTO events(investigation_id,branch_id,type,payload)
 VALUES($1,$2,'investigation.created',$3::jsonb)`, id, root, payload); err != nil {
		return "", err
	}
	changes, _ := json.Marshal(map[string]string{"environment_id": envKey, "incident_id": incidentID, "investigation_id": id})
	_, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.incident.investigation_opened',$3,'success',$4::jsonb)`, user.ID, app, incidentID, changes)
	return id, err
}

func clip(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
