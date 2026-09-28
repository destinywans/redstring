package v3observe

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

type traceRow struct {
	ID         string   `json:"id"`
	Time       string   `json:"time"`
	Op         string   `json:"op"`
	Services   []string `json:"services"`
	DurationMS float64  `json:"duration_ms"`
	Spans      int      `json:"spans"`
	Errors     int      `json:"errors"`
	Href       string   `json:"href"`
}

func (s *Service) queryTraces(w http.ResponseWriter, r *http.Request, id string) ([]connectors.TraceSpan, time.Duration, bool) {
	envs, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return nil, 0, false
	}
	if s.Telemetry == nil {
		httpapi.Error(w, 503, "not_configured", "Trace provider is not configured")
		return nil, 0, false
	}
	if scope := r.URL.Query().Get("scope"); scope != "" && scope != "app" {
		if scope == "agent" {
			httpapi.Error(w, 503, "not_configured", "Agent trace provider is not configured")
		} else {
			httpapi.Error(w, 422, "invalid_scope", "Unknown trace scope")
		}
		return nil, 0, false
	}
	if r.URL.Query().Get("q") != "" {
		httpapi.Error(w, 422, "query_not_supported", "TraceQL search is not connected to the scoped provider")
		return nil, 0, false
	}
	if id != "" && !connectors.ValidTraceID(id) {
		httpapi.Error(w, 422, "invalid_trace", "Trace ID must be 32 hexadecimal characters")
		return nil, 0, false
	}
	targets, err := s.observationTargets(r.Context(), httpapi.CurrentUser(r), envs, "traces", r.URL.Query().Get("service"), []string{"tempo"})
	if err != nil {
		httpapi.Error(w, 503, "trace_scope_unavailable", "Trace scope lookup failed")
		return nil, 0, false
	}
	if len(targets) == 0 {
		httpapi.Error(w, 503, "not_configured", "No authorized trace binding is configured")
		return nil, 0, false
	}
	if len(targets) > 6 {
		httpapi.Error(w, 422, "scope_limit", "Select a service or narrow the environment")
		return nil, 0, false
	}
	resources := make([]string, 0, len(targets))
	seen := map[string]bool{}
	for _, target := range targets {
		if !seen[target.Resource] {
			resources = append(resources, target.Resource)
			seen[target.Resource] = true
		}
	}
	start := time.Now()
	results, err := s.Telemetry.QueryTracesBatchID(r.Context(), resources, observationWindow(), 20, id)
	if err != nil {
		httpapi.Error(w, 503, "telemetry_unavailable", "Scoped trace query failed")
		return nil, 0, false
	}
	spans := []connectors.TraceSpan{}
	seenSpan := map[string]bool{}
	for _, resource := range resources {
		result, exists := results[resource]
		if !exists || result.Source.Failure != nil {
			httpapi.Error(w, 503, "telemetry_unavailable", "One trace scope is unavailable")
			return nil, 0, false
		}
		for _, span := range result.Spans {
			key := span.TraceID + "\x00" + span.SpanID
			if !seenSpan[key] {
				spans = append(spans, span)
				seenSpan[key] = true
			}
		}
	}
	return spans, time.Since(start), true
}

func rowsFromSpans(spans []connectors.TraceSpan) []traceRow {
	groups := map[string][]connectors.TraceSpan{}
	for _, span := range spans {
		groups[span.TraceID] = append(groups[span.TraceID], span)
	}
	rows := make([]traceRow, 0, len(groups))
	for id, group := range groups {
		if id == "" {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Start.Before(group[j].Start) })
		root := group[0]
		for _, span := range group {
			if span.ParentSpanID == "" {
				root = span
				break
			}
		}
		services := []string{}
		seenService := map[string]bool{}
		end := root.Start.Add(time.Duration(root.DurationMS * float64(time.Millisecond)))
		errorsCount := 0
		for _, span := range group {
			if span.Error {
				errorsCount++
			}
			if span.Service != "" && !seenService[span.Service] {
				services = append(services, span.Service)
				seenService[span.Service] = true
			}
			if last := span.Start.Add(time.Duration(span.DurationMS * float64(time.Millisecond))); last.After(end) {
				end = last
			}
		}
		rows = append(rows, traceRow{ID: id, Time: root.Start.UTC().Format(time.RFC3339Nano), Op: root.Name,
			Services: services, DurationMS: end.Sub(root.Start).Seconds() * 1000, Spans: len(group), Errors: errorsCount, Href: "/traces/" + id})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Time > rows[j].Time })
	return rows
}

