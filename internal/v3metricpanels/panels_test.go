package v3metricpanels

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
)

func panelSeries(window connectors.TimeWindow, panel string, value float64, extra ...string) connectors.MetricSeries {
	labels := map[string]string{"panel": panel}
	if len(extra) == 2 {
		labels[extra[0]] = extra[1]
	}
	line := connectors.MetricSeries{Labels: labels}
	for i := 0; i <= Points; i++ {
		line.Samples = append(line.Samples, connectors.MetricSample{
			Timestamp: float64(window.Start.Unix() + int64(i*Step)), Value: fmt.Sprintf("%.3f", value)})
	}
	return line
}

func TestParseAcceptsSubsetsButNotMalformedSeries(t *testing.T) {
	end := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	window := connectors.TimeWindow{Start: end.Add(-time.Hour), End: end, StepSeconds: Step}
	set, ok := Parse([]connectors.MetricSeries{panelSeries(window, SLOMonthAvailability, 99.95), panelSeries(window, SLOBurn1h, 0.5)}, window)
	if !ok || !set.Has(SLOMonthAvailability, SLOBurn1h) || set.Has(Requests2xx) {
		t.Fatalf("subset rejected: %+v", set)
	}
	slo := set.SLO(99.9)
	if !slo.Consistent || slo.Availability == nil || slo.BudgetLeft == nil || math.Abs(*slo.BudgetLeft-50) > 0.001 ||
		slo.Burn1h == nil || slo.Burn6h != nil || slo.RequestsObserved {
		t.Fatalf("SLO from subset: %+v", slo)
	}
	if _, _, ok, consistent := set.Requests(); ok || !consistent {
		t.Fatal("absent request panels must be reported absent, not inconsistent")
	}
	for name, series := range map[string][]connectors.MetricSeries{
		"empty":        nil,
		"duplicate":    {panelSeries(window, SLOBurn1h, 1), panelSeries(window, SLOBurn1h, 1)},
		"unknown":      {panelSeries(window, "slo_burn_2h", 1)},
		"over 100 pct": {panelSeries(window, CPUSaturation, 101)},
		"partial heat": {panelSeries(window, LatencyHeat, 0.5, "bucket", ">2 s")},
		"bad bucket":   {panelSeries(window, LatencyHeat, 0.5, "bucket", "3 s")},
	} {
		if _, ok := Parse(series, window); ok {
			t.Fatalf("%s accepted", name)
		}
	}
	for name, change := range map[string]func(*connectors.MetricSeries){
		"off grid":          func(l *connectors.MetricSeries) { l.Samples[3].Timestamp += 30 },
		"repeated time":     func(l *connectors.MetricSeries) { l.Samples[4].Timestamp = l.Samples[3].Timestamp },
		"before the window": func(l *connectors.MetricSeries) { l.Samples[0].Timestamp -= Step },
		"infinite":          func(l *connectors.MetricSeries) { l.Samples[5].Value = "+Inf" },
		"negative":          func(l *connectors.MetricSeries) { l.Samples[5].Value = "-1" },
		"no samples":        func(l *connectors.MetricSeries) { l.Samples = nil },
	} {
		line := panelSeries(window, SLOBurn1h, 1)
		change(&line)
		if _, ok := Parse([]connectors.MetricSeries{line}, window); ok {
			t.Fatalf("%s accepted", name)
		}
	}
}

