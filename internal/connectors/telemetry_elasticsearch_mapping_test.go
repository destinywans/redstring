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

func TestElasticsearchScopeMappingResolvesExactFieldsOrFailsClosed(t *testing.T) {
	mapping := func(fields map[string]map[string]any) any {
		entries := map[string]any{}
		for name, definition := range fields {
			leaf := name[strings.LastIndex(name, ".")+1:]
			entries[name] = map[string]any{"full_name": name, "mapping": map[string]any{leaf: definition}}
		}
		return map[string]any{"mappings": entries}
	}
	for _, tc := range []struct {
		name       string
		mapping    any
		queryField string
		wantSearch bool
		indexLimit string
	}{
		{name: "direct keyword", mapping: map[string]any{"lab-logs": mapping(map[string]map[string]any{"cluster": {"type": "keyword"}})}, queryField: "cluster", wantSearch: true},
		{name: "keyword multi field", mapping: map[string]any{"lab-logs": mapping(map[string]map[string]any{"cluster.keyword": {"type": "keyword", "ignore_above": 256}})}, queryField: "cluster.keyword", wantSearch: true},
		{name: "unmapped", mapping: map[string]any{"lab-logs": mapping(nil)}},
		{name: "text only", mapping: map[string]any{"lab-logs": mapping(map[string]map[string]any{"cluster": {"type": "text"}})}},
		{name: "mutable alias target", mapping: map[string]any{"backing-1": mapping(map[string]map[string]any{"cluster": {"type": "keyword"}})}},
		{name: "mixed backing indices", mapping: map[string]any{"logs-1": mapping(map[string]map[string]any{"cluster": {"type": "keyword"}}), "logs-2": mapping(map[string]map[string]any{"cluster.keyword": {"type": "keyword"}})}},
		{name: "value not indexed", mapping: map[string]any{"lab-logs": mapping(map[string]map[string]any{"cluster.keyword": {"type": "keyword", "ignore_above": 2}})}},
		{name: "normalized keyword", mapping: map[string]any{"lab-logs": mapping(map[string]map[string]any{"cluster": {"type": "keyword", "normalizer": "lowercase"}})}},
		{name: "index-level ignore_above", mapping: map[string]any{"lab-logs": mapping(map[string]map[string]any{"cluster": {"type": "keyword"}})}, indexLimit: "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			searches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/lab-logs/_mapping/field/cluster,cluster.keyword":
					if r.Method != http.MethodGet {
						t.Errorf("mapping request used %s", r.Method)
					}
					_ = json.NewEncoder(w).Encode(tc.mapping)
				case "/lab-logs/_settings/index.mapping.ignore_above":
					settings := map[string]any{}
					for index := range tc.mapping.(map[string]any) {
						value := map[string]any{}
						if tc.indexLimit != "" {
							value["index.mapping.ignore_above"] = tc.indexLimit
						}
						settings[index] = map[string]any{"settings": value}
					}
					_ = json.NewEncoder(w).Encode(settings)
				case "/lab-logs/_search":
					searches++
					var query map[string]any
					if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
						t.Error(err)
						return
					}
					filters := query["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
					if len(filters) != 2 || filters[1].(map[string]any)["term"].(map[string]any)[tc.queryField] != "lab" {
						t.Errorf("scope query did not use verified field %q: %+v", tc.queryField, filters)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{}}})
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
				}
			}))
			defer server.Close()
			tm, err := NewTelemetry(TelemetryConfig{Elasticsearch: TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"app": {ElasticsearchIndex: "lab-logs", ElasticsearchFilters: map[string]string{"cluster": "lab"}}}})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			_, err = tm.QueryLogsSearch(context.Background(), "app", "elasticsearch", "", TimeWindow{Start: now.Add(-time.Minute), End: now}, 10)
			if tc.wantSearch && (err != nil || searches != 1) {
				t.Fatalf("verified mapping failed: %v, searches=%d", err, searches)
			}
			if !tc.wantSearch && (err == nil || searches != 0) {
				t.Fatalf("unverified mapping reached search: %v, searches=%d", err, searches)
			}
		})
	}
}

func TestElasticsearchSeverityRuntimeIncludesOTelFallback(t *testing.T) {
	script := elasticsearchLevelRuntime()["aiops_level"].(map[string]any)["script"].(map[string]string)["source"]
	for _, part := range []string{"s.containsKey('log.level')", "s.containsKey('level')", "s.containsKey('severity_text')", "for(def candidate:values)", "v=='info'", "emit(v); break;"} {
		if !strings.Contains(script, part) {
			t.Fatalf("runtime severity normalization missing %q", part)
		}
	}
	if strings.Index(script, "s.containsKey('severity_text')") < strings.Index(script, "s.containsKey('level')") {
		t.Fatal("runtime severity priority differs from returned log entries")
	}
	for _, labels := range []map[string]string{{"severity_text": "INFO"}, {"log.level": "unknown", "severity_text": "INFO"}, {"log.level": "", "severity_text": "INFO"}} {
		if got := enrichLogEntry(LogEntry{Labels: labels}).Level; got != "info" {
			t.Fatalf("OpenTelemetry fallback lost for %+v: %q", labels, got)
		}
	}
}

func TestElasticsearchScopeKeysNameSourceFields(t *testing.T) {
	for _, scope := range []ApplicationTelemetry{
		{ElasticsearchIndex: "lab-logs", ElasticsearchFilters: map[string]string{"cluster.keyword": "lab"}},
		{ElasticsearchIndex: "lab-logs", ElasticsearchFilters: map[string]string{"cluster": "lab"}, ElasticsearchPrefixes: map[string]string{"pod.keyword": "order-"}},
	} {
		if _, err := NewTelemetry(TelemetryConfig{Elasticsearch: TelemetryEndpoint{URL: "https://example.invalid"}, Applications: map[string]ApplicationTelemetry{"app": scope}}); err == nil {
			t.Fatalf("accepted .keyword as a _source scope key: %+v", scope)
		}
	}
}
