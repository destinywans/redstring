// Package v3events adapts durable operational events to the V3 API. It does
// not turn external infrastructure writes into direct UI mutations.
package v3events

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3events/alertseverity"
	"github.com/wans/aiops-canvas/internal/v3interactionaudit"
)

// Authorize resolves the persisted application and instance environment in
// the V3 matrix. It must not fall back to the historical application ACL.
type Authorize func(context.Context, core.User, string, string, string) error
type AuthorizeWithTx func(context.Context, pgx.Tx, core.User, string, string, string) error
type AuthorizeTeamWithTx func(context.Context, pgx.Tx, core.User, string, string, string) error

type Service struct {
	DB          *pgxpool.Pool
	Authorize   Authorize
	AuthorizeTx AuthorizeWithTx
	// Changes reads the GitLab changes and cluster revisions around an
	// incident (D11); nil leaves them not configured.
	Changes IncidentChanges
	// AuthorizeListTx is an optional view-only policy for the two held-lock list
	// helpers. Missing configuration retains the ordinary authority path.
	AuthorizeListTx AuthorizeWithTx
	// AuthorizeTeamTx checks events grants against the current locked owner team.
	// The composition root injects the same V3 policy used by AuthorizeTx.
	AuthorizeTeamTx AuthorizeTeamWithTx
	// GroupPolicySource and its explicit bindings are injected by the V3
	// composition root only for webhook sources known to use the configured
	// environment-bound Alertmanager. Missing bindings remain unavailable.
	GroupPolicySource   GroupPolicySource
	GroupPolicyBindings map[string]GroupPolicyBinding
	Location            *time.Location
}

func New(db *pgxpool.Pool, authorize Authorize, authorizeTx AuthorizeWithTx) *Service {
	return &Service{DB: db, Authorize: authorize, AuthorizeTx: authorizeTx, Location: time.Local}
}

// RegisterNew registers only routes that do not collide with operations.Register.
func (s *Service) RegisterNew(api *httpapi.Server) {
	api.Register("GET /api/v1/inbox", "viewer", s.Inbox)
	api.MarkV3ObjectRoute("GET /api/v1/inbox", "events", "view")
	api.Register("GET /api/v1/incidents/draft", "viewer", s.IncidentDraft)
	api.MarkV3ObjectRoute("GET /api/v1/incidents/draft", "events", "view")
	api.Register("POST /api/v1/incidents", "operator", s.CreateIncident)
	api.MarkV3ObjectRoute("POST /api/v1/incidents", "events", "change")
	api.Register("POST /api/v1/incidents/{id}/status", "operator", s.ChangeIncidentCaseStatus)
	api.MarkV3ObjectRoute("POST /api/v1/incidents/{id}/status", "events", "change")
	api.Register("GET /api/v1/alert-groups", "viewer", s.AlertGroups)
	api.MarkV3ObjectRoute("GET /api/v1/alert-groups", "events", "view")
	api.Register("GET /api/v1/alert-groups/{id}/policy", "viewer", s.AlertGroupPolicy)
	api.MarkV3ObjectRoute("GET /api/v1/alert-groups/{id}/policy", "events", "view")
	api.Register("GET /api/v1/alerts/{id}", "viewer", s.AlertDetail)
	api.MarkV3ObjectRoute("GET /api/v1/alerts/{id}", "events", "view")
	api.Register("POST /api/v1/alerts/{id}/claim", "operator", s.ClaimAlert)
	api.MarkV3ObjectRoute("POST /api/v1/alerts/{id}/claim", "events", "change")
	api.Register("POST /api/v1/incidents/{id}/timeline", "operator", s.AppendTimeline)
	api.MarkV3ObjectRoute("POST /api/v1/incidents/{id}/timeline", "events", "change")
	api.Register("GET /api/v1/incidents/{id}/timeline/manual", "viewer", s.ManualTimeline)
	api.MarkV3ObjectRoute("GET /api/v1/incidents/{id}/timeline/manual", "events", "view")
}

