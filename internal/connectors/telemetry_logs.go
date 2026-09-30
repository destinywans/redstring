package connectors

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"sort"
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
func canonicalLokiLevel(line string) (string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &fields) != nil || fields == nil {
		return "", errors.New("Loki line is not a JSON object")
	}
	var value string
	if json.Unmarshal(fields["level"], &value) != nil {
		return "", errors.New("Loki line lacks a top-level string level")
	}
	level := normalizedLogLevel(value)
	if level == "" {
		return "", errors.New("Loki line has an unknown top-level level")
	}
	return level, nil
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

// QueryLogsWithPatterns only returns patterns when the registered source can
// account for the whole requested window, independently of the bounded lines.
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
	out, err := t.queryLokiOptions(ctx, appID, w, limit, search, level)
	if err == nil {
		if len(out.Source.Warnings) != 0 {
			out.PatternError = "Complete Loki log counts are unavailable"
		} else if patterns, summary, patternErr := t.lokiCompletePatterns(ctx, appID, out.Source, level); patternErr != nil {
			out.PatternError = "Complete Loki log patterns are unavailable: " + patternErr.Error()
		} else if int64(len(out.Entries)) > summary.Total {
			out.PatternError = "Loki log sample exceeds complete count"
		} else {
			out.Summary = summary
			out.SummaryError = ""
			out.PatternSummary = patterns
		}
	}
	return out, err
}

const lokiTemplateLabel = "aiops_pattern_template"

type lokiPatternValue struct {
	Metric map[string]string `json:"metric"`
	Value  []json.RawMessage `json:"value"`
}

// An instant count_over_time at end-1ns covers [start,end) with no edge
// residual. Every requested bucket is counted explicitly; no bucket is
// calculated by subtracting other counts from the whole-window total.
func lokiCountAt(query string, start, end time.Time) (string, time.Time, error) {
	window := end.Sub(start)
	if window < time.Millisecond || window%time.Millisecond != 0 || start.UnixNano()%int64(time.Millisecond) != 0 || end.UnixNano()%int64(time.Millisecond) != 0 {
		return "", time.Time{}, errors.New("Loki pattern counts require millisecond window precision")
	}
	return "sum(count_over_time(" + query + "[" + strconv.FormatInt(window.Milliseconds(), 10) + "ms]))", end.Add(-time.Nanosecond), nil
}

func lokiIntegerValue(raw []json.RawMessage, at time.Time) (int64, error) {
	if len(raw) != 2 {
		return 0, errors.New("invalid Loki vector value")
	}
	var sampleTime float64
	numeric := strings.TrimSpace(string(raw[0]))
	if len(numeric) > 64 || json.Unmarshal(raw[0], &sampleTime) != nil || math.IsNaN(sampleTime) || math.IsInf(sampleTime, 0) {
		return 0, errors.New("Loki vector evaluation time does not match requested instant")
	}
	// Bound rational parsing, including tiny exponents that float64 would
	// silently underflow to zero, before allocating the exact decimal value.
	if i := strings.IndexAny(numeric, "eE"); i >= 0 {
		exponent, err := strconv.ParseInt(numeric[i+1:], 10, 16)
		if err != nil || exponent < -4096 || exponent > 4096 {
			return 0, errors.New("Loki vector evaluation time does not match requested instant")
		}
	}
	seconds, ok := new(big.Rat).SetString(numeric)
	if !ok {
		return 0, errors.New("Loki vector evaluation time does not match requested instant")
	}
	// Loki truncates vector evaluation timestamps to milliseconds. Compare
	// exact decimal nanoseconds: epoch float subtraction can reject a valid
	// end-1ns timestamp as 1.000166ms apart, or accept a truly >1ms sample.
	nanoseconds := seconds.Mul(seconds, new(big.Rat).SetInt64(int64(time.Second)))
	delta := nanoseconds.Sub(nanoseconds, new(big.Rat).SetInt64(at.UnixNano()))
	if delta.Abs(delta).Cmp(new(big.Rat).SetInt64(int64(time.Millisecond))) > 0 {
		return 0, errors.New("Loki vector evaluation time does not match requested instant")
	}
	var str string
	if json.Unmarshal(raw[1], &str) != nil {
		return 0, errors.New("invalid Loki vector value")
	}
	// Counts are JSON strings. Parse decimal integers directly, rather than
	// accepting a rounded floating-point representation of a large count.
	count, err := strconv.ParseInt(str, 10, 64)
	if err != nil || count < 0 {
		return 0, errors.New("noninteger Loki vector count")
	}
	return count, nil
}

