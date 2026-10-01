package v3events

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

// A binding is an operator assertion that these webhook source IDs feed the
// same environment-bound Alertmanager. Labels must independently agree with
// the configured environment and application mapping for each occurrence.
type GroupPolicyBinding struct {
	SourceIDs           map[string]bool
	EnvironmentLabel    string
	ApplicationBindings map[string]string
}

type GroupPolicySource interface {
	ObserveGroupPolicy(context.Context, string, map[string]string) (v3alertmanager.GroupPolicyObservation, error)
}

// These IDs describe a local aggregation of persisted incidents. They do not
// identify an Alertmanager group or disclose its routing configuration.
func localAlertGroupID(v incidentRecord) string {
	key := strings.Join([]string{v.EnvironmentID, v.ApplicationID, v.SourceID, v.Fingerprint}, "\x00")
	return "group:" + store.Hash(key)[:24]
}

type alertGroupInstance struct {
	ID       string    `json:"id"`
	Value    *string   `json:"value"`
	State    string    `json:"state"`
	Duration string    `json:"duration"`
	Series   []float64 `json:"series"`
	Pending  any       `json:"pending"`
}

type alertGroupNotify struct {
	Channel *string `json:"channel"`
	Text    string  `json:"text"`
	Status  *string `json:"status"`
	Scope   string  `json:"scope"`
}

type alertGroup struct {
	ID             string               `json:"id"`
	Severity       string               `json:"severity"`
	Labels         []label              `json:"labels"`
	Service        string               `json:"service"`
	SourceID       string               `json:"source_id"`
	Fingerprint    string               `json:"fingerprint"`
	EnvironmentID  string               `json:"environment_id"`
	EnvironmentKey string               `json:"environment_key"`
	Count          int                  `json:"count"`
	CountKind      string               `json:"count_kind"`
	State          string               `json:"state"`
	Since          string               `json:"since"`
	Notify         alertGroupNotify     `json:"notify"`
	Badges         []badge              `json:"badges"`
	Owner          *string              `json:"owner"`
	Inhibited      *bool                `json:"inhibited"`
	Instances      []alertGroupInstance `json:"instances"`
	LocalGrouping  []string             `json:"local_grouping"`
	Partial        bool                 `json:"partial"`
	Unavailable    []string             `json:"unavailable"`
}

type alertGroupAccumulator struct {
	group       alertGroup
	key         string
	earliest    time.Time
	ownerID     string
	ownerName   string
	sharedOwner bool
	latest      *localNotification
}

type localNotification struct {
	ID, IncidentID, ApplicationID, EnvironmentID, Channel, Status string
	Attempts                                                      int
	At                                                            time.Time
}

func alertGroupQuery(w http.ResponseWriter, r *http.Request) (string, string, string, map[string]bool, bool) {
	query := r.URL.Query()
	severities, valid := alertSeverities(query)
	if !valid {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_severity", "Choose P1, P2, P3, or P4")
		return "", "", "", nil, false
	}
	if len(query["state"]) > 1 || len(query["q"]) > 1 || len(query["env"]) > 1 || len(query["show_inhibited"]) > 1 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_filter", "Duplicate alert group filter")
		return "", "", "", nil, false
	}
	state := query.Get("state")
	if state != "" && state != "firing" && state != "pending" && state != "resolved" && state != "silenced" {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_state", "Unknown alert state")
		return "", "", "", nil, false
	}
	if state == "silenced" {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_state_unavailable", "Current group silence state has no exact source")
		return "", "", "", nil, false
	}
	showInhibited := query.Get("show_inhibited")
	if showInhibited != "" && showInhibited != "true" && showInhibited != "false" {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_show_inhibited", "Use true or false")
		return "", "", "", nil, false
	}
	if showInhibited == "false" {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_unavailable", "Current group inhibition has no exact source")
		return "", "", "", nil, false
	}
	q := strings.TrimSpace(query.Get("q"))
	if len([]rune(q)) > 200 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_query", "Search is too long")
		return "", "", "", nil, false
	}
	env := query.Get("env")
	if env != "" {
		scope, ok := httpapi.V3ScopeFromContext(r.Context())
		selected := false
		if ok {
			for _, id := range scope.EnvironmentIDs {
				selected = selected || id == env
			}
		}
		if !selected {
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Environment access denied")
			return "", "", "", nil, false
		}
	}
	return q, env, state, severities, true
}