// RegisterV3Incidents replaces the legacy incident reads at the composition root.
// Keep these separate from RegisterNew so callers can opt into the V3 handlers
// only after the legacy routes have been accounted for.
func (s *Service) RegisterV3Incidents(api *httpapi.Server) {
	v3interactionaudit.Mark(api, s.DB, "POST /api/v1/incidents/{id}/participants", "incident.participants.add")
	api.Register("POST /api/v1/incidents/{id}/participants", "viewer", s.AddParticipants)
	api.MarkV3ObjectRoute("POST /api/v1/incidents/{id}/participants", "events", "change")
	api.Register("GET /api/v1/incidents", "viewer", s.Incidents)
	api.MarkV3ObjectRoute("GET /api/v1/incidents", "events", "view")
	api.Register("GET /api/v1/incidents/{id}", "viewer", s.Incident)
	api.MarkV3ObjectRoute("GET /api/v1/incidents/{id}", "events", "view")
	api.Register("GET /api/v1/incidents/{id}/changes", "viewer", s.IncidentChanges)
	api.MarkV3ObjectRoute("GET /api/v1/incidents/{id}/changes", "events", "view")
	// PA-88: the operator route plus the incident's events.view.
	api.Register("POST /api/v1/incidents/{id}/investigation", "operator", s.OpenInvestigation)
	api.MarkV3ObjectRoute("POST /api/v1/incidents/{id}/investigation", "events", "view")
}

type incidentRecord struct {
	ID, SourceID, Fingerprint, ApplicationID, EnvironmentID, EnvironmentKey, Title, Severity, Status, AlertStatus, Summary, AssigneeID, Commander, InvestigationID string
	Labels                                                                                                                                                         map[string]string
	StartsAt, UpdatedAt                                                                                                                                            time.Time
	ResolvedAt                                                                                                                                                     *time.Time
	// UpstreamState is the last Alertmanager state the reconciler recorded
	// (PA-47); UpstreamCheckedAt is nil when it never checked this incident.
	UpstreamState     string
	UpstreamCheckedAt *time.Time
}

const incidentProjection = `i.id,i.source_id,i.fingerprint,i.labels,i.application_id,i.environment_id,e.key,i.title,i.severity,i.status,i.alert_status,i.summary,i.assignee_id,COALESCE(u.name,''),i.investigation_id,i.starts_at,i.updated_at,
 (SELECT MIN(closed.created_at) FROM events closed WHERE closed.investigation_id=i.investigation_id AND closed.type='investigation.closed'),
 COALESCE((SELECT us.state FROM v3_alert_upstream_states us WHERE us.incident_id=i.id),''),
 (SELECT us.checked_at FROM v3_alert_upstream_states us WHERE us.incident_id=i.id)`

const incidentSelect = `SELECT ` + incidentProjection + `
 FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 LEFT JOIN users u ON u.id=i.assignee_id`

// incidentListSelect resolves the live metadata for the requested environments
// once per statement, rather than joining it for every incident. The projection
// and the subsequent held-lock authorization remain shared with incident reads.
const incidentListSelect = `WITH incident_scopes AS MATERIALIZED (
 SELECT e.id,e.application_id,e.key FROM application_environments e
 JOIN v3_application_environments ve ON ve.application_id=e.application_id AND ve.environment_id=e.key
 JOIN applications a ON a.id=e.application_id AND NOT a.archived
 WHERE NOT e.archived AND e.key=ANY($1::text[])
)
SELECT ` + incidentProjection + `
 FROM ops_incidents i
 JOIN incident_scopes e ON e.id=i.environment_id AND e.application_id=i.application_id
 LEFT JOIN users u ON u.id=i.assignee_id`

func scanIncident(row pgx.Row) (incidentRecord, error) {
	var v incidentRecord
	err := row.Scan(&v.ID, &v.SourceID, &v.Fingerprint, &v.Labels, &v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey, &v.Title, &v.Severity, &v.Status, &v.AlertStatus, &v.Summary, &v.AssigneeID, &v.Commander, &v.InvestigationID, &v.StartsAt, &v.UpdatedAt, &v.ResolvedAt,
		&v.UpstreamState, &v.UpstreamCheckedAt)
	return v, err
}

