package v3observe

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3metricpanels"
)

// A reviewed dashboard expression returns one matrix with these exact panel
// labels. It must isolate the bound application, environment and resource.
// The handler validates the result shape; the operator reviews PromQL scope.
const dashboardPoints = v3metricpanels.Points
const dashboardStep = v3metricpanels.Step

var dashboardHeatBuckets = v3metricpanels.HeatBuckets

// LabelTimes, TickTimes and the incident bounds are RFC 3339 instants, shown
// in the viewer's time zone; the UTC "15:04" labels, ticks and incident label
// they replaced were removed (PA-83).
type dashboardWindow struct {
	LabelTimes      []string `json:"label_times"`
	TickTimes       []string `json:"tick_times"`
	ReleaseAt       *int     `json:"release_at"`
	RollbackAt      *int     `json:"rollback_at"`
	Incident        *[2]int  `json:"incident"`
	IncidentStartAt *string  `json:"incident_start_at"`
	IncidentEndAt   *string  `json:"incident_end_at"`
}

// Series and per-bin arrays hold null for a bin without a value (PA-44): no
// traffic for the error ratio, a gap in a latency or saturation panel.
type dashboardKPI struct {
	ID     string     `json:"id"`
	Label  string     `json:"label"`
	Value  string     `json:"value"`
	Unit   string     `json:"unit,omitempty"`
	Sub    string     `json:"sub,omitempty"`
	Tone   string     `json:"tone,omitempty"`
	Series []*float64 `json:"series"`
}

type dashboardBurn struct {
	Window string `json:"window"`
	Value  string `json:"value"`
	Tone   string `json:"tone"`
	Pct    int    `json:"pct"`
}

type dashboardRank struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Tone  string  `json:"tone,omitempty"`
}

type dashboardInstance struct {
	Label  string     `json:"label"`
	Values []*float64 `json:"values"`
	Tone   string     `json:"tone,omitempty"`
}

type dashboardRequests struct {
	OK     []*float64 `json:"ok"`
	Client []*float64 `json:"client"`
	Server []*float64 `json:"server"`
}

type dashboardSLO struct {
	Remaining    float64         `json:"remaining"`
	Availability string          `json:"availability"`
	Delta        string          `json:"delta"`
	Burn         []dashboardBurn `json:"burn"`
	Note         string          `json:"note"`
}

type dashboardLatency struct {
	P50     []*float64   `json:"p50"`
	P95     []*float64   `json:"p95"`
	P99     []*float64   `json:"p99"`
	Heat    [][]*float64 `json:"heat"`
	Buckets []string     `json:"buckets"`
}

// metricsDashboard is MetricsDashboard with explicit unknowns: a null block
// has its reason in Unavailable and is never replaced by zeros.
type metricsDashboard struct {
	Window      dashboardWindow     `json:"window"`
	KPIs        []dashboardKPI      `json:"kpis"`
	Requests    *dashboardRequests  `json:"requests"`
	SLO         *dashboardSLO       `json:"slo"`
	Latency     *dashboardLatency   `json:"latency"`
	Endpoints   []dashboardRank     `json:"endpoints"`
	Instances   []dashboardInstance `json:"instances"`
	Unavailable map[string]string   `json:"unavailable"`
}

// A nil marker means the platform has no record of that event in the window.
type dashboardMarkers struct {
	release, rollback, incidentStart, incidentEnd *time.Time
}

func dashboardRequest(r *http.Request) (string, bool, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) < 1 || len(q) > 4 {
		return "", false, false
	}
	for key, values := range q {
		if len(values) != 1 || (key != "service" && key != "range" && key != "align" && key != "source") {
			return "", false, false
		}
	}
	service := q.Get("service")
	if service == "" || len(service) > 200 || strings.TrimSpace(service) != service ||
		(q.Has("range") && q.Get("range") != "1h") ||
		(q.Has("align") && q.Get("align") != "true" && q.Get("align") != "false") {
		return "", false, false
	}
	return service, q.Get("align") == "true", true
}

func dashboardIndex(t, start time.Time) int {
	i := int(math.Round(t.Sub(start).Seconds() / dashboardStep))
	if i < 0 {
		return 0
	}
	if i >= dashboardPoints {
		return dashboardPoints - 1
	}
	return i
}

