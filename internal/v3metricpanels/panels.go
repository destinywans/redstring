// Package v3metricpanels parses the reviewed per-service "dashboard" query.
// The query returns one-hour matrices labelled with a panel name; any subset
// of the known panels may be present (PA-41). A missing panel is simply
// absent, and a missing or NaN sample is a gap (NaN in Set, PA-44): during an
// outage a bin can have no traffic and latency quantiles have no value.
// Callers report what needs an absent panel or a current gap as unknown and
// never substitute zeros. Everything returned must still be well formed.
package v3metricpanels

import (
	"math"
	"strconv"
	"strings"

	"github.com/destinywans/redstring/internal/connectors"
)

// Points is the number of visible 100-second bins in the one-hour window; a
// series carries Points+1 samples, the first being the window start.
const (
	Points = 36
	Step   = 100
)

// Scalar panels, one series each.
const (
	Requests2xx          = "requests_2xx"
	Requests4xx          = "requests_4xx"
	Requests5xx          = "requests_5xx"
	RequestsOther        = "requests_other"
	RequestsTotal        = "requests_total"
	LatencyP50           = "latency_p50_ms"
	LatencyP95           = "latency_p95_ms"
	LatencyP99           = "latency_p99_ms"
	ConnectionWaitP95    = "connection_wait_p95_ms"
	CPUSaturation        = "cpu_saturation_pct"
	SLOBudgetRemaining   = "slo_budget_remaining_pct"
	SLOMonthAvailability = "slo_month_availability_pct"
	SLOBurn1h            = "slo_burn_1h"
	SLOBurn6h            = "slo_burn_6h"
	SLOBurn3d            = "slo_burn_3d"
)

// Grouped panels and their group label.
const (
	LatencyHeat       = "latency_heat"
	EndpointP99       = "endpoint_p99_ms"
	InstanceWaitP95   = "instance_wait_p95_ms"
	maxGroups         = 12
	maxSeries         = 64
	maxGroupLabelSize = 100
)

// ScalarPanels lists every known single-series panel.
var ScalarPanels = []string{Requests2xx, Requests4xx, Requests5xx, RequestsOther, RequestsTotal,
	LatencyP50, LatencyP95, LatencyP99, ConnectionWaitP95, CPUSaturation,
	SLOBudgetRemaining, SLOMonthAvailability, SLOBurn1h, SLOBurn6h, SLOBurn3d}

var scalar = map[string]bool{
	Requests2xx: true, Requests4xx: true, Requests5xx: true, RequestsOther: true, RequestsTotal: true,
	LatencyP50: true, LatencyP95: true, LatencyP99: true, ConnectionWaitP95: true, CPUSaturation: true,
	SLOBudgetRemaining: true, SLOMonthAvailability: true, SLOBurn1h: true, SLOBurn6h: true, SLOBurn3d: true,
}

var grouped = map[string]string{LatencyHeat: "bucket", EndpointP99: "endpoint", InstanceWaitP95: "instance"}

// HeatBuckets are the reviewed latency heat rows, slowest first.
var HeatBuckets = []string{">2 s", "1 s", "500 ms", "250 ms", "100 ms", "50 ms", "25 ms", "<10 ms"}

// RequestPanels are needed together for request rate and 5xx ratio: the
// total proves that the three classes account for every request.
var RequestPanels = []string{Requests2xx, Requests4xx, Requests5xx, RequestsTotal}

// Set holds the visible bins of each present panel; a gap is NaN.
type Set struct {
	Plain  map[string][]float64
	Groups map[string]map[string][]float64
}

// Has reports whether every named scalar panel is present.
func (s Set) Has(panels ...string) bool {
	for _, panel := range panels {
		if _, ok := s.Plain[panel]; !ok {
			return false
		}
	}
	return true
}

// Group returns a grouped panel, or nil when it is absent.
func (s Set) Group(panel string) map[string][]float64 { return s.Groups[panel] }