// PA-44: missing and NaN samples are gaps, not a malformed result; the
// current value of a gap is unknown with not_generated.
func TestParseKeepsGapsAndZeroTraffic(t *testing.T) {
	end := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	window := connectors.TimeWindow{Start: end.Add(-time.Hour), End: end, StepSeconds: Step}
	latency := panelSeries(window, LatencyP99, 80)
	latency.Samples = append(latency.Samples[:10], latency.Samples[20:]...) // bins 10..19 missing
	latency.Samples[len(latency.Samples)-1].Value = "NaN"                   // and the current bin
	burn := panelSeries(window, SLOBurn1h, 2)
	burn.Samples = burn.Samples[1:] // no baseline
	set, ok := Parse([]connectors.MetricSeries{latency, burn, panelSeries(window, SLOMonthAvailability, 99.95)}, window)
	if !ok {
		t.Fatal("gaps rejected")
	}
	values := set.Plain[LatencyP99]
	if !Defined(values[8]) || Defined(values[9]) || Defined(values[18]) || !Defined(values[19]) || Defined(values[Points-1]) {
		t.Fatalf("gap positions: %v", values)
	}
	if _, reason := set.Current(LatencyP99); reason != ReasonNoCurrentData {
		t.Fatalf("current gap reason: %q", reason)
	}
	if _, reason := set.Current(CPUSaturation); reason != ReasonNotConfigured {
		t.Fatalf("absent panel reason: %q", reason)
	}
	if nullable := Nullable(values); nullable[9] != nil || nullable[8] == nil || *nullable[8] != 80 {
		t.Fatalf("nullable: %v", nullable[8:10])
	}
	if v, reason := set.Current(SLOBurn1h); reason != "" || v != 2 {
		t.Fatalf("missing baseline must not hide the current bin: %v %q", v, reason)
	}
	gapNow := panelSeries(window, SLOMonthAvailability, 99.95)
	gapNow.Samples[Points].Value = "NaN"
	set, _ = Parse([]connectors.MetricSeries{gapNow, burn}, window)
	if slo := set.SLO(99.9); slo.Availability != nil || slo.BudgetLeft != nil ||
		slo.Reasons[FieldAvailability] != ReasonNoCurrentData || slo.Reasons[FieldBudgetLeft] != ReasonNoCurrentData ||
		slo.Reasons[FieldBurn6h] != ReasonNotConfigured || slo.Burn1h == nil || !slo.Consistent {
		t.Fatalf("SLO with a current gap: %+v", slo)
	}

	// Zero traffic is 0 requests with an undefined error ratio, and a burn
	// cannot be cross-checked against a window without traffic.
	zero := []connectors.MetricSeries{panelSeries(window, Requests2xx, 0), panelSeries(window, Requests4xx, 0),
		panelSeries(window, Requests5xx, 0), panelSeries(window, RequestsTotal, 0), panelSeries(window, SLOBurn1h, 3)}
	set, ok = Parse(zero, window)
	rate, errorsPct, present, consistent := set.Requests()
	if !ok || !present || !consistent || rate[0] != 0 || Defined(errorsPct[0]) {
		t.Fatalf("zero traffic: %v %v %v %v", rate[0], errorsPct[0], present, consistent)
	}
	if slo := set.SLO(99.9); !slo.Consistent || slo.RequestsObserved {
		t.Fatalf("burn checked against no traffic: %+v", slo)
	}
	// A gap in one class is a gap in the rate, not a contradiction.
	partial := append([]connectors.MetricSeries(nil), zero[:4]...)
	partial[2] = panelSeries(window, Requests5xx, 0)
	partial[2].Samples[7].Value = "NaN"
	set, _ = Parse(partial, window)
	if rate, _, _, consistent := set.Requests(); !consistent || Defined(rate[6]) || !Defined(rate[7]) {
		t.Fatalf("class gap: %v %v", rate[6:8], consistent)
	}
}

func TestRequestsNeedTheTotalAndAgreeWithIt(t *testing.T) {
	end := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	window := connectors.TimeWindow{Start: end.Add(-time.Hour), End: end, StepSeconds: Step}
	base := []connectors.MetricSeries{panelSeries(window, Requests2xx, 9), panelSeries(window, Requests4xx, 0.5),
		panelSeries(window, Requests5xx, 0.5)}
	set, _ := Parse(base, window)
	if _, _, ok, _ := set.Requests(); ok {
		t.Fatal("classes without a total cannot prove the request rate")
	}
	set, _ = Parse(append(base, panelSeries(window, RequestsTotal, 10)), window)
	rate, errorsPct, ok, consistent := set.Requests()
	if !ok || !consistent || rate[0] != 10 || errorsPct[0] != 5 {
		t.Fatalf("requests: %v %v %v %v", rate[:1], errorsPct[:1], ok, consistent)
	}
	set, _ = Parse(append(base, panelSeries(window, RequestsTotal, 11)), window)
	if _, _, ok, consistent := set.Requests(); !ok || consistent {
		t.Fatal("a total that the classes do not account for was accepted")
	}
	// A 5% error share against a 99.9% target is a 50x burn; a 1 h burn that
	// says otherwise contradicts the requests.
	set, _ = Parse(append(base, panelSeries(window, RequestsTotal, 10), panelSeries(window, SLOBurn1h, 1)), window)
	if slo := set.SLO(99.9); slo.Consistent || !slo.RequestsObserved {
		t.Fatalf("contradictory burn accepted: %+v", slo)
	}
}
