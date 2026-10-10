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
	observed, err := telemetry.QueryObservedTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"},
		Window: TimeWindow{Start: base, End: base.Add(30 * time.Second)}})
	if err != nil || !observed.PopulationComplete || len(observed.Traces) != 2 {
		t.Fatalf("attested observation: %+v %v", observed, err)
	}
	// Without the deployment attestation the same closed traces are returned,
	// but never as a complete population.
	telemetry.config.TempoSearchFailClosed = false
	if _, err := telemetry.QueryCompleteTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"},
		Window: TimeWindow{Start: base, End: base.Add(30 * time.Second)}}); err == nil || err.(*TelemetryError).Kind != "configuration" {
		t.Fatalf("unattested complete search was accepted: %v", err)
	}
	observed, err = telemetry.QueryObservedTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"},
		Window: TimeWindow{Start: base, End: base.Add(30 * time.Second)}})
	if err != nil || observed.PopulationComplete || len(observed.Traces) != 2 || len(observed.Traces[0].Spans) != 1 {
		t.Fatalf("unattested observation: %+v %v", observed, err)
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

// PA-19 P3 stage 2 step 4: one logical query over several Tempo clients
// shares one budget, so the search and trace ID limits hold for the whole
// query; a search URI over 8 KiB is refused before it is sent.
func TestTraceSearchBudgetIsSharedAndLongSearchesAreRefused(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	next := 0
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/search":
			searches++
			items := []map[string]string{}
			for i := 0; i < 15; i++ {
				next++
				items = append(items, map[string]string{"traceID": fmt.Sprintf("%032x", next)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/traces/")
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{completeTraceBatch(id, "orders", "lab", "v1", "", base)}})
		}
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	query := CompleteTraceQuery{Resources: []string{"app"}, Window: TimeWindow{Start: base, End: base.Add(30 * time.Second)}}
	// Alone, each call stays under 40 trace IDs (15 each).
	for i := 0; i < 3; i++ {
		if _, err := telemetry.QueryObservedTraces(context.Background(), query); err != nil {
			t.Fatalf("separate call %d: %v", i, err)
		}
	}
	// Under one budget, the third call takes the query past 40 IDs.
	query.Budget = &TraceSearchBudget{}
	for i := 0; i < 2; i++ {
		if _, err := telemetry.QueryObservedTraces(context.Background(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := telemetry.QueryObservedTraces(context.Background(), query); err == nil || err.(*TelemetryError).Kind != "limit" {
		t.Fatalf("shared budget not enforced: %v", err)
	}

	// A filter that would push the search URI over 8 KiB never leaves.
	before := searches
	long := map[string]string{}
	for i := 0; i < 8; i++ {
		long[fmt.Sprintf("k%d", i)] = strings.Repeat("v", 1000)
	}
	telemetry.config.Applications["wide"] = ApplicationTelemetry{TempoService: "wide", TempoAttributes: long}
	_, err = telemetry.QueryObservedTraces(context.Background(), CompleteTraceQuery{Resources: []string{"wide"},
		Window: TimeWindow{Start: base, End: base.Add(30 * time.Second)}})
	if err == nil || err.(*TelemetryError).Kind != "request_too_long" || searches != before {
		t.Fatalf("long search: %v (searches %d -> %d)", err, before, searches)
	}
	if selector := TempoScopeSelector(ApplicationTelemetry{TempoService: "orders", TempoAttributes: map[string]string{"b": "2", "a": "1"}}); selector !=
		`{ resource.service.name = "orders" && resource.a = "1" && resource.b = "2" }` {
		t.Fatalf("selector: %s", selector)
	}
}

// PA-78: Tempo search drops leading zeroes from trace IDs (29-31 hex digits).
// The list pads them to the 32 digits the detail route accepts, and reads
// the trace under that ID.
func TestCompleteTempoSearchPadsShortTraceIDs(t *testing.T) {
	const short, full = "abcabcabcabcabcabcabcabcabcab", "000abcabcabcabcabcabcabcabcabcab"
	base := time.Now().UTC().Add(-time.Minute)
	var read string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/search":
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": []map[string]string{{"traceID": short}}})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			read = strings.TrimPrefix(r.URL.Path, "/api/traces/")
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{completeTraceBatch(full, "orders", "lab", "v1", "", base)}})
		}
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{
		"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := telemetry.QueryObservedTraces(context.Background(), CompleteTraceQuery{Resources: []string{"app"},
		Window: TimeWindow{Start: base.Add(-time.Minute), End: base.Add(time.Minute)}})
	if err != nil || len(observed.Traces) != 1 || observed.Traces[0].ID != full || read != full || !ValidTraceID(observed.Traces[0].ID) {
		t.Fatalf("short ID: %+v read=%q %v", observed.Traces, read, err)
	}
}