// These are independent persisted events. The latest rollback is not asserted
// to reverse the latest release; the dashboard must not tell that causal story.
// An event absent from the window is a nil marker; a failed read is an error.
func (s *Service) loadDashboardMarkers(ctx context.Context, app, env string, window connectors.TimeWindow) (dashboardMarkers, error) {
	var m dashboardMarkers
	optional := func(err error, at time.Time, target **time.Time) error {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err == nil {
			*target = &at
		}
		return err
	}
	var release, rollback, start time.Time
	err := s.Store.DB.QueryRow(ctx, `SELECT de.created_at FROM deployment_events de
 JOIN deployment_executions x ON x.id=de.execution_id
 JOIN deployment_plans p ON p.id=x.plan_id
 WHERE p.application_id=$1 AND p.environment_id=(SELECT id FROM application_environments WHERE application_id=$1 AND key=$2 AND NOT archived)
 AND de.type='succeeded' AND x.status='succeeded' AND de.created_at BETWEEN $3 AND $4
 ORDER BY de.created_at DESC LIMIT 1`, app, env, window.Start, window.End).Scan(&release)
	if err = optional(err, release, &m.release); err != nil {
		return m, err
	}
	err = s.Store.DB.QueryRow(ctx, `SELECT event.created_at FROM events event
 JOIN investigations i ON i.id=event.investigation_id
 JOIN executions x ON x.id=event.payload->>'execution_id' AND x.status='succeeded'
 JOIN action_plans p ON p.id=x.plan_id AND p.investigation_id=i.id
 AND COALESCE(p.proposal->>'target_revision','')<>''
 AND COALESCE(p.proposal->>'target_template_digest','')<>''
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
	WHERE i.application_id=$1 AND e.key=$2 AND NOT e.archived AND event.type='execution.succeeded'
	AND event.created_at BETWEEN $3 AND $4 ORDER BY event.created_at DESC LIMIT 1`, app, env, window.Start, window.End).Scan(&rollback)
	if err = optional(err, rollback, &m.rollback); err != nil {
		return m, err
	}
	var end *time.Time
	err = s.Store.DB.QueryRow(ctx, `SELECT i.starts_at,i.ends_at FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND e.key=$2 AND NOT e.archived
 WHERE i.application_id=$1 AND NOT i.suspected
	AND i.starts_at < $4 AND (i.ends_at IS NULL OR i.ends_at > $3)
	ORDER BY i.starts_at DESC LIMIT 1`, app, env, window.Start, window.End).Scan(&start, &end)
	if err = optional(err, start, &m.incidentStart); err != nil || m.incidentStart == nil {
		return m, err
	}
	incidentEnd := window.End
	if end != nil {
		incidentEnd = *end
	}
	if incidentEnd.Before(start) {
		return m, errors.New("invalid incident interval")
	}
	m.incidentEnd = &incidentEnd
	return m, nil
}

func dashboardTone(value float64) string {
	if value >= 10 {
		return "danger"
	}
	if value >= 1 {
		return "warn"
	}
	return "ok"
}

func dashboardBurnValue(window string, v float64) dashboardBurn {
	pct := int(math.Round(math.Min(100, v*10)))
	return dashboardBurn{Window: window, Value: fmt.Sprintf("%.2f×", v), Tone: dashboardTone(v), Pct: pct}
}

