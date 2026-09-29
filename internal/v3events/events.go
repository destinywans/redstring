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
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
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
	// AuthorizeTeamTx checks events grants against the current locked owner team.
	// The composition root injects the same V3 policy used by AuthorizeTx.
	AuthorizeTeamTx AuthorizeTeamWithTx
	Location        *time.Location
}

func New(db *pgxpool.Pool, authorize Authorize, authorizeTx AuthorizeWithTx) *Service {
	return &Service{DB: db, Authorize: authorize, AuthorizeTx: authorizeTx, Location: time.Local}
}

// RegisterNew registers only routes that do not collide with operations.Register.
func (s *Service) RegisterNew(api *httpapi.Server) {
	api.Register("GET /api/v1/inbox", "viewer", s.Inbox)
	api.MarkV3ObjectRoute("GET /api/v1/inbox", "events", "view")
	api.Register("GET /api/v1/alert-groups", "viewer", s.AlertGroups)
	api.MarkV3ObjectRoute("GET /api/v1/alert-groups", "events", "view")
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
	api.Register("GET /api/v1/incidents", "viewer", s.Incidents)
	api.MarkV3ObjectRoute("GET /api/v1/incidents", "events", "view")
	api.Register("GET /api/v1/incidents/{id}", "viewer", s.Incident)
	api.MarkV3ObjectRoute("GET /api/v1/incidents/{id}", "events", "view")
}

type incidentRecord struct {
	ID, ApplicationID, EnvironmentID, EnvironmentKey, Title, Severity, Status, AlertStatus, Summary, AssigneeID, Commander, InvestigationID string
	StartsAt, UpdatedAt                                                                                                                     time.Time
	ResolvedAt                                                                                                                              *time.Time
}

const incidentSelect = `SELECT i.id,i.application_id,i.environment_id,e.key,i.title,i.severity,i.status,i.alert_status,i.summary,i.assignee_id,COALESCE(u.name,''),i.investigation_id,i.starts_at,i.updated_at,
 (SELECT MIN(closed.created_at) FROM events closed WHERE closed.investigation_id=i.investigation_id AND closed.type='investigation.closed')
 FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 LEFT JOIN users u ON u.id=i.assignee_id`

func scanIncident(row pgx.Row) (incidentRecord, error) {
	var v incidentRecord
	err := row.Scan(&v.ID, &v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey, &v.Title, &v.Severity, &v.Status, &v.AlertStatus, &v.Summary, &v.AssigneeID, &v.Commander, &v.InvestigationID, &v.StartsAt, &v.UpdatedAt, &v.ResolvedAt)
	return v, err
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
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Duration  string `json:"duration"`
	Commander string `json:"commander"`
}

type inboxItem struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Tone    string `json:"tone"`
	Title   string `json:"title"`
	Sub     string `json:"sub"`
	Time    string `json:"time"`
	Unread  bool   `json:"unread"`
	Day     string `json:"day"`
	Href    string `json:"href"`
	AlertID string `json:"alert_id,omitempty"`
}

type label struct {
	K string `json:"k"`
	V string `json:"v"`
}
type badge struct {
	Text string `json:"text"`
	Tone string `json:"tone"`
}
type alertInstance struct {
	ID       string    `json:"id"`
	Value    string    `json:"value"`
	State    string    `json:"state"`
	Duration string    `json:"duration"`
	Series   []float64 `json:"series"`
}
type alertGroup struct {
	ID       string  `json:"id"`
	Severity string  `json:"severity"`
	Labels   []label `json:"labels"`
	Service  string  `json:"service"`
	Count    int     `json:"count"`
	State    string  `json:"state"`
	Since    string  `json:"since"`
	Notify   struct {
		Channel string `json:"channel,omitempty"`
		Text    string `json:"text"`
	} `json:"notify"`
	Badges    []badge         `json:"badges"`
	Owner     string          `json:"owner,omitempty"`
	Instances []alertInstance `json:"instances"`
}

