// Package v3traces projects bounded, complete Tempo reads into the V3 trace
// contracts. A sampled or partially authorized trace is never a 200 response.
package v3traces

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

type TraceRow struct {
	ID            string   `json:"id"`
	Time          string   `json:"time"`
	Op            string   `json:"op"`
	Services      []string `json:"services"`
	DurationMS    float64  `json:"duration_ms"`
	Spans         int      `json:"spans"`
	Errors        int      `json:"errors"`
	Href          string   `json:"href"`
	Version       string   `json:"-"`
	Agent         string   `json:"agent,omitempty"`
	AgentID       string   `json:"agent_id,omitempty"`
	Model         string   `json:"model,omitempty"`
	Tokens        string   `json:"tokens,omitempty"`
	Status        string   `json:"status,omitempty"`
	ApplicationID string   `json:"application_id,omitempty"`
	EnvironmentID string   `json:"environment_id,omitempty"`
	Missing       []string `json:"missing,omitempty"`
}

type TraceFacet struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Items []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	} `json:"items"`
}

type TraceList struct {
	Scope     string            `json:"scope"`
	Total     int               `json:"total"`
	Took      string            `json:"took"`
	Colors    map[string]string `json:"colors"`
	Facets    []TraceFacet      `json:"facets"`
	Views     []TraceViewOption `json:"views"`
	Histogram struct {
		Counts [][2]int `json:"counts"`
		Axis   []string `json:"axis"`
	} `json:"histogram"`
	Scatter          []TraceScatter `json:"scatter"`
	Items            []TraceRow     `json:"items"`
	Source           string         `json:"source,omitempty"`
	AggregationScope string         `json:"aggregation_scope,omitempty"`
	WindowStartAt    string         `json:"window_start_at,omitempty"`
	WindowEndAt      string         `json:"window_end_at,omitempty"`
	ExcludedRunning  *int           `json:"excluded_running,omitempty"`
	Partial          bool           `json:"partial,omitempty"`
	Missing          []string       `json:"missing,omitempty"`
	HistogramStates  []string       `json:"histogram_non_success_states,omitempty"`
}

type TraceViewOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Count int    `json:"count"`
	Query string `json:"query"`
}

type TraceScatter struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Error bool    `json:"error"`
	ID    string  `json:"id"`
}

type TraceSpanView struct {
	ID       string              `json:"id"`
	Depth    int                 `json:"depth"`
	Service  string              `json:"service"`
	Op       string              `json:"op"`
	Start    float64             `json:"start"`
	End      float64             `json:"end"`
	Status   string              `json:"status"`
	Critical bool                `json:"critical"`
	Attrs    []map[string]any    `json:"attrs"`
	Events   []map[string]string `json:"events"`
}

type TraceGraphNode struct {
	ID     string `json:"id"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Sub    string `json:"sub"`
	Danger bool   `json:"danger,omitempty"`
}

type TraceGraphEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label"`
	Danger bool   `json:"danger,omitempty"`
}

type TraceView struct {
	ID         string            `json:"id"`
	TraceID    string            `json:"trace_id"`
	Op         string            `json:"op"`
	DurationMS float64           `json:"duration_ms"`
	Time       string            `json:"time"`
	Version    string            `json:"version"`
	Colors     map[string]string `json:"colors"`
	Spans      []TraceSpanView   `json:"spans"`
	FlameNote  string            `json:"flame_note"`
	Graph      struct {
		Nodes []TraceGraphNode `json:"nodes"`
		Edges []TraceGraphEdge `json:"edges"`
	} `json:"graph"`
}

var palette = []string{"#4577C8", "#12A784", "#B879E3", "#D88747", "#6D8B55", "#B35A72"}

func traceColors(traces []connectors.CompleteTrace) map[string]string {
	set := map[string]bool{}
	for _, trace := range traces {
		for _, span := range trace.Spans {
			set[span.Service] = true
		}
	}
	services := make([]string, 0, len(set))
	for service := range set {
		services = append(services, service)
	}
	sort.Strings(services)
	colors := map[string]string{}
	for i, service := range services {
		colors[service] = palette[i%len(palette)]
	}
	return colors
}

func traceRoot(trace connectors.CompleteTrace) (connectors.TraceSpan, time.Time, time.Time) {
	root := trace.Spans[0]
	start := root.Start
	end := root.Start.Add(time.Duration(root.DurationMS * float64(time.Millisecond)))
	for _, span := range trace.Spans {
		if span.ParentSpanID == "" {
			root = span
		}
		if span.Start.Before(start) {
			start = span.Start
		}
		last := span.Start.Add(time.Duration(span.DurationMS * float64(time.Millisecond)))
		if last.After(end) {
			end = last
		}
	}
	return root, start, end
}

