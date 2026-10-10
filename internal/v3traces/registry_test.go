package v3traces

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

// timedTempo stores one single-span trace per second of [from, to) for one
// service and answers searches by trace start.
func timedTempo(t *testing.T, service string, offset int, from, to time.Time) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idAt := func(second int64) string { return fmt.Sprintf("%08x%024x", offset, second) }
		switch {
		case r.URL.Path == "/api/search":
			a, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			b, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			items := []map[string]string{}
			for second := max(a, from.Unix()); second < min(b, to.Unix()) && len(items) < 20; second++ {
				items = append(items, map[string]string{"traceID": idAt(second)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/traces/")
			second, _ := strconv.ParseInt(id[8:], 16, 64)
			start := time.Unix(second, 0)
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{map[string]any{
				"resource": map[string]any{"attributes": []any{
					map[string]any{"key": "service.name", "value": map[string]any{"stringValue": service}},
					map[string]any{"key": "deployment.environment", "value": map[string]any{"stringValue": "lab"}}}},
				"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{"traceId": id, "spanId": "1111111111111111", "name": "GET /",
					"startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10),
					"endTimeUnixNano":   strconv.FormatInt(start.Add(time.Millisecond).UnixNano(), 10)}}}}}}})
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// PA-78 with two registered Tempos: each gets half of the trace budget, the
// second searches only what the first covered, and the result is the
// window both covered completely; together they stay within the limits.
func TestRegistryObservedStopsAtACommonWindow(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-100 * time.Second)
	group := func(service string, offset int) traceGroup {
		server := timedTempo(t, service, offset, start, end)
		client, err := connectors.NewTelemetry(connectors.TelemetryConfig{Tempo: connectors.TelemetryEndpoint{URL: server.URL, AllowHTTP: true},
			TempoSearchFailClosed: true, Applications: map[string]connectors.ApplicationTelemetry{
				service: {TempoService: service, TempoAttributes: map[string]string{"deployment.environment": "lab"}}}})
		if err != nil {
			t.Fatal(err)
		}
		return traceGroup{client: client, all: []string{service}, selected: []string{service}}
	}
	observed, err := registryObserved(context.Background(), []traceGroup{group("checkout", 1), group("orders", 2)},
		connectors.CompleteTraceQuery{Window: connectors.TimeWindow{Start: start, End: end}})
	if err != nil {
		t.Fatal(err)
	}
	covered := end.Unix() - observed.WindowStart.Unix()
	if !observed.Truncated || !observed.PopulationComplete || !observed.WindowStart.After(start) ||
		len(observed.Traces) > connectors.CompleteTraceIDLimit || int64(len(observed.Traces)) != 2*covered {
		t.Fatalf("common window: from %v, %d traces over %d s", observed.WindowStart, len(observed.Traces), covered)
	}
	for _, trace := range observed.Traces {
		if traceStart(trace).Before(observed.WindowStart) {
			t.Fatalf("trace %s is older than the common window", trace.ID)
		}
	}
}
