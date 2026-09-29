package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestElasticsearchPatternsRequireCompleteScopedAggregation(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	window := TimeWindow{Start: end.Add(-30 * time.Minute), End: end}
	makeTrend := func(count int64) map[string]any {
		buckets := make([]any, 30)
		for i := range buckets {
			value := int64(0)
			if i == 29 {
				value = count
			}
			buckets[i] = map[string]any{"key": window.Start.Add(time.Duration(i) * time.Minute).UnixMilli(), "doc_count": value}
		}
		return map[string]any{"buckets": buckets}
	}
	makeResponse := func() map[string]any {
		hits := make([]any, 100)
		for i := range hits {
			hits[i] = map[string]any{"_source": map[string]any{"@timestamp": end.Add(-time.Minute).Format(time.RFC3339Nano),
				"message": "checkout accepted", "level": "info", "cluster": "lab"}}
		}
		volume := make([]any, 30)
		for i := range volume {
			value := int64(0)
			if i == 29 {
				value = 101
			}
			volume[i] = map[string]any{"key": window.Start.Add(time.Duration(i) * time.Minute).UnixMilli(), "doc_count": value}
		}
		return map[string]any{
			"timed_out": false, "_shards": map[string]any{"total": 1, "successful": 1, "skipped": 0, "failed": 0},
			"hits": map[string]any{"total": map[string]any{"value": 101, "relation": "eq"}, "hits": hits},
			"aggregations": map[string]any{
				"volume": map[string]any{"buckets": volume}, "errors": map[string]any{"doc_count": 0},
				"patterns": map[string]any{"buckets": []any{
					map[string]any{"key": "checkout accepted", "doc_count": 60, "trend": makeTrend(60)},
					map[string]any{"key": "checkout <_>", "doc_count": 41, "trend": makeTrend(41)},
				}},
			},
		}
	}
	response := makeResponse()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/lab-logs/_search" {
			t.Errorf("unexpected Elasticsearch request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		raw, _ := json.Marshal(body)
		for _, required := range []string{`"cluster.keyword":"lab"`, `"aiops_level":"info"`, `"match_phrase":{"message":"checkout"}`, `"categorize_text":{"field":"message"`, `"shard_size":128`, `"track_total_hits":true`} {
			if !strings.Contains(string(raw), required) {
				t.Errorf("missing scoped exact pattern request %s: %s", required, raw)
			}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	tm, err := NewTelemetry(TelemetryConfig{Elasticsearch: TelemetryEndpoint{URL: server.URL, AllowHTTP: true},
		Applications: map[string]ApplicationTelemetry{"app": {ElasticsearchIndex: "lab-logs", ElasticsearchFilters: map[string]string{"cluster": "lab"}}}})
	if err != nil {
		t.Fatal(err)
	}
	read := func() LogEvidence {
		t.Helper()
		out, err := tm.QueryLogsWithPatterns(context.Background(), "app", "elasticsearch", "checkout", "info", window, 100)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := read()
	if out.Summary == nil || out.Summary.Total != 101 || len(out.Entries) != 100 || out.PatternSummary == nil || out.PatternError != "" || len(out.PatternSummary.Patterns) != 2 || len(out.PatternSummary.Patterns[0].Buckets) != 30 {
		t.Fatalf("101 exact matches with a 100-hit sample should retain complete patterns: %+v", out)
	}
	response = makeResponse()
	response["aggregations"].(map[string]any)["patterns"].(map[string]any)["buckets"].([]any)[1].(map[string]any)["doc_count"] = 40
	if out = read(); out.PatternSummary != nil || out.PatternError == "" {
		t.Fatal("truncated categories accepted")
	}
	response = makeResponse()
	response["aggregations"].(map[string]any)["patterns"].(map[string]any)["buckets"].([]any)[0].(map[string]any)["trend"].(map[string]any)["buckets"].([]any)[29].(map[string]any)["doc_count"] = 59
	if out = read(); out.PatternSummary != nil || out.PatternError == "" {
		t.Fatal("incomplete trend accepted")
	}
	for _, mutation := range []func(map[string]any){
		func(v map[string]any) { v["terminated_early"] = true },
		func(v map[string]any) {
			v["aggregations"].(map[string]any)["patterns"].(map[string]any)["buckets"] = []any{}
		},
	} {
		response = makeResponse()
		mutation(response)
		if out = read(); out.PatternSummary != nil || out.PatternError == "" {
			t.Fatal("partial or missing-message aggregation accepted")
		}
	}
	response = makeResponse()
	for _, partial := range []func(map[string]any){
		func(v map[string]any) { v["timed_out"] = true },
		func(v map[string]any) { v["_shards"].(map[string]any)["failed"] = 1 },
	} {
		response = makeResponse()
		partial(response)
		if _, err := tm.QueryLogsWithPatterns(context.Background(), "app", "elasticsearch", "checkout", "info", window, 100); err == nil {
			t.Fatal("partial Elasticsearch response accepted")
		}
	}
}