// visibleAlertIncidentsTx captures incident fields from one statement snapshot
// without locking every incident against claims. Current authorization locks
// remain through the response transaction; a missing team callback never
// permits falling back to a legacy ACL.
func (s *Service) visibleAlertIncidentsTx(r *http.Request, tx pgx.Tx, requestedEnv string) ([]incidentRecord, error) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || s.AuthorizeTx == nil {
		return nil, store.ErrForbidden
	}
	rows, err := tx.Query(r.Context(), incidentListSelect+` WHERE e.key=ANY($1::text[]) AND ($2='' OR e.key=$2)
		ORDER BY i.updated_at DESC,i.id DESC LIMIT 5001`, scope.EnvironmentIDs, requestedEnv)
	if err != nil {
		return nil, err
	}
	candidates := make([]incidentRecord, 0)
	for rows.Next() {
		if len(candidates) == 5000 {
			rows.Close()
			return nil, errTooManyIncidents
		}
		v, scanErr := scanIncident(rows)
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
	// The first scope still locks app, then actor/session, then grants. Later
	// scopes reuse those held locks, but re-read scope, policy and DB expiry.
	guard := newListAuthorityGuard(r, tx)
	allowed := make(map[string]bool)
	denied := make(map[string]bool)
	visible := make([]incidentRecord, 0, len(candidates))
	for _, v := range candidates {
		key := v.ApplicationID + "\x00" + v.EnvironmentID
		if !allowed[key] && !denied[key] {
			authErr := s.authorizeAlertWithGuard(r, tx, v, "view", guard)
			if errors.Is(authErr, store.ErrForbidden) || errors.Is(authErr, store.ErrNotFound) {
				denied[key] = true
			} else if authErr != nil {
				return nil, authErr
			} else {
				allowed[key] = true
			}
		}
		if allowed[key] {
			visible = append(visible, v)
		}
	}
	return visible, nil
}

func activeAlertReadSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
		WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return store.ErrSessionRevoked
	}
	return err
}

func groupSearchMatches(v incidentRecord, q string) bool {
	if q == "" {
		return true
	}
	return searchtext.Match(strings.Join([]string{v.Title, v.ApplicationID, v.EnvironmentKey, v.SourceID, v.Fingerprint,
		"service=" + v.ApplicationID, "environment=" + v.EnvironmentKey}, " "), q)
}

func aggregateAlertGroups(values []incidentRecord, now time.Time) (map[string]*alertGroupAccumulator, error) {
	groups := make(map[string]*alertGroupAccumulator)
	for _, v := range values {
		severityCode, valid := alertDetailSeverity(v.Severity)
		if !valid || strings.TrimSpace(v.SourceID) == "" || strings.TrimSpace(v.Fingerprint) == "" || strings.TrimSpace(v.Title) == "" || v.StartsAt.IsZero() || v.EnvironmentKey == "" {
			return nil, store.ErrInvalidReference
		}
		if v.AlertStatus != "firing" && v.AlertStatus != "pending" && v.AlertStatus != "resolved" {
			return nil, store.ErrInvalidReference
		}
		id := localAlertGroupID(v)
		key := strings.Join([]string{v.EnvironmentID, v.ApplicationID, v.SourceID, v.Fingerprint}, "\x00")
		g := groups[id]
		if g == nil {
			g = &alertGroupAccumulator{key: key, earliest: v.StartsAt, ownerID: v.AssigneeID,
				ownerName: v.Commander, sharedOwner: v.AssigneeID != "" && v.Commander != ""}
			g.group = alertGroup{
				ID: id, Severity: severityCode,
				Labels: []label{{K: "application_id", V: v.ApplicationID}, {K: "environment", V: v.EnvironmentKey},
					{K: "source_id", V: v.SourceID}, {K: "fingerprint", V: v.Fingerprint}},
				Service: v.ApplicationID, SourceID: v.SourceID, Fingerprint: v.Fingerprint,
				EnvironmentID: v.EnvironmentID, EnvironmentKey: v.EnvironmentKey,
				CountKind: "persisted_incidents", State: "resolved", Since: v.StartsAt.UTC().Format(time.RFC3339Nano),
				Notify: alertGroupNotify{Text: "平台无通知记录；外部通知状态未知", Scope: "platform_queue"},
				Badges: []badge{}, Instances: []alertGroupInstance{},
				LocalGrouping: []string{"environment_id", "application_id", "source_id", "fingerprint"},
				Partial:       true,
				Unavailable: []string{"provider_grouping", "provider_labels", "current_value", "metric_series",
					"pending_progress", "silence_state", "inhibition", "external_notification_status"},
			}
			groups[id] = g
		} else if g.key != key {
			// A truncated hash collision must not merge different scope identities.
			return nil, store.ErrInvalidReference
		}
		g.group.Count++
		if severityRank(severityCode) < severityRank(g.group.Severity) {
			g.group.Severity = severityCode
		}
		if v.AlertStatus == "firing" || v.AlertStatus == "pending" && g.group.State != "firing" {
			g.group.State = v.AlertStatus
		}
		if v.AlertStatus != "resolved" {
			g.group.Instances = append(g.group.Instances, alertGroupInstance{
				ID: v.ID, State: v.AlertStatus, Duration: duration(v.StartsAt, now),
			})
		}
		if v.StartsAt.Before(g.earliest) {
			g.earliest = v.StartsAt
			g.group.Since = v.StartsAt.UTC().Format(time.RFC3339Nano)
		}
		if v.AssigneeID == "" || v.AssigneeID != g.ownerID || v.Commander == "" {
			g.sharedOwner = false
		}
	}
	for _, g := range groups {
		if g.sharedOwner {
			owner := g.ownerName
			g.group.Owner = &owner
		} else {
			g.group.Unavailable = append(g.group.Unavailable, "single_owner")
		}
	}
	return groups, nil
}

