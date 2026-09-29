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
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// A reviewed dashboard expression returns one matrix with these exact panel
// labels. It must isolate the bound application, environment and resource.
// The handler validates the result shape; the operator reviews PromQL scope.
const dashboardPoints = 36
const dashboardSamples = dashboardPoints + 1 // inclusive baseline + 36 bin ends
const dashboardStep = 100

var dashboardHeatBuckets = []string{">2 s", "1 s", "500 ms", "250 ms", "100 ms", "50 ms", "25 ms", "<10 ms"}

var dashboardPanels = map[string]bool{
	"requests_2xx": true, "requests_4xx": true, "requests_5xx": true,
	"requests_other": true, "requests_total": true,
	"latency_p50_ms": true, "latency_p95_ms": true, "latency_p99_ms": true,
	"connection_wait_p95_ms": true, "cpu_saturation_pct": true,
	"slo_budget_remaining_pct": true, "slo_month_availability_pct": true,
	"slo_burn_1h": true, "slo_burn_6h": true, "slo_burn_3d": true,
	"latency_heat": true, "endpoint_p99_ms": true, "instance_wait_p95_ms": true,
}

type dashboardWindow struct {
	Labels        []string `json:"labels"`
	Ticks         []string `json:"ticks"`
	ReleaseAt     int      `json:"release_at"`
	RollbackAt    int      `json:"rollback_at"`
	Incident      [2]int   `json:"incident"`
	IncidentLabel string   `json:"incident_label"`
}

type dashboardKPI struct {
	ID     string    `json:"id"`
	Label  string    `json:"label"`
	Value  string    `json:"value"`
	Unit   string    `json:"unit,omitempty"`
	Sub    string    `json:"sub,omitempty"`
	Tone   string    `json:"tone,omitempty"`
	Series []float64 `json:"series"`
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
	Label  string    `json:"label"`
	Values []float64 `json:"values"`
	Tone   string    `json:"tone,omitempty"`
}

type metricsDashboard struct {
	Window   dashboardWindow `json:"window"`
	KPIs     []dashboardKPI  `json:"kpis"`
	Requests struct {
		OK     []float64 `json:"ok"`
		Client []float64 `json:"client"`
		Server []float64 `json:"server"`
	} `json:"requests"`
	SLO struct {
		Remaining    float64         `json:"remaining"`
		Availability string          `json:"availability"`
		Delta        string          `json:"delta"`
		Burn         []dashboardBurn `json:"burn"`
		Note         string          `json:"note"`
	} `json:"slo"`
	Latency struct {
		P50     []float64   `json:"p50"`
		P95     []float64   `json:"p95"`
		P99     []float64   `json:"p99"`
		Heat    [][]float64 `json:"heat"`
		Buckets []string    `json:"buckets"`
	} `json:"latency"`
	Endpoints []dashboardRank     `json:"endpoints"`
	Instances []dashboardInstance `json:"instances"`
}

type dashboardMarkers struct {
	release, rollback, incidentStart, incidentEnd time.Time
}