func rowFor(trace connectors.CompleteTrace, serviceFilter string) TraceRow {
	root, start, end := traceRoot(trace)
	seen := map[string]bool{}
	services := []string{}
	errors := 0
	for _, span := range trace.Spans {
		if !seen[span.Service] {
			services = append(services, span.Service)
			seen[span.Service] = true
		}
		if span.Error {
			errors++
		}
	}
	sort.Strings(services)
	href := "/traces/" + trace.ID
	if serviceFilter != "" {
		href += "?service=" + url.QueryEscape(serviceFilter)
	}
	version := root.Version
	if version == "" {
		version = "unknown"
	}
	return TraceRow{ID: trace.ID, Time: start.UTC().Format(time.RFC3339Nano), Op: root.Name,
		Services: services, DurationMS: end.Sub(start).Seconds() * 1000, Spans: len(trace.Spans), Errors: errors,
		Href: href, Version: version}
}

func facet(key, label string, counts map[string]int) TraceFacet {
	result := TraceFacet{Key: key, Label: label}
	result.Items = make([]struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}, 0, len(counts))
	for value, count := range counts {
		result.Items = append(result.Items, struct {
			Value string `json:"value"`
			Count int    `json:"count"`
		}{value, count})
	}
	sort.Slice(result.Items, func(i, j int) bool {
		if result.Items[i].Count != result.Items[j].Count {
			return result.Items[i].Count > result.Items[j].Count
		}
		return result.Items[i].Value < result.Items[j].Value
	})
	return result
}

// BuildList counts every accepted trace in the complete bounded search set.
// A view filters that set; counts and charts all refer to the same population.
func BuildList(traces []connectors.CompleteTrace, window connectors.TimeWindow, view, service string, took time.Duration) TraceList {
	base := make([]TraceRow, 0, len(traces))
	for _, trace := range traces {
		base = append(base, rowFor(trace, service))
	}
	result := TraceList{Scope: "app", Took: fmt.Sprintf("%d ms", took.Milliseconds()),
		Colors: traceColors(traces), Facets: []TraceFacet{}, Scatter: []TraceScatter{}, Items: []TraceRow{}}
	errorCount, slowCount := 0, 0
	for _, row := range base {
		if row.Errors > 0 {
			errorCount++
		}
		if row.DurationMS >= 1000 {
			slowCount++
		}
	}
	result.Views = []TraceViewOption{{ID: "all", Label: "All", Count: len(base), Query: ""},
		{ID: "errors", Label: "Errors", Count: errorCount, Query: "{ status = error }"},
		{ID: "slow", Label: "Slow", Count: slowCount, Query: "{ trace:duration >= 1s }"}}
	for _, row := range base {
		if view == "errors" && row.Errors == 0 || view == "slow" && row.DurationMS < 1000 {
			continue
		}
		result.Items = append(result.Items, row)
	}
	sort.Slice(result.Items, func(i, j int) bool {
		if result.Items[i].Time != result.Items[j].Time {
			return result.Items[i].Time > result.Items[j].Time
		}
		return result.Items[i].ID < result.Items[j].ID
	})
	result.Total = len(result.Items)
	services, statuses, operations, versions := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	result.Histogram.Counts = make([][2]int, 12)
	result.Histogram.Axis = make([]string, 12)
	start, end := window.Start.UTC(), window.End.UTC()
	width := end.Sub(start)
	if width <= 0 {
		width = time.Hour
	}
	for i := range result.Histogram.Axis {
		result.Histogram.Axis[i] = start.Add(time.Duration(i) * width / 12).Format("15:04")
	}
	maxDuration := 1.0
	for _, row := range result.Items {
		maxDuration = math.Max(maxDuration, row.DurationMS)
	}
	for _, row := range result.Items {
		for _, service := range row.Services {
			services[service]++
		}
		status := "ok"
		if row.Errors > 0 {
			status = "error"
		}
		statuses[status]++
		operations[row.Op]++
		versions[row.Version]++
		at, _ := time.Parse(time.RFC3339Nano, row.Time)
		fraction := float64(at.Sub(start)) / float64(width)
		fraction = math.Max(0, math.Min(0.999999, fraction))
		bucket := int(fraction * 12)
		if row.Errors > 0 {
			result.Histogram.Counts[bucket][1]++
		} else {
			result.Histogram.Counts[bucket][0]++
		}
		result.Scatter = append(result.Scatter, TraceScatter{X: 4 + 432*fraction,
			Y: 105 - 100*row.DurationMS/maxDuration, Error: row.Errors > 0, ID: row.ID})
	}
	result.Facets = []TraceFacet{facet("service", "Service", services), facet("status", "Status", statuses),
		facet("op", "Operation", operations), facet("version", "Version", versions)}
	return result
}