func incidentNotificationScope(ctx context.Context, tx pgx.Tx, values []incidentRecord) error {
	if len(values) == 0 {
		return nil
	}
	ids, apps, envs := make([]string, 0, len(values)), make([]string, 0, len(values)), make([]string, 0, len(values))
	for _, v := range values {
		ids, apps, envs = append(ids, v.ID), append(apps, v.ApplicationID), append(envs, v.EnvironmentID)
	}
	var mismatched bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM unnest($1::text[],$2::text[],$3::text[]) AS expected(id,app,env)
		JOIN ops_notifications n ON n.incident_id=expected.id
		WHERE n.application_id<>expected.app OR n.environment_id<>expected.env
	)`, ids, apps, envs).Scan(&mismatched)
	if err != nil {
		return err
	}
	if mismatched {
		return errAlertNotificationScope
	}
	return nil
}

func scanLocalNotification(rows pgx.Rows) (localNotification, error) {
	var n localNotification
	err := rows.Scan(&n.ID, &n.IncidentID, &n.ApplicationID, &n.EnvironmentID,
		&n.Channel, &n.Status, &n.At, &n.Attempts)
	if err != nil {
		return n, err
	}
	if n.ID == "" || n.IncidentID == "" || n.Channel == "" || n.At.IsZero() || n.Attempts < 0 {
		return n, store.ErrInvalidReference
	}
	switch n.Status {
	case "pending", "sending", "retrying", "failed", "sent":
		return n, nil
	default:
		return n, store.ErrInvalidReference
	}
}

func latestLocalGroupNotifications(ctx context.Context, tx pgx.Tx, values []incidentRecord) (map[string]localNotification, error) {
	out := make(map[string]localNotification)
	if err := incidentNotificationScope(ctx, tx, values); err != nil || len(values) == 0 {
		return out, err
	}
	ids := make([]string, 0, len(values))
	expected := make(map[string]incidentRecord, len(values))
	for _, v := range values {
		ids = append(ids, v.ID)
		expected[v.ID] = v
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (incident_id)
		id,incident_id,application_id,environment_id,channel,status,created_at,attempts
		FROM ops_notifications WHERE incident_id=ANY($1::text[])
		ORDER BY incident_id,created_at DESC,id DESC`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		n, scanErr := scanLocalNotification(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		v, ok := expected[n.IncidentID]
		if !ok || n.ApplicationID != v.ApplicationID || n.EnvironmentID != v.EnvironmentID {
			return nil, errAlertNotificationScope
		}
		out[n.IncidentID] = n
	}
	return out, rows.Err()
}

