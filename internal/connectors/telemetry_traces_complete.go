package connectors

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CompleteTrace is the trace as stored by Tempo, accepted only when every
// returned resource belongs to one of the selected registered scopes and the
// span tree is closed. It does not claim that every operation was instrumented.
type CompleteTrace struct {
	ID               string
	Spans            []TraceSpan
	MatchedResources []string
}

// CompleteTraceQuery is deliberately small. A search that cannot exhaust its
// bounded window is an error, never a population count inferred from a sample.
type CompleteTraceQuery struct {
	Resources       []string
	SearchResources []string
	Window          TimeWindow
	TraceID         string
	Filter          string
}

type completeTraceRegistration struct {
	resource string
	config   ApplicationTelemetry
}

func tempoSelectorsOverlap(a, b ApplicationTelemetry) bool {
	if a.TempoService == "" || b.TempoService == "" || a.TempoService != b.TempoService {
		return false
	}
	for key, value := range a.TempoAttributes {
		if other, ok := b.TempoAttributes[key]; ok && other != value {
			return false
		}
	}
	return true
}

const (
	maxCompleteTraceSearches = 64
	maxCompleteTraceIDs      = 40
	completeTraceSearchLimit = 20
	maxCompleteTraceSpans    = 500
)

const tracePredicate = `[A-Za-z_][A-Za-z0-9_.:]*\s*(=|!=|>=|<=|>|<)\s*("[A-Za-z0-9_./: -]{1,120}"|[A-Za-z0-9_.:-]{1,120})`

var traceSelector = regexp.MustCompile(`^\{\s*(` + tracePredicate + `(\s*(&&|\|\|)\s*` + tracePredicate + `)*)?\s*\}$`)
var safeTraceRoute = regexp.MustCompile(`^/[A-Za-z0-9_./:{}-]{1,100}$`)
var safeTraceOperationConfig = regexp.MustCompile(`^[A-Za-z0-9_./:{} -]{1,120}$`)

// ValidTraceSelector accepts a small, bounded TraceQL predicate subset. It
// excludes pipelines, unions, regular expressions and nested span selectors.
func ValidTraceSelector(value string) bool {
	return value == "" || len(value) <= 512 && traceSelector.MatchString(value)
}

func completeTraceOperation(span otelSpan, allowed []string) string {
	approved := map[string]bool{}
	for _, operation := range allowed {
		approved[operation] = true
	}
	method, route := "", ""
	for _, attr := range span.Attributes {
		switch attr.Key {
		case "http.method", "http.request.method":
			value := traceAttributeValue(attr)
			switch value {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
				method = value
			}
		case "http.route":
			value := traceAttributeValue(attr)
			if safeTraceRoute.MatchString(value) && !strings.Contains(value, "//") {
				route = value
			}
		}
	}
	if route != "" {
		if method != "" && approved[method+" "+route] {
			return method + " " + route
		}
		if approved[route] {
			return route
		}
	}
	if approved[span.Name] {
		return span.Name
	}
	return "operation withheld"
}

// TraceObservation is the result of a bounded search whose population may or
// may not be attested. Every trace in it passed the same closed-tree checks.
type TraceObservation struct {
	Traces []CompleteTrace
	// PopulationComplete is true only when the deployment attests that Tempo
	// search discovers every stored trace (tempo_search_fail_closed).
	PopulationComplete bool
}

// QueryCompleteTraces returns a complete, bounded search population or an
// error. Tempo's search selects the first N matches; reaching N requires a
// narrower query. Every selected trace is then read without start/end, since
// Tempo documents that time-bounded trace-by-ID queries can be partial.
func (t *Telemetry) QueryCompleteTraces(ctx context.Context, q CompleteTraceQuery) ([]CompleteTrace, error) {
	return t.queryStoredTraces(ctx, q, true)
}

// QueryObservedTraces runs the same bounded search and closed-tree reads
// without requiring the search completeness attestation. Callers must not
// derive totals, facets or distributions unless PopulationComplete is true.
func (t *Telemetry) QueryObservedTraces(ctx context.Context, q CompleteTraceQuery) (TraceObservation, error) {
	traces, err := t.queryStoredTraces(ctx, q, false)
	if err != nil {
		return TraceObservation{}, err
	}
	return TraceObservation{Traces: traces, PopulationComplete: t.config.TempoSearchFailClosed}, nil
}

