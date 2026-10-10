package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var telemetryField = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)
var telemetryIndex = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

const elasticsearchTemplateField = "message_template.keyword"

func validateExtendedTelemetry(c TelemetryConfig) error {
	for _, a := range c.Applications {
		if a.ElasticsearchIndex != "" && (!telemetryIndex.MatchString(a.ElasticsearchIndex) || len(a.ElasticsearchFilters) == 0) {
			return errors.New("Elasticsearch requires a literal index and exact application scope filters")
		}
		if a.ElasticsearchPatternField != "" && (a.ElasticsearchIndex == "" || a.ElasticsearchPatternField != elasticsearchTemplateField) {
			return errors.New("Elasticsearch pattern field must be the dedicated message_template.keyword field on a scoped index")
		}
		for _, m := range []map[string]string{a.ElasticsearchFilters, a.ElasticsearchPrefixes} {
			for k := range m {
				if strings.HasSuffix(k, ".keyword") {
					return errors.New("Elasticsearch scope keys must name source fields, not .keyword multi-fields")
				}
			}
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
		for k := range a.TempoAttributes {
			// The search adds "resource." itself; a prefixed key would query
			// resource.resource.<key> and silently match nothing (D19).
			if strings.HasPrefix(k, "resource.") || strings.HasPrefix(k, "span.") {
				return errors.New("Tempo attribute keys name the resource attribute without a resource. or span. prefix")
			}
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
	return t.queryElasticsearchOptionsInternal(ctx, appID, w, limit, search, level, summary, false)
}
func (t *Telemetry) queryElasticsearchOptionsWithPatterns(ctx context.Context, appID string, w TimeWindow, limit int, search, level string) (LogEvidence, error) {
	return t.queryElasticsearchOptionsInternal(ctx, appID, w, limit, search, level, true, true)
}
func (t *Telemetry) queryElasticsearchOptionsInternal(ctx context.Context, appID string, w TimeWindow, limit int, search, level string, summary, patterns bool) (LogEvidence, error) {
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
	scopeFields, err := t.elasticsearchScopeFields(ctx, a, s)
	if err != nil {
		return out, err
	}
	filters := []any{map[string]any{"range": map[string]any{"@timestamp": map[string]any{"gte": s.Window.Start, "lt": s.Window.End}}}}
	for k, v := range a.ElasticsearchFilters {
		filters = append(filters, map[string]any{"term": map[string]string{scopeFields[k]: v}})
	}
	for k, v := range a.ElasticsearchPrefixes {
		filters = append(filters, map[string]any{"prefix": map[string]string{scopeFields[k]: v}})
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
	if patterns {
		request["aggs"].(map[string]any)["patterns"] = elasticsearchPatternAggregation(s.Window, a.ElasticsearchPatternField)
		if a.ElasticsearchPatternField != "" {
			request["aggs"].(map[string]any)["missing_patterns"] = map[string]any{"missing": map[string]string{"field": a.ElasticsearchPatternField}}
		}
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
	if patterns {
		if out.Summary == nil {
			out.PatternError = "Exact log total is unavailable"
		} else {
			out.PatternSummary, err = decodeElasticsearchPatterns(raw, s.Window, out.Summary, a.ElasticsearchPatternField != "")
			if err != nil {
				out.PatternError = "Complete log patterns are unavailable"
			}
		}
	}
	out.Empty = len(out.Entries) == 0
	out.LimitReached = len(out.Entries) == limit
	return out, nil
}

// A registered source field is not necessarily a text field with a .keyword
// multi-field. Resolve the exact searchable field on every concrete index so
// an unmapped term query cannot silently produce a complete-looking zero.
func (t *Telemetry) elasticsearchScopeFields(ctx context.Context, a ApplicationTelemetry, source *TelemetrySource) (map[string]string, error) {
	values := map[string]int{}
	for k, v := range a.ElasticsearchFilters {
		values[k] = utf8.RuneCountInString(v)
	}
	for k, v := range a.ElasticsearchPrefixes {
		values[k] = max(values[k], utf8.RuneCountInString(v))
	}
	fields := make([]string, 0, len(values)*2)
	for k := range values {
		fields = append(fields, k, k+".keyword")
	}
	sort.Strings(fields)
	path := "/" + url.PathEscape(a.ElasticsearchIndex) + "/_mapping/field/" + strings.Join(fields, ",")
	source.URL = strings.TrimRight(t.elasticsearch.config.URL, "/") + path
	raw, err := t.elasticsearch.request(ctx, "GET", path, nil, source)
	if err != nil {
		return nil, err
	}
	var indices map[string]struct {
		Mappings map[string]struct {
			FullName string                     `json:"full_name"`
			Mapping  map[string]json.RawMessage `json:"mapping"`
		} `json:"mappings"`
	}
	if json.Unmarshal(raw, &indices) != nil || len(indices) != 1 {
		return nil, failure(source, "protocol", "Invalid Elasticsearch field mappings", nil)
	}
	if _, ok := indices[a.ElasticsearchIndex]; !ok {
		return nil, failure(source, "configuration", "Elasticsearch scope requires one concrete index, not a mutable alias or data stream", nil)
	}
	settingsPath := "/" + url.PathEscape(a.ElasticsearchIndex) + "/_settings/index.mapping.ignore_above?flat_settings=true"
	source.URL = strings.TrimRight(t.elasticsearch.config.URL, "/") + settingsPath
	settingsRaw, err := t.elasticsearch.request(ctx, "GET", settingsPath, nil, source)
	if err != nil {
		return nil, err
	}
	var settings map[string]struct {
		Settings map[string]string `json:"settings"`
	}
	if json.Unmarshal(settingsRaw, &settings) != nil || len(settings) != len(indices) {
		return nil, failure(source, "protocol", "Invalid Elasticsearch index settings", nil)
	}
	resolved := map[string]string{}
	for indexName, index := range indices {
		indexSettings, ok := settings[indexName]
		if !ok || indexSettings.Settings == nil {
			return nil, failure(source, "protocol", "Elasticsearch index settings do not cover scope mappings", nil)
		}
		indexIgnoreAbove := 0
		if value, ok := indexSettings.Settings["index.mapping.ignore_above"]; ok {
			indexIgnoreAbove, err = strconv.Atoi(value)
			if err != nil || indexIgnoreAbove < 1 {
				return nil, failure(source, "protocol", "Invalid Elasticsearch index ignore_above setting", err)
			}
		}
		for k, valueLength := range values {
			chosen := ""
			for _, candidate := range []string{k, k + ".keyword"} {
				field, ok := index.Mappings[candidate]
				if !ok || field.FullName != candidate {
					continue
				}
				leaf := candidate[strings.LastIndex(candidate, ".")+1:]
				var definition struct {
					Type        string `json:"type"`
					Index       *bool  `json:"index"`
					IgnoreAbove *int   `json:"ignore_above"`
					Normalizer  string `json:"normalizer"`
				}
				if json.Unmarshal(field.Mapping[leaf], &definition) != nil || definition.Type != "keyword" ||
					(definition.Index != nil && !*definition.Index) || definition.Normalizer != "" ||
					(definition.IgnoreAbove != nil && *definition.IgnoreAbove < valueLength) ||
					(definition.IgnoreAbove == nil && indexIgnoreAbove != 0 && indexIgnoreAbove < valueLength) {
					continue
				}
				chosen = candidate
				break
			}
			if chosen == "" || (resolved[k] != "" && resolved[k] != chosen) {
				return nil, failure(source, "configuration", "Elasticsearch scope field mapping is missing or inconsistent", nil)
			}
			resolved[k] = chosen
		}
	}
	return resolved, nil
}