// newDashboard lays out the window and the persisted markers. Metric blocks
// stay null until applyMetrics proves them.
func newDashboard(window connectors.TimeWindow, marker dashboardMarkers, markerReason ...string) metricsDashboard {
	absent := "not_generated"
	if len(markerReason) > 0 {
		absent = markerReason[0]
	}
	out := metricsDashboard{Unavailable: map[string]string{}}
	out.Window.LabelTimes = make([]string, dashboardPoints)
	visibleStart := window.Start.Add(dashboardStep * time.Second)
	for i := range out.Window.LabelTimes {
		out.Window.LabelTimes[i] = visibleStart.Add(time.Duration(i*dashboardStep) * time.Second).UTC().Format(time.RFC3339)
	}
	out.Window.TickTimes = []string{}
	for _, i := range []int{0, 9, 18, 27, 35} {
		out.Window.TickTimes = append(out.Window.TickTimes, out.Window.LabelTimes[i])
	}
	inWindow := func(at *time.Time) bool { return at != nil && !at.Before(window.Start) && !at.After(window.End) }
	if inWindow(marker.release) {
		index := dashboardIndex(*marker.release, visibleStart)
		out.Window.ReleaseAt = &index
	} else {
		out.Unavailable["window.release_at"] = absent
	}
	if inWindow(marker.rollback) {
		index := dashboardIndex(*marker.rollback, visibleStart)
		out.Window.RollbackAt = &index
	} else {
		out.Unavailable["window.rollback_at"] = absent
	}
	if marker.incidentStart != nil && marker.incidentEnd != nil {
		incident := [2]int{dashboardIndex(*marker.incidentStart, visibleStart), dashboardIndex(*marker.incidentEnd, visibleStart)}
		if incident[1] <= incident[0] {
			// A shorter-than-one-bin incident is displayed at the nearest visible
			// bin; the label retains the real start/end times.
			if incident[0] == dashboardPoints-1 {
				incident[0]--
			} else {
				incident[1]++
			}
		}
		startAt, endAt := marker.incidentStart.UTC().Format(time.RFC3339), marker.incidentEnd.UTC().Format(time.RFC3339)
		out.Window.Incident = &incident
		out.Window.IncidentStartAt, out.Window.IncidentEndAt = &startAt, &endAt
	} else {
		out.Unavailable["window.incident"] = absent
	}
	out.metricsUnavailable("unavailable")
	return out
}

func (out *metricsDashboard) metricsUnavailable(reason string) {
	out.KPIs, out.Requests, out.Latency, out.Endpoints, out.Instances, out.SLO = nil, nil, nil, nil, nil, nil
	for _, field := range []string{"kpis", "requests", "latency", "endpoints", "instances", "slo"} {
		out.Unavailable[field] = reason
	}
}

