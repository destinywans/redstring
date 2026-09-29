package connectors

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
)

func completeTraceBatch(id, service, environment, version string, parent string, start time.Time) map[string]any {
	attributes := []any{
		map[string]any{"key": "service.name", "value": map[string]any{"stringValue": service}},
		map[string]any{"key": "deployment.environment", "value": map[string]any{"stringValue": environment}},
		map[string]any{"key": "service.version", "value": map[string]any{"stringValue": version}},
	}
	span := map[string]any{"traceId": id, "spanId": "bbbbbbbbbbbbbbbb", "name": "GET /orders",
		"startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10),
		"endTimeUnixNano":   strconv.FormatInt(start.Add(20*time.Millisecond).UnixNano(), 10),
		"status":            map[string]any{"code": 2}}
	if parent != "" {
		span["parentSpanId"] = parent
	}
	return map[string]any{"resource": map[string]any{"attributes": attributes},
		"scopeSpans": []any{map[string]any{"spans": []any{span}}}}
}

func TestCompleteTempoSearchSplitsAndReadsOnlyAuthorizedClosedTraces(t *testing.T) {
	const first = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const second = "cccccccccccccccccccccccccccccccc"
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	searches, details := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/search":
			searches++
			if r.URL.Query().Get("limit") != "20" || !strings.Contains(r.URL.Query().Get("q"), `resource.service.name = "orders"`) ||
				!strings.Contains(r.URL.Query().Get("q"), `resource.deployment.environment = "lab"`) {
				t.Errorf("search lost registered scope: %s", r.URL.String())
			}
			start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			if end-start > 15 {
				items := []map[string]string{}
				for i := 0; i < 20; i++ {
					items = append(items, map[string]string{"traceID": fmt.Sprintf("%032x", i+1)})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
				return
			}
			id := first
			if start >= base.Unix()+15 {
				id = second
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": []map[string]string{{"traceID": id}}})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			details++
			if r.URL.RawQuery != "" {
				t.Errorf("time range can make a trace partial: %s", r.URL.String())
			}
			id := strings.TrimPrefix(r.URL.Path, "/api/traces/")
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{completeTraceBatch(id, "orders", "lab", "v1.2.3", "", base)}})
		default:
			t.Errorf("unexpected Tempo path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, TempoSearchFailClosed: true, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	traces, err := telemetry.QueryCompleteTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"},
		Window: TimeWindow{Start: base, End: base.Add(30 * time.Second)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 || searches < 3 || details != 2 {
		t.Fatalf("search population not exhausted: traces=%d searches=%d details=%d", len(traces), searches, details)
	}
	for _, trace := range traces {
		if len(trace.Spans) != 1 || trace.Spans[0].Version != "v1.2.3" || !trace.Spans[0].Error {
			t.Fatalf("lost stored trace provenance: %+v", trace)
		}
	}
}

func TestCompleteTempoTraceRejectsUnboundOrOpenTree(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := time.Now().UTC().Add(-time.Minute)
	service, parent := "orders", ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{completeTraceBatch(id, service, "lab", "v1", parent, base)}})
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, TempoSearchFailClosed: true, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	query := CompleteTraceQuery{Resources: []string{"app"}, TraceID: id}
	service = "private-service"
	if _, err := telemetry.QueryCompleteTraces(context.Background(), query); err == nil || err.(*TelemetryError).Kind != "scope" {
		t.Fatalf("unbound service was returned: %v", err)
	}
	service, parent = "orders", "cccccccccccccccc"
	if _, err := telemetry.QueryCompleteTraces(context.Background(), query); err == nil || err.(*TelemetryError).Kind != "partial" {
		t.Fatalf("missing parent was returned as complete: %v", err)
	}
}

func TestValidTraceSelectorRejectsPipelinesAndUnions(t *testing.T) {
	if !ValidTraceSelector("") || !ValidTraceSelector(`{ status = error && span.http.method = "GET" }`) ||
		!ValidTraceSelector(`{ status = error || span.http.status_code >= 500 }`) {
		t.Fatal("bounded span selector was rejected")
	}
	for _, value := range []string{`{ status = error } || { resource.service.name = "other" }`,
		`{ span.http.route =~ "/orders/.*" }`, `{ status = }`,
		`{ status = error } | count_over_time()`, "{ status = error }\n{ status = ok }"} {
		if ValidTraceSelector(value) {
			t.Fatalf("unsafe or unsupported selector accepted: %q", value)
		}
	}
}

