package v3observe

import (
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

func TestTraceDetailPayloadUsesOnlyObservedScopedSpanData(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	spans := []connectors.TraceSpan{
		{SpanID: "child", ParentSpanID: "root", Service: "db", Name: "query", Start: start.Add(10 * time.Millisecond), DurationMS: 90, Error: true,
			Attributes: []connectors.TraceAttribute{{Key: "db.system", Value: "postgresql"}},
			Events:     []connectors.TraceEvent{{At: start.Add(20 * time.Millisecond), Name: "exception", Text: "TimeoutError"}}},
		{SpanID: "root", Service: "api", Name: "request", Start: start, DurationMS: 50},
	}
	payload := traceDetailPayload("trace", spans)
	if payload["duration_ms"].(float64) != 100 || payload["op"] != "request" || payload["time"] != start.Format(time.RFC3339Nano) {
		t.Fatalf("wrong observed trace envelope: %+v", payload)
	}
	if payload["partial"] != true {
		t.Fatalf("scoped Span view must state it is partial: %+v", payload)
	}
	items := payload["spans"].([]map[string]any)
	if len(items) != 2 || items[0]["id"] != "root" || items[1]["depth"] != 1 || items[1]["status"] != "error" {
		t.Fatalf("wrong scoped span tree: %+v", items)
	}
	if _, exists := items[1]["critical"]; exists {
		t.Fatal("critical path was inferred without evidence")
	}
	if _, exists := items[1]["logs"]; exists {
		t.Fatal("logs were fabricated")
	}
	if _, exists := items[1]["links"]; exists {
		t.Fatal("links were fabricated")
	}
	attrs := items[1]["attrs"].([]map[string]string)
	events := items[1]["events"].([]map[string]string)
	if attrs[0]["k"] != "db.system" || attrs[0]["v"] != "postgresql" || events[0]["name"] != "exception" || events[0]["text"] != "TimeoutError" {
		t.Fatalf("source-backed detail missing: attrs=%+v events=%+v", attrs, events)
	}
}