func dashboardRequest(r *http.Request) (string, bool, bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) < 1 || len(q) > 3 {
		return "", false, false
	}
	for key, values := range q {
		if len(values) != 1 || (key != "service" && key != "range" && key != "align") {
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
// Missing tables or events fail closed because the frozen DTO cannot represent
// an absent marker without inventing a position.
func (s *Service) loadDashboardMarkers(ctx context.Context, app, env string, window connectors.TimeWindow) (dashboardMarkers, error) {
	var m dashboardMarkers
	err := s.Store.DB.QueryRow(ctx, `SELECT de.created_at FROM deployment_events de
 JOIN deployment_executions x ON x.id=de.execution_id
 JOIN deployment_plans p ON p.id=x.plan_id
 WHERE p.application_id=$1 AND p.environment_id=(SELECT id FROM application_environments WHERE application_id=$1 AND key=$2 AND NOT archived)
 AND de.type='succeeded' AND x.status='succeeded' AND de.created_at BETWEEN $3 AND $4
 ORDER BY de.created_at DESC LIMIT 1`, app, env, window.Start, window.End).Scan(&m.release)
	if err != nil {
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
	AND event.created_at BETWEEN $3 AND $4 ORDER BY event.created_at DESC LIMIT 1`, app, env, window.Start, window.End).Scan(&m.rollback)
	if err != nil {
		return m, err
	}
	var end *time.Time
	err = s.Store.DB.QueryRow(ctx, `SELECT i.starts_at,i.ends_at FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND e.key=$2 AND NOT e.archived
 WHERE i.application_id=$1 AND NOT i.suspected
	AND i.starts_at < $4 AND (i.ends_at IS NULL OR i.ends_at > $3)
	ORDER BY i.starts_at DESC LIMIT 1`, app, env, window.Start, window.End).Scan(&m.incidentStart, &end)
	if err != nil {
		return m, err
	}
	m.incidentEnd = window.End
	if end != nil {
		m.incidentEnd = *end
	}
	if m.incidentEnd.Before(m.incidentStart) {
		return m, errors.New("invalid incident interval")
	}
	return m, nil
}

func dashboardSeries(result v3integrations.PrometheusMetricEvidence, window connectors.TimeWindow) (map[string][]float64, map[string]map[string][]float64, bool) {
	if result.Empty || result.CapturedAt.IsZero() || result.ApplicationID == "" || result.EnvironmentID == "" ||
		result.BindingID == "" || result.ConnectorID == "" || result.ResourceID == "" || result.QueryID != "dashboard" || len(result.Series) > 64 {
		return nil, nil, false
	}
	plain := map[string][]float64{}
	grouped := map[string]map[string][]float64{}
	for _, line := range result.Series {
		panel := line.Labels["panel"]
		if !dashboardPanels[panel] || len(line.Labels) > 3 || len(line.Samples) != dashboardSamples {
			return nil, nil, false
		}
		var group string
		switch panel {
		case "latency_heat":
			if len(line.Labels) != 2 {
				return nil, nil, false
			}
			group = line.Labels["bucket"]
			found := false
			for _, bucket := range dashboardHeatBuckets {
				found = found || group == bucket
			}
			if !found {
				return nil, nil, false
			}
		case "endpoint_p99_ms":
			if len(line.Labels) != 2 {
				return nil, nil, false
			}
			group = line.Labels["endpoint"]
		case "instance_wait_p95_ms":
			if len(line.Labels) != 2 {
				return nil, nil, false
			}
			group = line.Labels["instance"]
		default:
			if len(line.Labels) != 1 {
				return nil, nil, false
			}
		}
		if (panel == "latency_heat" || panel == "endpoint_p99_ms" || panel == "instance_wait_p95_ms") && group == "" {
			return nil, nil, false
		}
		if group != "" && (len(group) > 100 || strings.TrimSpace(group) != group || strings.ContainsAny(group, "\r\n\x00")) {
			return nil, nil, false
		}
		values := make([]float64, dashboardPoints)
		for i, sample := range line.Samples {
			expected := float64(window.Start.Unix() + int64(i*dashboardStep))
			value, err := strconv.ParseFloat(sample.Value, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 ||
				math.IsNaN(sample.Timestamp) || math.IsInf(sample.Timestamp, 0) || math.Abs(sample.Timestamp-expected) > 0.5 {
				return nil, nil, false
			}
			if strings.HasSuffix(panel, "_pct") && value > 100 {
				return nil, nil, false
			}
			if panel == "latency_heat" && value > 1 {
				return nil, nil, false
			}
			// The first sample proves full-hour coverage. Each visible point is
			// the right edge of one 100-second bin, not a truncated response.
			if i > 0 {
				values[i-1] = value
			}
		}
		if group == "" {
			if _, exists := plain[panel]; exists {
				return nil, nil, false
			}
			plain[panel] = values
		} else {
			if grouped[panel] == nil {
				grouped[panel] = map[string][]float64{}
			}
			if _, exists := grouped[panel][group]; exists {
				return nil, nil, false
			}
			grouped[panel][group] = values
		}
	}
	for panel := range dashboardPanels {
		if panel == "latency_heat" || panel == "endpoint_p99_ms" || panel == "instance_wait_p95_ms" {
			continue
		}
		if len(plain[panel]) != dashboardPoints {
			return nil, nil, false
		}
	}
	if len(grouped["latency_heat"]) != len(dashboardHeatBuckets) ||
		len(grouped["endpoint_p99_ms"]) == 0 || len(grouped["endpoint_p99_ms"]) > 12 ||
		len(grouped["instance_wait_p95_ms"]) == 0 || len(grouped["instance_wait_p95_ms"]) > 12 {
		return nil, nil, false
	}
	return plain, grouped, true
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

func buildDashboard(result v3integrations.PrometheusMetricEvidence, window connectors.TimeWindow, marker dashboardMarkers, sloTarget float64) (metricsDashboard, bool) {
	var out metricsDashboard
	plain, grouped, ok := dashboardSeries(result, window)
	if !ok || sloTarget <= 0 || sloTarget >= 100 {
		return out, false
	}
	out.Window.Labels = make([]string, dashboardPoints)
	visibleStart := window.Start.Add(dashboardStep * time.Second)
	if marker.release.Before(window.Start) || marker.rollback.Before(window.Start) ||
		marker.release.After(window.End) || marker.rollback.After(window.End) {
		return out, false
	}
	for i := range out.Window.Labels {
		out.Window.Labels[i] = visibleStart.Add(time.Duration(i*dashboardStep) * time.Second).UTC().Format("15:04")
	}
	out.Window.Ticks = []string{out.Window.Labels[0], out.Window.Labels[9], out.Window.Labels[18], out.Window.Labels[27], out.Window.Labels[35]}
	out.Window.ReleaseAt = dashboardIndex(marker.release, visibleStart)
	out.Window.RollbackAt = dashboardIndex(marker.rollback, visibleStart)
	out.Window.Incident = [2]int{dashboardIndex(marker.incidentStart, visibleStart), dashboardIndex(marker.incidentEnd, visibleStart)}
	out.Window.IncidentLabel = fmt.Sprintf("Incident · %s–%s UTC", marker.incidentStart.UTC().Format("15:04"), marker.incidentEnd.UTC().Format("15:04"))
	if out.Window.Incident[1] <= out.Window.Incident[0] {
		// A shorter-than-one-bin incident is displayed at the nearest visible
		// bin; the label retains the real start/end times.
		if out.Window.Incident[0] == dashboardPoints-1 {
			out.Window.Incident[0]--
		} else {
			out.Window.Incident[1]++
		}
	}
	out.Requests.OK, out.Requests.Client, out.Requests.Server = plain["requests_2xx"], plain["requests_4xx"], plain["requests_5xx"]
	out.Latency.P50, out.Latency.P95, out.Latency.P99 = plain["latency_p50_ms"], plain["latency_p95_ms"], plain["latency_p99_ms"]
	out.Latency.Buckets = append([]string(nil), dashboardHeatBuckets...)
	out.Latency.Heat = make([][]float64, 0, len(dashboardHeatBuckets))
	for _, bucket := range dashboardHeatBuckets {
		out.Latency.Heat = append(out.Latency.Heat, grouped["latency_heat"][bucket])
	}
	rate, errorsPct := make([]float64, dashboardPoints), make([]float64, dashboardPoints)
	for i := range rate {
		if out.Latency.P50[i] > out.Latency.P95[i] || out.Latency.P95[i] > out.Latency.P99[i] {
			return metricsDashboard{}, false
		}
		if plain["requests_other"][i] != 0 {
			return metricsDashboard{}, false
		}
		rate[i] = out.Requests.OK[i] + out.Requests.Client[i] + out.Requests.Server[i]
		if math.IsNaN(rate[i]) || math.IsInf(rate[i], 0) {
			return metricsDashboard{}, false
		}
		if math.Abs(rate[i]-plain["requests_total"][i]) > 0.001 {
			return metricsDashboard{}, false
		}
		if rate[i] <= 0 {
			return metricsDashboard{}, false
		}
		errorsPct[i] = out.Requests.Server[i] * 100 / rate[i]
		if math.IsNaN(errorsPct[i]) || math.IsInf(errorsPct[i], 0) {
			return metricsDashboard{}, false
		}
	}
	last := dashboardPoints - 1
	out.KPIs = []dashboardKPI{
		{ID: "rps", Label: "Requests", Value: fmt.Sprintf("%.2f", rate[last]), Unit: "/s", Sub: "Observed request rate", Series: rate},
		{ID: "err", Label: "5xx ratio", Value: fmt.Sprintf("%.2f", errorsPct[last]), Unit: "%", Sub: "Observed server errors", Series: errorsPct},
		{ID: "p99", Label: "P99 latency", Value: fmt.Sprintf("%.1f", out.Latency.P99[last]), Unit: "ms", Sub: "Observed latency", Series: out.Latency.P99},
		{ID: "wait", Label: "Connection wait P95", Value: fmt.Sprintf("%.1f", plain["connection_wait_p95_ms"][last]), Unit: "ms", Sub: "Observed wait", Series: plain["connection_wait_p95_ms"]},
		{ID: "cpu", Label: "CPU saturation", Value: fmt.Sprintf("%.1f", plain["cpu_saturation_pct"][last]), Unit: "%", Sub: "Observed saturation", Series: plain["cpu_saturation_pct"]},
	}
	availability := plain["slo_month_availability_pct"][last]
	// The catalog's monthly availability and budget panels must represent the
	// same request-based SLO. Refuse contradictory evidence instead of showing
	// unrelated percentages as one coherent budget.
	remaining := math.Max(0, math.Min(100, (availability-sloTarget)/(100-sloTarget)*100))
	if math.Abs(remaining-plain["slo_budget_remaining_pct"][last]) > 0.1 {
		return metricsDashboard{}, false
	}
	// Request-rate bins cover the same hour as the displayed 1 h burn. Its
	// reviewed PromQL must use the same 5xx-based availability definition.
	var totalRequests, serverErrors float64
	for i := range rate {
		totalRequests += rate[i]
		serverErrors += out.Requests.Server[i]
	}
	if totalRequests <= 0 || math.IsNaN(totalRequests) || math.IsInf(totalRequests, 0) || math.IsNaN(serverErrors) || math.IsInf(serverErrors, 0) {
		return metricsDashboard{}, false
	}
	derivedBurn := serverErrors / totalRequests / (1 - sloTarget/100)
	if math.IsNaN(derivedBurn) || math.IsInf(derivedBurn, 0) {
		return metricsDashboard{}, false
	}
	if math.Abs(derivedBurn-plain["slo_burn_1h"][last]) > math.Max(0.05, derivedBurn*0.05) {
		return metricsDashboard{}, false
	}
	out.SLO.Remaining = remaining
	out.SLO.Availability = fmt.Sprintf("%.3f%%", availability)
	out.SLO.Delta = fmt.Sprintf("%+.3f%%", availability-sloTarget)
	out.SLO.Burn = []dashboardBurn{dashboardBurnValue("1 h", plain["slo_burn_1h"][last]), dashboardBurnValue("6 h", plain["slo_burn_6h"][last]), dashboardBurnValue("3 d", plain["slo_burn_3d"][last])}
	out.SLO.Note = "Observed error budget burn rates"
	for label, values := range grouped["endpoint_p99_ms"] {
		out.Endpoints = append(out.Endpoints, dashboardRank{Label: label, Value: values[last]})
	}
	sort.Slice(out.Endpoints, func(i, j int) bool {
		if out.Endpoints[i].Value == out.Endpoints[j].Value {
			return out.Endpoints[i].Label < out.Endpoints[j].Label
		}
		return out.Endpoints[i].Value > out.Endpoints[j].Value
	})
	for label, values := range grouped["instance_wait_p95_ms"] {
		out.Instances = append(out.Instances, dashboardInstance{Label: label, Values: values})
	}
	sort.Slice(out.Instances, func(i, j int) bool { return out.Instances[i].Label < out.Instances[j].Label })
	return out, true
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

func (s *Service) currentDashboardSource(ctx context.Context, r *http.Request, service string, result v3integrations.PrometheusMetricEvidence, before dashboardSourceSeal, target float64) bool {
	current, state := s.dashboardSource(ctx, result.ApplicationID, result.EnvironmentID)
	if state != "" || !reflect.DeepEqual(current, before) || current.binding != result.BindingID || current.connector != result.ConnectorID || current.resource != result.ResourceID {
		return false
	}
	var currentService string
	if s.Store.DB.QueryRow(ctx, `SELECT id FROM applications WHERE id=$1 AND (id=$2 OR key=$2) AND NOT archived`, result.ApplicationID, service).Scan(&currentService) != nil {
		return false
	}
	var currentTarget float64
	if s.Store.DB.QueryRow(ctx, `SELECT slo_target_percent FROM v3_service_environment_profiles WHERE application_id=$1 AND environment_id=$2`, result.ApplicationID, result.EnvironmentID).Scan(&currentTarget) != nil || currentTarget != target {
		return false
	}
	user := httpapi.CurrentUser(r)
	serviceGrant := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: result.EnvironmentID, ApplicationID: result.ApplicationID})
	if errors.Is(serviceGrant, store.ErrForbidden) && current.team != "" {
		serviceGrant = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: result.EnvironmentID, TeamID: current.team})
	}
	if serviceGrant != nil {
		return false
	}
	// The reader requires an environment/global connector grant, regardless
	// of any narrower application or team connector grant.
	connectorGrant := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "connectors", Action: "view", EnvironmentID: result.EnvironmentID})
	return connectorGrant == nil && s.dashboardEventGrant(ctx, r, result)
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

func (s *Service) dashboardPreGrantForService(ctx context.Context, r *http.Request, service, environment string) (string, bool) {
	rows, err := s.Store.DB.Query(ctx, `SELECT a.id,COALESCE(a.owner_team_id,'') FROM applications a JOIN v3_application_environments ve
 ON ve.application_id=a.id AND ve.environment_id=$2
 WHERE (a.id=$1 OR a.key=$1) AND NOT a.archived LIMIT 2`, service, environment)
	if err != nil {
		return "", false
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
		return "", false
	}
	app, team := apps[0].app, apps[0].team
	user := httpapi.CurrentUser(r)
	serviceGrant := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: environment, ApplicationID: app})
	if errors.Is(serviceGrant, store.ErrForbidden) && team != "" {
		serviceGrant = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: environment, TeamID: team})
	}
	if serviceGrant != nil {
		return "", false
	}
	if s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "connectors", Action: "view", EnvironmentID: environment}) != nil {
		return "", false
	}
	return app, s.dashboardEventGrant(ctx, r, v3integrations.PrometheusMetricEvidence{ApplicationID: app, EnvironmentID: environment})
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
	if s.MetricReader == nil {
		httpapi.Error(w, 503, "not_configured", "Registered Prometheus metric reads are not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	app, granted := s.dashboardPreGrantForService(ctx, r, service, environments[0])
	if !granted {
		httpapi.Error(w, 403, "forbidden", "Incident timeline is outside your authorized scope")
		return
	}
	before, sourceState := s.dashboardSource(ctx, app, environments[0])
	if sourceState != "" {
		if sourceState == "ambiguous_binding" {
			httpapi.Error(w, 409, "ambiguous_binding", "Select one Prometheus binding for this service and environment")
			return
		}
		if sourceState == "metric_scope_unavailable" {
			httpapi.Error(w, 503, "metric_scope_unavailable", "Metric source lookup failed")
			return
		}
		httpapi.Error(w, 503, "not_configured", "No registered Prometheus source is available")
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
	result, err := s.MetricReader.QueryService(ctx, scope, httpapi.CurrentUser(r), service, environments[0], "dashboard", window)
	if err != nil {
		metricSamplesReadError(w, err)
		return
	}
	if result.EnvironmentID != environments[0] || result.QueryID != "dashboard" {
		httpapi.Error(w, 503, "metric_result_incomplete", "Dashboard source provenance is incomplete")
		return
	}
	if !s.dashboardEventGrant(ctx, r, result) {
		httpapi.Error(w, 403, "forbidden", "Incident timeline is outside your authorized scope")
		return
	}
	var target float64
	err = s.Store.DB.QueryRow(ctx, `SELECT slo_target_percent FROM v3_service_environment_profiles
 WHERE application_id=$1 AND environment_id=$2`, result.ApplicationID, result.EnvironmentID).Scan(&target)
	if err != nil || target <= 0 || target >= 100 {
		httpapi.Error(w, 503, "slo_unavailable", "Service SLO target is not configured")
		return
	}
	markers, err := s.loadDashboardMarkers(ctx, result.ApplicationID, result.EnvironmentID, window)
	if err != nil {
		httpapi.Error(w, 503, "dashboard_markers_unavailable", "Release, rollback or incident marker is unavailable")
		return
	}
	dashboard, complete := buildDashboard(result, window, markers, target)
	if !complete {
		httpapi.Error(w, 503, "metric_result_incomplete", "Dashboard metric panels are incomplete")
		return
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
	if ctx.Err() != nil || !s.currentDashboardSource(ctx, r, service, result, before, target) {
		httpapi.Error(w, 409, "metric_source_changed", "Dashboard source or authorization changed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, dashboard)
}
