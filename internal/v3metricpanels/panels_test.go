package v3metricpanels

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
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
	short := panelSeries(window, SLOBurn1h, 1)
	short.Samples = short.Samples[1:]
	if _, ok := Parse([]connectors.MetricSeries{short}, window); ok {
		t.Fatal("missing baseline accepted")
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