func (t *Telemetry) queryStoredTraces(ctx context.Context, q CompleteTraceQuery, requireAttested bool) ([]CompleteTrace, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	source := TelemetrySource{Provider: "tempo", CapturedAt: t.now().UTC()}
	if t.tempo == nil {
		return nil, failure(&source, "configuration", "Tempo is not configured", nil)
	}
	if len(q.Resources) == 0 || len(q.Resources) > 6 {
		return nil, failure(&source, "scope", "Select one to six registered trace scopes", nil)
	}
	if q.TraceID != "" && !ValidTraceID(q.TraceID) {
		return nil, failure(&source, "scope", "Trace ID must contain 32 hexadecimal characters", nil)
	}
	if !ValidTraceSelector(q.Filter) {
		return nil, failure(&source, "scope", "Trace filter must be one bounded TraceQL span selector", nil)
	}
	var err error
	if q.TraceID == "" {
		if requireAttested && !t.config.TempoSearchFailClosed {
			return nil, failure(&source, "configuration", "Tempo search completeness is not verified for this deployment", nil)
		}
		source.Window, err = t.window(q.Window, false)
		if err != nil {
			return nil, failure(&source, "scope", err.Error(), err)
		}
	}
	ids := map[string]bool{}
	registrations := make([]completeTraceRegistration, 0, len(q.Resources))
	seenResources := map[string]bool{}
	searches := 0
	for _, resource := range q.Resources {
		if seenResources[resource] {
			continue
		}
		seenResources[resource] = true
		registration, ok := t.config.Applications[resource]
		if !ok || registration.TempoService == "" || len(registration.TempoAttributes) == 0 {
			return nil, failure(&source, "configuration", "Trace resource lacks a registered service and environment scope", nil)
		}
		registrations = append(registrations, completeTraceRegistration{resource: resource, config: registration})
	}
	for _, selected := range registrations {
		for resource, other := range t.config.Applications {
			if resource != selected.resource && tempoSelectorsOverlap(selected.config, other) {
				return nil, failure(&source, "configuration", "Registered Tempo selectors overlap across resources", nil)
			}
		}
	}
	searchResources := q.SearchResources
	if len(searchResources) == 0 {
		searchResources = q.Resources
	}
	selected := map[string]bool{}
	for _, resource := range searchResources {
		if !seenResources[resource] {
			return nil, failure(&source, "scope", "Search resource is not authorized for raw trace reads", nil)
		}
		selected[resource] = true
		if q.TraceID != "" {
			continue
		}
		registration := t.config.Applications[resource]
		clauses := []string{"resource.service.name = " + strconv.Quote(registration.TempoService)}
		keys := make([]string, 0, len(registration.TempoAttributes))
		for key := range registration.TempoAttributes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			clauses = append(clauses, "resource."+key+" = "+strconv.Quote(registration.TempoAttributes[key]))
		}
		selector := "{ " + strings.Join(clauses, " && ") + " }"
		if q.Filter != "" {
			selector += " && " + q.Filter
		}
		var search func(int64, int64) error
		search = func(start, end int64) error {
			searches++
			if searches > maxCompleteTraceSearches {
				return failure(&source, "limit", "Trace window requires too many search requests", nil)
			}
			source.Query = selector
			source.URL = strings.TrimRight(t.tempo.config.URL, "/") + "/api/search"
			params := url.Values{"q": {selector}, "start": {strconv.FormatInt(start, 10)}, "end": {strconv.FormatInt(end, 10)}, "limit": {strconv.Itoa(completeTraceSearchLimit)}}
			raw, err := t.tempo.get(ctx, "/api/search", params, &source)
			if err != nil {
				return err
			}
			var response struct {
				Traces []struct {
					ID string `json:"traceID"`
				} `json:"traces"`
			}
			if json.Unmarshal(raw, &response) != nil || len(response.Traces) > completeTraceSearchLimit {
				return failure(&source, "protocol", "Invalid Tempo search response", nil)
			}
			if len(response.Traces) == completeTraceSearchLimit {
				if end-start <= 1 {
					return failure(&source, "limit", "Trace population exceeds the smallest searchable window", nil)
				}
				mid := start + (end-start)/2
				if err := search(start, mid); err != nil {
					return err
				}
				return search(mid, end)
			}
			for _, item := range response.Traces {
				id := searchTraceID(item.ID)
				if id == "" {
					return failure(&source, "protocol", "Invalid Tempo trace ID", nil)
				}
				ids[id] = true
				if len(ids) > maxCompleteTraceIDs {
					return failure(&source, "limit", "Trace population exceeds the bounded result limit", nil)
				}
			}
			return nil
		}
		if err := search(source.Window.Start.Unix(), source.Window.End.Unix()); err != nil {
			return nil, err
		}
	}
	if q.TraceID != "" {
		ids[strings.ToLower(q.TraceID)] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	traces := make([]CompleteTrace, 0, len(ordered))
	for _, id := range ordered {
		source.URL = strings.TrimRight(t.tempo.config.URL, "/") + "/api/traces/" + id
		raw, err := t.tempo.get(ctx, "/api/traces/"+id, nil, &source)
		if err != nil {
			if q.TraceID != "" && source.HTTPStatus == 404 {
				return []CompleteTrace{}, nil
			}
			return nil, err
		}
		trace, err := projectCompleteTrace(&source, id, raw, registrations)
		if err != nil {
			return nil, err
		}
		matchedSelected := false
		for _, resource := range trace.MatchedResources {
			if selected[resource] {
				matchedSelected = true
				break
			}
		}
		if !matchedSelected {
			return nil, failure(&source, "scope", "Trace does not contain the selected service", nil)
		}
		traces = append(traces, trace)
	}
	return traces, nil
}