func TestCompleteTempoTraceAllowsAuthorizedDownstreamService(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := time.Now().UTC().Add(-time.Minute)
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search" {
			searches++
			if !strings.Contains(r.URL.Query().Get("q"), `resource.service.name = "orders"`) {
				t.Errorf("search did not use selected service: %s", r.URL.Query().Get("q"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": []map[string]string{{"traceID": id}}})
			return
		}
		root := completeTraceBatch(id, "orders", "lab", "v1", "", base)
		child := completeTraceBatch(id, "payments", "lab", "v2", "bbbbbbbbbbbbbbbb", base.Add(2*time.Millisecond))
		child["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)["spanId"] = "cccccccccccccccc"
		_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{root, child}})
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, TempoSearchFailClosed: true, Applications: map[string]ApplicationTelemetry{
		"orders":   {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
		"payments": {TempoService: "payments", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	query := CompleteTraceQuery{Resources: []string{"orders", "payments"}, SearchResources: []string{"orders"},
		Window: TimeWindow{Start: base.Add(-time.Second), End: base.Add(time.Second)}}
	traces, err := telemetry.QueryCompleteTraces(context.Background(), query)
	if err != nil || len(traces) != 1 || len(traces[0].Spans) != 2 || len(traces[0].MatchedResources) != 2 || searches != 1 {
		t.Fatalf("authorized downstream service was lost: traces=%+v searches=%d error=%v", traces, searches, err)
	}
	query.Resources = []string{"orders"}
	if _, err := telemetry.QueryCompleteTraces(context.Background(), query); err == nil || err.(*TelemetryError).Kind != "scope" {
		t.Fatalf("unbound downstream service was returned: %v", err)
	}
}

func TestCompleteTempoSearchHonorsWholeQueryDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, TempoSearchFailClosed: true, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = telemetry.QueryCompleteTraces(ctx, CompleteTraceQuery{Resources: []string{"app"},
		Window: TimeWindow{Start: start.Add(-time.Minute), End: start}})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("query did not honor caller deadline: elapsed=%s error=%v", time.Since(start), err)
	}
}

func TestCompleteTempoRejectsOverlappingHiddenRegistration(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{
		"visible": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
		"hidden":  {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab", "cluster": "secret"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = telemetry.QueryCompleteTraces(context.Background(), CompleteTraceQuery{Resources: []string{"visible"}, TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err == nil || err.(*TelemetryError).Kind != "configuration" || calls != 0 {
		t.Fatalf("overlapping hidden selector was queried: calls=%d error=%v", calls, err)
	}
}

func TestCompleteTempoListRequiresFailClosedSearchAttestation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = telemetry.QueryCompleteTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"},
		Window: TimeWindow{Start: now.Add(-time.Minute), End: now}})
	if err == nil || err.(*TelemetryError).Kind != "configuration" || calls != 0 {
		t.Fatalf("unverified source was queried as complete: calls=%d error=%v", calls, err)
	}
}

func TestCompleteTempoOperationUsesSafeRouteInsteadOfRawName(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := time.Now().UTC().Add(-time.Minute)
	route := "/users/{id}"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batch := completeTraceBatch(id, "orders", "lab", "v1", "", base)
		span := batch["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
		span["name"] = "GET /users/alice@example.com?token=secret"
		span["attributes"] = []any{
			map[string]any{"key": "http.route", "value": map[string]any{"stringValue": route}},
			map[string]any{"key": "http.request.method", "value": map[string]any{"stringValue": "GET"}},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{batch}})
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"},
			TempoSafeOperations: []string{"GET /users/{id}"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	traces, err := telemetry.QueryCompleteTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"}, TraceID: id})
	if err != nil || len(traces) != 1 || traces[0].Spans[0].Name != "GET /users/{id}" {
		t.Fatalf("unsafe raw operation leaked: traces=%+v error=%v", traces, err)
	}
	route = "/users/alice"
	traces, err = telemetry.QueryCompleteTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"}, TraceID: id})
	if err != nil || len(traces) != 1 || traces[0].Spans[0].Name != "operation withheld" {
		t.Fatalf("unreviewed concrete route leaked: traces=%+v error=%v", traces, err)
	}
}