func localNotificationText(status string) string {
	switch status {
	case "pending":
		return "平台通知待发送；外部状态未知"
	case "sending":
		return "平台通知发送中；外部状态未知"
	case "retrying":
		return "平台通知重试中；外部状态未知"
	case "failed":
		return "平台通知发送失败；外部状态未知"
	case "sent":
		return "平台通知传输接口已接受；未确认接收人已读"
	default:
		return "平台通知状态未知"
	}
}

func respondAlertGroupError(w http.ResponseWriter, err error) {
	if errors.Is(err, errAlertNotificationScope) {
		httpapi.Error(w, http.StatusServiceUnavailable, "alert_group_unavailable", "Alert notification scope is inconsistent")
		return
	}
	if errors.Is(err, errAlertPolicyScope) {
		httpapi.Error(w, http.StatusServiceUnavailable, "alert_policy_scope_unavailable", "Configured alert policy scope is inconsistent")
		return
	}
	respondAlertAuthorityError(w, err)
}

func (s *Service) AlertGroups(w http.ResponseWriter, r *http.Request) {
	q, env, state, severities, valid := alertGroupQuery(w, r)
	if !valid {
		return
	}
	if s == nil || s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	values, err := s.visibleAlertIncidentsTx(r, tx, env)
	if err != nil {
		respondAlertGroupError(w, err)
		return
	}
	filtered := make([]incidentRecord, 0, len(values))
	for _, v := range values {
		if groupSearchMatches(v, q) {
			filtered = append(filtered, v)
		}
	}
	groups, err := aggregateAlertGroups(filtered, time.Now())
	if err != nil {
		respondAlertGroupError(w, err)
		return
	}
	notifications, err := latestLocalGroupNotifications(r.Context(), tx, filtered)
	if err != nil {
		respondAlertGroupError(w, err)
		return
	}
	for _, v := range filtered {
		n, ok := notifications[v.ID]
		if !ok {
			continue
		}
		g := groups[localAlertGroupID(v)]
		if g.latest == nil || n.At.After(g.latest.At) || n.At.Equal(g.latest.At) && n.ID > g.latest.ID {
			copy := n
			g.latest = &copy
			g.group.Notify.Channel = &copy.Channel
			g.group.Notify.Status = &copy.Status
			g.group.Notify.Text = localNotificationText(copy.Status)
		}
	}
	items := make([]alertGroup, 0, len(groups))
	counts := map[string]any{}
	all, firing, pending, resolved := 0, 0, 0, 0
	counts["silenced"] = nil
	for _, g := range groups {
		if !matchesAlertSeverity(severities, g.group.Severity) {
			continue
		}
		all++
		switch g.group.State {
		case "firing":
			firing++
		case "pending":
			pending++
		case "resolved":
			resolved++
		}
		if state == "" || g.group.State == state {
			items = append(items, g.group)
		}
	}
	counts["all"], counts["firing"], counts["pending"], counts["resolved"] = all, firing, pending, resolved
	sort.Slice(items, func(i, j int) bool {
		if items[i].Severity != items[j].Severity {
			return severityRank(items[i].Severity) < severityRank(items[j].Severity)
		}
		return items[i].ID < items[j].ID
	})
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertGroupError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "counts": counts, "partial": true,
		"aggregation": "local_persisted_incidents",
		"unavailable": []string{"provider_grouping", "silence_state", "inhibition", "external_notification_status"},
	})
}