// applyMetrics fills the metric blocks from one reviewed result. The query
// may return any subset of panels (PA-41): a block whose panels are absent
// stays null with not_configured, and a list keeps only present entries.
// Present panels that contradict each other leave every metric block
// unknown; a budget or burn that contradicts the requests hides only the SLO.
func (out *metricsDashboard) applyMetrics(result v3integrations.PrometheusMetricEvidence, window connectors.TimeWindow, sloTarget *float64) bool {
	if result.Empty || result.CapturedAt.IsZero() || result.ApplicationID == "" || result.EnvironmentID == "" ||
		result.BindingID == "" || result.ConnectorID == "" || result.ResourceID == "" || result.QueryID != "dashboard" {
		return false
	}
	p, ok := v3metricpanels.Parse(result.Series, window)
	if !ok || !p.LatencyOrdered() {
		return false
	}
	rate, errorsPct, requests, consistent := p.Requests()
	if !consistent {
		return false
	}
	for _, field := range []string{"kpis", "requests", "latency", "endpoints", "instances", "slo"} {
		delete(out.Unavailable, field)
	}
	missing := func(field string) { out.Unavailable[field] = "not_configured" }
	nullable := v3metricpanels.Nullable
	last := dashboardPoints - 1
	out.KPIs = []dashboardKPI{}
	// A KPI without a value in the current bin is unknown (not_generated);
	// its series keeps the other bins.
	kpi := func(id, label, unit, sub, format string, series []float64) {
		if !v3metricpanels.Defined(series[last]) {
			out.Unavailable["kpis."+id] = v3metricpanels.ReasonNoCurrentData
			return
		}
		out.KPIs = append(out.KPIs, dashboardKPI{ID: id, Label: label, Value: fmt.Sprintf(format, series[last]), Unit: unit, Sub: sub, Series: nullable(series)})
	}
	if requests {
		out.Requests = &dashboardRequests{OK: nullable(p.Plain[v3metricpanels.Requests2xx]), Client: nullable(p.Plain[v3metricpanels.Requests4xx]),
			Server: nullable(p.Plain[v3metricpanels.Requests5xx])}
		kpi("rps", "Requests", "/s", "Observed request rate", "%.2f", rate)
		kpi("err", "5xx ratio", "%", "Observed server errors", "%.2f", errorsPct)
	} else {
		out.Requests = nil
		missing("requests")
		missing("kpis.rps")
		missing("kpis.err")
	}
	for _, k := range []struct{ id, panel, label, unit, sub, format string }{
		{"p99", v3metricpanels.LatencyP99, "P99 latency", "ms", "Observed latency", "%.1f"},
		{"wait", v3metricpanels.ConnectionWaitP95, "Connection wait P95", "ms", "Observed wait", "%.1f"},
		{"cpu", v3metricpanels.CPUSaturation, "CPU saturation", "%", "Observed saturation", "%.1f"},
	} {
		if series, ok := p.Plain[k.panel]; ok {
			kpi(k.id, k.label, k.unit, k.sub, k.format, series)
		} else {
			missing("kpis." + k.id)
		}
	}
	if len(out.KPIs) == 0 {
		out.KPIs = nil
		// Panels exist but none has a current value: not_generated.
		out.Unavailable["kpis"] = "not_configured"
		for field, reason := range out.Unavailable {
			if strings.HasPrefix(field, "kpis.") && reason == v3metricpanels.ReasonNoCurrentData {
				out.Unavailable["kpis"] = reason
			}
		}
	}
	if p.Has(v3metricpanels.LatencyP50, v3metricpanels.LatencyP95, v3metricpanels.LatencyP99) {
		out.Latency = &dashboardLatency{P50: nullable(p.Plain[v3metricpanels.LatencyP50]), P95: nullable(p.Plain[v3metricpanels.LatencyP95]),
			P99: nullable(p.Plain[v3metricpanels.LatencyP99])}
		if heat := p.Group(v3metricpanels.LatencyHeat); heat != nil {
			out.Latency.Buckets = append([]string(nil), dashboardHeatBuckets...)
			for _, bucket := range dashboardHeatBuckets {
				out.Latency.Heat = append(out.Latency.Heat, nullable(heat[bucket]))
			}
		} else {
			missing("latency.heat")
			missing("latency.buckets")
		}
	} else {
		out.Latency = nil
		missing("latency")
	}
	if endpoints := p.Group(v3metricpanels.EndpointP99); endpoints != nil {
		// The ranking is by the current bin; endpoints without a current
		// value are left out.
		out.Endpoints = []dashboardRank{}
		for label, values := range endpoints {
			if v3metricpanels.Defined(values[last]) {
				out.Endpoints = append(out.Endpoints, dashboardRank{Label: label, Value: values[last]})
			}
		}
		sort.Slice(out.Endpoints, func(i, j int) bool {
			if out.Endpoints[i].Value == out.Endpoints[j].Value {
				return out.Endpoints[i].Label < out.Endpoints[j].Label
			}
			return out.Endpoints[i].Value > out.Endpoints[j].Value
		})
		if len(out.Endpoints) == 0 {
			out.Endpoints = nil
			out.Unavailable["endpoints"] = v3metricpanels.ReasonNoCurrentData
		}
	} else {
		out.Endpoints = nil
		missing("endpoints")
	}
	if instances := p.Group(v3metricpanels.InstanceWaitP95); instances != nil {
		out.Instances = []dashboardInstance{}
		for label, values := range instances {
			out.Instances = append(out.Instances, dashboardInstance{Label: label, Values: nullable(values)})
		}
		sort.Slice(out.Instances, func(i, j int) bool { return out.Instances[i].Label < out.Instances[j].Label })
	} else {
		out.Instances = nil
		missing("instances")
	}
	out.SLO = nil
	if sloTarget == nil || *sloTarget <= 0 || *sloTarget >= 100 {
		missing("slo")
		return true
	}
	target := *sloTarget
	slo := p.SLO(target)
	switch {
	case !slo.Consistent:
		// Budget or burn panels contradict the request evidence.
		out.Unavailable["slo"] = "unavailable"
	case slo.Availability == nil:
		out.Unavailable["slo"] = slo.Reasons[v3metricpanels.FieldAvailability]
	default:
		availability := *slo.Availability
		out.SLO = &dashboardSLO{Remaining: *slo.BudgetLeft, Availability: fmt.Sprintf("%.3f%%", availability),
			Delta: fmt.Sprintf("%+.3f%%", availability-target), Note: "Observed error budget burn rates", Burn: []dashboardBurn{}}
		for _, burn := range []struct {
			window, key, field string
			value              *float64
		}{{"1 h", "1h", v3metricpanels.FieldBurn1h, slo.Burn1h}, {"6 h", "6h", v3metricpanels.FieldBurn6h, slo.Burn6h},
			{"3 d", "3d", v3metricpanels.FieldBurn3d, slo.Burn3d}} {
			if burn.value == nil {
				out.Unavailable["slo.burn."+burn.key] = slo.Reasons[burn.field]
				continue
			}
			out.SLO.Burn = append(out.SLO.Burn, dashboardBurnValue(burn.window, *burn.value))
		}
	}
	return true
}