func projectCompleteTrace(source *TelemetrySource, id string, raw []byte, registrations []completeTraceRegistration) (CompleteTrace, error) {
	var data struct {
		Batches       []otelBatch `json:"batches"`
		ResourceSpans []otelBatch `json:"resourceSpans"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return CompleteTrace{}, failure(source, "protocol", "Invalid Tempo trace response", nil)
	}
	out := CompleteTrace{ID: id, Spans: []TraceSpan{}, MatchedResources: []string{}}
	seen := map[string]bool{}
	matchedResources := map[string]bool{}
	for _, batch := range append(data.Batches, data.ResourceSpans...) {
		attrs := map[string]string{}
		for _, attr := range batch.Resource.Attributes {
			attrs[attr.Key] = traceAttributeValue(attr)
		}
		service := attrs["service.name"]
		matched := false
		var safeOperations []string
		for _, registration := range registrations {
			if service != registration.config.TempoService {
				continue
			}
			matches := true
			for key, value := range registration.config.TempoAttributes {
				if attrs[key] != value {
					matches = false
					break
				}
			}
			if matches {
				matched = true
				matchedResources[registration.resource] = true
				safeOperations = registration.config.TempoSafeOperations
			}
		}
		version := attrs["service.version"]
		if !safeTraceValue.MatchString(version) {
			version = ""
		}
		for _, scope := range append(batch.Scopes, batch.LegacyScopes...) {
			for _, sp := range scope.Spans {
				if !matched {
					return CompleteTrace{}, failure(source, "scope", "Trace includes a resource outside the authorized registration", nil)
				}
				sid, tid, pid := spanID(sp.SpanID, 8), spanID(sp.TraceID, 16), spanID(sp.ParentID, 8)
				if sid == "" || tid != id || (sp.ParentID != "" && pid == "") || seen[sid] {
					return CompleteTrace{}, failure(source, "protocol", "Invalid or repeated Tempo span identity", nil)
				}
				seen[sid] = true
				start, e1 := strconv.ParseInt(sp.Start, 10, 64)
				end, e2 := strconv.ParseInt(sp.End, 10, 64)
				if e1 != nil || e2 != nil || end < start {
					return CompleteTrace{}, failure(source, "protocol", "Invalid Tempo span timing", nil)
				}
				if len(out.Spans) == maxCompleteTraceSpans {
					return CompleteTrace{}, failure(source, "limit", "Trace exceeds the bounded span limit", nil)
				}
				out.Spans = append(out.Spans, TraceSpan{TraceID: id, SpanID: sid, ParentSpanID: pid,
					Service: service, Version: version, Name: completeTraceOperation(sp, safeOperations), Start: time.Unix(0, start).UTC(),
					DurationMS: float64(end-start) / 1e6,
					Error:      string(sp.Status.Code) == "2" || string(sp.Status.Code) == `"STATUS_CODE_ERROR"`,
					Attributes: projectTraceAttributes(sp.Attributes), Events: projectTraceEvents(sp.Events, start, end)})
			}
		}
	}
	if len(out.Spans) == 0 {
		return CompleteTrace{}, failure(source, "protocol", "Tempo returned an empty trace", nil)
	}
	for resource := range matchedResources {
		out.MatchedResources = append(out.MatchedResources, resource)
	}
	sort.Strings(out.MatchedResources)
	roots := 0
	for _, span := range out.Spans {
		if span.ParentSpanID == "" {
			roots++
		} else if !seen[span.ParentSpanID] {
			return CompleteTrace{}, failure(source, "partial", "Tempo trace is missing a parent span", nil)
		}
	}
	if roots != 1 {
		return CompleteTrace{}, failure(source, "partial", "Tempo trace does not contain one root span", nil)
	}
	byID := map[string]TraceSpan{}
	for _, span := range out.Spans {
		byID[span.SpanID] = span
	}
	visited := map[string]bool{}
	var check func(string, map[string]bool) bool
	check = func(id string, path map[string]bool) bool {
		if path[id] {
			return false
		}
		if visited[id] {
			return true
		}
		path[id] = true
		parent := byID[id].ParentSpanID
		if parent != "" && !check(parent, path) {
			return false
		}
		delete(path, id)
		visited[id] = true
		return true
	}
	for id := range byID {
		if !check(id, map[string]bool{}) {
			return CompleteTrace{}, failure(source, "partial", "Tempo trace contains a cyclic parent graph", nil)
		}
	}
	return out, nil
}
