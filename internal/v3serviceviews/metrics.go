package v3serviceviews

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// MetricReader resolves an authorized, exact service/environment Prometheus
// binding and a reviewed dashboard expression. It rechecks both after reading.
type MetricReader interface {
	QueryService(context.Context, httpapi.V3EnvironmentScope, core.User, string, string, string,
		connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error)
}

type MetricError struct{ Code string }

func (e *MetricError) Error() string { return e.Code }

type Burn struct {
	Window string `json:"window"`
	Value  string `json:"value"`
	Tone   string `json:"tone"`
	Pct    int    `json:"pct"`
}

type KPI struct {
	ID     string    `json:"id"`
	Label  string    `json:"label"`
	Value  string    `json:"value"`
	Unit   string    `json:"unit,omitempty"`
	Sub    string    `json:"sub,omitempty"`
	Tone   string    `json:"tone,omitempty"`
	Series []float64 `json:"series"`
}

type Metrics struct {
	// Status is the observed SLO burn signal, not a Kubernetes readiness claim.
	Status       string
	Availability string
	Target       string
	BudgetLeft   float64
	Burn         []Burn
	Signals      []KPI
	CapturedAt   time.Time
	BindingID    string
	ConnectorID  string
	ResourceID   string
}

const metricStep = 100
const metricPoints = 36

// These are the scalar panels needed for both the service card and details.
// The dashboard query may additionally return its reviewed grouped panels.
var serviceMetricPanels = map[string]bool{
	"requests_2xx": true, "requests_4xx": true, "requests_5xx": true,
	"requests_other": true, "requests_total": true,
	"latency_p50_ms": true, "latency_p95_ms": true, "latency_p99_ms": true,
	"connection_wait_p95_ms": true, "cpu_saturation_pct": true,
	"slo_budget_remaining_pct": true, "slo_month_availability_pct": true,
	"slo_burn_1h": true, "slo_burn_6h": true, "slo_burn_3d": true,
}

var serviceGroupedPanels = map[string]string{
	"latency_heat": "bucket", "endpoint_p99_ms": "endpoint", "instance_wait_p95_ms": "instance",
}

var serviceHeatBuckets = map[string]bool{
	">2 s": true, "1 s": true, "500 ms": true, "250 ms": true,
	"100 ms": true, "50 ms": true, "25 ms": true, "<10 ms": true,
}

// ReadMetrics does not supply placeholder health or SLO values. Any incomplete,
// stale, cross-scope or contradictory result fails as metric_result_incomplete.
func ReadMetrics(ctx context.Context, reader MetricReader, scope httpapi.V3EnvironmentScope, user core.User,
	serviceKey, appID, env string, target float64, now time.Time) (Metrics, error) {
	var zero Metrics
	if reader == nil || serviceKey == "" || appID == "" || env == "" || now.IsZero() ||
		math.IsNaN(target) || math.IsInf(target, 0) || target <= 0 || target >= 100 {
		return zero, &MetricError{Code: "slo_unavailable"}
	}
	end := now.UTC().Truncate(metricStep * time.Second)
	window := connectors.TimeWindow{Start: end.Add(-time.Hour), End: end, StepSeconds: metricStep}
	evidence, err := reader.QueryService(ctx, scope, user, serviceKey, env, "dashboard", window)
	if err != nil {
		return zero, err
	}
	if evidence.Empty || evidence.ApplicationID != appID || evidence.EnvironmentID != env ||
		evidence.QueryID != "dashboard" || evidence.BindingID == "" || evidence.ConnectorID == "" ||
		evidence.ResourceID == "" || evidence.CapturedAt.IsZero() || evidence.CapturedAt.Before(end) ||
		evidence.CapturedAt.After(now.Add(15*time.Second)) {
		return zero, &MetricError{Code: "metric_result_incomplete"}
	}
	panels, ok := serviceMetricSeries(evidence, window)
	if !ok {
		return zero, &MetricError{Code: "metric_result_incomplete"}
	}
	projected, err := projectServiceMetrics(panels, target, evidence.CapturedAt)
	if err != nil {
		return zero, err
	}
	projected.BindingID = evidence.BindingID
	projected.ConnectorID = evidence.ConnectorID
	projected.ResourceID = evidence.ResourceID
	return projected, nil
}