// currentDashboardSource is a final fail-closed check after both the provider
// and local event reads. QueryService also checks grants and binding before and
// after the Prometheus call; this prevents a later revocation leaking the DTO.
type dashboardSourceSeal struct{ team, binding, connector, resource, endpoint, auth, credential, tools, appVersion, envVersion, serviceVersion, bindingVersion, connectorVersion string }

func (s *Service) dashboardSource(ctx context.Context, app, env string) (dashboardSourceSeal, string) {
	var seal dashboardSourceSeal
	var total int
	if s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM v3_connector_targets t JOIN v3_connectors c
 ON c.id=t.connector_id AND c.environment_id=t.environment_id AND c.provider='prometheus'
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.target_kind='metric_resource'`, app, env).Scan(&total) != nil {
		return seal, "metric_scope_unavailable"
	}
	if total > 1 {
		return seal, "ambiguous_binding"
	}
	if total == 0 {
		return seal, "not_configured"
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT COALESCE(a.owner_team_id,''),t.id,t.connector_id,t.external_id,
 c.endpoint,c.auth_method,c.credential_ref,c.tools::text,a.xmin::text,e.xmin::text,ve.xmin::text,t.xmin::text,c.xmin::text
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN v3_connector_targets t ON t.application_id=a.id AND t.environment_id=ve.environment_id AND t.target_kind='metric_resource'
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id AND c.provider='prometheus' AND c.enabled
 AND c.tools @> '[{"name":"query_metrics","enabled":true}]'::jsonb
 WHERE a.id=$1 AND NOT a.archived LIMIT 2`, app, env)
	if err != nil {
		return seal, "metric_scope_unavailable"
	}
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&seal.team, &seal.binding, &seal.connector, &seal.resource, &seal.endpoint, &seal.auth, &seal.credential, &seal.tools, &seal.appVersion, &seal.envVersion, &seal.serviceVersion, &seal.bindingVersion, &seal.connectorVersion); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return seal, "metric_scope_unavailable"
	}
	if count != 1 {
		return seal, "not_configured"
	}
	return seal, ""
}

func (s *Service) currentDashboardSource(ctx context.Context, r *http.Request, service string, grants dashboardGrants, result *v3integrations.PrometheusMetricEvidence, before dashboardSourceSeal, beforeState string, target *float64) bool {
	current, ok := s.dashboardGrantsForService(ctx, r, service, grants.env)
	if !ok || current != grants {
		return false
	}
	seal, state := dashboardSourceSeal{}, "not_configured"
	switch {
	case !grants.metrics:
		state = "no_authorized_target"
	case s.MetricReader != nil:
		seal, state = s.dashboardSource(ctx, grants.app, grants.env)
	}
	if state != beforeState || !reflect.DeepEqual(seal, before) {
		return false
	}
	if result != nil && (state != "" || seal.binding != result.BindingID || seal.connector != result.ConnectorID || seal.resource != result.ResourceID) {
		return false
	}
	currentTarget, err := s.dashboardSLOTarget(ctx, grants.app, grants.env)
	return err == nil && (currentTarget == nil) == (target == nil) && (currentTarget == nil || *currentTarget == *target)
}

// dashboardSLOTarget is nil when no usable target is configured. Only a failed
// read is an error; 0 and 100 cannot define an error budget.
func (s *Service) dashboardSLOTarget(ctx context.Context, app, env string) (*float64, error) {
	var target float64
	err := s.Store.DB.QueryRow(ctx, `SELECT slo_target_percent FROM v3_service_environment_profiles
 WHERE application_id=$1 AND environment_id=$2`, app, env).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (target <= 0 || target >= 100) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &target, nil
}

