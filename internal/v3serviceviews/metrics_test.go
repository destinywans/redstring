package v3serviceviews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3metricpanels"
)

type metricsReaderStub struct {
	evidence v3integrations.PrometheusMetricEvidence
	err      error
	called   bool
	window   connectors.TimeWindow
}

func (s *metricsReaderStub) QueryService(_ context.Context, _ httpapi.V3EnvironmentScope, _ core.User,
	service, env, query string, window connectors.TimeWindow) (v3integrations.PrometheusMetricEvidence, error) {
	s.called = true
	s.window = window
	if service != "checkout" || env != "dev" || query != "dashboard" {
		return v3integrations.PrometheusMetricEvidence{}, errors.New("unexpected query scope")
	}
	return s.evidence, s.err
}

func serviceFixture(now time.Time) v3integrations.PrometheusMetricEvidence {
	end := now.Truncate(metricStep * time.Second)
	out := v3integrations.PrometheusMetricEvidence{
		ApplicationID: "app", EnvironmentID: "dev", BindingID: "binding", ConnectorID: "prom",
		ResourceID: "app-metrics", QueryID: "dashboard", CapturedAt: now,
	}
	for _, panel := range v3metricpanels.ScalarPanels {
		value := 1.0
		switch panel {
		case "requests_2xx":
			value = 998
		case "requests_total":
			value = 1000
		case "requests_other":
			value = 0
		case "slo_month_availability_pct":
			value = 99.95
		case "slo_budget_remaining_pct":
			value = 50
		case "latency_p50_ms":
			value = 20
		case "latency_p95_ms":
			value = 40
		case "latency_p99_ms":
			value = 60
		}
		line := connectors.MetricSeries{Labels: map[string]string{"panel": panel}}
		for i := 0; i <= metricPoints; i++ {
			line.Samples = append(line.Samples, connectors.MetricSample{
				Timestamp: float64(end.Add(-time.Hour).Unix() + int64(i*metricStep)),
				Value:     fmt.Sprintf("%.3f", value),
			})
		}
		out.Series = append(out.Series, line)
	}
	addGroup := func(panel, key, group string, value float64) {
		line := connectors.MetricSeries{Labels: map[string]string{"panel": panel, key: group}}
		for i := 0; i <= metricPoints; i++ {
			line.Samples = append(line.Samples, connectors.MetricSample{
				Timestamp: float64(end.Add(-time.Hour).Unix() + int64(i*metricStep)),
				Value:     fmt.Sprintf("%.3f", value),
			})
		}
		out.Series = append(out.Series, line)
	}
	for _, bucket := range v3metricpanels.HeatBuckets {
		addGroup("latency_heat", "bucket", bucket, 0.25)
	}
	addGroup("endpoint_p99_ms", "endpoint", "GET /cart", 25)
	addGroup("instance_wait_p95_ms", "instance", "pod-1", 5)
	return out
}

