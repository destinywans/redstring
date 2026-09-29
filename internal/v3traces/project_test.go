package v3traces

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

func TestProjectionCountsFullPopulationAndBuildsClosedTraceView(t *testing.T) {
	start := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	trace := connectors.CompleteTrace{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Spans: []connectors.TraceSpan{
		{TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "1111111111111111", Service: "api", Version: "v2.8.1",
			Name: "GET /orders", Start: start, DurationMS: 1200},
		{TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "2222222222222222", ParentSpanID: "1111111111111111",
			Service: "db", Name: "query", Start: start.Add(200 * time.Millisecond), DurationMS: 900, Error: true,
			Attributes: []connectors.TraceAttribute{{Key: "db.system", Value: "postgresql"}}},
	}}
	window := connectors.TimeWindow{Start: start.Add(-time.Hour), End: start.Add(time.Minute)}
	list := BuildList([]connectors.CompleteTrace{trace}, window, "all", "api", 15*time.Millisecond)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Href != "/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?service=api" ||
		list.Items[0].Errors != 1 || list.Items[0].Spans != 2 || list.Views[1].Count != 1 || list.Views[2].Count != 1 {
		t.Fatalf("inconsistent trace list: %+v", list)
	}
	buckets := 0
	for _, pair := range list.Histogram.Counts {
		buckets += pair[0] + pair[1]
	}
	if buckets != list.Total || len(list.Scatter) != list.Total || len(list.Facets) != 4 {
		t.Fatalf("list facets or charts do not cover the population: %+v", list)
	}
	view, err := BuildView(trace)
	if err != nil {
		t.Fatal(err)
	}
	if view.Op != "GET /orders" || view.Version != "v2.8.1" || view.DurationMS != 1200 || len(view.Spans) != 2 ||
		!view.Spans[0].Critical || !view.Spans[1].Critical || len(view.Graph.Nodes) != 2 || len(view.Graph.Edges) != 1 {
		t.Fatalf("trace detail projection mismatch: %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\"partial\"", "\"missing\"", "\"logs\"", "\"links\""} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("unproven relation in full view: %s", encoded)
		}
	}
}

func TestTraceViewKeepsDescendantsAdjacentForWaterfall(t *testing.T) {
	start := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	trace := connectors.CompleteTrace{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Spans: []connectors.TraceSpan{
		{SpanID: "root", Service: "api", Name: "root", Start: start, DurationMS: 100},
		{SpanID: "a", ParentSpanID: "root", Service: "api", Name: "a", Start: start.Add(10 * time.Millisecond), DurationMS: 70},
		{SpanID: "b", ParentSpanID: "root", Service: "api", Name: "b", Start: start.Add(20 * time.Millisecond), DurationMS: 20},
		{SpanID: "a1", ParentSpanID: "a", Service: "db", Name: "a1", Start: start.Add(30 * time.Millisecond), DurationMS: 10},
	}}
	view, err := BuildView(trace)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, span := range view.Spans {
		got = append(got, span.ID)
	}
	if strings.Join(got, ",") != "root,a,a1,b" || view.Spans[2].Depth != 2 || view.Spans[3].Depth != 1 {
		t.Fatalf("waterfall parent adjacency is broken: %v", got)
	}
}