// D19: tempo_attributes keys name resource attributes; the search adds
// "resource." itself, so a prefixed key is refused when the registration or
// a catalog scope is loaded instead of matching nothing.
func TestTempoAttributeKeysRefusePrefixes(t *testing.T) {
	for _, key := range []string{"resource.deployment.environment", "span.http.route"} {
		_, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: "http://127.0.0.1:3200", AllowHTTP: true},
			Applications: map[string]ApplicationTelemetry{"app": {TempoService: "orders", TempoAttributes: map[string]string{key: "lab"}}}})
		if err == nil || !strings.Contains(err.Error(), "prefix") {
			t.Fatalf("%s accepted: %v", key, err)
		}
		if ValidateApplicationScope(ApplicationTelemetry{TempoService: "orders", TempoAttributes: map[string]string{key: "lab"}}) == nil {
			t.Fatalf("%s accepted as a catalog scope", key)
		}
	}
	if ValidateApplicationScope(ApplicationTelemetry{TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}}) != nil {
		t.Fatal("a plain key was refused")
	}
}

// PA-78: a list search that reaches its budget goes newest-first and keeps
// the most recent complete part of the window: every trace of
// [WindowStart, end) and nothing older. The complete query keeps refusing.
func TestObservedTraceSearchStopsAtTheBudgetNewestFirst(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-100 * time.Second)
	// One trace per second, ID = its second.
	idAt := func(second int64) string { return fmt.Sprintf("%032x", second) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/search":
			a, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			b, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			items := []map[string]string{}
			for second := a; second < b && len(items) < 20; second++ {
				items = append(items, map[string]string{"traceID": idAt(second)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/traces/")
			second, _ := strconv.ParseInt(id, 16, 64)
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{completeTraceBatch(id, "orders", "lab", "v1", "", time.Unix(second, 0))}})
		}
	}))
	defer server.Close()
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, TempoSearchFailClosed: true,
		Applications: map[string]ApplicationTelemetry{"app": {TempoService: "orders", TempoAttributes: map[string]string{"deployment.environment": "lab"}}}})
	if err != nil {
		t.Fatal(err)
	}
	query := CompleteTraceQuery{Resources: []string{"app"}, Window: TimeWindow{Start: start, End: end}}
	observed, err := telemetry.QueryObservedTraces(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	from := observed.WindowStart.Unix()
	if !observed.Truncated || !observed.WindowStart.After(start) || !observed.WindowEnd.Equal(end) ||
		len(observed.Traces) > 40 || int64(len(observed.Traces)) != end.Unix()-from {
		t.Fatalf("truncated window: from %v (%d traces, truncated %v)", observed.WindowStart, len(observed.Traces), observed.Truncated)
	}
	for _, trace := range observed.Traces {
		if second, _ := strconv.ParseInt(trace.ID, 16, 64); second < from {
			t.Fatalf("trace %d is older than the covered window %d", second, from)
		}
	}
	if _, err := telemetry.QueryCompleteTraces(context.Background(), query); err == nil || err.(*TelemetryError).Kind != "limit" {
		t.Fatalf("complete query over the budget: %v", err)
	}
	// A window that fits is complete and not truncated.
	query.Window.Start = end.Add(-30 * time.Second)
	if observed, err = telemetry.QueryObservedTraces(context.Background(), query); err != nil || observed.Truncated ||
		len(observed.Traces) != 30 || !observed.WindowStart.Equal(query.Window.Start) {
		t.Fatalf("window within the budget: %d %v %v", len(observed.Traces), observed.Truncated, err)
	}
	// More than 20 traces in the newest second: nothing complete, 413.
	dense := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]string{}
		for i := 0; i < 20; i++ {
			items = append(items, map[string]string{"traceID": fmt.Sprintf("%032x", i+1)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
	}))
	defer dense.Close()
	telemetry.tempo.config.URL = dense.URL
	query.Window.Start = start
	if _, err := telemetry.QueryObservedTraces(context.Background(), query); err == nil || err.(*TelemetryError).Kind != "limit" {
		t.Fatalf("dense newest second: %v", err)
	}
}

// PA-78: the 64-search budget stops the search the same way. Six services,
// each search of a window over two seconds is full and smaller ones are
// empty: every complete leaf costs six searches.
func TestObservedTraceSearchStopsAtTheSearchBudget(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-256 * time.Second)
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		searches++
		a, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		b, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		items := []map[string]string{}
		if b-a > 2 {
			for i := 0; i < 20; i++ {
				items = append(items, map[string]string{"traceID": fmt.Sprintf("%032x", i+1)})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
	}))
	defer server.Close()
	applications := map[string]ApplicationTelemetry{}
	resources := []string{}
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("svc-%d", i)
		applications[name] = ApplicationTelemetry{TempoService: name, TempoAttributes: map[string]string{"deployment.environment": "lab"}}
		resources = append(resources, name)
	}
	telemetry, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: applications})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := telemetry.QueryObservedTraces(context.Background(), CompleteTraceQuery{Resources: resources,
		Window: TimeWindow{Start: start, End: end}})
	if err != nil || !observed.Truncated || searches != maxCompleteTraceSearches || len(observed.Traces) != 0 ||
		!observed.WindowStart.After(start) || end.Sub(observed.WindowStart) > 32*time.Second {
		t.Fatalf("search budget: searches=%d from=%v truncated=%v %v", searches, observed.WindowStart, observed.Truncated, err)
	}
}