func (t *Telemetry) lokiStrictVector(ctx context.Context, source TelemetrySource, query string, at time.Time) ([]lokiPatternValue, error) {
	local := source
	local.Warnings = nil
	raw, err := t.loki.get(ctx, "/loki/api/v1/query", url.Values{"query": {query}, "time": {strconv.FormatInt(at.UnixNano(), 10)}}, &local)
	if err != nil {
		return nil, err
	}
	v, err := decodeTelemetry(raw, &local)
	if err != nil {
		return nil, err
	}
	if len(local.Warnings) != 0 || v.Data.ResultType != "vector" || len(v.Data.Result) == 0 || string(v.Data.Result) == "null" {
		return nil, errors.New("incomplete Loki vector response")
	}
	var rows []lokiPatternValue
	if json.Unmarshal(v.Data.Result, &rows) != nil || len(rows) > 64 {
		return nil, errors.New("Loki pattern cardinality exceeds budget")
	}
	return rows, nil
}

func (t *Telemetry) lokiExactCount(ctx context.Context, source TelemetrySource, selector string, start, end time.Time) (int64, error) {
	query, at, err := lokiCountAt(selector, start, end)
	if err != nil {
		return 0, err
	}
	rows, err := t.lokiStrictVector(ctx, source, query, at)
	if err != nil || len(rows) > 1 {
		return 0, errors.New("incomplete Loki exact count")
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if len(rows[0].Metric) != 0 {
		return 0, errors.New("Loki exact count has unexpected labels")
	}
	return lokiIntegerValue(rows[0].Value, at)
}

func (t *Telemetry) lokiPatternCounts(ctx context.Context, source TelemetrySource, start, end time.Time) (map[string]int64, error) {
	_, at, err := lokiCountAt(source.Query, start, end)
	if err != nil {
		return nil, err
	}
	// Drop a pre-existing stream label before the named JSON extraction so
	// source labels cannot impersonate the trusted per-line template field.
	pipeline := source.Query + " | drop " + lokiTemplateLabel + " | json " + lokiTemplateLabel + `="message_template"` + " | __error__=\"\""
	query := "sum by (" + lokiTemplateLabel + ") (count_over_time(" + pipeline + "[" + strconv.FormatInt(end.Sub(start).Milliseconds(), 10) + "ms]))"
	rows, err := t.lokiStrictVector(ctx, source, query, at)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, row := range rows {
		if len(row.Metric) != 1 {
			return nil, errors.New("Loki template group has unexpected labels")
		}
		template, ok := row.Metric[lokiTemplateLabel]
		if !ok || strings.TrimSpace(template) == "" || len(template) > 1024 {
			return nil, errors.New("Loki template is missing or too long")
		}
		count, err := lokiIntegerValue(row.Value, at)
		if err != nil || count == 0 || counts[template] != 0 {
			return nil, errors.New("invalid or duplicate Loki template count")
		}
		counts[template] = count
	}
	return counts, nil
}

func (t *Telemetry) lokiCompletePatterns(ctx context.Context, appID string, source TelemetrySource, level string) (*LogPatternSummary, *LogSummary, error) {
	app := t.config.Applications[appID]
	if app.LokiPatternField != "message_template" || app.LokiCompleteLookbackSeconds == 0 {
		return nil, nil, errors.New("trusted Loki template source is not registered")
	}
	// Up to 34 sequential bounded HTTP requests can follow this check. Keep
	// their worst-case timeout inside the trusted source coverage horizon so
	// a late request cannot be silently shortened by max_query_lookback.
	coverageMargin := time.Duration(34*t.config.TimeoutSeconds+5) * time.Second
	if t.now().UTC().Sub(source.Window.Start)+coverageMargin > time.Duration(app.LokiCompleteLookbackSeconds)*time.Second {
		return nil, nil, errors.New("requested window exceeds registered Loki query lookback and retention")
	}
	width := time.Duration(logBucketSeconds(source.Window)) * time.Second
	if width <= 0 || source.Window.End.Sub(source.Window.Start) > 32*width {
		return nil, nil, errors.New("Loki histogram exceeds bucket budget")
	}
	total, err := t.lokiExactCount(ctx, source, source.Query, source.Window.Start, source.Window.End)
	if err != nil {
		return nil, nil, err
	}
	whole, err := t.lokiPatternCounts(ctx, source, source.Window.Start, source.Window.End)
	if err != nil {
		return nil, nil, err
	}
	var covered int64
	for _, count := range whole {
		if count > total-covered {
			return nil, nil, errors.New("Loki pattern total exceeds exact count")
		}
		covered += count
	}
	if covered != total {
		return nil, nil, errors.New("Loki patterns do not cover the exact total")
	}
	keys := make([]string, 0, len(whole))
	for key := range whole {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	patterns := &LogPatternSummary{Patterns: make([]LogPattern, 0, len(keys))}
	for _, key := range keys {
		patterns.Patterns = append(patterns.Patterns, LogPattern{Template: key, Count: whole[key], Buckets: []LogBucket{}})
	}
	summary := &LogSummary{Total: total, BucketSeconds: int(width / time.Second), Buckets: []LogBucket{}}
	var bucketTotal int64
	for start := source.Window.Start; start.Before(source.Window.End); start = start.Add(width) {
		end := start.Add(width)
		if end.After(source.Window.End) {
			end = source.Window.End
		}
		groups, err := t.lokiPatternCounts(ctx, source, start, end)
		if err != nil {
			return nil, nil, err
		}
		var bucketCount int64
		for i := range patterns.Patterns {
			pattern := &patterns.Patterns[i]
			count := groups[pattern.Template]
			if count > pattern.Count || count > total-bucketCount {
				return nil, nil, errors.New("Loki bucket exceeds exact pattern count")
			}
			bucketCount += count
			pattern.Buckets = append(pattern.Buckets, LogBucket{Timestamp: start, Count: count})
			delete(groups, pattern.Template)
		}
		if len(groups) != 0 || bucketCount > total-bucketTotal {
			return nil, nil, errors.New("Loki bucket has unregistered template or exceeds exact total")
		}
		bucketTotal += bucketCount
		summary.Buckets = append(summary.Buckets, LogBucket{Timestamp: start, Count: bucketCount})
	}
	if bucketTotal != total || len(summary.Buckets) > 32 {
		return nil, nil, errors.New("Loki histogram does not cover exact total")
	}
	for _, pattern := range patterns.Patterns {
		var sum int64
		for _, bucket := range pattern.Buckets {
			sum += bucket.Count
		}
		if sum != pattern.Count {
			return nil, nil, errors.New("Loki template buckets do not cover whole-window count")
		}
	}
	if level == "" {
		errorsQuery := source.Query + lokiCanonicalLevelFilter("error|fatal|critical|panic")
		count, err := t.lokiExactCount(ctx, source, errorsQuery, source.Window.Start, source.Window.End)
		if err != nil || count > total {
			return nil, nil, errors.New("incomplete Loki error count")
		}
		summary.ErrorCount = &count
	} else {
		count := int64(0)
		if level == "error" || level == "fatal" {
			count = total
		}
		summary.ErrorCount = &count
	}
	return patterns, summary, nil
}
func logBucketSeconds(w TimeWindow) int {
	return max(1, int(math.Ceil(w.End.Sub(w.Start).Seconds()/30)))
}

// Registered ECS fields take precedence. The JSON fallback covers structured
// application stdout without interpreting arbitrary user text as a query.
func elasticsearchLevelRuntime() map[string]any {
	return map[string]any{"aiops_level": map[string]any{"type": "keyword", "script": map[string]string{"source": `def s=params._source; def values=new ArrayList(); if(s.containsKey('log.level') && s['log.level'] instanceof String) {values.add(s['log.level']);} else if(s.containsKey('log') && s.log instanceof Map) {values.add(s.log.level);} if(s.containsKey('level')) {values.add(s.level);} if(s.containsKey('severity_text')) {values.add(s.severity_text);} if(s.containsKey('message') && s.message instanceof String) {def m=/^\s*\{[^{}]*"level"\s*:\s*"([A-Za-z]+)"/.matcher(s.message); if(m.find()) {values.add(m.group(1));}} for(def candidate:values) {if(!(candidate instanceof String)) {continue;} def v=candidate.toLowerCase(); if(v=='warning') {v='warn';} if(v=='critical'||v=='panic') {v='fatal';} if(v=='trace'||v=='debug'||v=='info'||v=='warn'||v=='error'||v=='fatal') {emit(v); break;}}`}}}
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
	return lokiCanonicalLevelFilter(pattern)
}
func lokiCanonicalLevelFilter(pattern string) string {
	// Named extraction reads only the top-level JSON level. Drop a stream label
	// with the same name first so it cannot override the parsed log value.
	return " | drop aiops_level | json aiops_level=\"level\" | __error__=\"\" | aiops_level=~" + strconv.Quote("(?i)^("+pattern+")$")
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
	errorsCount, err := t.lokiCount(ctx, s, countQuery(s.Query+lokiCanonicalLevelFilter("error|fatal|critical|panic"), window), end)
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