type alertGroupTimelineEntry struct {
	ID       string `json:"id"`
	Time     string `json:"time"`
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	Done     *bool  `json:"done"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	Incident string `json:"incident_id"`
}

type alertGroupPolicyResponse struct {
	GroupBy           any                       `json:"group_by"`
	Route             *string                   `json:"route"`
	Timing            any                       `json:"timing"`
	Timeline          []alertGroupTimelineEntry `json:"timeline"`
	Inhibition        any                       `json:"inhibition"`
	GroupID           string                    `json:"group_id"`
	Receivers         []string                  `json:"receivers"`
	PolicySource      *string                   `json:"policy_source"`
	PolicyObservedAt  *string                   `json:"policy_observed_at"`
	LocalGrouping     []string                  `json:"local_grouping"`
	TimelineScope     string                    `json:"timeline_scope"`
	TimelineTruncated bool                      `json:"timeline_truncated"`
	Partial           bool                      `json:"partial"`
	Unavailable       []string                  `json:"unavailable"`
}

type groupPolicyTiming struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
	Help  string `json:"help"`
}

var errAlertPolicyScope = errors.New("configured Alertmanager policy scope does not match persisted alert")

func groupMembers(visible []incidentRecord, id string) []incidentRecord {
	members := make([]incidentRecord, 0)
	for _, v := range visible {
		if localAlertGroupID(v) == id {
			members = append(members, v)
		}
	}
	return members
}

func digestGroupMembers(members []incidentRecord) (string, error) {
	// encoding/json sorts map keys. The digest is an in-request drift check,
	// never a public revision or a substitute for current authorization.
	encoded, err := json.Marshal(members)
	if err != nil {
		return "", err
	}
	return store.Hash(string(encoded)), nil
}

// configuredGroupPolicyLabels permits one exact provider read only when every
// member has the same authenticated webhook label set and the operator's
// explicit source, environment and application bindings agree with storage.
func (s *Service) configuredGroupPolicyLabels(members []incidentRecord) (map[string]string, string, error) {
	if s.GroupPolicySource == nil || len(members) == 0 {
		return nil, "policy_source", nil
	}
	first := members[0]
	binding, configured := s.GroupPolicyBindings[first.EnvironmentKey]
	if !configured || !binding.SourceIDs[first.SourceID] || binding.EnvironmentLabel == "" || len(binding.ApplicationBindings) == 0 {
		return nil, "policy_source_binding", nil
	}
	if len(first.Labels) == 0 {
		return nil, "occurrence_labels", nil
	}
	for _, v := range members {
		if len(v.Labels) == 0 {
			return nil, "occurrence_labels", nil
		}
		if v.SourceID != first.SourceID || v.ApplicationID != first.ApplicationID || v.EnvironmentKey != first.EnvironmentKey {
			return nil, "", errAlertPolicyScope
		}
		if !maps.Equal(v.Labels, first.Labels) {
			return nil, "route_ambiguous", nil
		}
	}
	labels := first.Labels
	if labels[binding.EnvironmentLabel] != first.EnvironmentKey ||
		binding.ApplicationBindings[labels["application"]] != first.ApplicationID || labels["application"] == "" {
		return nil, "", errAlertPolicyScope
	}
	return maps.Clone(labels), "", nil
}

func applyObservedGroupPolicy(result *alertGroupPolicyResponse, observed v3alertmanager.GroupPolicyObservation) {
	result.GroupBy = append([]string{}, observed.GroupBy...)
	result.Receivers = append([]string{}, observed.Receivers...)
	route := strings.Join(observed.Receivers, ", ")
	result.Route = &route
	result.Timing = []groupPolicyTiming{
		{Key: "group_wait", Label: "分组等待", Value: observed.GroupWait, Help: "Alertmanager 当前配置的首次分组等待时间"},
		{Key: "group_interval", Label: "分组间隔", Value: observed.GroupInterval, Help: "Alertmanager 当前配置的新告警通知间隔"},
		{Key: "repeat_interval", Label: "重复间隔", Value: observed.RepeatInterval, Help: "Alertmanager 当前配置的重复通知间隔"},
	}
	source := "configured_alertmanager"
	at := observed.ObservedAt.UTC().Format(time.RFC3339Nano)
	result.PolicySource = &source
	result.PolicyObservedAt = &at
}

func localGroupTimeline(ctx context.Context, tx pgx.Tx, values []incidentRecord) ([]alertGroupTimelineEntry, bool, error) {
	if err := incidentNotificationScope(ctx, tx, values); err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(values))
	expected := make(map[string]incidentRecord, len(values))
	for _, v := range values {
		ids = append(ids, v.ID)
		expected[v.ID] = v
	}
	rows, err := tx.Query(ctx, `SELECT id,incident_id,application_id,environment_id,channel,status,created_at,attempts
		FROM ops_notifications WHERE incident_id=ANY($1::text[])
		ORDER BY created_at DESC,id DESC LIMIT 501`, ids)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	timeline := make([]alertGroupTimelineEntry, 0)
	truncated := false
	for rows.Next() {
		n, scanErr := scanLocalNotification(rows)
		if scanErr != nil {
			return nil, false, scanErr
		}
		v, ok := expected[n.IncidentID]
		if !ok || n.ApplicationID != v.ApplicationID || n.EnvironmentID != v.EnvironmentID {
			return nil, false, errAlertNotificationScope
		}
		if len(timeline) == 500 {
			truncated = true
			break
		}
		timeline = append(timeline, alertGroupTimelineEntry{
			ID: n.ID, Time: n.At.UTC().Format(time.RFC3339Nano), Channel: n.Channel,
			Text: localNotificationText(n.Status), Status: n.Status, Attempts: n.Attempts, Incident: n.IncidentID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	for left, right := 0, len(timeline)-1; left < right; left, right = left+1, right-1 {
		timeline[left], timeline[right] = timeline[right], timeline[left]
	}
	return timeline, truncated, nil
}

func (s *Service) AlertGroupPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Alert group not found")
		return
	}
	if s == nil || s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "events_unavailable", "Event storage unavailable")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	visible, err := s.visibleAlertIncidentsTx(r, tx, "")
	if err != nil {
		respondAlertGroupError(w, err)
		return
	}
	members := groupMembers(visible, id)
	if len(members) == 0 {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Alert group not found")
		return
	}
	if _, err = aggregateAlertGroups(members, time.Now()); err != nil {
		respondAlertGroupError(w, err)
		return
	}
	before, err := digestGroupMembers(members)
	if err != nil {
		respondErr(w, err)
		return
	}
	labels, unavailablePolicy, err := s.configuredGroupPolicyLabels(members)
	if err != nil {
		respondAlertGroupError(w, err)
		return
	}
	var observed *v3alertmanager.GroupPolicyObservation
	if labels != nil {
		// Provider I/O must not hold incident, session or RBAC locks. The same
		// group and current authority are checked again after the read.
		if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
			respondAlertGroupError(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			respondErr(w, err)
			return
		}
		value, sourceErr := s.GroupPolicySource.ObserveGroupPolicy(r.Context(), members[0].EnvironmentKey, labels)
		switch {
		case sourceErr == nil:
			observed = &value
		case errors.Is(sourceErr, v3alertmanager.ErrNotConfigured):
			unavailablePolicy = "policy_source"
		case errors.Is(sourceErr, v3alertmanager.ErrGroupPolicyAmbiguous):
			unavailablePolicy = "route_ambiguous"
		default:
			httpapi.Error(w, http.StatusServiceUnavailable, "alert_policy_unavailable", "Configured Alertmanager policy is unavailable")
			return
		}
		tx, err = s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			respondErr(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		visible, err = s.visibleAlertIncidentsTx(r, tx, "")
		if err != nil {
			respondAlertGroupError(w, err)
			return
		}
		members = groupMembers(visible, id)
		if len(members) == 0 {
			httpapi.Error(w, http.StatusNotFound, "not_found", "Alert group not found")
			return
		}
		if _, err = aggregateAlertGroups(members, time.Now()); err != nil {
			respondAlertGroupError(w, err)
			return
		}
		after, digestErr := digestGroupMembers(members)
		if digestErr != nil {
			respondErr(w, digestErr)
			return
		}
		if after != before {
			httpapi.Error(w, http.StatusConflict, "alert_group_changed", "Alert group changed; refresh before reading policy")
			return
		}
		// The operator binding can be replaced only by process configuration;
		// the persisted app, environment, source and labels were re-read above.
	}
	timeline, truncated, err := localGroupTimeline(r.Context(), tx, members)
	if err != nil {
		respondAlertGroupError(w, err)
		return
	}
	unavailable := []string{"inhibition", "timeline[].done", "external_notification_history", "receiver_acknowledgement"}
	if observed == nil {
		unavailable = append(unavailable, "group_by", "route", "timing", unavailablePolicy)
	}
	if truncated {
		unavailable = append(unavailable, "timeline_complete")
	}
	result := alertGroupPolicyResponse{
		GroupID: id, LocalGrouping: []string{"environment_id", "application_id", "source_id", "fingerprint"},
		Timeline: timeline, TimelineScope: "platform_queue", TimelineTruncated: truncated,
		Partial: true, Unavailable: unavailable,
	}
	if observed != nil {
		applyObservedGroupPolicy(&result, *observed)
	}
	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertGroupError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, result)
}
