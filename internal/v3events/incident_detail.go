package v3events

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var errTooManyIncidentNotes = errors.New("incident manual timeline exceeds read limit")
var errTooManyCaseAlerts = errors.New("incident case alert links exceed read limit")

// IncidentDetailResponse reports only facts bound to the persisted incident.
// In particular, an alert's start time is not an incident lifecycle event,
// and an assignee is not necessarily the incident commander.
type IncidentDetailResponse struct {
	ID              string                  `json:"id"`
	ApplicationID   string                  `json:"application_id"`
	EnvironmentID   string                  `json:"environment_id"`
	EnvironmentKey  string                  `json:"environment_key"`
	SourceID        string                  `json:"source_id"`
	Severity        string                  `json:"severity"`
	Title           string                  `json:"title"`
	Status          string                  `json:"status"`
	WorkStatus      string                  `json:"work_status"`
	AlertStatus     string                  `json:"alert_status"`
	Summary         string                  `json:"summary"`
	AlertStartedAt  string                  `json:"alert_started_at"`
	UpdatedAt       string                  `json:"updated_at"`
	ResolvedAt      *string                 `json:"resolved_at"`
	Duration        *string                 `json:"duration"`
	DurationBasis   *string                 `json:"duration_basis"`
	AssigneeID      string                  `json:"assignee_id"`
	Assignee        *incidentDetailAssignee `json:"assignee"`
	Commander       *string                 `json:"commander"`
	Stage           *int                    `json:"stage"`
	Timeline        []incidentDetailNote    `json:"timeline"`
	TimelineScope   string                  `json:"timeline_scope"`
	Impact          incidentDetailImpact    `json:"impact"`
	Roles           []incidentDetailRole    `json:"roles"`
	Links           []incidentDetailLink    `json:"links"`
	InvestigationID string                  `json:"investigation_id"`
	Partial         bool                    `json:"partial"`
	Unavailable     []string                `json:"unavailable"`
}