func (s *Service) dashboardEventGrant(ctx context.Context, r *http.Request, result v3integrations.PrometheusMetricEvidence) bool {
	user := httpapi.CurrentUser(r)
	err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: "view", EnvironmentID: result.EnvironmentID, ApplicationID: result.ApplicationID})
	if errors.Is(err, store.ErrForbidden) {
		var team string
		if s.Store.DB.QueryRow(ctx, `SELECT COALESCE(owner_team_id,'') FROM applications WHERE id=$1 AND NOT archived`, result.ApplicationID).Scan(&team) == nil && team != "" {
			err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: "view", EnvironmentID: result.EnvironmentID, TeamID: team})
		}
	}
	return err == nil
}

// dashboardGrants are evaluated separately so a visible service is not a
// page-level 403 when only its metric source or incident markers are not
// authorized: those blocks are null with no_authorized_target instead.
type dashboardGrants struct {
	app, env        string
	metrics, events bool
}

// dashboardGrantsForService requires services.view on exactly one persisted
// application; a hidden and an absent service are indistinguishable (false).
func (s *Service) dashboardGrantsForService(ctx context.Context, r *http.Request, service, environment string) (dashboardGrants, bool) {
	rows, err := s.Store.DB.Query(ctx, `SELECT a.id,COALESCE(a.owner_team_id,'') FROM applications a JOIN v3_application_environments ve
 ON ve.application_id=a.id AND ve.environment_id=$2
 WHERE (a.id=$1 OR a.key=$1) AND NOT a.archived LIMIT 2`, service, environment)
	if err != nil {
		return dashboardGrants{}, false
	}
	type candidate struct{ app, team string }
	var apps []candidate
	for rows.Next() {
		var item candidate
		if rows.Scan(&item.app, &item.team) != nil {
			break
		}
		apps = append(apps, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(apps) != 1 {
		return dashboardGrants{}, false
	}
	app, team := apps[0].app, apps[0].team
	user := httpapi.CurrentUser(r)
	serviceGrant := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: environment, ApplicationID: app})
	if errors.Is(serviceGrant, store.ErrForbidden) && team != "" {
		serviceGrant = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: environment, TeamID: team})
	}
	if serviceGrant != nil {
		return dashboardGrants{}, false
	}
	// The reader requires an environment/global connector grant, regardless
	// of any narrower application or team connector grant.
	metrics := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "connectors", Action: "view", EnvironmentID: environment}) == nil
	events := s.dashboardEventGrant(ctx, r, v3integrations.PrometheusMetricEvidence{ApplicationID: app, EnvironmentID: environment})
	return dashboardGrants{app: app, env: environment, metrics: metrics, events: events}, true
}

func alignedDashboardEnd(now, incident time.Time) (time.Time, bool) {
	if incident.IsZero() || incident.After(now) || incident.Before(now.Add(-23*time.Hour)) {
		return time.Time{}, false
	}
	end := incident.Add(30 * time.Minute).Truncate(dashboardStep * time.Second)
	if end.After(now) {
		end = now
	}
	return end, !end.Before(now.Add(-23 * time.Hour))
}