// Parse validates every series of one result. Unknown panels, malformed or
// duplicated series, off-grid or repeated timestamps, infinite or
// out-of-range values and a partial latency heat map make the whole result
// untrustworthy (false). An empty result is also false. Bins without a
// sample, and NaN samples, are gaps.
func Parse(series []connectors.MetricSeries, window connectors.TimeWindow) (Set, bool) {
	out := Set{Plain: map[string][]float64{}, Groups: map[string]map[string][]float64{}}
	if len(series) == 0 || len(series) > maxSeries {
		return out, false
	}
	heat := map[string]bool{}
	for _, bucket := range HeatBuckets {
		heat[bucket] = true
	}
	for _, line := range series {
		panel := line.Labels["panel"]
		groupKey, isGrouped := grouped[panel]
		if !scalar[panel] && !isGrouped {
			return out, false
		}
		if len(line.Samples) == 0 || len(line.Samples) > Points+1 || (!isGrouped && len(line.Labels) != 1) || (isGrouped && len(line.Labels) != 2) {
			return out, false
		}
		group := ""
		if isGrouped {
			group = line.Labels[groupKey]
			if group == "" || len(group) > maxGroupLabelSize || strings.TrimSpace(group) != group ||
				strings.ContainsAny(group, "\r\n\x00") || (panel == LatencyHeat && !heat[group]) {
				return out, false
			}
			if _, duplicate := out.Groups[panel][group]; duplicate {
				return out, false
			}
		} else if _, duplicate := out.Plain[panel]; duplicate {
			return out, false
		}
		values := make([]float64, Points)
		for i := range values {
			values[i] = math.NaN()
		}
		previous := -1
		for _, sample := range line.Samples {
			v, err := strconv.ParseFloat(sample.Value, 64)
			if math.IsNaN(sample.Timestamp) || math.IsInf(sample.Timestamp, 0) {
				return out, false
			}
			offset := sample.Timestamp - float64(window.Start.Unix())
			index := int(math.Round(offset / Step))
			if err != nil || math.IsInf(v, 0) || v < 0 || index < 0 || index > Points || index <= previous ||
				math.Abs(offset-float64(index*Step)) > 0.5 ||
				(strings.HasSuffix(panel, "_pct") && v > 100) || (panel == LatencyHeat && v > 1) {
				return out, false
			}
			previous = index
			// Sample 0 is the window start; each visible point is the right
			// edge of one 100-second bin. A NaN sample stays a gap.
			if index > 0 {
				values[index-1] = v
			}
		}
		if isGrouped {
			if out.Groups[panel] == nil {
				out.Groups[panel] = map[string][]float64{}
			}
			out.Groups[panel][group] = values
		} else {
			out.Plain[panel] = values
		}
	}
	if groups, ok := out.Groups[LatencyHeat]; ok && len(groups) != len(HeatBuckets) {
		return out, false
	}
	for _, panel := range []string{EndpointP99, InstanceWaitP95} {
		if len(out.Groups[panel]) > maxGroups {
			return out, false
		}
	}
	return out, true
}

// Requests derives the per-bin request rate and 5xx ratio from the request
// panels. ok is false when they are absent; consistent is false when present
// panels contradict each other (classes must sum to the total, other
// statuses must be zero when reported). A bin without traffic has rate 0
// and an undefined ratio (NaN, PA-44); a bin with any gap is a gap in both.
func (s Set) Requests() (rate, errorsPct []float64, ok, consistent bool) {
	if !s.Has(RequestPanels...) {
		return nil, nil, false, true
	}
	rate, errorsPct = make([]float64, Points), make([]float64, Points)
	other := s.Plain[RequestsOther]
	for i := range rate {
		ok2, ok4, ok5, total := s.Plain[Requests2xx][i], s.Plain[Requests4xx][i], s.Plain[Requests5xx][i], s.Plain[RequestsTotal][i]
		if other != nil && Defined(other[i]) && other[i] != 0 {
			return nil, nil, true, false
		}
		if !Defined(ok2) || !Defined(ok4) || !Defined(ok5) || !Defined(total) {
			rate[i], errorsPct[i] = math.NaN(), math.NaN()
			continue
		}
		rate[i] = ok2 + ok4 + ok5
		if math.Abs(rate[i]-total) > 0.001 {
			return nil, nil, true, false
		}
		if rate[i] == 0 {
			errorsPct[i] = math.NaN()
		} else {
			errorsPct[i] = 100 * ok5 / rate[i]
		}
	}
	return rate, errorsPct, true, true
}