// A V3 case is a human-confirmed incident, distinct from an incoming alert
// occurrence. Nullable fields have no verified value when they were not set.
type caseRecord struct {
	ID, ApplicationID, EnvironmentID, EnvironmentKey, Title, Severity, Status, ActorID string
	Impact, CommanderText, InvestigationID                                             *string
	CreatedAt, UpdatedAt                                                               time.Time
	StatusRevision                                                                     int64
	ResolvedStatusEventID                                                              *int64
	ResolvedAt                                                                         *time.Time
}

const caseSelect = `SELECT c.id,c.application_id,c.environment_id,c.environment_key,
 c.title,c.severity,c.impact,c.commander_text,c.status,c.actor_id,c.investigation_id,
 c.created_at,c.updated_at,c.status_revision,c.resolved_status_event_id,c.resolved_at
 FROM v3_incident_cases c
 JOIN application_environments e ON e.id=c.environment_id AND e.application_id=c.application_id
   AND e.key=c.environment_key AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=c.application_id
   AND ve.environment_id=c.environment_key
 JOIN applications a ON a.id=c.application_id AND NOT a.archived`

func scanCase(row pgx.Row) (caseRecord, error) {
	var v caseRecord
	err := row.Scan(&v.ID, &v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey,
		&v.Title, &v.Severity, &v.Impact, &v.CommanderText, &v.Status,
		&v.ActorID, &v.InvestigationID, &v.CreatedAt, &v.UpdatedAt,
		&v.StatusRevision, &v.ResolvedStatusEventID, &v.ResolvedAt)
	return v, err
}

func validCase(v caseRecord) bool {
	if v.ID == "" || v.ApplicationID == "" || v.EnvironmentID == "" || v.EnvironmentKey == "" ||
		v.ActorID == "" || strings.TrimSpace(v.Title) == "" || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() ||
		v.UpdatedAt.Before(v.CreatedAt) || v.StatusRevision < 1 ||
		(v.ResolvedStatusEventID == nil) != (v.ResolvedAt == nil) ||
		(v.ResolvedStatusEventID != nil && v.Status != "resolved") {
		return false
	}
	switch v.Severity {
	case "P1", "P2", "P3", "P4":
	default:
		return false
	}
	switch v.Status {
	case "active", "mitigated", "resolved":
		return true
	default:
		return false
	}
}

func (s *Service) scoped(r *http.Request, app, env, action string) error {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || s.Authorize == nil || s.DB == nil {
		return store.ErrForbidden
	}
	found := false
	for _, id := range scope.EnvironmentIDs {
		if id == env {
			found = true
			break
		}
	}
	if !found {
		return store.ErrNotFound
	}
	return s.Authorize(r.Context(), httpapi.CurrentUser(r), app, env, action)
}

// Mutations authorize through the same transaction that holds the incident
// lock, so a revoked grant cannot race with the durable timeline write.
func (s *Service) scopedTx(r *http.Request, tx pgx.Tx, app, env, action string) error {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || s.AuthorizeTx == nil || tx == nil {
		return store.ErrForbidden
	}
	for _, id := range scope.EnvironmentIDs {
		if id == env {
			return s.AuthorizeTx(r.Context(), tx, httpapi.CurrentUser(r), app, env, action)
		}
	}
	return store.ErrNotFound
}

func (s *Service) incident(ctx context.Context, id string) (incidentRecord, error) {
	return scanIncident(s.DB.QueryRow(ctx, incidentSelect+` WHERE i.id=$1`, id))
}

type incidentSummary struct {
	ID string `json:"id"`
	// Severity is null when the recorded value maps onto no level;
	// SeverityReason then says why (PA-239).
	Severity       *string `json:"severity"`
	SeverityReason *string `json:"severity_reason,omitempty"`
	Title          string  `json:"title"`
	Status         string  `json:"status"`
	Duration       string  `json:"duration"`
	Commander      string  `json:"commander"`
}

type label struct {
	K string `json:"k"`
	V string `json:"v"`
}
type badge struct {
	Text string `json:"text"`
	Tone string `json:"tone"`
}

func severityRank(code string) int { return alertseverity.Rank(code) }

