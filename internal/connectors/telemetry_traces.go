package connectors

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type TraceSpan struct {
	TraceID      string           `json:"trace_id"`
	SpanID       string           `json:"span_id"`
	ParentSpanID string           `json:"parent_span_id,omitempty"`
	Service      string           `json:"service"`
	Version      string           `json:"version,omitempty"`
	Name         string           `json:"name"`
	Start        time.Time        `json:"start"`
	DurationMS   float64          `json:"duration_ms"`
	Error        bool             `json:"error"`
	Attributes   []TraceAttribute `json:"attributes,omitempty"`
	Events       []TraceEvent     `json:"events,omitempty"`
}
type TraceAttribute struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type TraceEvent struct {
	At   time.Time `json:"at"`
	Name string    `json:"name"`
	Text string    `json:"text,omitempty"`
}
type TraceEvidence struct {
	Source       TelemetrySource `json:"source"`
	Spans        []TraceSpan     `json:"spans"`
	TraceCount   int             `json:"trace_count"`
	Empty        bool            `json:"empty"`
	LimitReached bool            `json:"limit_reached"`
}
type otelAttribute struct {
	Key   string `json:"key"`
	Value struct {
		String string          `json:"stringValue"`
		Int    json.RawMessage `json:"intValue"`
		Bool   *bool           `json:"boolValue"`
	} `json:"value"`
}
type otelEvent struct {
	Name       string          `json:"name"`
	Time       string          `json:"timeUnixNano"`
	Attributes []otelAttribute `json:"attributes"`
}
type otelSpan struct {
	TraceID  string `json:"traceId"`
	SpanID   string `json:"spanId"`
	ParentID string `json:"parentSpanId"`
	Name     string `json:"name"`
	Start    string `json:"startTimeUnixNano"`
	End      string `json:"endTimeUnixNano"`
	Status   struct {
		Code json.RawMessage `json:"code"`
	} `json:"status"`
	Attributes []otelAttribute `json:"attributes"`
	Events     []otelEvent     `json:"events"`
}
type otelScope struct {
	Spans []otelSpan `json:"spans"`
}
type otelBatch struct {
	Resource struct {
		Attributes []otelAttribute `json:"attributes"`
	} `json:"resource"`
	Scopes       []otelScope `json:"scopeSpans"`
	LegacyScopes []otelScope `json:"instrumentationLibrarySpans"`
}

