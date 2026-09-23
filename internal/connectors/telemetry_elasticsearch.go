package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var telemetryField = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)
var telemetryIndex = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

func validateExtendedTelemetry(c TelemetryConfig) error {
	for _, a := range c.Applications {
		if a.ElasticsearchIndex != "" && (!telemetryIndex.MatchString(a.ElasticsearchIndex) || len(a.ElasticsearchFilters) == 0) {
			return errors.New("Elasticsearch requires a literal index and exact application scope filters")
		}
		for _, m := range []map[string]string{a.ElasticsearchFilters, a.ElasticsearchPrefixes, a.TempoAttributes} {
			for k, v := range m {
				if !telemetryField.MatchString(k) || v == "" || len(v) > 1024 {
					return errors.New("invalid registered telemetry field scope")
				}
			}
		}
		if a.TempoService != "" && (len(a.TempoService) > 256 || len(a.TempoAttributes) == 0) {
			return errors.New("Tempo requires service and environment resource attribute scope")
		}
	}
	return nil
}
func (t *Telemetry) QueryLogsSearch(ctx context.Context, appID, provider, search string, w TimeWindow, limit int) (LogEvidence, error) {
	if len(search) > 256 {
		return LogEvidence{}, &TelemetryError{Kind: "scope", Message: "Search is limited to 256 bytes"}
	}
	if provider == "elasticsearch" {
		return t.queryElasticsearch(ctx, appID, w, limit, search)
	}
	if provider != "loki" && provider != "" {
		return LogEvidence{}, &TelemetryError{Kind: "scope", Message: "Unknown registered log provider"}
	}
	return t.queryLoki(ctx, appID, w, limit, search)
}
func sourceField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	parts := strings.Split(key, ".")
	var v any = m
	for _, p := range parts {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[p]
	}
	s, _ := v.(string)
	return s
}
func (t *Telemetry) queryElasticsearch(ctx context.Context, appID string, w TimeWindow, limit int, search string) (LogEvidence, error) {
	return t.queryElasticsearchOptions(ctx, appID, w, limit, search, "", false)
}
func (t *Telemetry) queryElasticsearchOptions(ctx context.Context, appID string, w TimeWindow, limit int, search, level string, summary bool) (LogEvidence, error) {
	out := LogEvidence{Source: TelemetrySource{Provider: "elasticsearch", ApplicationID: appID, CapturedAt: t.now().UTC()}, Entries: []LogEntry{}, Empty: true}
	s := &out.Source
	a, ok := t.config.Applications[appID]
	if !ok || a.ElasticsearchIndex == "" || len(a.ElasticsearchFilters) == 0 {
		return out, failure(s, "scope", "Application has no registered Elasticsearch scope", nil)
	}
	if t.elasticsearch == nil {
		return out, failure(s, "configuration", "Elasticsearch is not configured", nil)
	}
	var err error
	s.Window, err = t.window(w, false)
	if err != nil {
		return out, failure(s, "scope", err.Error(), err)
	}
	if limit == 0 {
		limit = min(100, t.config.MaxLogEntries)
	}
	if limit < 1 || limit > t.config.MaxLogEntries {
		return out, failure(s, "scope", "Log limit exceeds configured maximum", nil)
	}
	filters := []any{map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": s.Window.Start, "lt": s.Window.End}}}}
	for k, v := range a.ElasticsearchFilters {
		filters = append(filters, map[string]any{"term": map[string]string{k + ".keyword": v}})
	}
	for k, v := range a.ElasticsearchPrefixes {
		filters = append(filters, map[string]any{"prefix": map[string]string{k + ".keyword": v}})
	}
	if level != "" {
		filters = append(filters, map[string]any{"term": map[string]string{"aiops_level": level}})
	}
	query := map[string]any{"bool": map[string]any{"filter": filters}}
	if search != "" {
		query["bool"].(map[string]any)["must"] = []any{map[string]any{"match_phrase": map[string]string{"message": search}}}
	}
	request := map[string]any{"size": limit, "timeout": "10s", "track_total_hits": summary, "query": query, "sort": []any{map[string]string{"@timestamp": "desc"}}}
	if level != "" || summary {
		request["runtime_mappings"] = elasticsearchLevelRuntime()
	}
	if summary {
		request["aggs"] = elasticsearchLogAggregations(s.Window)
	}
	body, _ := json.Marshal(request)
	s.Query = string(body)
	path := "/" + url.PathEscape(a.ElasticsearchIndex) + "/_search"
	s.URL = strings.TrimRight(t.elasticsearch.config.URL, "/") + path
	raw, err := t.elasticsearch.request(ctx, "POST", path, body, s)
	if err != nil {
		return out, err
	}
	var data struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Hits struct {
			Hits []struct {
				Source map[string]any `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		return out, failure(s, "protocol", "Invalid Elasticsearch response", err)
	}
	if data.TimedOut || data.Shards.Failed > 0 {
		return out, failure(s, "partial", "Elasticsearch query timed out or has failed shards; narrow the window", nil)
	}
	if len(data.Hits.Hits) > limit {
		return out, failure(s, "limit", "Elasticsearch exceeded the query limit", nil)
	}
	for _, hit := range data.Hits.Hits {
		for k, v := range a.ElasticsearchFilters {
			if sourceField(hit.Source, k) != v {
				return out, failure(s, "scope", "Elasticsearch returned a document outside registered scope", nil)
			}
		}
		for k, v := range a.ElasticsearchPrefixes {
			if !strings.HasPrefix(sourceField(hit.Source, k), v) {
				return out, failure(s, "scope", "Elasticsearch returned a document outside registered scope", nil)
			}
		}
		at, e := time.Parse(time.RFC3339Nano, sourceField(hit.Source, "@timestamp"))
		if e != nil || at.Before(s.Window.Start) || !at.Before(s.Window.End) {
			return out, failure(s, "scope", "Elasticsearch returned a document outside requested time", e)
		}
		labels := map[string]string{}
		for _, k := range []string{"kubernetes.pod.name", "kubernetes.namespace", "orchestrator.cluster.name", "log.iostream", "log.level"} {
			if v := sourceField(hit.Source, k); v != "" {
				labels[k] = v
			}
		}
		for _, k := range []string{"trace.id", "trace_id", "level", "severity_text"} {
			if v := sourceField(hit.Source, k); v != "" {
				labels[k] = v
			}
		}
		out.Entries = append(out.Entries, enrichLogEntry(LogEntry{Timestamp: at, Line: sourceField(hit.Source, "message"), Labels: labels}))
	}
	sort.SliceStable(out.Entries, func(i, j int) bool { return out.Entries[i].Timestamp.Before(out.Entries[j].Timestamp) })
	if summary {
		out.Summary, err = decodeElasticsearchSummary(raw, s.Window)
		if err != nil {
			out.SummaryError = "日志统计暂不可用，请重试"
		}
	}
	out.Empty = len(out.Entries) == 0
	out.LimitReached = len(out.Entries) == limit
	return out, nil
}