// A repeated severity parameter is a union over the aggregated group's
// highest severity. The value "all" only means no severity filter.
func alertSeverities(query url.Values) (map[string]bool, bool) {
	values := query["severity"]
	if len(values) == 0 || len(values) == 1 && values[0] == "" {
		return nil, true
	}
	selected := make(map[string]bool, len(values))
	for _, raw := range values {
		value := strings.ToUpper(strings.TrimSpace(raw))
		if value == "ALL" && len(values) == 1 {
			return nil, true
		}
		if value != "P1" && value != "P2" && value != "P3" && value != "P4" {
			return nil, false
		}
		selected[value] = true
	}
	return selected, true
}

func matchesAlertSeverity(selected map[string]bool, severity string) bool {
	return selected == nil || selected[severity]
}

func inboxFilter(query url.Values) (string, bool) {
	filters, tabs := query["filter"], query["tab"]
	if len(filters) > 1 || len(tabs) > 1 {
		return "", false
	}
	filter := "all"
	if len(filters) == 1 && filters[0] != "" {
		filter = filters[0]
	}
	if len(tabs) == 1 && tabs[0] != "" {
		if len(filters) == 1 && filters[0] != "" && tabs[0] != filter {
			return "", false
		}
		filter = tabs[0]
	}
	switch filter {
	case "all", "mine", "mentions", "subscribed":
		return filter, true
	default:
		return "", false
	}
}

func inboxTone(severityCode string) string {
	switch severityCode {
	case "P1":
		return "danger"
	case "P2":
		return "warn"
	default:
		return "neutral"
	}
}

func incidentStatus(v incidentRecord) string {
	if v.Status == "resolved" {
		return "resolved"
	}
	if v.AlertStatus == "resolved" {
		return "mitigated"
	}
	return "active"
}
func duration(since, now time.Time) string {
	d := now.Sub(since)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}
func summarize(v incidentRecord, now time.Time, t ...text) incidentSummary {
	status := incidentStatus(v)
	elapsed := duration(v.StartsAt, now)
	if status == "resolved" {
		// The operations relay resolves an incident from investigation.closed.
		// updated_at may change later, and ends_at tracks the alert, not the
		// incident. Historic resolved records without a close event have no
		// trustworthy duration; expose that gap instead of inventing one.
		elapsed = firstText(t).unrecorded()
		if v.ResolvedAt != nil {
			elapsed = duration(v.StartsAt, *v.ResolvedAt)
		}
	}
	level, reason := alertseverity.StoredPtr(v.Severity, v.Labels)
	return incidentSummary{ID: v.ID, Severity: level, SeverityReason: reason, Title: v.Title, Status: status, Duration: elapsed, Commander: v.Commander}
}