// BuildView uses the closed parent tree to identify the root-to-last-finishing
// path. It is a timing view of the stored trace, not a claim about uninstrumented work.
func BuildView(trace connectors.CompleteTrace) (TraceView, error) {
	byID := map[string]connectors.TraceSpan{}
	for _, span := range trace.Spans {
		byID[span.SpanID] = span
	}
	root, start, end := traceRoot(trace)
	depth := map[string]int{}
	var calculateDepth func(string, map[string]bool) (int, error)
	calculateDepth = func(id string, path map[string]bool) (int, error) {
		if value, ok := depth[id]; ok {
			return value, nil
		}
		if path[id] {
			return 0, fmt.Errorf("cyclic trace parent graph")
		}
		path[id] = true
		span := byID[id]
		if span.ParentSpanID == "" {
			depth[id] = 0
		} else {
			parentDepth, err := calculateDepth(span.ParentSpanID, path)
			if err != nil {
				return 0, err
			}
			depth[id] = parentDepth + 1
		}
		delete(path, id)
		return depth[id], nil
	}
	for id := range byID {
		if _, err := calculateDepth(id, map[string]bool{}); err != nil {
			return TraceView{}, err
		}
	}
	children := map[string][]connectors.TraceSpan{}
	for _, span := range trace.Spans {
		children[span.ParentSpanID] = append(children[span.ParentSpanID], span)
	}
	for parent := range children {
		sort.Slice(children[parent], func(i, j int) bool {
			if children[parent][i].Start.Equal(children[parent][j].Start) {
				return children[parent][i].SpanID < children[parent][j].SpanID
			}
			return children[parent][i].Start.Before(children[parent][j].Start)
		})
	}
	ordered := make([]connectors.TraceSpan, 0, len(trace.Spans))
	var walk func(string)
	walk = func(parent string) {
		for _, span := range children[parent] {
			ordered = append(ordered, span)
			walk(span.SpanID)
		}
	}
	walk("")
	if len(ordered) != len(trace.Spans) {
		return TraceView{}, fmt.Errorf("trace tree is not rooted")
	}
	critical := map[string]bool{}
	hasChild := map[string]bool{}
	for _, span := range ordered {
		if span.ParentSpanID != "" {
			hasChild[span.ParentSpanID] = true
		}
	}
	var last connectors.TraceSpan
	for _, span := range ordered {
		if hasChild[span.SpanID] {
			continue
		}
		if last.SpanID == "" || span.Start.Add(time.Duration(span.DurationMS*float64(time.Millisecond))).After(last.Start.Add(time.Duration(last.DurationMS*float64(time.Millisecond)))) {
			last = span
		}
	}
	for id := last.SpanID; id != ""; id = byID[id].ParentSpanID {
		critical[id] = true
	}
	view := TraceView{ID: trace.ID, TraceID: trace.ID, Op: root.Name, Time: start.Format(time.RFC3339Nano),
		DurationMS: end.Sub(start).Seconds() * 1000, Version: root.Version,
		Colors: traceColors([]connectors.CompleteTrace{trace}), Spans: []TraceSpanView{},
		FlameNote: "Span intervals from the stored Tempo trace; the highlighted path follows the latest finishing descendant."}
	if view.Version == "" {
		view.Version = "unknown"
	}
	view.Graph.Nodes = []TraceGraphNode{}
	view.Graph.Edges = []TraceGraphEdge{}
	services := map[string]bool{}
	edges := map[string]bool{}
	for _, span := range ordered {
		item := TraceSpanView{ID: span.SpanID, Depth: depth[span.SpanID], Service: span.Service, Op: span.Name,
			Start: span.Start.Sub(start).Seconds() * 1000, End: span.Start.Sub(start).Seconds()*1000 + span.DurationMS,
			Status: "ok", Critical: critical[span.SpanID], Attrs: []map[string]any{}, Events: []map[string]string{}}
		if span.Error {
			item.Status = "error"
		}
		for _, attr := range span.Attributes {
			item.Attrs = append(item.Attrs, map[string]any{"k": attr.Key, "v": attr.Value})
		}
		for _, event := range span.Events {
			item.Events = append(item.Events, map[string]string{"at": event.At.Format(time.RFC3339Nano), "name": event.Name, "text": event.Text})
		}
		view.Spans = append(view.Spans, item)
		services[span.Service] = true
		if span.ParentSpanID != "" {
			parent := byID[span.ParentSpanID]
			if parent.Service != span.Service {
				edges[parent.Service+"\x00"+span.Service] = true
			}
		}
	}
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		view.Graph.Nodes = append(view.Graph.Nodes, TraceGraphNode{ID: name, X: 80 + (i%3)*170, Y: 60 + (i/3)*110, Sub: name})
	}
	keys := make([]string, 0, len(edges))
	for key := range edges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := []byte(key)
		separator := 0
		for parts[separator] != 0 {
			separator++
		}
		view.Graph.Edges = append(view.Graph.Edges, TraceGraphEdge{From: string(parts[:separator]), To: string(parts[separator+1:]), Label: "call"})
	}
	return view, nil
}