// LatencyOrdered checks p50 <= p95 <= p99 for whichever of them are present.
func (s Set) LatencyOrdered() bool {
	order := []string{LatencyP50, LatencyP95, LatencyP99}
	for i := range Points {
		previous := -1.0
		for _, panel := range order {
			values, ok := s.Plain[panel]
			if !ok || !Defined(values[i]) {
				continue
			}
			if values[i] < previous {
				return false
			}
			previous = values[i]
		}
	}
	return true
}

// SLO is the request-based SLO evidence for a target strictly between 0 and
// 100. Availability needs the monthly availability panel; the budget is
// derived from it and, when the budget panel has a current value, must agree
// with it. When the request panels carried traffic in the window and the 1 h
// burn is known, the burn must agree with the 5xx share of the window.
// Reasons says why a nil field is unknown: not_configured (no panel) or
// not_generated (the panel has no value in the current bin).
type SLO struct {
	Availability     *float64
	BudgetLeft       *float64
	Burn1h           *float64
	Burn6h           *float64
	Burn3d           *float64
	Reasons          map[string]string
	Consistent       bool
	RequestsObserved bool
}

// SLO field names used as Reasons keys.
const (
	FieldAvailability = "availability"
	FieldBudgetLeft   = "budget_left"
	FieldBurn1h       = "burn_1h"
	FieldBurn6h       = "burn_6h"
	FieldBurn3d       = "burn_3d"
)

// Reasons for unknown values, in the V3 unknown vocabulary.
const (
	ReasonNotConfigured = "not_configured"
	ReasonNoCurrentData = "not_generated"
)

func (s Set) SLO(target float64) SLO {
	out := SLO{Consistent: true, Reasons: map[string]string{}}
	at := func(field, panel string) *float64 {
		v, reason := s.Current(panel)
		if reason != "" {
			if field != "" {
				out.Reasons[field] = reason
			}
			return nil
		}
		return &v
	}
	out.Burn1h, out.Burn6h, out.Burn3d = at(FieldBurn1h, SLOBurn1h), at(FieldBurn6h, SLOBurn6h), at(FieldBurn3d, SLOBurn3d)
	out.Availability = at(FieldAvailability, SLOMonthAvailability)
	if out.Availability == nil {
		out.Reasons[FieldBudgetLeft] = out.Reasons[FieldAvailability]
	}
	if target <= 0 || target >= 100 || math.IsNaN(target) {
		return out
	}
	if out.Availability != nil {
		budget := math.Max(0, math.Min(100, (*out.Availability-target)/(100-target)*100))
		out.BudgetLeft = &budget
		if remaining := at("", SLOBudgetRemaining); remaining != nil && math.Abs(budget-*remaining) > 0.1 {
			out.Consistent = false
		}
	}
	rate, _, ok, consistent := s.Requests()
	if ok && consistent && out.Burn1h != nil {
		var total, server float64
		for i := range rate {
			if Defined(rate[i]) {
				total += rate[i]
				server += s.Plain[Requests5xx][i]
			}
		}
		// A window without any traffic has no burn to compare with.
		if total > 0 {
			out.RequestsObserved = true
			derived := server / total / (1 - target/100)
			if math.IsNaN(derived) || math.IsInf(derived, 0) || math.Abs(derived-*out.Burn1h) > math.Max(0.05, derived*0.05) {
				out.Consistent = false
			}
		}
	}
	return out
}

// Defined reports whether v is a sample rather than a gap.
func Defined(v float64) bool { return !math.IsNaN(v) }

// Current returns a panel's value in the last visible bin, or the reason it
// is unknown: not_configured without the panel, not_generated on a gap.
func (s Set) Current(panel string) (float64, string) {
	values, ok := s.Plain[panel]
	if !ok {
		return 0, ReasonNotConfigured
	}
	if v := values[Points-1]; Defined(v) {
		return v, ""
	}
	return 0, ReasonNoCurrentData
}

// Nullable converts a series to JSON numbers with null for each gap.
func Nullable(values []float64) []*float64 {
	if values == nil {
		return nil
	}
	out := make([]*float64, len(values))
	for i, v := range values {
		if Defined(v) {
			value := v
			out[i] = &value
		}
	}
	return out
}
