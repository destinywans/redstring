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

func TestLokiPatternsRequireExactWholeWindowAndEveryBucket(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	window := TimeWindow{Start: end.Add(-30 * time.Minute), End: end}
	mode := "complete"
	queries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loki/api/v1/query_range" {
			if r.URL.Query().Get("direction") != "backward" || r.URL.Query().Get("limit") != "100" ||
				r.URL.Query().Get("start") != strconv.FormatInt(window.Start.UnixNano(), 10) ||
				r.URL.Query().Get("end") != strconv.FormatInt(window.End.UnixNano(), 10) {
				t.Errorf("unbounded Loki sample: %s", r.URL)
			}
			values := make([][]string, 100)
			for i := range values {
				values[i] = []string{strconv.FormatInt(end.Add(-time.Second).UnixNano(), 10), `{"level":"info","message":"checkout accepted","message_template":"checkout <id>"}`}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"stream": map[string]string{"application": "demo", "namespace": "lab", "level": "error"}, "values": values}}}})
			return
		}
		if r.URL.Path != "/loki/api/v1/query" {
			t.Errorf("unexpected Loki route: %s", r.URL)
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		queries++
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, `{application="demo",namespace="lab"} |= "checkout"`) ||
			!strings.Contains(query, `| drop aiops_level | json aiops_level="level" | __error__="" | aiops_level=~"(?i)^(info)$"`) {
			t.Errorf("Loki aggregation lost search/level/scope: %s", query)
		}
		grouped := strings.Contains(query, "sum by (aiops_pattern_template)")
		if grouped && !strings.Contains(query, `| drop aiops_pattern_template | json aiops_pattern_template="message_template" | __error__=""`) {
			t.Errorf("Loki aggregation does not parse trusted line template: %s", query)
		}
		full := strings.Contains(query, "[1800000ms]")
		last := r.URL.Query().Get("time") == strconv.FormatInt(end.Add(-time.Nanosecond).UnixNano(), 10)
		rows := []any{}
		ns, _ := strconv.ParseInt(r.URL.Query().Get("time"), 10, 64)
		add := func(template string, count int64) {
			metric := map[string]string{}
			if template != "" {
				metric["aiops_pattern_template"] = template
			}
			sampleTime := float64(ns) / float64(time.Second)
			if mode == "timestamp_mismatch" && grouped && !full {
				sampleTime = float64(end.Add(-time.Minute).Unix())
			}
			rows = append(rows, map[string]any{"metric": metric, "value": []any{sampleTime, strconv.FormatInt(count, 10)}})
		}
		if grouped {
			if full || last {
				add("checkout <id>", 60)
				if mode != "truncated" || !full {
					count := int64(41)
					if mode == "bucket_mismatch" && last && !full {
						count = 40
					}
					add("checkout accepted", count)
				}
				if mode == "missing_template" && full {
					rows = rows[:1]
					add("", 41)
				}
			}
		} else if full || last {
			// Every sampled line falls into the final bucket.
			rows = append(rows, map[string]any{"metric": map[string]string{}, "value": []any{float64(ns) / float64(time.Second), "101"}})
		}
		response := map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": rows}}
		if mode == "warning" && grouped && full {
			response["warnings"] = []string{"partial upstream"}
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	tm, err := NewTelemetry(TelemetryConfig{Loki: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"demo": {
		LokiLabels: map[string]string{"application": "demo", "namespace": "lab"}, LokiPatternField: "message_template", LokiCompleteLookbackSeconds: 7200}}})
	if err != nil {
		t.Fatal(err)
	}
	tm.now = func() time.Time { return end }
	read := func() LogEvidence {
		t.Helper()
		out, err := tm.QueryLogsWithPatterns(context.Background(), "demo", "loki", "checkout", "info", window, 100)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := read()
	if out.PatternError != "" || out.PatternSummary == nil || out.Summary == nil || out.Summary.Total != 101 ||
		len(out.Entries) != 100 || out.Entries[0].Level != "info" || len(out.Summary.Buckets) != 30 || len(out.PatternSummary.Patterns) != 2 ||
		len(out.PatternSummary.Patterns[0].Buckets) != 30 || queries != 32 {
		t.Fatalf("bounded sample must retain complete 101-count patterns: queries=%d result=%+v", queries, out)
	}
	for _, failure := range []string{"truncated", "bucket_mismatch", "missing_template", "warning", "timestamp_mismatch"} {
		mode = failure
		out = read()
		if out.PatternSummary != nil || out.PatternError == "" || out.PatternState != "unavailable" {
			t.Fatalf("%s was accepted: %+v", failure, out)
		}
		// Ungrouped exact counts inside the registered horizon remain proven.
		if out.Summary == nil || out.Summary.Total != 101 || len(out.Summary.Buckets) != 30 ||
			out.Summary.Buckets[29].Count != 101 || out.SummaryState != "" || out.SummaryError != "" {
			t.Fatalf("%s lost the exact count fallback: %+v", failure, out.Summary)
		}
	}
	unsafeApp := tm.config.Applications["demo"]
	unsafeApp.LokiCompleteLookbackSeconds = 1799
	tm.config.Applications["demo"] = unsafeApp
	before := queries
	out = read()
	if out.PatternSummary != nil || out.PatternError == "" || queries != before {
		t.Fatalf("unsafe tenant lookback reached count queries: %+v", out)
	}
}

