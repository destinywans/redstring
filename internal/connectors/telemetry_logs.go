package connectors

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func ValidLogLevel(level string) bool {
	switch level {
	case "", "trace", "debug", "info", "warn", "error", "fatal":
		return true
	}
	return false
}
func normalizedLogLevel(level string) string {
	level = strings.ToLower(level)
	switch level {
	case "warning":
		level = "warn"
	case "critical", "panic":
		level = "fatal"
	}
	if !ValidLogLevel(level) {
		return ""
	}
	return level
}
func ValidTraceID(id string) bool {
	if len(id) != 32 || id == strings.Repeat("0", 32) {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func enrichLogEntry(entry LogEntry) LogEntry {
	for _, key := range []string{"log.level", "level", "severity_text"} {
		if entry.Level == "" {
			entry.Level = normalizedLogLevel(entry.Labels[key])
		}
	}
	for _, key := range []string{"trace.id", "trace_id"} {
		if ValidTraceID(entry.Labels[key]) {
			entry.TraceID = strings.ToLower(entry.Labels[key])
			break
		}
	}
	var fields map[string]any
	if json.Unmarshal([]byte(entry.Line), &fields) == nil {
		if entry.Level == "" {
			entry.Level = normalizedLogLevel(sourceField(fields, "level"))
		}
		if entry.TraceID == "" {
			for _, key := range []string{"trace_id", "trace.id"} {
				if id := sourceField(fields, key); ValidTraceID(id) {
					entry.TraceID = strings.ToLower(id)
					break
				}
			}
		}
	}
	return entry
}

// Summary is optional so existing evidence reads retain their original request budget.
func (t *Telemetry) QueryLogsOptions(ctx context.Context, appID, provider, search, level string, summary bool, w TimeWindow, limit int) (LogEvidence, error) {
	if len(search) > 256 || !ValidLogLevel(level) {
		return LogEvidence{}, &TelemetryError{Kind: "scope", Message: "Invalid log search or level"}
	}
	if provider == "elasticsearch" {
		return t.queryElasticsearchOptions(ctx, appID, w, limit, search, level, summary)
	}
	if provider != "" && provider != "loki" {
		return LogEvidence{}, &TelemetryError{Kind: "scope", Message: "Unknown registered log provider"}
	}
	out, err := t.queryLokiOptions(ctx, appID, w, limit, search, level)
	if err == nil && summary {
		out.Summary, err = t.lokiSummary(ctx, out.Source)
		if err != nil {
			out.SummaryError = "日志统计暂不可用，请重试"
			err = nil
		}
	}
	return out, err
}

// QueryLogsWithPatterns requests complete pattern counts in the same scoped
// Elasticsearch search as the exact total. Loki has no registered complete
// pattern producer, so nonempty Loki results remain explicitly incomplete.
func (t *Telemetry) QueryLogsWithPatterns(ctx context.Context, appID, provider, search, level string, w TimeWindow, limit int) (LogEvidence, error) {
	if len(search) > 256 || !ValidLogLevel(level) {
		return LogEvidence{}, &TelemetryError{Kind: "scope", Message: "Invalid log search or level"}
	}
	if provider == "elasticsearch" {
		return t.queryElasticsearchOptionsWithPatterns(ctx, appID, w, limit, search, level)
	}
	if provider != "" && provider != "loki" {
		return LogEvidence{}, &TelemetryError{Kind: "scope", Message: "Unknown registered log provider"}
	}
	out, err := t.QueryLogsOptions(ctx, appID, provider, search, level, true, w, limit)
	if err == nil && out.Summary != nil && out.SummaryError == "" && out.Summary.Total == 0 {
		out.PatternSummary = &LogPatternSummary{Patterns: []LogPattern{}}
	} else if err == nil {
		out.PatternError = "Complete log patterns are unavailable from this Loki source"
	}
	return out, err
}
func logBucketSeconds(w TimeWindow) int {
	return max(1, int(math.Ceil(w.End.Sub(w.Start).Seconds()/30)))
}

// Registered ECS fields take precedence. The JSON fallback covers structured
// application stdout without interpreting arbitrary user text as a query.
func elasticsearchLevelRuntime() map[string]any {
	return map[string]any{"aiops_level": map[string]any{"type": "keyword", "script": map[string]string{"source": `def s=params._source; def v=null; if(s.containsKey('log.level')) {v=s['log.level'];} else if(s.containsKey('log') && s.log instanceof Map) {v=s.log.level;} if(v==null && s.containsKey('level')) {v=s.level;} if(v==null && s.containsKey('message') && s.message instanceof String) {def m=/(?:^|[,{])\s*"level"\s*:\s*"([A-Za-z]+)"/.matcher(s.message); if(m.find()) {v=m.group(1);}} if(v!=null) {v=v.toString().toLowerCase(); if(v=='warning') {v='warn';} if(v=='critical'||v=='panic') {v='fatal';} emit(v);}`}}}
}
func elasticsearchLogAggregations(w TimeWindow) map[string]any {
	step := logBucketSeconds(w)
	return map[string]any{
		"volume": map[string]any{"date_histogram": map[string]any{"field": "@timestamp", "fixed_interval": fmt.Sprintf("%ds", step), "offset": fmt.Sprintf("%dms", w.Start.UnixMilli()%int64(step*1000)), "min_doc_count": 0, "extended_bounds": map[string]int64{"min": w.Start.UnixMilli(), "max": w.End.Add(-time.Millisecond).UnixMilli()}}},
		"errors": map[string]any{"filter": map[string]any{"terms": map[string]any{"aiops_level": []string{"error", "fatal"}}}},
	}
}
func elasticsearchPatternAggregation(w TimeWindow, keywordField string) map[string]any {
	result := map[string]any{"aggs": map[string]any{"trend": map[string]any{"date_histogram": elasticsearchLogAggregations(w)["volume"].(map[string]any)["date_histogram"]}}}
	if keywordField == "" {
		result["categorize_text"] = map[string]any{"field": "message", "size": 64, "shard_size": 128, "min_doc_count": 1}
	} else {
		result["terms"] = map[string]any{"field": keywordField, "size": 64, "shard_size": 128, "min_doc_count": 1,
			"order": map[string]string{"_count": "desc"}, "show_term_doc_count_error": true}
	}
	return result
}

// A category or term may be omitted by upstream size/shard_size limits. The
// registered keyword path additionally requires zero missing documents and
// zero term count errors. Both paths must conserve exact totals and buckets.
func decodeElasticsearchPatterns(raw []byte, w TimeWindow, summary *LogSummary, exactTerms bool) (*LogPatternSummary, error) {
	var data struct {
		TimedOut        *bool `json:"timed_out"`
		TerminatedEarly bool  `json:"terminated_early"`
		Shards          *struct {
			Total      int `json:"total"`
			Successful int `json:"successful"`
			Skipped    int `json:"skipped"`
			Failed     int `json:"failed"`
		} `json:"_shards"`
		Aggregations *struct {
			MissingPatterns *struct {
				Count int64 `json:"doc_count"`
			} `json:"missing_patterns"`
			Patterns *struct {
				SumOtherDocCount        *int64 `json:"sum_other_doc_count"`
				DocCountErrorUpperBound *int64 `json:"doc_count_error_upper_bound"`
				Buckets                 []struct {
					Key                     string `json:"key"`
					Count                   int64  `json:"doc_count"`
					DocCountErrorUpperBound *int64 `json:"doc_count_error_upper_bound"`
					Trend                   *struct {
						Buckets []struct {
							Key   int64 `json:"key"`
							Count int64 `json:"doc_count"`
						} `json:"buckets"`
					} `json:"trend"`
				} `json:"buckets"`
			} `json:"patterns"`
		} `json:"aggregations"`
	}
	if json.Unmarshal(raw, &data) != nil || data.TimedOut == nil || *data.TimedOut || data.TerminatedEarly ||
		data.Shards == nil || data.Shards.Total < 1 || data.Shards.Failed != 0 ||
		data.Shards.Successful+data.Shards.Skipped != data.Shards.Total ||
		data.Aggregations == nil || data.Aggregations.Patterns == nil || data.Aggregations.Patterns.Buckets == nil ||
		len(data.Aggregations.Patterns.Buckets) > 64 || summary == nil || summary.Total < 0 {
		return nil, errors.New("incomplete Elasticsearch pattern aggregation")
	}
	if exactTerms && (data.Aggregations.Patterns.SumOtherDocCount == nil || *data.Aggregations.Patterns.SumOtherDocCount != 0 ||
		data.Aggregations.Patterns.DocCountErrorUpperBound == nil || *data.Aggregations.Patterns.DocCountErrorUpperBound != 0 ||
		data.Aggregations.MissingPatterns == nil || data.Aggregations.MissingPatterns.Count != 0) {
		return nil, errors.New("approximate or truncated Elasticsearch pattern terms")
	}
	if w.Start.UnixNano()%int64(time.Millisecond) != 0 || w.End.UnixNano()%int64(time.Millisecond) != 0 {
		return nil, errors.New("pattern histogram requires millisecond timestamps")
	}
	stepMS := int64(logBucketSeconds(w)) * 1000
	durationMS := w.End.Sub(w.Start).Milliseconds()
	if durationMS <= 0 || stepMS <= 0 {
		return nil, errors.New("invalid pattern window")
	}
	wantBuckets := int((durationMS + stepMS - 1) / stepMS)
	if wantBuckets < 1 || wantBuckets > 32 {
		return nil, errors.New("pattern histogram exceeded bucket budget")
	}
	if len(summary.Buckets) != wantBuckets {
		return nil, errors.New("incomplete Elasticsearch volume histogram")
	}
	for i, bucket := range summary.Buckets {
		if bucket.Timestamp.UnixMilli() != w.Start.UnixMilli()+int64(i)*stepMS {
			return nil, errors.New("unaligned Elasticsearch volume histogram")
		}
	}
	out := &LogPatternSummary{Patterns: make([]LogPattern, 0, len(data.Aggregations.Patterns.Buckets))}
	seen := map[string]bool{}
	var categoryTotal int64
	trendTotals := make([]int64, wantBuckets)
	for _, category := range data.Aggregations.Patterns.Buckets {
		if exactTerms && (category.DocCountErrorUpperBound == nil || *category.DocCountErrorUpperBound != 0) {
			return nil, errors.New("approximate Elasticsearch pattern term count")
		}
		if strings.TrimSpace(category.Key) == "" || (exactTerms && len(category.Key) > 1024) || seen[category.Key] || category.Count < 1 || category.Count > summary.Total-categoryTotal ||
			category.Trend == nil || len(category.Trend.Buckets) != wantBuckets {
			return nil, errors.New("incomplete Elasticsearch pattern category")
		}
		seen[category.Key] = true
		pattern := LogPattern{Template: category.Key, Count: category.Count, Buckets: make([]LogBucket, 0, wantBuckets)}
		var trendTotal int64
		for i, bucket := range category.Trend.Buckets {
			if bucket.Key != w.Start.UnixMilli()+int64(i)*stepMS || bucket.Count < 0 || bucket.Count > category.Count-trendTotal {
				return nil, errors.New("inconsistent Elasticsearch pattern trend")
			}
			trendTotal += bucket.Count
			if bucket.Count > summary.Buckets[i].Count-trendTotals[i] {
				return nil, errors.New("inconsistent Elasticsearch pattern and volume histograms")
			}
			trendTotals[i] += bucket.Count
			pattern.Buckets = append(pattern.Buckets, LogBucket{Timestamp: time.UnixMilli(bucket.Key).UTC(), Count: bucket.Count})
		}
		if trendTotal != category.Count {
			return nil, errors.New("incomplete Elasticsearch pattern trend")
		}
		categoryTotal += category.Count
		out.Patterns = append(out.Patterns, pattern)
	}
	if categoryTotal != summary.Total {
		return nil, errors.New("Elasticsearch pattern categories do not cover exact total")
	}
	for i, count := range trendTotals {
		if count != summary.Buckets[i].Count {
			return nil, errors.New("Elasticsearch pattern trends do not cover volume histogram")
		}
	}
	return out, nil
}
func decodeElasticsearchSummary(raw []byte, w TimeWindow) (*LogSummary, error) {
	var data struct {
		Hits struct {
			Total *struct {
				Value    int64  `json:"value"`
				Relation string `json:"relation"`
			} `json:"total"`
		} `json:"hits"`
		Aggregations *struct {
			Volume struct {
				Buckets []struct {
					Key   int64 `json:"key"`
					Count int64 `json:"doc_count"`
				} `json:"buckets"`
			} `json:"volume"`
			Errors *struct {
				Count int64 `json:"doc_count"`
			} `json:"errors"`
		} `json:"aggregations"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Hits.Total == nil || data.Hits.Total.Relation != "eq" || data.Aggregations == nil || data.Aggregations.Errors == nil {
		return nil, errors.New("incomplete log aggregations")
	}
	out := &LogSummary{Total: data.Hits.Total.Value, ErrorCount: &data.Aggregations.Errors.Count, BucketSeconds: logBucketSeconds(w), Buckets: []LogBucket{}}
	if len(data.Aggregations.Volume.Buckets) > 32 {
		return nil, errors.New("histogram exceeded bucket budget")
	}
	var count int64
	for _, b := range data.Aggregations.Volume.Buckets {
		if b.Count < 0 {
			return nil, errors.New("invalid bucket")
		}
		count += b.Count
		out.Buckets = append(out.Buckets, LogBucket{Timestamp: time.UnixMilli(b.Key).UTC(), Count: b.Count})
	}
	if count != out.Total || *out.ErrorCount < 0 || *out.ErrorCount > out.Total {
		return nil, errors.New("inconsistent log summary")
	}
	return out, nil
}
func lokiLevelFilter(level string) string {
	pattern := level
	if level == "warn" {
		pattern = "warn|warning"
	}
	if level == "fatal" {
		pattern = "fatal|critical|panic"
	}
	// Only a JSON level field is used; arbitrary words in a message are not severity.
	return " |~ " + strconv.Quote(`(?i)(^|[,{])\s*"level"\s*:\s*"(`+pattern+`)"`)
}
func (t *Telemetry) lokiCount(ctx context.Context, source TelemetrySource, query string, at time.Time) (int64, error) {
	raw, err := t.loki.get(ctx, "/loki/api/v1/query", url.Values{"query": {query}, "time": {strconv.FormatInt(at.UnixNano(), 10)}}, &source)
	if err != nil {
		return 0, err
	}
	v, err := decodeTelemetry(raw, &source)
	if err != nil {
		return 0, err
	}
	if v.Data.ResultType != "vector" {
		return 0, errors.New("invalid Loki count type")
	}
	var result []struct {
		Value []json.RawMessage `json:"value"`
	}
	if json.Unmarshal(v.Data.Result, &result) != nil || len(result) > 1 {
		return 0, errors.New("invalid Loki count")
	}
	if len(result) == 0 {
		return 0, nil
	}
	if len(result[0].Value) != 2 {
		return 0, errors.New("invalid Loki count pair")
	}
	var value string
	if json.Unmarshal(result[0].Value[1], &value) != nil {
		return 0, errors.New("invalid Loki count value")
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f != math.Trunc(f) || f > float64(math.MaxInt64) {
		return 0, errors.New("invalid Loki count value")
	}
	return int64(f), nil
}
func (t *Telemetry) lokiSummary(ctx context.Context, s TelemetrySource) (*LogSummary, error) {
	window := s.Window.End.Sub(s.Window.Start)
	if window < time.Millisecond || window%time.Millisecond != 0 {
		return nil, errors.New("log summary requires millisecond window precision")
	}
	end := s.Window.End.Add(-time.Nanosecond)
	countQuery := func(selector string, d time.Duration) string {
		return "sum(count_over_time(" + selector + "[" + strconv.FormatInt(d.Milliseconds(), 10) + "ms]))"
	}
	total, err := t.lokiCount(ctx, s, countQuery(s.Query, window), end)
	if err != nil {
		return nil, err
	}
	errorsCount, err := t.lokiCount(ctx, s, countQuery(s.Query+" |~ "+strconv.Quote(`(?i)(^|[,{])\s*"level"\s*:\s*"(error|fatal|critical|panic)"`), window), end)
	if err != nil {
		return nil, err
	}
	step := logBucketSeconds(s.Window)
	width := time.Duration(step) * time.Second
	out := &LogSummary{Total: total, ErrorCount: &errorsCount, BucketSeconds: step, Buckets: []LogBucket{}}
	// Loki range queries align timestamps to the UTC step grid. Query only full
	// interior buckets, then count clipped edges separately so no outside-window
	// traffic is included in the histogram.
	firstBoundary := s.Window.Start.Truncate(width)
	if firstBoundary.Before(s.Window.Start) {
		firstBoundary = firstBoundary.Add(width)
	}
	lastBoundary := s.Window.End.Truncate(width)
	if firstBoundary.After(s.Window.End) {
		firstBoundary = s.Window.End
	}
	if firstBoundary.After(s.Window.Start) {
		d := firstBoundary.Sub(s.Window.Start)
		if d%time.Millisecond != 0 {
			return nil, errors.New("log histogram requires millisecond timestamps")
		}
		c, e := t.lokiCount(ctx, s, countQuery(s.Query, d), firstBoundary.Add(-time.Nanosecond))
		if e != nil {
			return nil, e
		}
		out.Buckets = append(out.Buckets, LogBucket{Timestamp: s.Window.Start, Count: c})
	}
	n := int(lastBoundary.Sub(firstBoundary) / width)
	if n > 0 {
		first := firstBoundary.Add(width)
		last := lastBoundary
		raw, e := t.loki.get(ctx, "/loki/api/v1/query_range", url.Values{"query": {countQuery(s.Query, width)}, "start": {strconv.FormatInt(first.UnixNano(), 10)}, "end": {strconv.FormatInt(last.UnixNano(), 10)}, "step": {strconv.Itoa(step)}}, &s)
		if e != nil {
			return nil, e
		}
		v, e := decodeTelemetry(raw, &s)
		if e != nil {
			return nil, e
		}
		if v.Data.ResultType != "matrix" {
			return nil, errors.New("invalid Loki histogram")
		}
		var rows []struct {
			Values [][]json.RawMessage `json:"values"`
		}
		if json.Unmarshal(v.Data.Result, &rows) != nil || len(rows) > 1 {
			return nil, errors.New("invalid Loki histogram")
		}
		values := make(map[int]int64)
		for _, row := range rows {
			if len(row.Values) > 34 {
				return nil, errors.New("histogram exceeded budget")
			}
			for _, pair := range row.Values {
				if len(pair) != 2 {
					return nil, errors.New("invalid Loki histogram pair")
				}
				var ts float64
				var str string
				if json.Unmarshal(pair[0], &ts) != nil || json.Unmarshal(pair[1], &str) != nil {
					return nil, errors.New("invalid Loki histogram values")
				}
				if ts < float64(first.Unix()) || ts > float64(last.Unix()) {
					continue
				}
				f, e := strconv.ParseFloat(str, 64)
				index := int(math.Round((ts - float64(first.Unix())) / float64(step)))
				if e != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f != math.Trunc(f) || f > float64(math.MaxInt64) || index < 0 || index >= n {
					return nil, errors.New("invalid Loki histogram bucket")
				}
				values[index] = int64(f)
			}
		}
		for i := 0; i < n; i++ {
			out.Buckets = append(out.Buckets, LogBucket{Timestamp: firstBoundary.Add(time.Duration(i) * width), Count: values[i]})
		}
	}
	var counted int64
	for _, b := range out.Buckets {
		counted += b.Count
	}
	if counted > total || errorsCount > total {
		return nil, errors.New("inconsistent Loki summary")
	}
	if lastBoundary.Before(s.Window.End) && lastBoundary.After(s.Window.Start) {
		out.Buckets = append(out.Buckets, LogBucket{Timestamp: lastBoundary, Count: total - counted})
	} else if counted != total {
		return nil, errors.New("incomplete Loki summary")
	}

	return out, nil
}