// Trace attributes can include URLs, SQL, headers and user data. Only a small
// set of low-cardinality diagnostic values is suitable for this shared view.
var safeTraceValue = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,120}$`)
var safeTraceKeys = map[string]bool{
	"http.method": true, "http.request.method": true, "http.status_code": true,
	"http.response.status_code": true, "db.system": true, "rpc.system": true,
	"messaging.system": true,
}

func traceAttributeValue(attr otelAttribute) string {
	if attr.Value.String != "" {
		return attr.Value.String
	}
	if len(attr.Value.Int) != 0 {
		var value string
		if json.Unmarshal(attr.Value.Int, &value) != nil {
			value = string(attr.Value.Int)
		}
		return value
	}
	if attr.Value.Bool != nil {
		return strconv.FormatBool(*attr.Value.Bool)
	}
	return ""
}

func projectTraceAttributes(attrs []otelAttribute) []TraceAttribute {
	out := []TraceAttribute{}
	seen := map[string]bool{}
	for _, attr := range attrs {
		if !safeTraceKeys[attr.Key] || seen[attr.Key] {
			continue
		}
		value := traceAttributeValue(attr)
		if !safeTraceValue.MatchString(value) {
			continue
		}
		out = append(out, TraceAttribute{Key: attr.Key, Value: value})
		seen[attr.Key] = true
		if len(out) == 8 {
			break
		}
	}
	return out
}

func projectTraceEvents(events []otelEvent, start, end int64) []TraceEvent {
	out := []TraceEvent{}
	for _, event := range events {
		if event.Name != "exception" && event.Name != "error" {
			continue
		}
		at, err := strconv.ParseInt(event.Time, 10, 64)
		if err != nil || at < start || at > end {
			continue
		}
		item := TraceEvent{At: time.Unix(0, at).UTC(), Name: event.Name}
		for _, attr := range event.Attributes {
			if attr.Key == "exception.type" {
				if value := traceAttributeValue(attr); safeTraceValue.MatchString(value) {
					item.Text = value
				}
				break
			}
		}
		out = append(out, item)
		if len(out) == 8 {
			break
		}
	}
	return out
}

func spanID(value string, size int) string {
	if b, e := hex.DecodeString(value); e == nil && len(b) == size {
		return strings.ToLower(value)
	}
	if b, e := base64.StdEncoding.DecodeString(value); e == nil && len(b) == size {
		return hex.EncodeToString(b)
	}
	return ""
}

// Tempo 2.7 search omits leading zeroes in hex trace IDs; OTLP spans retain all 16 bytes.
func searchTraceID(value string) string {
	if value == "" || len(value) > 32 {
		return ""
	}
	padded := strings.Repeat("0", 32-len(value)) + value
	if _, err := hex.DecodeString(padded); err != nil {
		return ""
	}
	return strings.ToLower(padded)
}
func (t *Telemetry) QueryTraces(ctx context.Context, appID string, w TimeWindow, limit int) (TraceEvidence, error) {
	results, err := t.QueryTracesBatch(ctx, []string{appID}, w, limit)
	out := results[appID]
	if err != nil {
		return out, err
	}
	if out.Source.Failure != nil {
		return out, out.Source.Failure
	}
	return out, nil
}

// Search each selected scope, then share a bounded, round-robin trace sample.
// Each trace is fetched once and only explicitly selected registered resources
// can receive its spans. At most 6 search + 20 detail HTTP requests per batch.
func (t *Telemetry) QueryTracesBatch(ctx context.Context, appIDs []string, w TimeWindow, limit int) (map[string]TraceEvidence, error) {
	return t.QueryTracesBatchID(ctx, appIDs, w, limit, "")
}

func (t *Telemetry) QueryTracesBatchID(ctx context.Context, appIDs []string, w TimeWindow, limit int, traceID string) (map[string]TraceEvidence, error) {
	if traceID != "" && !ValidTraceID(traceID) {
		return nil, &TelemetryError{Kind: "scope", Message: "Trace ID must contain 32 hexadecimal characters"}
	}
	traceID = strings.ToLower(traceID)
	results := map[string]TraceEvidence{}
	if limit == 0 {
		limit = 10
	}
	if len(appIDs) < 1 || len(appIDs) > 6 || limit < 1 || limit > 20 {
		return results, &TelemetryError{Kind: "scope", Message: "Select at most six trace scopes and a search limit of 1 to 20"}
	}
	candidates := map[string][]string{}
	order := []string{}
	for _, appID := range appIDs {
		if _, exists := results[appID]; exists {
			continue
		}
		out := TraceEvidence{Source: TelemetrySource{Provider: "tempo", ApplicationID: appID, CapturedAt: t.now().UTC()}, Spans: []TraceSpan{}, Empty: true}
		s := &out.Source
		a, ok := t.config.Applications[appID]
		if !ok || a.TempoService == "" || len(a.TempoAttributes) == 0 {
			failure(s, "scope", "Application has no registered Tempo service and environment scope", nil)
			results[appID] = out
			continue
		}
		if t.tempo == nil {
			failure(s, "configuration", "Tempo is not configured", nil)
			results[appID] = out
			continue
		}
		var err error
		s.Window, err = t.window(w, false)
		if err != nil {
			failure(s, "scope", err.Error(), err)
			results[appID] = out
			continue
		}
		clauses := []string{"resource.service.name = " + strconv.Quote(a.TempoService)}
		keys := []string{}
		for k := range a.TempoAttributes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			clauses = append(clauses, "resource."+k+" = "+strconv.Quote(a.TempoAttributes[k]))
		}
		s.Query = "{ " + strings.Join(clauses, " && ") + " }"
		s.URL = strings.TrimRight(t.tempo.config.URL, "/") + "/api/search"
		s.Warnings = []string{"各选中范围共同发现并共享最多20条Trace；仅返回当前应用与环境授权范围内的span，不代表完整链路或全量流量。"}
		if traceID != "" {
			s.Query = "trace_id=" + traceID
			s.Warnings = []string{"仅返回所选应用与环境范围内、且在所选时间内的Span。"}
			results[appID] = out
			candidates[appID] = []string{traceID}
			order = append(order, appID)
			continue
		}
		params := url.Values{"q": {s.Query}, "start": {strconv.FormatInt(s.Window.Start.Unix(), 10)}, "end": {strconv.FormatInt(s.Window.End.Unix(), 10)}, "limit": {strconv.Itoa(limit)}}
		raw, err := t.tempo.get(ctx, "/api/search", params, s)
		if err != nil {
			results[appID] = out
			continue
		}
		var found struct {
			Traces []struct {
				ID string `json:"traceID"`
			} `json:"traces"`
		}
		if err = json.Unmarshal(raw, &found); err != nil {
			failure(s, "protocol", "Invalid Tempo search response", err)
			results[appID] = out
			continue
		}
		if len(found.Traces) > limit {
			failure(s, "limit", "Tempo returned more traces than requested", nil)
			results[appID] = out
			continue
		}
		out.LimitReached = len(found.Traces) == limit
		for _, trace := range found.Traces {
			id := searchTraceID(trace.ID)
			if id == "" {
				failure(s, "protocol", "Invalid Tempo trace ID", nil)
				break
			}
			candidates[appID] = append(candidates[appID], id)
		}
		results[appID] = out
		if out.Source.Failure == nil {
			order = append(order, appID)
		}
	}
	ids := []string{}
	seen := map[string]bool{}
	capped := false
	for index := 0; index < limit; index++ {
		for _, appID := range order {
			if index >= len(candidates[appID]) {
				continue
			}
			id := candidates[appID][index]
			if seen[id] {
				continue
			}
			seen[id] = true
			if len(ids) >= 20 {
				capped = true
				continue
			}
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		// HTTP provenance used here is not returned as a broader scope registration.
		var source TelemetrySource
		for _, appID := range order {
			source = results[appID].Source
			break
		}
		raw, err := t.tempo.get(ctx, "/api/traces/"+id, url.Values{"start": {strconv.FormatInt(source.Window.Start.Unix(), 10)}, "end": {strconv.FormatInt(source.Window.End.Unix(), 10)}}, &source)
		var data struct {
			Batches       []otelBatch `json:"batches"`
			ResourceSpans []otelBatch `json:"resourceSpans"`
		}
		if err == nil {
			if decodeErr := json.Unmarshal(raw, &data); decodeErr != nil {
				err = failure(&source, "protocol", "Invalid Tempo trace response", decodeErr)
			}
		}
		if err != nil && traceID != "" && source.HTTPStatus == 404 {
			continue
		}
		if err != nil {
			for _, appID := range order {
				out := results[appID]
				out.Source.HTTPStatus = source.HTTPStatus
				failure(&out.Source, "partial", "Shared trace sample could not be fully read; narrow the window or retry", err)
				results[appID] = out
			}
			break
		}
		for _, appID := range order {
			out := results[appID]
			if out.Source.Failure != nil {
				continue
			}
			out.Source.HTTPStatus = source.HTTPStatus
			if err = appendTraceSpans(&out, t.config.Applications[appID], id, append(data.Batches, data.ResourceSpans...)); err != nil {
				results[appID] = out
				continue
			}
			out.LimitReached = out.LimitReached || capped
			results[appID] = out
		}
	}
	for appID, out := range results {
		sort.SliceStable(out.Spans, func(i, j int) bool { return out.Spans[i].Start.Before(out.Spans[j].Start) })
		out.Empty = len(out.Spans) == 0
		results[appID] = out
	}
	return results, nil
}

func appendTraceSpans(out *TraceEvidence, a ApplicationTelemetry, id string, batches []otelBatch) error {
	s := &out.Source
	countBefore := len(out.Spans)
	for _, batch := range batches {
		attrs := map[string]string{}
		for _, v := range batch.Resource.Attributes {
			attrs[v.Key] = v.Value.String
		}
		if attrs["service.name"] != a.TempoService {
			continue
		}
		matches := true
		for k, v := range a.TempoAttributes {
			if attrs[k] != v {
				matches = false
			}
		}
		if !matches {
			continue
		}
		for _, scope := range append(batch.Scopes, batch.LegacyScopes...) {
			for _, sp := range scope.Spans {
				sid := spanID(sp.SpanID, 8)
				tid := spanID(sp.TraceID, 16)
				pid := spanID(sp.ParentID, 8)
				if tid != id || sid == "" {
					return failure(s, "protocol", "Tempo span identity mismatch", nil)
				}
				start, e1 := strconv.ParseInt(sp.Start, 10, 64)
				end, e2 := strconv.ParseInt(sp.End, 10, 64)
				if e1 != nil || e2 != nil || end < start {
					return failure(s, "protocol", "Invalid span timing", nil)
				}
				at := time.Unix(0, start).UTC()
				if at.Before(s.Window.Start) || !at.Before(s.Window.End) {
					continue
				}
				if len(out.Spans) >= 500 {
					out.LimitReached = true
					continue
				}
				out.Spans = append(out.Spans, TraceSpan{TraceID: tid, SpanID: sid, ParentSpanID: pid, Service: a.TempoService, Name: sp.Name, Start: at, DurationMS: float64(end-start) / 1e6, Error: string(sp.Status.Code) == "2" || string(sp.Status.Code) == `"STATUS_CODE_ERROR"`, Attributes: projectTraceAttributes(sp.Attributes), Events: projectTraceEvents(sp.Events, start, end)})
			}
		}
	}
	if len(out.Spans) > countBefore {
		out.TraceCount++
	}
	return nil
}