func TestLokiPatternsAcceptVerifiedZeroAndRejectUnregisteredSource(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	window := TimeWindow{Start: end.Add(-30 * time.Minute), End: end}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := "vector"
		if r.URL.Path == "/loki/api/v1/query_range" {
			kind = "streams"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":%q,"result":[]}}`, kind)
	}))
	defer server.Close()
	tm, err := NewTelemetry(TelemetryConfig{Loki: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"demo": {
		LokiLabels: map[string]string{"application": "demo"}, LokiPatternField: "message_template", LokiCompleteLookbackSeconds: 7200}}})
	if err != nil {
		t.Fatal(err)
	}
	tm.now = func() time.Time { return end }
	out, err := tm.QueryLogsWithPatterns(context.Background(), "demo", "loki", "", "info", window, 100)
	if err != nil || out.PatternError != "" || out.Summary == nil || out.Summary.Total != 0 || out.PatternSummary == nil || len(out.PatternSummary.Patterns) != 0 || len(out.Summary.Buckets) != 30 {
		t.Fatalf("verified empty window should be complete: %+v %v", out, err)
	}
	tm.config.Applications["demo"] = ApplicationTelemetry{LokiLabels: map[string]string{"application": "demo"}}
	out, err = tm.QueryLogsWithPatterns(context.Background(), "demo", "loki", "", "info", window, 100)
	if err != nil || out.PatternSummary != nil || out.PatternError == "" || out.PatternState != "not_configured" ||
		out.Summary != nil || out.SummaryState != "not_configured" || out.Entries == nil {
		t.Fatalf("unregistered template was accepted: %+v %v", out, err)
	}
	// A registered lookback alone proves exact counts; templates stay unknown.
	lookbackOnly := ApplicationTelemetry{LokiLabels: map[string]string{"application": "demo"}, LokiCompleteLookbackSeconds: 7200}
	if _, err := NewTelemetry(TelemetryConfig{Loki: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"demo": lookbackOnly}}); err != nil {
		t.Fatalf("count-only Loki registration rejected: %v", err)
	}
	tm.config.Applications["demo"] = lookbackOnly
	out, err = tm.QueryLogsWithPatterns(context.Background(), "demo", "loki", "", "info", window, 100)
	if err != nil || out.PatternSummary != nil || out.PatternState != "not_configured" || out.Summary == nil ||
		out.Summary.Total != 0 || len(out.Summary.Buckets) != 30 || out.SummaryState != "" || out.SummaryError != "" {
		t.Fatalf("count-only registration must prove counts without patterns: %+v %v", out, err)
	}
	lookbackOnly.LokiCompleteLookbackSeconds = 1799
	tm.config.Applications["demo"] = lookbackOnly
	out, err = tm.QueryLogsWithPatterns(context.Background(), "demo", "loki", "", "info", window, 100)
	if err != nil || out.Summary != nil || out.SummaryState != "unavailable" {
		t.Fatalf("count outside the registered lookback was accepted: %+v %v", out, err)
	}
	for _, invalid := range []ApplicationTelemetry{
		{LokiLabels: map[string]string{"application": "demo"}, LokiPatternField: "user_email", LokiCompleteLookbackSeconds: 7200},
		{LokiLabels: map[string]string{"application": "demo"}, LokiPatternField: "message_template"},
		{LokiLabels: map[string]string{"application": "demo"}, LokiCompleteLookbackSeconds: -1},
	} {
		if _, err := NewTelemetry(TelemetryConfig{Loki: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"demo": invalid}}); err == nil {
			t.Fatalf("accepted unsafe Loki pattern registration: %+v", invalid)
		}
	}
}

