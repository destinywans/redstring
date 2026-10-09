package v3serviceviews

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3metricpanels"
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

// Metrics is the projection of one reviewed dashboard result. A partial
// panel set is accepted (PA-41): each field that needs an absent panel is left
// empty and named in Missing with not_configured. Status and SLO use only the
// panels they need; nothing absent is replaced with zero.
type Metrics struct {
	// Status is the observed SLO burn signal, not a Kubernetes readiness
	// claim. Empty when the 1 h burn or the monthly availability is absent.
	Status       string
	Availability *string
	Target       string
	BudgetLeft   *float64
	// Burn and Signals contain only the windows and signals that are present.
	Burn        []Burn
	Signals     []KPI
	Missing     map[string]string
	CapturedAt  time.Time
	BindingID   string
	ConnectorID string
	ResourceID  string
}

const metricStep = v3metricpanels.Step

// ReadMetrics does not supply placeholder health or SLO values. A result
// captured before the window end fails as metric_result_stale; any other
// malformed, cross-scope or contradictory result as metric_result_incomplete.
// Absent panels are not an error; they are reported in Metrics.Missing.
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
		evidence.ResourceID == "" || evidence.CapturedAt.IsZero() ||
		evidence.CapturedAt.After(now.Add(15*time.Second)) {
		return zero, &MetricError{Code: "metric_result_incomplete"}
	}
	if evidence.CapturedAt.Before(end) {
		return zero, &MetricError{Code: "metric_result_stale"}
	}
	panels, ok := v3metricpanels.Parse(evidence.Series, window)
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

func projectServiceMetrics(p v3metricpanels.Set, target float64, captured time.Time) (Metrics, error) {
	out := Metrics{Target: fmt.Sprintf("%g%%", target), CapturedAt: captured, Missing: map[string]string{}}
	rate, errorsPct, requests, consistent := p.Requests()
	slo := p.SLO(target)
	// Present panels must agree with each other; a contradiction makes the
	// whole result untrustworthy rather than partially shown.
	if !consistent || !slo.Consistent || !p.LatencyOrdered() {
		return Metrics{}, &MetricError{Code: "metric_result_incomplete"}
	}
	if slo.Availability != nil {
		availability := fmt.Sprintf("%.3f%%", *slo.Availability)
		out.Availability = &availability
	} else {
		out.Missing["slo.availability"] = reasonNotConfigured
	}
	if slo.BudgetLeft != nil {
		out.BudgetLeft = slo.BudgetLeft
	} else {
		out.Missing["slo.budget_left"] = reasonNotConfigured
	}
	for _, burn := range []struct {
		window, key string
		value       *float64
	}{{"1 h", "1h", slo.Burn1h}, {"6 h", "6h", slo.Burn6h}, {"3 d", "3d", slo.Burn3d}} {
		if burn.value == nil {
			out.Missing["slo.burn."+burn.key] = reasonNotConfigured
			continue
		}
		out.Burn = append(out.Burn, serviceBurn(burn.window, *burn.value))
	}
	if slo.Burn1h != nil && slo.BudgetLeft != nil {
		oneHour, budget := *slo.Burn1h, *slo.BudgetLeft
		out.Status = "ok"
		if oneHour >= 10 || budget == 0 {
			out.Status = "danger"
		} else if oneHour >= 1 || budget < 50 {
			out.Status = "warn"
		}
	} else {
		out.Missing["status"] = reasonNotConfigured
	}
	last := v3metricpanels.Points - 1
	signal := func(id, label, unit, sub, format string, series []float64) {
		out.Signals = append(out.Signals, KPI{ID: id, Label: label, Value: fmt.Sprintf(format, series[last]), Unit: unit, Sub: sub, Series: series})
	}
	if requests {
		signal("rps", "Requests", "/s", "Observed request rate", "%.2f", rate)
		signal("err", "5xx ratio", "%", "Observed server errors", "%.2f", errorsPct)
	} else {
		out.Missing["signals.rps"], out.Missing["signals.err"] = reasonNotConfigured, reasonNotConfigured
	}
	for _, s := range []struct{ id, panel, label, unit, sub, format string }{
		{"p99", v3metricpanels.LatencyP99, "P99 latency", "ms", "Observed latency", "%.1f"},
		{"wait", v3metricpanels.ConnectionWaitP95, "Connection wait P95", "ms", "Observed wait", "%.1f"},
		{"cpu", v3metricpanels.CPUSaturation, "CPU saturation", "%", "Observed saturation", "%.1f"},
	} {
		if series, ok := p.Plain[s.panel]; ok {
			signal(s.id, s.label, s.unit, s.sub, s.format, series)
		} else {
			out.Missing["signals."+s.id] = reasonNotConfigured
		}
	}
	return out, nil
}

// metricReason maps a failed metric read to the reason its fields are null.
// A source that changed during the read cannot be reported per field; a
// cancelled request is not a source failure. Everything else is the metric
// source's own failure and leaves the rest of the reply intact.
func metricReason(ctx context.Context, err error) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	var readErr *v3integrations.PrometheusReadError
	if errors.As(err, &readErr) {
		switch readErr.Code {
		case "source_changed":
			return "", errSourceChanged
		case "source_not_found":
			return reasonNotConnected, nil
		case "query_unregistered":
			return reasonNotConfigured, nil
		case "forbidden":
			return reasonNoAuthorized, nil
		}
		return reasonUnavailable, nil
	}
	var metricErr *MetricError
	if errors.As(err, &metricErr) {
		switch metricErr.Code {
		case "slo_unavailable":
			return reasonNotConfigured, nil
		case "metric_result_stale":
			return reasonStale, nil
		}
	}
	return reasonUnavailable, nil
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