func (s *Service) AlertGroups(w http.ResponseWriter, r *http.Request) {
	severities, valid := alertSeverities(r.URL.Query())
	if !valid {
		httpapi.Error(w, 422, "invalid_severity", "Choose P1, P2, P3, or P4")
		return
	}
	state := r.URL.Query().Get("state")
	if state != "" && state != "firing" && state != "pending" && state != "silenced" && state != "resolved" {
		httpapi.Error(w, 422, "invalid_state", "Unknown alert state")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 200 {
		httpapi.Error(w, 422, "invalid_query", "Search is too long")
		return
	}
	requestedEnv := r.URL.Query().Get("env")
	if requestedEnv != "" {
		scope, ok := httpapi.V3ScopeFromContext(r.Context())
		allowed := false
		if ok {
			for _, env := range scope.EnvironmentIDs {
				if env == requestedEnv {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			httpapi.Error(w, 403, "forbidden", "Environment access denied")
			return
		}
	}
	values, err := s.listVisible(r)
	if err != nil {
		respondErr(w, err)
		return
	}
	notifications, err := s.latestNotifications(r.Context(), values)
	if err != nil {
		respondErr(w, err)
		return
	}
	groups := map[string]*alertGroup{}
	earliest := map[string]time.Time{}
	notifiedAt := map[string]time.Time{}
	now := time.Now()
	loc := s.Location
	if loc == nil {
		loc = time.Local
	}
	for _, v := range values {
		if requestedEnv != "" && v.EnvironmentKey != requestedEnv {
			continue
		}
		if q != "" && !searchtext.Match(v.Title+" "+v.ApplicationID+" "+v.EnvironmentKey, q) {
			continue
		}
		key := v.EnvironmentKey + "\x00" + v.ApplicationID + "\x00" + v.Title
		g := groups[key]
		if g == nil {
			g = &alertGroup{ID: "group:" + store.Hash(key)[:24], Severity: severity(v.Severity), Labels: []label{{K: "service", V: v.ApplicationID}, {K: "alertname", V: v.Title}, {K: "environment", V: v.EnvironmentKey}}, Service: v.ApplicationID, State: "resolved", Since: v.StartsAt.In(loc).Format("01/02 15:04"), Badges: []badge{}, Instances: []alertInstance{}}
			g.Notify.Text = "通知分组状态未接入"
			groups[key] = g
			earliest[key] = v.StartsAt
		}
		g.Count++
		if n, ok := notifications[v.ID]; ok && n.At.After(notifiedAt[key]) {
			notifiedAt[key] = n.At
			g.Notify.Channel = n.Channel
			g.Notify.Text = notificationText(n.Status)
		}
		if severityRank(severity(v.Severity)) < severityRank(g.Severity) {
			g.Severity = severity(v.Severity)
		}
		if v.AlertStatus == "firing" {
			g.State = "firing"
			g.Instances = append(g.Instances, alertInstance{ID: v.ID, Value: "指标未接入", State: "firing", Duration: duration(v.StartsAt, now), Series: []float64{}})
		}
		if v.StartsAt.Before(earliest[key]) {
			earliest[key] = v.StartsAt
			g.Since = v.StartsAt.In(loc).Format("01/02 15:04")
		}
	}
	all := make([]alertGroup, 0, len(groups))
	for _, g := range groups {
		all = append(all, *g)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Severity != all[j].Severity {
			return severityRank(all[i].Severity) < severityRank(all[j].Severity)
		}
		return all[i].ID < all[j].ID
	})
	counts := map[string]int{"all": 0, "firing": 0, "pending": 0, "silenced": 0, "resolved": 0}
	items := make([]alertGroup, 0, len(all))
	for _, g := range all {
		if !matchesAlertSeverity(severities, g.Severity) {
			continue
		}
		counts["all"]++
		counts[g.State]++
		if state == "" || state == g.State {
			items = append(items, g)
		}
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts})
}

type notificationRecord struct {
	Channel, Status string
	At              time.Time
}

func (s *Service) latestNotifications(ctx context.Context, incidents []incidentRecord) (map[string]notificationRecord, error) {
	out := map[string]notificationRecord{}
	if len(incidents) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(incidents))
	for _, v := range incidents {
		ids = append(ids, v.ID)
	}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT ON (incident_id) incident_id,channel,status,created_at FROM ops_notifications WHERE incident_id=ANY($1::text[]) ORDER BY incident_id,created_at DESC`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n notificationRecord
		if err := rows.Scan(&id, &n.Channel, &n.Status, &n.At); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func notificationText(status string) string {
	switch status {
	case "sent":
		return "已投递"
	case "pending":
		return "待投递"
	case "sending":
		return "投递中"
	case "retrying":
		return "重试中"
	case "failed":
		return "投递失败"
	default:
		return "通知状态未知"
	}
}

func severityRank(code string) int {
	switch code {
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	default:
		return 4
	}
}

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

func (s *Service) Inbox(w http.ResponseWriter, r *http.Request) {
	filter, valid := inboxFilter(r.URL.Query())
	if !valid {
		httpapi.Error(w, 422, "invalid_filter", "Unknown or conflicting inbox filter")
		return
	}
	values, err := s.listVisible(r)
	if err != nil {
		respondErr(w, err)
		return
	}
	loc := s.Location
	if loc == nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	yesterday := today.AddDate(0, 0, -1)
	items := make([]inboxItem, 0)
	counts := map[string]int{"all": 0, "mine": 0, "mentions": 0, "subscribed": 0}
	for _, v := range values {
		at := v.UpdatedAt.In(loc)
		if at.Before(yesterday) {
			continue
		}
		counts["all"]++
		mine := v.AssigneeID != "" && v.AssigneeID == httpapi.CurrentUser(r).ID
		if mine {
			counts["mine"]++
		}
		if filter == "mine" && !mine || filter == "mentions" || filter == "subscribed" {
			continue
		}
		day := "today"
		if at.Before(today) {
			day = "yesterday"
		}
		sub := v.Summary
		if sub == "" {
			sub = "告警来源：" + v.ApplicationID
		}
		item := inboxItem{ID: "alert:" + v.ID, Kind: "alert", Tone: inboxTone(severity(v.Severity)), Title: v.Title, Sub: sub, Time: at.Format("15:04"), Unread: false, Day: day, Href: "/inbox?item=alert:" + v.ID, AlertID: v.ID}
		items = append(items, item)
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts})
}

func severity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "p1", "critical", "fatal":
		return "P1"
	case "p2", "warning", "warn", "high":
		return "P2"
	case "p3", "info", "medium":
		return "P3"
	default:
		return "P4"
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
func summarize(v incidentRecord, now time.Time) incidentSummary {
	status := incidentStatus(v)
	elapsed := duration(v.StartsAt, now)
	if status == "resolved" {
		// The operations relay resolves an incident from investigation.closed.
		// updated_at may change later, and ends_at tracks the alert, not the
		// incident. Historic resolved records without a close event have no
		// trustworthy duration; expose that gap instead of inventing one.
		elapsed = "未记录"
		if v.ResolvedAt != nil {
			elapsed = duration(v.StartsAt, *v.ResolvedAt)
		}
	}
	return incidentSummary{ID: v.ID, Severity: severity(v.Severity), Title: v.Title, Status: status, Duration: elapsed, Commander: v.Commander}
}

func (s *Service) listVisible(r *http.Request) ([]incidentRecord, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || s.DB == nil || s.Authorize == nil {
		return nil, store.ErrForbidden
	}
	rows, err := s.DB.Query(r.Context(), incidentSelect+` WHERE e.key=ANY($1::text[]) ORDER BY i.updated_at DESC,i.id DESC LIMIT 5001`, scope.EnvironmentIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]incidentRecord, 0)
	scanned := 0
	for rows.Next() {
		scanned++
		// A bounded query must never be mistaken for complete totals.
		if scanned > 5000 {
			return nil, errTooManyIncidents
		}
		v, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		if err = s.scoped(r, v.ApplicationID, v.EnvironmentKey, "view"); err != nil {
			if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

var errTooManyIncidents = errors.New("V3 incident list exceeds unpaginated safety limit")

func (s *Service) Incidents(w http.ResponseWriter, r *http.Request) {
	filter, ok := incidentFilter(r.URL.Query())
	if !ok {
		httpapi.Error(w, 422, "invalid_status", "Use active, resolved, or all")
		return
	}
	values, err := s.listVisible(r)
	if err != nil {
		respondErr(w, err)
		return
	}
	items, counts := incidentList(values, filter, time.Now())
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts})
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

func incidentList(values []incidentRecord, filter string, now time.Time) ([]incidentSummary, map[string]int) {
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
		items = append(items, summarize(v, now))
	}
	return items, counts
}

// Incident deliberately does not synthesize required SLO/error-budget numbers.
// The incident identity and timeline can be read, but V3 Incident currently
// requires numeric impact fields that ops_incidents never captured.
func (s *Service) Incident(w http.ResponseWriter, r *http.Request) {
	v, err := s.incident(r.Context(), r.PathValue("id"))
	if err != nil {
		respondErr(w, err)
		return
	}
	if err = s.scoped(r, v.ApplicationID, v.EnvironmentKey, "view"); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.Error(w, 503, "impact_unavailable", "Incident impact data is not connected; no SLO numbers will be invented")
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