func (s *Service) traces(w http.ResponseWriter, r *http.Request) {
	spans, took, ok := s.queryTraces(w, r, "")
	if !ok {
		return
	}
	rows := rowsFromSpans(spans)
	view := r.URL.Query().Get("view")
	filtered := make([]traceRow, 0, len(rows))
	for _, row := range rows {
		if view == "errors" && row.Errors == 0 || view == "slow" && row.DurationMS <= 1000 {
			continue
		}
		filtered = append(filtered, row)
	}
	if view != "" && view != "all" && view != "errors" && view != "slow" {
		httpapi.Error(w, 422, "invalid_view", "Unknown saved trace view")
		return
	}
	facetCounts := map[string]int{}
	scatter := make([]map[string]any, 0, len(filtered))
	for i, row := range filtered {
		for _, service := range row.Services {
			facetCounts[service]++
		}
		scatter = append(scatter, map[string]any{"x": i, "y": row.DurationMS, "error": row.Errors > 0, "id": row.ID})
	}
	services := make([]string, 0, len(facetCounts))
	for service := range facetCounts {
		services = append(services, service)
	}
	sort.Strings(services)
	facetItems := make([]map[string]any, 0, len(services))
	for _, service := range services {
		facetItems = append(facetItems, map[string]any{"value": service, "count": facetCounts[service]})
	}
	counts := make([][2]int, 0, 12)
	axis := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		counts = append(counts, [2]int{})
		axis = append(axis, time.Now().UTC().Add(time.Duration(i-11)*5*time.Minute).Format("15:04"))
	}
	// Counts are an actual returned trace sample, not a claim about all traffic.
	for _, row := range filtered {
		t, err := time.Parse(time.RFC3339Nano, row.Time)
		if err != nil {
			continue
		}
		bucket := 11 - int(time.Since(t).Minutes()/5)
		if bucket >= 0 && bucket < len(counts) {
			if row.Errors > 0 {
				counts[bucket][1]++
			} else {
				counts[bucket][0]++
			}
		}
	}
	httpapi.WriteJSON(w, 200, map[string]any{"scope": "app", "total": len(filtered), "took": fmt.Sprintf("%.2f s", took.Seconds()),
		"colors": map[string]string{}, "facets": []any{map[string]any{"key": "service", "label": "Service", "items": facetItems}},
		"views":     []any{map[string]any{"id": "all", "label": "All", "query": ""}, map[string]any{"id": "errors", "label": "Errors", "query": ""}, map[string]any{"id": "slow", "label": "Slow > 1 s", "query": ""}},
		"histogram": map[string]any{"counts": counts, "axis": axis}, "scatter": scatter, "items": filtered})
}

func (s *Service) traceDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(r.PathValue("id"))
	spans, _, ok := s.queryTraces(w, r, id)
	if !ok {
		return
	}
	selected := []connectors.TraceSpan{}
	for _, span := range spans {
		if strings.EqualFold(span.TraceID, id) {
			selected = append(selected, span)
		}
	}
	if len(selected) == 0 {
		httpapi.Error(w, 404, "not_found", "Trace not found in accessible scope")
		return
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Start.Before(selected[j].Start) })
	root := selected[0]
	for _, span := range selected {
		if span.ParentSpanID == "" {
			root = span
			break
		}
	}
	byID := map[string]connectors.TraceSpan{}
	for _, span := range selected {
		byID[span.SpanID] = span
	}
	depth := func(span connectors.TraceSpan) int {
		seen := map[string]bool{}
		current := span.ParentSpanID
		result := 0
		for current != "" && result < 20 {
			if seen[current] {
				break
			}
			seen[current] = true
			parent, ok := byID[current]
			if !ok {
				break
			}
			result++
			current = parent.ParentSpanID
		}
		return result
	}
	out := make([]map[string]any, 0, len(selected))
	nodes := []map[string]any{}
	edges := []map[string]any{}
	serviceIndex := map[string]int{}
	for _, span := range selected {
		start := span.Start.Sub(root.Start).Seconds() * 1000
		out = append(out, map[string]any{"id": span.SpanID, "depth": depth(span), "service": span.Service, "op": span.Name,
			"start": start, "end": start + span.DurationMS, "status": map[bool]string{true: "error", false: "ok"}[span.Error]})
		if _, seen := serviceIndex[span.Service]; !seen && span.Service != "" {
			serviceIndex[span.Service] = len(nodes)
			nodes = append(nodes, map[string]any{"id": span.Service, "x": 20 + 220*(len(nodes)%4), "y": 40 + 110*(len(nodes)/4),
				"sub": fmt.Sprintf("%.0f ms", span.DurationMS), "danger": span.Error})
		}
		if parent, found := byID[span.ParentSpanID]; found && parent.Service != span.Service && parent.Service != "" && span.Service != "" {
			edges = append(edges, map[string]any{"from": parent.Service, "to": span.Service, "label": span.Name, "danger": span.Error})
		}
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": id, "trace_id": id, "op": root.Name, "duration_ms": root.DurationMS,
		"time": root.Start.UTC().Format(time.RFC3339Nano), "version": "", "colors": map[string]string{}, "spans": out,
		"flame_note": "", "graph": map[string]any{"nodes": nodes, "edges": edges}})
}
