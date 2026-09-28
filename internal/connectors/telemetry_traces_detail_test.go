package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTempoTraceDetailProjectsBoundedSafeAttributesAndEvents(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-time.Minute).UnixNano()
	id := strings.Repeat("a", 32)
	span := func(service string, n int) map[string]any {
		attrs := []any{
			map[string]any{"key": "http.request.method", "value": map[string]any{"stringValue": "GET"}},
			map[string]any{"key": "http.response.status_code", "value": map[string]any{"intValue": "503"}},
			map[string]any{"key": "http.url", "value": map[string]any{"stringValue": "https://example.invalid/?token=secret"}},
		}
		events := []any{
			map[string]any{"name": "exception", "timeUnixNano": fmt.Sprint(start + 1000), "attributes": []any{
				map[string]any{"key": "exception.type", "value": map[string]any{"stringValue": "TimeoutError"}},
				map[string]any{"key": "exception.message", "value": map[string]any{"stringValue": "secret message"}},
			}},
			map[string]any{"name": "log", "timeUnixNano": fmt.Sprint(start + 2000), "attributes": []any{
				map[string]any{"key": "message", "value": map[string]any{"stringValue": "secret log"}},
			}},
		}
		return map[string]any{"traceId": id, "spanId": fmt.Sprintf("%016x", n), "name": service,
			"startTimeUnixNano": fmt.Sprint(start), "endTimeUnixNano": fmt.Sprint(start + 1000000),
			"attributes": attrs, "events": events}
	}
	batch := func(service string, spans []any) map[string]any {
		return map[string]any{"resource": map[string]any{"attributes": []any{
			map[string]any{"key": "service.name", "value": map[string]any{"stringValue": service}},
			map[string]any{"key": "cluster", "value": map[string]any{"stringValue": "lab"}},
		}}, "scopeSpans": []any{map[string]any{"spans": spans}}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/traces/"+id {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"batches": []any{
			batch("private", []any{span("private", 1)}),
			batch("order", []any{span("order", 2)}),
		}})
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "order", TempoAttributes: map[string]string{"cluster": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := telemetry.QueryTracesBatchID(context.Background(), []string{"app"}, TimeWindow{Start: now.Add(-2 * time.Minute), End: now}, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	out := results["app"]
	if out.Source.Failure != nil || len(out.Spans) != 1 || out.Spans[0].Service != "order" {
		t.Fatalf("scope failure: %+v", out)
	}
	if len(out.Spans[0].Attributes) != 2 || out.Spans[0].Attributes[1].Value != "503" || len(out.Spans[0].Events) != 1 || out.Spans[0].Events[0].Text != "TimeoutError" {
		t.Fatalf("safe projection failure: %+v", out.Spans[0])
	}
	encoded, _ := json.Marshal(out.Spans)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "private") {
		t.Fatalf("sensitive or out-of-scope trace content leaked: %s", encoded)
	}
}

func TestTempoTraceDetailMarksSpanTruncation(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-time.Minute).UnixNano()
	id := strings.Repeat("b", 32)
	spans := make([]any, 501)
	for i := range spans {
		spans[i] = map[string]any{"traceId": id, "spanId": fmt.Sprintf("%016x", i+1), "name": "work",
			"startTimeUnixNano": fmt.Sprint(start), "endTimeUnixNano": fmt.Sprint(start + 1000000)}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"batches": []any{map[string]any{
			"resource": map[string]any{"attributes": []any{
				map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "order"}},
				map[string]any{"key": "cluster", "value": map[string]any{"stringValue": "lab"}},
			}}, "scopeSpans": []any{map[string]any{"spans": spans}},
		}}})
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "order", TempoAttributes: map[string]string{"cluster": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := telemetry.QueryTracesBatchID(context.Background(), []string{"app"}, TimeWindow{Start: now.Add(-2 * time.Minute), End: now}, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	if out := results["app"]; out.Source.Failure != nil || !out.LimitReached || len(out.Spans) != 500 {
		t.Fatalf("trace truncation was not signaled: %+v", out)
	}
}