func serviceMetricSeries(evidence v3integrations.PrometheusMetricEvidence, window connectors.TimeWindow) (map[string][]float64, bool) {
	if len(evidence.Series) < len(serviceMetricPanels) || len(evidence.Series) > 64 {
		return nil, false
	}
	panels := make(map[string][]float64, len(serviceMetricPanels))
	groups := map[string]map[string]bool{}
	for _, line := range evidence.Series {
		panel := line.Labels["panel"]
		groupKey, grouped := serviceGroupedPanels[panel]
		if !serviceMetricPanels[panel] && !grouped {
			return nil, false
		}
		if len(line.Samples) != metricPoints+1 || (!grouped && len(line.Labels) != 1) ||
			(grouped && len(line.Labels) != 2) {
			return nil, false
		}
		if grouped {
			group := line.Labels[groupKey]
			if group == "" || len(group) > 100 || strings.TrimSpace(group) != group ||
				strings.ContainsAny(group, "\r\n\x00") || groups[panel][group] ||
				(panel == "latency_heat" && !serviceHeatBuckets[group]) {
				return nil, false
			}
			if groups[panel] == nil {
				groups[panel] = map[string]bool{}
			}
			groups[panel][group] = true
		} else if _, exists := panels[panel]; exists {
			return nil, false
		}
		values := make([]float64, metricPoints)
		for i, sample := range line.Samples {
			v, err := strconv.ParseFloat(sample.Value, 64)
			expected := float64(window.Start.Unix() + int64(i*metricStep))
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 ||
				math.IsNaN(sample.Timestamp) || math.IsInf(sample.Timestamp, 0) ||
				math.Abs(sample.Timestamp-expected) > 0.5 ||
				(panel == "cpu_saturation_pct" && v > 100) ||
				(panel == "slo_month_availability_pct" && v > 100) ||
				(panel == "slo_budget_remaining_pct" && v > 100) ||
				(panel == "latency_heat" && v > 1) {
				return nil, false
			}
			if i > 0 {
				values[i-1] = v
			}
		}
		if !grouped {
			panels[panel] = values
		}
	}
	if len(panels) != len(serviceMetricPanels) {
		return nil, false
	}
	if len(groups["latency_heat"]) != len(serviceHeatBuckets) ||
		len(groups["endpoint_p99_ms"]) < 1 || len(groups["endpoint_p99_ms"]) > 12 ||
		len(groups["instance_wait_p95_ms"]) < 1 || len(groups["instance_wait_p95_ms"]) > 12 {
		return nil, false
	}
	return panels, true
}

func projectServiceMetrics(p map[string][]float64, target float64, captured time.Time) (Metrics, error) {
	var out Metrics
	rate := make([]float64, metricPoints)
	errorsPct := make([]float64, metricPoints)
	var requestSum, errorSum float64
	for i := range rate {
		if p["latency_p50_ms"][i] > p["latency_p95_ms"][i] ||
			p["latency_p95_ms"][i] > p["latency_p99_ms"][i] || p["requests_other"][i] != 0 {
			return out, &MetricError{Code: "metric_result_incomplete"}
		}
		rate[i] = p["requests_2xx"][i] + p["requests_4xx"][i] + p["requests_5xx"][i]
		if rate[i] <= 0 || math.IsInf(rate[i], 0) || math.Abs(rate[i]-p["requests_total"][i]) > 0.001 {
			return out, &MetricError{Code: "metric_result_incomplete"}
		}
		errorsPct[i] = 100 * p["requests_5xx"][i] / rate[i]
		requestSum += rate[i]
		errorSum += p["requests_5xx"][i]
	}
	last := metricPoints - 1
	availability := p["slo_month_availability_pct"][last]
	budget := math.Max(0, math.Min(100, (availability-target)/(100-target)*100))
	derivedBurn := errorSum / requestSum / (1 - target/100)
	if math.IsNaN(derivedBurn) || math.IsInf(derivedBurn, 0) ||
		math.Abs(budget-p["slo_budget_remaining_pct"][last]) > 0.1 ||
		math.Abs(derivedBurn-p["slo_burn_1h"][last]) > math.Max(0.05, derivedBurn*0.05) {
		return out, &MetricError{Code: "metric_result_incomplete"}
	}
	oneHour := p["slo_burn_1h"][last]
	status := "ok"
	if oneHour >= 10 || budget == 0 {
		status = "danger"
	} else if oneHour >= 1 || budget < 50 {
		status = "warn"
	}
	out = Metrics{Status: status, Availability: fmt.Sprintf("%.3f%%", availability),
		Target: fmt.Sprintf("%g%%", target), BudgetLeft: budget, CapturedAt: captured}
	out.Burn = []Burn{
		serviceBurn("1 h", oneHour),
		serviceBurn("6 h", p["slo_burn_6h"][last]),
		serviceBurn("3 d", p["slo_burn_3d"][last]),
	}
	out.Signals = []KPI{
		{ID: "rps", Label: "Requests", Value: fmt.Sprintf("%.2f", rate[last]), Unit: "/s", Sub: "Observed request rate", Series: rate},
		{ID: "err", Label: "5xx ratio", Value: fmt.Sprintf("%.2f", errorsPct[last]), Unit: "%", Sub: "Observed server errors", Series: errorsPct},
		{ID: "p99", Label: "P99 latency", Value: fmt.Sprintf("%.1f", p["latency_p99_ms"][last]), Unit: "ms", Sub: "Observed latency", Series: p["latency_p99_ms"]},
		{ID: "wait", Label: "Connection wait P95", Value: fmt.Sprintf("%.1f", p["connection_wait_p95_ms"][last]), Unit: "ms", Sub: "Observed wait", Series: p["connection_wait_p95_ms"]},
		{ID: "cpu", Label: "CPU saturation", Value: fmt.Sprintf("%.1f", p["cpu_saturation_pct"][last]), Unit: "%", Sub: "Observed saturation", Series: p["cpu_saturation_pct"]},
	}
	return out, nil
}

func serviceBurn(window string, value float64) Burn {
	tone := "ok"
	if value >= 10 {
		tone = "danger"
	} else if value >= 1 {
		tone = "warn"
	}
	return Burn{Window: window, Value: fmt.Sprintf("%.2f×", value), Tone: tone,
		Pct: int(math.Round(math.Min(100, value*10)))}
}