func TestLokiLevelCountsExcludeUnsampledNestedPayloadLevel(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Minute)
	window := TimeWindow{Start: end.Add(-30 * time.Minute), End: end}
	const canonicalFilter = `| drop aiops_level | json aiops_level="level" | __error__="" | aiops_level=~"(?i)^(info)$"`
	lines := make([]string, 101)
	for i := 0; i < 100; i++ {
		lines[i] = `{"level":"info","message":"checkout","message_template":"checkout <id>"}`
	}
	lines[100] = `{"payload":{"level":"info"},"message":"checkout","message_template":"checkout <id>"}`
	if _, err := canonicalLokiLevel(lines[100]); err == nil {
		t.Fatal("nested payload level was treated as top-level severity")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if !strings.Contains(query, canonicalFilter) {
			t.Errorf("query can count unsampled nested payload.level: %s", query)
		}
		if r.URL.Path == "/loki/api/v1/query_range" {
			values := make([][]string, 100)
			for i, line := range lines[:100] {
				values[i] = []string{strconv.FormatInt(end.Add(-time.Second).UnixNano(), 10), line}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"stream": map[string]string{"application": "demo"}, "values": values}}}})
			return
		}
		if r.URL.Path != "/loki/api/v1/query" {
			t.Errorf("unexpected route: %s", r.URL)
			return
		}
		grouped := strings.Contains(query, "sum by (aiops_pattern_template)")
		full := strings.Contains(query, "[1800000ms]")
		last := r.URL.Query().Get("time") == strconv.FormatInt(end.Add(-time.Nanosecond).UnixNano(), 10)
		rows := []any{}
		if full || last {
			metric := map[string]string{}
			if grouped {
				metric["aiops_pattern_template"] = "checkout <id>"
			}
			// The controlled source has 101 lines. Only the 100 lines with a
			// top-level level belong to the selected INFO query.
			count := 100
			if !strings.Contains(query, canonicalFilter) {
				count = 101
			}
			ns, _ := strconv.ParseInt(r.URL.Query().Get("time"), 10, 64)
			rows = append(rows, map[string]any{"metric": metric, "value": []any{float64(ns) / float64(time.Second), strconv.Itoa(count)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": rows}})
	}))
	defer server.Close()
	tm, err := NewTelemetry(TelemetryConfig{Loki: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"demo": {
		LokiLabels: map[string]string{"application": "demo"}, LokiPatternField: "message_template", LokiCompleteLookbackSeconds: 7200}}})
	if err != nil {
		t.Fatal(err)
	}
	tm.now = func() time.Time { return end }
	out, err := tm.QueryLogsWithPatterns(context.Background(), "demo", "loki", "checkout", "info", window, 100)
	if err != nil || out.PatternError != "" || out.Summary == nil || out.Summary.Total != 100 ||
		out.PatternSummary == nil || len(out.PatternSummary.Patterns) != 1 || out.PatternSummary.Patterns[0].Count != 100 || len(out.Entries) != 100 {
		t.Fatalf("nested unsampled level changed complete INFO result: total=%v patterns=%v entries=%d err=%v patternErr=%s", out.Summary, out.PatternSummary, len(out.Entries), err, out.PatternError)
	}
}

func TestLokiCanonicalLevelPreservesSupportedAliases(t *testing.T) {
	for _, test := range []struct {
		line  string
		level string
	}{
		{`{"level":"WARNING"}`, "warn"},
		{`{"level":"critical"}`, "fatal"},
		{`{"level":"panic"}`, "fatal"},
	} {
		got, err := canonicalLokiLevel(test.line)
		if err != nil || got != test.level {
			t.Fatalf("canonical level %q: got %q, %v", test.line, got, err)
		}
	}
	if !strings.Contains(lokiLevelFilter("warn"), "warn|warning") || !strings.Contains(lokiLevelFilter("fatal"), "fatal|critical|panic") {
		t.Fatal("Loki query lost registered severity aliases")
	}
}