func TestReadMetricsProjectsCompleteEvidence(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	reader := &metricsReaderStub{evidence: serviceFixture(now)}
	got, err := ReadMetrics(t.Context(), reader, httpapi.V3EnvironmentScope{}, core.User{}, "checkout", "app", "dev", 99.9, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reader.called || reader.window.StepSeconds != 100 || reader.window.End.Sub(reader.window.Start) != time.Hour {
		t.Fatalf("unexpected read window: %+v", reader.window)
	}
	if got.Status != "warn" || got.Availability == nil || *got.Availability != "99.950%" || got.Target != "99.9%" ||
		got.BudgetLeft == nil || math.Abs(*got.BudgetLeft-50) > 0.001 || len(got.Burn) != 3 || len(got.Signals) != 5 || len(got.Missing) != 0 ||
		len(got.Signals[0].Series) != metricPoints || !got.CapturedAt.Equal(now) {
		t.Fatalf("unexpected projection: %+v", got)
	}
}

func TestReadMetricsRejectsIncompleteOrContradictoryEvidence(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	tests := map[string]func(*v3integrations.PrometheusMetricEvidence){
		"wrong application": func(e *v3integrations.PrometheusMetricEvidence) { e.ApplicationID = "other" },
		"wrong environment": func(e *v3integrations.PrometheusMetricEvidence) { e.EnvironmentID = "prod" },
		"wrong query":       func(e *v3integrations.PrometheusMetricEvidence) { e.QueryID = "request_rate" },
		"stale":             func(e *v3integrations.PrometheusMetricEvidence) { e.CapturedAt = now.Add(-3 * time.Minute) },
		"future":            func(e *v3integrations.PrometheusMetricEvidence) { e.CapturedAt = now.Add(time.Minute) },
		"unknown panel":     func(e *v3integrations.PrometheusMetricEvidence) { e.Series[0].Labels["panel"] = "requests_teapot" },
		"no panels":         func(e *v3integrations.PrometheusMetricEvidence) { e.Series = nil },
		"missing heat": func(e *v3integrations.PrometheusMetricEvidence) {
			for i := range e.Series {
				if e.Series[i].Labels["panel"] == "latency_heat" {
					e.Series = append(e.Series[:i], e.Series[i+1:]...)
					return
				}
			}
		},
		"repeated time": func(e *v3integrations.PrometheusMetricEvidence) {
			e.Series[0].Samples[4].Timestamp = e.Series[0].Samples[3].Timestamp
		},
		"off-grid":         func(e *v3integrations.PrometheusMetricEvidence) { e.Series[0].Samples[3].Timestamp++ },
		"not finite":       func(e *v3integrations.PrometheusMetricEvidence) { e.Series[0].Samples[3].Value = "+Inf" },
		"unexpected label": func(e *v3integrations.PrometheusMetricEvidence) { e.Series[0].Labels["secret"] = "value" },
		"wrong total": func(e *v3integrations.PrometheusMetricEvidence) {
			for i := range e.Series {
				if e.Series[i].Labels["panel"] == "requests_total" {
					e.Series[i].Samples[2].Value = "999"
				}
			}
		},
		"wrong budget": func(e *v3integrations.PrometheusMetricEvidence) {
			for i := range e.Series {
				if e.Series[i].Labels["panel"] == "slo_budget_remaining_pct" {
					e.Series[i].Samples[metricPoints].Value = "70"
				}
			}
		},
		"wrong burn": func(e *v3integrations.PrometheusMetricEvidence) {
			for i := range e.Series {
				if e.Series[i].Labels["panel"] == "slo_burn_1h" {
					e.Series[i].Samples[metricPoints].Value = "2"
				}
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			evidence := serviceFixture(now)
			mutate(&evidence)
			reader := &metricsReaderStub{evidence: evidence}
			_, err := ReadMetrics(t.Context(), reader, httpapi.V3EnvironmentScope{}, core.User{}, "checkout", "app", "dev", 99.9, now)
			var metricErr *MetricError
			want := "metric_result_incomplete"
			if name == "stale" {
				want = "metric_result_stale"
			}
			if !errors.As(err, &metricErr) || metricErr.Code != want {
				t.Fatalf("expected typed %s error, got %v", want, err)
			}
		})
	}
}

func TestReadMetricsRejectsMissingTarget(t *testing.T) {
	now := time.Now().UTC()
	reader := &metricsReaderStub{evidence: serviceFixture(now)}
	_, err := ReadMetrics(t.Context(), reader, httpapi.V3EnvironmentScope{}, core.User{}, "checkout", "app", "dev", 100, now)
	var metricErr *MetricError
	if !errors.As(err, &metricErr) || metricErr.Code != "slo_unavailable" || reader.called {
		t.Fatalf("expected target rejection before source read, got %v", err)
	}
}

const metricPoints = v3metricpanels.Points

func withoutPanels(e v3integrations.PrometheusMetricEvidence, panels ...string) v3integrations.PrometheusMetricEvidence {
	drop := map[string]bool{}
	for _, panel := range panels {
		drop[panel] = true
	}
	kept := e.Series[:0:0]
	for _, line := range e.Series {
		if !drop[line.Labels["panel"]] {
			kept = append(kept, line)
		}
	}
	e.Series = kept
	return e
}

// PA-41: a reviewed query may return any subset of the panels. Each field
// that needs an absent panel is reported missing; nothing becomes zero.
func TestReadMetricsAcceptsPartialPanelSets(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	read := func(e v3integrations.PrometheusMetricEvidence) Metrics {
		t.Helper()
		got, err := ReadMetrics(t.Context(), &metricsReaderStub{evidence: e}, httpapi.V3EnvironmentScope{}, core.User{}, "checkout", "app", "dev", 99.9, now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	signals := func(m Metrics) string {
		ids := []string{}
		for _, s := range m.Signals {
			ids = append(ids, s.ID)
		}
		return strings.Join(ids, ",")
	}
	// The lab has no connection-wait metric, heat map or grouped panels.
	lab := read(withoutPanels(serviceFixture(now), v3metricpanels.ConnectionWaitP95, v3metricpanels.LatencyHeat,
		v3metricpanels.EndpointP99, v3metricpanels.InstanceWaitP95))
	if lab.Status != "warn" || lab.Availability == nil || lab.BudgetLeft == nil || len(lab.Burn) != 3 ||
		signals(lab) != "rps,err,p99,cpu" || lab.Missing["signals.wait"] != "not_configured" || len(lab.Missing) != 1 {
		t.Fatalf("lab panel set: %+v", lab)
	}
	// Minimal status and SLO set: availability and the three burn windows.
	slo := read(withoutPanels(serviceFixture(now), v3metricpanels.RequestPanels[0], v3metricpanels.RequestPanels[1],
		v3metricpanels.RequestPanels[2], v3metricpanels.RequestPanels[3], v3metricpanels.RequestsOther,
		v3metricpanels.LatencyP50, v3metricpanels.LatencyP95, v3metricpanels.LatencyP99, v3metricpanels.ConnectionWaitP95,
		v3metricpanels.CPUSaturation, v3metricpanels.SLOBudgetRemaining))
	if slo.Status != "warn" || slo.BudgetLeft == nil || len(slo.Burn) != 3 || len(slo.Signals) != 0 ||
		slo.Missing["signals.rps"] != "not_configured" || slo.Missing["status"] != "" {
		t.Fatalf("minimal SLO set: %+v", slo)
	}
	// Without the 1 h burn there is no status; other burns stay visible.
	noBurn := read(withoutPanels(serviceFixture(now), v3metricpanels.SLOBurn1h))
	if noBurn.Status != "" || noBurn.Missing["status"] != "not_configured" || noBurn.Missing["slo.burn.1h"] != "not_configured" ||
		len(noBurn.Burn) != 2 || noBurn.BudgetLeft == nil {
		t.Fatalf("missing 1 h burn: %+v", noBurn)
	}
	// Signals only: no availability, budget, burn or status.
	signalsOnly := read(withoutPanels(serviceFixture(now), v3metricpanels.SLOMonthAvailability, v3metricpanels.SLOBudgetRemaining,
		v3metricpanels.SLOBurn1h, v3metricpanels.SLOBurn6h, v3metricpanels.SLOBurn3d))
	if signalsOnly.Status != "" || signalsOnly.Availability != nil || signalsOnly.BudgetLeft != nil || len(signalsOnly.Burn) != 0 ||
		len(signalsOnly.Signals) != 5 || signalsOnly.Missing["slo.availability"] != "not_configured" {
		t.Fatalf("signals only: %+v", signalsOnly)
	}
	// Request classes without the total cannot prove the request rate.
	noTotal := read(withoutPanels(serviceFixture(now), v3metricpanels.RequestsTotal))
	if strings.Contains(signals(noTotal), "rps") || noTotal.Missing["signals.err"] != "not_configured" {
		t.Fatalf("requests without total: %+v", noTotal)
	}
}

// PA-44: an outage in the last bins (no traffic, no latency quantile) keeps
// the service's status and SLO; only the values without a current sample are
// unknown, and the series show the gaps as null.
func TestReadMetricsKeepsStatusThroughAnOutage(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 30, 15, 0, time.UTC)
	evidence := serviceFixture(now)
	for i := range evidence.Series {
		line := &evidence.Series[i]
		switch line.Labels["panel"] {
		case "requests_2xx", "requests_4xx", "requests_5xx", "requests_total":
			for j := metricPoints - 5; j <= metricPoints; j++ {
				line.Samples[j].Value = "0"
			}
		case "latency_p50_ms", "latency_p95_ms", "latency_p99_ms":
			line.Samples = line.Samples[:metricPoints-4] // the last five bins are missing
		case "connection_wait_p95_ms":
			line.Samples = append(line.Samples[:20], line.Samples[25:]...) // a gap earlier in the hour
		}
	}
	reader := &metricsReaderStub{evidence: evidence}
	got, err := ReadMetrics(t.Context(), reader, httpapi.V3EnvironmentScope{}, core.User{}, "checkout", "app", "dev", 99.9, now)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]KPI{}
	for _, signal := range got.Signals {
		ids[signal.ID] = signal
	}
	rps, ok := ids["rps"]
	if got.Status == "" || got.Availability == nil || len(got.Burn) != 3 || !ok || rps.Value != "0.00" ||
		got.Missing["signals.err"] != "not_generated" || got.Missing["signals.p99"] != "not_generated" ||
		ids["wait"].Value == "" || ids["wait"].Series[20] != nil || ids["wait"].Series[18] == nil || ids["cpu"].Value == "" {
		t.Fatalf("outage projection: status=%q missing=%v signals=%v", got.Status, got.Missing, ids)
	}
	if rps.Series[metricPoints-1] == nil || *rps.Series[metricPoints-1] != 0 || rps.Series[0] == nil {
		t.Fatalf("zero traffic must stay 0 in the series: %v", rps.Series[metricPoints-2:])
	}
	raw, _ := json.Marshal(got.Signals)
	if !strings.Contains(string(raw), "null") {
		t.Fatalf("gaps must serialize as null: %s", raw)
	}
}