type incidentDetailAssignee struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type incidentDetailNote struct {
	ID    string `json:"id"`
	Time  string `json:"time"`
	Actor struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"actor"`
	Text string `json:"text"`
}

type incidentDetailImpact struct {
	Peak       *string   `json:"peak"`
	BudgetUsed *string   `json:"budget_used"`
	BudgetLeft *float64  `json:"budget_left"`
	Note       *string   `json:"note"`
	Series     []float64 `json:"series"`
}

type incidentDetailRole struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

type incidentDetailLink struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Href  string `json:"href"`
}

// CaseIncidentDetailResponse keeps alert-only facts nullable. The commander
// text is human-confirmed display data, not an assigned or verified role.
type CaseIncidentDetailResponse struct {
	ID                    string                  `json:"id"`
	RecordKind            string                  `json:"record_kind"`
	ApplicationID         string                  `json:"application_id"`
	EnvironmentID         string                  `json:"environment_id"`
	EnvironmentKey        string                  `json:"environment_key"`
	SourceID              *string                 `json:"source_id"`
	Severity              string                  `json:"severity"`
	Title                 string                  `json:"title"`
	Status                string                  `json:"status"`
	StatusRevision        int64                   `json:"status_revision"`
	ResolvedStatusEventID *int64                  `json:"resolved_status_event_id"`
	WorkStatus            *string                 `json:"work_status"`
	AlertStatus           *string                 `json:"alert_status"`
	Summary               *string                 `json:"summary"`
	CreatedAt             string                  `json:"created_at"`
	AlertStartedAt        *string                 `json:"alert_started_at"`
	UpdatedAt             string                  `json:"updated_at"`
	ResolvedAt            *string                 `json:"resolved_at"`
	Duration              *string                 `json:"duration"`
	DurationBasis         *string                 `json:"duration_basis"`
	AssigneeID            *string                 `json:"assignee_id"`
	Assignee              *incidentDetailAssignee `json:"assignee"`
	Commander             *string                 `json:"commander"`
	CommanderProvenance   string                  `json:"commander_provenance"`
	Stage                 *int                    `json:"stage"`
	Timeline              []incidentDetailNote    `json:"timeline"`
	TimelineScope         string                  `json:"timeline_scope"`
	Impact                incidentDetailImpact    `json:"impact"`
	ImpactProvenance      string                  `json:"impact_provenance"`
	Roles                 []incidentDetailRole    `json:"roles"`
	Links                 []incidentDetailLink    `json:"links"`
	InvestigationID       *string                 `json:"investigation_id"`
	AlertIDs              []string                `json:"alert_ids"`
	Partial               bool                    `json:"partial"`
	Unavailable           []string                `json:"unavailable"`
}

// A link has no FK to ops_incidents because that table is created after the
// store migrations. Validate every target against the case's persisted scope
// before reporting it as an associated alert ID.
func readCaseAlertIDs(r *http.Request, tx pgx.Tx, v caseRecord) ([]string, error) {
	rows, err := tx.Query(r.Context(), `SELECT link.alert_id,i.application_id,i.environment_id
		FROM v3_incident_case_alerts link
		LEFT JOIN ops_incidents i ON i.id=link.alert_id
		WHERE link.case_id=$1 ORDER BY link.alert_id LIMIT 65`, v.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		if len(items) == 64 {
			return nil, errTooManyCaseAlerts
		}
		var id string
		var app, env *string
		if err := rows.Scan(&id, &app, &env); err != nil {
			return nil, err
		}
		if id == "" || app == nil || env == nil || *app != v.ApplicationID || *env != v.EnvironmentID {
			return nil, store.ErrInvalidReference
		}
		items = append(items, id)
	}
	return items, rows.Err()
}

func (s *Service) caseIncidentDetail(w http.ResponseWriter, r *http.Request, tx pgx.Tx, v caseRecord) {
	if s.AuthorizeTx == nil {
		respondIncidentDetailError(w, store.ErrInvalidReference)
		return
	}
	if err := s.authorizeAlertTx(r, tx, incidentRecord{ApplicationID: v.ApplicationID,
		EnvironmentID: v.EnvironmentID, EnvironmentKey: v.EnvironmentKey}, "view"); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	if !validCase(v) {
		respondIncidentDetailError(w, store.ErrInvalidReference)
		return
	}
	if v.InvestigationID != nil {
		var app string
		var env *string
		err := tx.QueryRow(r.Context(), `SELECT application_id,environment_id FROM investigations
			WHERE id=$1 FOR SHARE`, *v.InvestigationID).Scan(&app, &env)
		if err != nil || app != v.ApplicationID || env == nil || *env != v.EnvironmentID {
			if errors.Is(err, pgx.ErrNoRows) || err == nil {
				err = store.ErrInvalidReference
			}
			respondIncidentDetailError(w, err)
			return
		}
	}
	alertIDs, err := readCaseAlertIDs(r, tx, v)
	if err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	if len(alertIDs) == 0 {
		respondIncidentDetailError(w, store.ErrInvalidReference)
		return
	}
	timeline, err := readIncidentManualTimeline(r, tx, v.ID)
	if err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	result := CaseIncidentDetailResponse{
		ID: v.ID, RecordKind: "case", ApplicationID: v.ApplicationID,
		EnvironmentID: v.EnvironmentID, EnvironmentKey: v.EnvironmentKey,
		Severity: v.Severity, Title: v.Title, Status: v.Status,
		StatusRevision: v.StatusRevision, ResolvedStatusEventID: v.ResolvedStatusEventID,
		CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: v.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Commander: v.CommanderText, CommanderProvenance: "unknown",
		Timeline: timeline, TimelineScope: "manual_only",
		Impact: incidentDetailImpact{Note: v.Impact}, ImpactProvenance: "unknown",
		Roles: []incidentDetailRole{}, Links: []incidentDetailLink{},
		InvestigationID: v.InvestigationID, AlertIDs: alertIDs, Partial: true,
		Unavailable: []string{"source_id", "work_status", "alert_status", "summary", "alert_started_at",
			"duration", "duration_basis", "assignee", "stage", "impact.peak",
			"impact.budget_used", "impact.budget_left", "impact.series", "timeline.system_events",
			"links.alert_group", "links.proposal", "links.change"},
	}
	if v.ResolvedAt != nil {
		resolvedAt := v.ResolvedAt.UTC().Format(time.RFC3339Nano)
		result.ResolvedAt = &resolvedAt
	} else {
		result.Unavailable = append(result.Unavailable, "resolved_at")
	}
	if v.CommanderText != nil {
		result.CommanderProvenance = "human_confirmed"
	} else {
		result.Unavailable = append(result.Unavailable, "commander")
	}
	if v.Impact != nil {
		result.ImpactProvenance = "human_confirmed"
	} else {
		result.Unavailable = append(result.Unavailable, "impact.note")
	}
	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func incidentDetailState(v incidentRecord) (string, bool) {
	switch v.Status {
	case "open", "investigating", "resolved":
	default:
		return "", false
	}
	switch v.AlertStatus {
	case "firing", "pending", "resolved":
	default:
		return "", false
	}
	if v.Status == "resolved" {
		return "resolved", true
	}
	if v.AlertStatus == "resolved" {
		return "mitigated", true
	}
	return "active", true
}

func readIncidentManualTimeline(r *http.Request, tx pgx.Tx, incidentID string) ([]incidentDetailNote, error) {
	rows, err := tx.Query(r.Context(), `SELECT t.id,t.created_at,u.id,u.v3_identity_kind,u.name,t.text
		FROM v3_incident_timeline t JOIN users u ON u.id=t.actor_id
		WHERE t.incident_id=$1 ORDER BY t.created_at,t.id LIMIT 501`, incidentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]incidentDetailNote, 0)
	for rows.Next() {
		var created time.Time
		var item incidentDetailNote
		if err := rows.Scan(&item.ID, &created, &item.Actor.ID, &item.Actor.Kind, &item.Actor.Name, &item.Text); err != nil {
			return nil, err
		}
		if len(items) == 500 {
			return nil, errTooManyIncidentNotes
		}
		if item.ID == "" || item.Actor.ID == "" || created.IsZero() || strings.TrimSpace(item.Actor.Name) == "" || strings.TrimSpace(item.Text) == "" ||
			(item.Actor.Kind != "human" && item.Actor.Kind != "machine") {
			return nil, store.ErrInvalidReference
		}
		item.Time = created.UTC().Format(time.RFC3339Nano)
		items = append(items, item)
	}
	return items, rows.Err()
}

// Incident reads the incident, current V3 grant and manual notes in one
// transaction. The incident and authorization source rows remain locked until
// the response has been assembled and the session is rechecked.
func (s *Service) Incident(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	if r.PathValue("id") == "" {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Event not found")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	caseValue, err := scanCase(tx.QueryRow(r.Context(), caseSelect+` WHERE c.id=$1 FOR SHARE OF c`, r.PathValue("id")))
	if err == nil {
		s.caseIncidentDetail(w, r, tx, caseValue)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		respondIncidentDetailError(w, err)
		return
	}
	v, err := scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1 FOR SHARE OF i`, r.PathValue("id")))
	if err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	if err = s.authorizeAlertTx(r, tx, v, "view"); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	severityCode, ok := alertDetailSeverity(v.Severity)
	if !ok || v.ID == "" || v.ApplicationID == "" || v.EnvironmentID == "" || v.EnvironmentKey == "" ||
		v.SourceID == "" || strings.TrimSpace(v.Title) == "" || v.StartsAt.IsZero() || v.UpdatedAt.IsZero() {
		respondIncidentDetailError(w, store.ErrInvalidReference)
		return
	}
	status, ok := incidentDetailState(v)
	if !ok {
		respondIncidentDetailError(w, store.ErrInvalidReference)
		return
	}
	// An investigation ID alone is not proof that the referenced investigation
	// belongs to this incident's current application and environment.
	if v.InvestigationID != "" {
		var investigationApplication string
		var investigationEnvironment *string
		err = tx.QueryRow(r.Context(), `SELECT application_id,environment_id FROM investigations WHERE id=$1 FOR SHARE`, v.InvestigationID).
			Scan(&investigationApplication, &investigationEnvironment)
		if errors.Is(err, pgx.ErrNoRows) {
			respondIncidentDetailError(w, store.ErrInvalidReference)
			return
		}
		if err != nil {
			respondIncidentDetailError(w, err)
			return
		}
		if investigationApplication != v.ApplicationID || investigationEnvironment == nil || *investigationEnvironment != v.EnvironmentID {
			respondIncidentDetailError(w, store.ErrInvalidReference)
			return
		}
	}
	timeline, err := readIncidentManualTimeline(r, tx, v.ID)
	if err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	result := IncidentDetailResponse{
		ID: v.ID, ApplicationID: v.ApplicationID, EnvironmentID: v.EnvironmentID,
		EnvironmentKey: v.EnvironmentKey, SourceID: v.SourceID,
		Severity: severityCode, Title: v.Title, Status: status,
		WorkStatus: v.Status, AlertStatus: v.AlertStatus, Summary: v.Summary,
		AlertStartedAt: v.StartsAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:      v.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Timeline:       timeline, TimelineScope: "manual_only",
		Roles: []incidentDetailRole{}, Links: []incidentDetailLink{},
		AssigneeID: v.AssigneeID, InvestigationID: v.InvestigationID, Partial: true,
		Unavailable: []string{"duration", "duration_basis", "commander", "stage", "impact.peak", "impact.budget_used", "impact.budget_left", "impact.note", "impact.series", "timeline.system_events", "links.alert_group", "links.proposal", "links.change", "links.investigation"},
	}
	if v.AssigneeID != "" {
		if strings.TrimSpace(v.Commander) == "" {
			result.Unavailable = append(result.Unavailable, "assignee.name")
		} else {
			result.Assignee = &incidentDetailAssignee{ID: v.AssigneeID, Name: v.Commander}
			result.Roles = append(result.Roles, incidentDetailRole{Role: "assignee", Name: v.Commander})
		}
	}
	if status == "resolved" {
		if v.InvestigationID != "" && v.ResolvedAt != nil {
			if v.ResolvedAt.IsZero() {
				respondIncidentDetailError(w, store.ErrInvalidReference)
				return
			}
			resolvedAt := v.ResolvedAt.UTC().Format(time.RFC3339Nano)
			result.ResolvedAt = &resolvedAt
		} else {
			result.Unavailable = append(result.Unavailable, "resolved_at")
		}
	}
	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondIncidentDetailError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func respondIncidentDetailError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusNotFound, "not_found", "Event not found")
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
	case errors.Is(err, errTooManyIncidentNotes):
		httpapi.Error(w, http.StatusServiceUnavailable, "pagination_required", "Manual timeline exceeds the current read limit")
	case errors.Is(err, errTooManyCaseAlerts):
		httpapi.Error(w, http.StatusServiceUnavailable, "pagination_required", "Incident alert links exceed the current read limit")
	case errors.Is(err, store.ErrInvalidReference):
		httpapi.Error(w, http.StatusServiceUnavailable, "incident_detail_unavailable", "Persisted incident data is inconsistent")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
	}
}
