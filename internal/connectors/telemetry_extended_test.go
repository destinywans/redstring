package connectors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelemetryTempoFiltersEveryReturnedResource(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-time.Minute).UnixNano()
	tid := "0" + strings.Repeat("a", 31)
	id64 := func(n int) string { return base64.StdEncoding.EncodeToString(make([]byte, n)) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search" {
			if !strings.Contains(r.URL.Query().Get("q"), `resource.service.name = "order"`) {
				t.Error("missing scoped TraceQL")
			}
			fmt.Fprintf(w, `{"traces":[{"traceID":%q}]}`, strings.TrimLeft(tid, "0"))
			return
		}
		batch := func(service, cluster string) map[string]any {
			return map[string]any{"resource": map[string]any{"attributes": []any{map[string]any{"key": "service.name", "value": map[string]string{"stringValue": service}}, map[string]any{"key": "k8s.cluster.name", "value": map[string]string{"stringValue": cluster}}}}, "scopeSpans": []any{map[string]any{"spans": []any{map[string]any{"traceId": tid, "spanId": id64(8), "name": "private-" + service + "-" + cluster, "startTimeUnixNano": fmt.Sprint(start), "endTimeUnixNano": fmt.Sprint(start + 1000000), "status": map[string]int{"code": 2}}}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"batches": []any{batch("secret-service", "lab"), batch("order", "other-cluster"), batch("order", "lab")}})
	}))
	defer server.Close()
	tm, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"app": {TempoService: "order", TempoAttributes: map[string]string{"k8s.cluster.name": "lab"}}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tm.QueryTraces(context.Background(), "app", TimeWindow{Start: now.Add(-2 * time.Minute), End: now}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Spans) != 1 || out.Spans[0].Name != "private-order-lab" || out.Spans[0].SpanID != strings.Repeat("0", 16) || !out.Spans[0].Error {
		t.Fatalf("unexpected authorized spans: %+v", out.Spans)
	}
}
func TestTelemetryElasticsearchScopesAndSearch(t *testing.T) {
	now := time.Now().UTC()
	wrong := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/lab-logs/_settings/index.mapping.ignore_above" {
			writeKeywordIndexSettings(w, "lab-logs")
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/lab-logs/_mapping/field/cluster,cluster.keyword,pod,pod.keyword" {
			writeKeywordScopeMapping(w, "lab-logs", "cluster.keyword", "pod.keyword")
			return
		}
		if r.Method != "POST" || r.URL.Path != "/lab-logs/_search" {
			t.Error("unexpected Elasticsearch route")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), `"cluster.keyword":"lab"`) || !strings.Contains(string(raw), `"pod.keyword":"order-"`) || !strings.Contains(string(raw), `"match_phrase"`) {
			t.Error("query lacks fixed scopes or bounded server search")
		}
		pod := "order-123"
		if wrong {
			pod = "private-app-123"
		}
		json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{map[string]any{"_source": map[string]any{"@timestamp": now.Add(-time.Minute).Format(time.RFC3339Nano), "message": "actual log", "cluster": "lab", "pod": pod}}}}})
	}))
	defer server.Close()
	tm, err := NewTelemetry(TelemetryConfig{Elasticsearch: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"app": {ElasticsearchIndex: "lab-logs", ElasticsearchFilters: map[string]string{"cluster": "lab"}, ElasticsearchPrefixes: map[string]string{"pod": "order-"}}}})
	if err != nil {
		t.Fatal(err)
	}
	window := TimeWindow{Start: now.Add(-2 * time.Minute), End: now}
	out, err := tm.QueryLogsSearch(context.Background(), "app", "elasticsearch", "actual", window, 10)
	if err != nil || len(out.Entries) != 1 {
		t.Fatalf("query failed: %v %+v", err, out)
	}
	wrong = true
	_, err = tm.QueryLogsSearch(context.Background(), "app", "elasticsearch", "actual", window, 10)
	if err == nil {
		t.Fatal("out-of-scope response accepted")
	}
}

// Independent searches deliberately return disjoint IDs. Shared reads must still
// recover selected cross-service parents, without returning the third service.
func TestTelemetryTraceBatchSharesSampleWithoutExpandingAuthorizedServices(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-time.Minute).UnixNano()
	details := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/search" {
			id := "1"
			if strings.Contains(r.URL.Query().Get("q"), `"checkout"`) {
				id = "2"
			}
			fmt.Fprintf(w, `{"traces":[{"traceID":%q}]}`, id)
			return
		}
		details++
		tid := strings.TrimPrefix(r.URL.Path, "/api/traces/")
		batch := func(service, sid, parent string) map[string]any {
			return map[string]any{"resource": map[string]any{"attributes": []any{map[string]any{"key": "service.name", "value": map[string]string{"stringValue": service}}, map[string]any{"key": "cluster", "value": map[string]string{"stringValue": "lab"}}}}, "scopeSpans": []any{map[string]any{"spans": []any{map[string]any{"traceId": tid, "spanId": sid, "parentSpanId": parent, "name": service, "startTimeUnixNano": fmt.Sprint(start), "endTimeUnixNano": fmt.Sprint(start + 1000000)}}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"batches": []any{batch("order", "1111111111111111", ""), batch("checkout", "2222222222222222", "1111111111111111"), batch("private", "3333333333333333", "2222222222222222")}})
	}))
	defer server.Close()
	config := TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{}}
	for _, app := range []string{"order", "checkout", "private"} {
		config.Applications[app] = ApplicationTelemetry{TempoService: app, TempoAttributes: map[string]string{"cluster": "lab"}}
	}
	tm, err := NewTelemetry(config)
	if err != nil {
		t.Fatal(err)
	}
	results, err := tm.QueryTracesBatch(context.Background(), []string{"order", "checkout"}, TimeWindow{Start: now.Add(-2 * time.Minute), End: now}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || details != 2 {
		t.Fatalf("unexpected scopes or duplicate reads: %d scopes, %d detail requests", len(results), details)
	}
	for _, app := range []string{"order", "checkout"} {
		out := results[app]
		if out.Source.Failure != nil || len(out.Spans) != 2 {
			t.Fatalf("%s did not receive shared traces: %+v", app, out)
		}
		for _, span := range out.Spans {
			if span.Service != app {
				t.Fatal("out-of-scope service leaked")
			}
		}
	}
	if results["checkout"].Spans[0].ParentSpanID != results["order"].Spans[0].SpanID {
		t.Fatal("shared parent-child relation lost")
	}
}