func (s *Service) metricDashboard(w http.ResponseWriter, r *http.Request) {
	if src := r.URL.Query()["source"]; len(src) > 0 {
		if len(src) != 1 || (src[0] != "prometheus" && src[0] != "victoriametrics") {
			httpapi.Error(w, 422, "invalid_source", "Select one metrics provider")
			return
		}
		if src[0] == "victoriametrics" {
			httpapi.Error(w, 503, "metrics_source_not_connected", "VictoriaMetrics dashboard is not connected")
			return
		}
	}

	service, align, valid := dashboardRequest(r)
	if !valid {
		httpapi.Error(w, 422, "invalid_metric_request", "Choose one service, a one-hour range and optional alignment")
		return
	}
	environments, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return
	}
	scope, scoped := httpapi.V3ScopeFromContext(r.Context())
	if !scoped || scope.All || len(environments) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Choose one environment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	grants, visible := s.dashboardGrantsForService(ctx, r, service, environments[0])
	if !visible || align && !grants.events {
		httpapi.Error(w, 403, "forbidden", "Service or incident timeline is outside your authorized scope")
		return
	}
	app := grants.app
	// No registered Prometheus source is a deployment state, not a failed
	// read: the metric blocks are null with not_configured and the persisted
	// markers are still shown. Without connectors.view no source is looked up.
	before, sourceState := dashboardSourceSeal{}, "not_configured"
	switch {
	case !grants.metrics:
		sourceState = "no_authorized_target"
	case s.MetricReader != nil:
		before, sourceState = s.dashboardSource(ctx, app, environments[0])
	}
	switch sourceState {
	case "ambiguous_binding":
		httpapi.Error(w, 409, "ambiguous_binding", "Select one Prometheus binding for this service and environment")
		return
	case "metric_scope_unavailable":
		httpapi.Error(w, 503, "metric_scope_unavailable", "Metric source lookup failed")
		return
	}
	end := time.Now().UTC().Truncate(time.Duration(dashboardStep) * time.Second)
	if align {
		var incident time.Time
		if err := s.Store.DB.QueryRow(ctx, `SELECT i.starts_at FROM ops_incidents i
		 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND e.key=$2 AND NOT e.archived
		 WHERE i.application_id=$1 AND NOT i.suspected
		 AND i.starts_at BETWEEN $3 AND $4 ORDER BY i.starts_at DESC LIMIT 1`,
			app, environments[0], end.Add(-23*time.Hour), end).Scan(&incident); err != nil {
			httpapi.Error(w, 503, "incident_alignment_unavailable", "No reviewed incident is available for alignment")
			return
		}
		var aligned bool
		end, aligned = alignedDashboardEnd(end, incident)
		if !aligned {
			httpapi.Error(w, 503, "incident_alignment_unavailable", "Incident is outside the supported metric window")
			return
		}
	}
	window := connectors.TimeWindow{Start: end.Add(time.Hour * -1), End: end, StepSeconds: dashboardStep}
	var result *v3integrations.PrometheusMetricEvidence
	metricReason := "not_configured"
	if sourceState == "no_authorized_target" {
		metricReason = sourceState
	}
	if sourceState == "" {
		evidence, err := s.MetricReader.QueryService(ctx, scope, httpapi.CurrentUser(r), service, environments[0], "dashboard", window)
		var readErr *v3integrations.PrometheusReadError
		switch {
		case err == nil:
			if evidence.EnvironmentID != environments[0] || evidence.QueryID != "dashboard" || evidence.ApplicationID != app {
				httpapi.Error(w, 503, "metric_result_incomplete", "Dashboard source provenance is incomplete")
				return
			}
			result, metricReason = &evidence, "unavailable"
		case errors.As(err, &readErr) && readErr.Code == "query_unregistered":
			// The source exists but no reviewed dashboard query is registered.
		default:
			metricSamplesReadError(w, err)
			return
		}
	}
	target, err := s.dashboardSLOTarget(ctx, app, environments[0])
	if err != nil {
		httpapi.Error(w, 503, "slo_unavailable", "Service SLO target could not be read")
		return
	}
	var markers dashboardMarkers
	markerReason := "no_authorized_target"
	if grants.events {
		markerReason = "not_generated"
		if markers, err = s.loadDashboardMarkers(ctx, app, environments[0], window); err != nil {
			httpapi.Error(w, 503, "dashboard_markers_unavailable", "Release, rollback or incident markers could not be read")
			return
		}
	}
	dashboard := newDashboard(window, markers, markerReason)
	if result == nil || !dashboard.applyMetrics(*result, window, target) {
		dashboard.metricsUnavailable(metricReason)
	}
	// The upstream read may outlive the authenticated session. Recheck the
	// exact cookie before returning any dashboard data.
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Session is no longer active")
		return
	}
	currentUser, sessionErr := s.Store.Session(ctx, cookie.Value)
	if sessionErr != nil {
		if errors.Is(store.ClassifyError(sessionErr), store.ErrNotFound) {
			httpapi.Error(w, 401, "unauthorized", "Session is no longer active")
		} else {
			httpapi.Error(w, 503, "session_unavailable", "Session verification failed")
		}
		return
	}
	if currentUser.ID != httpapi.CurrentUser(r).ID {
		httpapi.Error(w, 401, "unauthorized", "Session is no longer active")
		return
	}
	if ctx.Err() != nil || !s.currentDashboardSource(ctx, r, service, grants, result, before, sourceState, target) {
		httpapi.Error(w, 409, "metric_source_changed", "Dashboard source or authorization changed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	dashboard.localize(dashboardTextFor(r.Header.Get("Accept-Language")))
	httpapi.WriteJSON(w, 200, dashboard)
}
