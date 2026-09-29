package v3serviceviews

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3integrations"
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
	for panel := range serviceMetricPanels {
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
	for bucket := range serviceHeatBuckets {
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
	if got.Status != "warn" || got.Availability != "99.950%" || got.Target != "99.9%" ||
		math.Abs(got.BudgetLeft-50) > 0.001 || len(got.Burn) != 3 || len(got.Signals) != 5 ||
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
		"missing panel":     func(e *v3integrations.PrometheusMetricEvidence) { e.Series = e.Series[1:] },
		"missing heat": func(e *v3integrations.PrometheusMetricEvidence) {
			for i := range e.Series {
				if e.Series[i].Labels["panel"] == "latency_heat" {
					e.Series = append(e.Series[:i], e.Series[i+1:]...)
					return
				}
			}
		},
		"missing baseline": func(e *v3integrations.PrometheusMetricEvidence) { e.Series[0].Samples = e.Series[0].Samples[1:] },
		"off-grid":         func(e *v3integrations.PrometheusMetricEvidence) { e.Series[0].Samples[3].Timestamp++ },
		"not finite":       func(e *v3integrations.PrometheusMetricEvidence) { e.Series[0].Samples[3].Value = "NaN" },
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
			if !errors.As(err, &metricErr) || metricErr.Code != "metric_result_incomplete" {
				t.Fatalf("expected typed incomplete error, got %v", err)
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