// listVisible captures alert fields in one statement before checking current
// authority. The caller's link-table lock keeps case membership stable; the
// read projection must not hold incident row locks against claims.
func (s *Service) listVisible(r *http.Request, tx pgx.Tx) ([]incidentRecord, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || tx == nil || (s.Authorize == nil && s.AuthorizeTx == nil) {
		return nil, store.ErrForbidden
	}
	rows, err := tx.Query(r.Context(), incidentSelect+` WHERE e.key=ANY($1::text[])
 AND NOT EXISTS (SELECT 1 FROM v3_incident_case_alerts linked WHERE linked.alert_id=i.id)
 ORDER BY i.updated_at DESC,i.id DESC LIMIT 5001`, scope.EnvironmentIDs)
	if err != nil {
		return nil, err
	}
	out := make([]incidentRecord, 0)
	candidates := make([]incidentRecord, 0)
	scanned := 0
	for rows.Next() {
		scanned++
		// A bounded query must never be mistaken for complete totals.
		if scanned > 5000 {
			rows.Close()
			return nil, errTooManyIncidents
		}
		v, err := scanIncident(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range candidates {
		if s.AuthorizeTx != nil {
			err = s.authorizeAlertTx(r, tx, v, "view")
		} else {
			// Older callers without transaction-aware V3 policy keep their
			// existing read behavior; created cases remain unavailable to them.
			err = s.scoped(r, v.ApplicationID, v.EnvironmentKey, "view")
		}
		if err != nil {
			if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// listVisibleCases similarly captures case fields without blocking lifecycle
// writes. Current authority is checked against each captured scope, and the
// caller retains the link-table boundary until both list reads finish.
func (s *Service) listVisibleCases(r *http.Request, tx pgx.Tx) ([]caseRecord, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || tx == nil {
		return nil, store.ErrForbidden
	}
	rows, err := tx.Query(r.Context(), caseSelect+` WHERE c.environment_key=ANY($1::text[])
 ORDER BY c.updated_at DESC,c.id DESC LIMIT 5001`, scope.EnvironmentIDs)
	if err != nil {
		return nil, err
	}
	candidates := make([]caseRecord, 0)
	for rows.Next() {
		if len(candidates) == 5000 {
			rows.Close()
			return nil, errTooManyIncidents
		}
		v, scanErr := scanCase(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(candidates) > 0 && s.AuthorizeTx == nil {
		return nil, store.ErrInvalidReference
	}
	out := make([]caseRecord, 0, len(candidates))
	for _, v := range candidates {
		// authorizeAlertTx uses only the persisted application/environment
		// identity. It also locks the current owner, session and V3 grant rows.
		identity := incidentRecord{ApplicationID: v.ApplicationID,
			EnvironmentID: v.EnvironmentID, EnvironmentKey: v.EnvironmentKey}
		err = s.authorizeAlertTx(r, tx, identity, "view")
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !validCase(v) {
			return nil, store.ErrInvalidReference
		}
		out = append(out, v)
	}
	return out, nil
}

var errTooManyIncidents = errors.New("V3 incident list exceeds unpaginated safety limit")

func (s *Service) Incidents(w http.ResponseWriter, r *http.Request) {
	filter, ok := incidentFilter(r.URL.Query())
	if !ok {
		httpapi.Error(w, 422, "invalid_status", "Use active, resolved, or all")
		return
	}
	if s == nil || s.DB == nil {
		respondErr(w, store.ErrInvalidReference)
		return
	}
	// Read committed lets the final V3 grant check observe a concurrent revoke.
	// The link table lock serializes creation with both list reads, so one alert
	// cannot appear both alone and inside a newly committed case.
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `LOCK TABLE v3_incident_case_alerts IN SHARE MODE`); err != nil {
		respondErr(w, err)
		return
	}
	values, err := s.listVisible(r, tx)
	if err != nil {
		respondErr(w, err)
		return
	}
	cases, err := s.listVisibleCases(r, tx)
	if err != nil {
		respondErr(w, err)
		return
	}
	if len(values)+len(cases) > 5000 {
		respondErr(w, errTooManyIncidents)
		return
	}
	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondErr(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	items, counts := incidentListWithCases(values, cases, filter, time.Now(), textFor(r.Header.Get("Accept-Language")))
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts})
}

type caseSummary struct {
	ID            string  `json:"id"`
	Severity      string  `json:"severity"`
	Title         string  `json:"title"`
	Status        string  `json:"status"`
	Duration      *string `json:"duration"`
	Commander     *string `json:"commander"`
	RecordKind    string  `json:"record_kind"`
	ApplicationID string  `json:"application_id"`
	EnvironmentID string  `json:"environment_id"`
}

type datedIncidentSummary struct {
	updated time.Time
	id      string
	status  string
	value   any
}

func incidentListWithCases(alerts []incidentRecord, cases []caseRecord, filter string, now time.Time, t ...text) ([]any, map[string]int) {
	items := make([]datedIncidentSummary, 0, len(alerts)+len(cases))
	counts := map[string]int{"all": 0, "active": 0, "resolved": 0}
	for _, v := range alerts {
		status := incidentStatus(v)
		counts["all"]++
		if status == "resolved" {
			counts["resolved"]++
		} else {
			counts["active"]++
		}
		items = append(items, datedIncidentSummary{v.UpdatedAt, v.ID, status, summarize(v, now, t...)})
	}
	for _, v := range cases {
		counts["all"]++
		if v.Status == "resolved" {
			counts["resolved"]++
		} else {
			counts["active"]++
		}
		items = append(items, datedIncidentSummary{v.UpdatedAt, v.ID, v.Status,
			caseSummary{ID: v.ID, Severity: v.Severity, Title: v.Title, Status: v.Status,
				Commander: v.CommanderText, RecordKind: "case", ApplicationID: v.ApplicationID,
				EnvironmentID: v.EnvironmentID}})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].updated.Equal(items[j].updated) {
			return items[i].id > items[j].id
		}
		return items[i].updated.After(items[j].updated)
	})
	visible := make([]any, 0, len(items))
	for _, item := range items {
		if filter == "active" && item.status == "resolved" || filter == "resolved" && item.status != "resolved" {
			continue
		}
		visible = append(visible, item.value)
	}
	return visible, counts
}

func incidentFilter(query url.Values) (string, bool) {
	status := query["status"]
	if len(status) == 0 {
		return "active", true
	}
	if len(status) != 1 {
		return "", false
	}
	switch status[0] {
	case "active", "resolved", "all":
		return status[0], true
	default:
		return "", false
	}
}

func incidentList(values []incidentRecord, filter string, now time.Time, t ...text) ([]incidentSummary, map[string]int) {
	items := make([]incidentSummary, 0, len(values))
	counts := map[string]int{"all": 0, "active": 0, "resolved": 0}
	for _, v := range values {
		status := incidentStatus(v)
		counts["all"]++
		if status == "resolved" {
			counts["resolved"]++
		} else {
			counts["active"]++
		}
		if filter == "active" && status == "resolved" || filter == "resolved" && status != "resolved" {
			continue
		}
		items = append(items, summarize(v, now, t...))
	}
	return items, counts
}

type timelineEntry struct {
	Time  string `json:"time"`
	Actor struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"actor"`
	Text string `json:"text"`
}

func (s *Service) AppendTimeline(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text string `json:"text"`
	}
	if !httpapi.Decode(w, r, &input) {
		return
	}
	input.Text = strings.TrimSpace(input.Text)
	if n := len([]rune(input.Text)); n < 1 || n > 2000 {
		httpapi.Error(w, 422, "invalid_text", "Timeline text must contain 1–2000 characters")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	caseValue, caseErr := scanCase(tx.QueryRow(r.Context(), caseSelect+` WHERE c.id=$1 FOR UPDATE OF c`, r.PathValue("id")))
	if caseErr == nil {
		s.appendCaseTimeline(w, r, tx, caseValue, input.Text)
		return
	}
	if !errors.Is(caseErr, pgx.ErrNoRows) {
		respondErr(w, caseErr)
		return
	}
	v, err := scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1 FOR UPDATE OF i`, r.PathValue("id")))
	if err != nil {
		respondErr(w, err)
		return
	}
	if err = s.scopedTx(r, tx, v.ApplicationID, v.EnvironmentKey, "change"); err != nil {
		respondErr(w, err)
		return
	}
	id := store.ID()
	user := httpapi.CurrentUser(r)
	var created time.Time
	err = tx.QueryRow(r.Context(), `INSERT INTO v3_incident_timeline(id,incident_id,actor_id,text) VALUES($1,$2,$3,$4) RETURNING created_at`, id, v.ID, user.ID, input.Text).Scan(&created)
	if err != nil {
		respondErr(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE ops_incidents SET updated_at=$2 WHERE id=$1`, v.ID, created)
	if err != nil {
		respondErr(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user',$2,'incident.timeline.append',$3,'succeeded',jsonb_build_object('entry_id',$4::text,'environment_id',$5::text))`, user.ID, v.ApplicationID, v.ID, id, v.EnvironmentKey)
	if err != nil {
		respondErr(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	entry := timelineEntry{Time: created.UTC().Format(time.RFC3339Nano), Text: input.Text}
	entry.Actor.Kind = "person"
	entry.Actor.Name = user.Name
	httpapi.WriteJSON(w, 201, entry)
}

func respondErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Event not found")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Event access denied")
	case errors.Is(err, errTooManyIncidents):
		httpapi.Error(w, 503, "pagination_required", "Incident results exceed the current V3 list contract")
	default:
		httpapi.Error(w, 503, "events_unavailable", "Event storage unavailable")
	}
}
