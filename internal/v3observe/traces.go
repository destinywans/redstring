package v3observe

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
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
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if len(service) > 200 {
		httpapi.Error(w, 422, "invalid_service", "Service filter is too long")
		return nil, 0, false
	}
	targets, err := s.observationTargets(r.Context(), httpapi.CurrentUser(r), envs, "traces", service, []string{"tempo"})
	if err != nil {
		if errors.Is(err, errObservationScopeLimit) {
			httpapi.Error(w, 422, "scope_limit", "Narrow the service or environment")
			return nil, 0, false
		}
		if errors.Is(err, errAmbiguousObservationBinding) {
			httpapi.Error(w, 409, "ambiguous_binding", "Select one Tempo binding for this service and environment")
			return nil, 0, false
		}
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Traces are outside your authorized scope")
			return nil, 0, false
		}
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
	results, err := s.Telemetry.QueryTracesBatchID(r.Context(), resources, observationWindow(time.Hour), 20, id)
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
		if id != "" && result.LimitReached {
			httpapi.Error(w, 413, "trace_too_large", "Trace exceeds the scoped span limit")
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

func traceHref(id, service string) string {
	href := "/traces/" + id
	if service != "" {
		href += "?" + url.Values{"service": {service}}.Encode()
	}
	return href
}

func (s *Service) traces(w http.ResponseWriter, r *http.Request) {
	view := r.URL.Query().Get("view")
	if view != "" && view != "all" && view != "errors" && view != "slow" {
		httpapi.Error(w, 422, "invalid_view", "Unknown saved trace view")
		return
	}
	spans, took, ok := s.queryTraces(w, r, "")
	if !ok {
		return
	}
	rows := rowsFromSpans(spans)
	filtered := make([]traceRow, 0, len(rows))
	for _, row := range rows {
		if view == "errors" && row.Errors == 0 || view == "slow" && row.DurationMS <= 1000 {
			continue
		}
		row.Href = traceHref(row.ID, strings.TrimSpace(r.URL.Query().Get("service")))
		filtered = append(filtered, row)
	}
	// Tempo search returns at most 20 traces across selected scopes. Neither a
	// smaller search result nor an empty result proves a complete population:
	// its query/index semantics do not supply an exact total, facets or time
	// histogram. Keep observed rows explicitly marked as a sample and fail the
	// full TraceList contract until a scoped aggregation source is connected.
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 503, map[string]any{
		"error": map[string]string{"code": "trace_aggregation_unavailable", "message": "Complete trace counts, facets and histogram are not available from the registered source"},
		"scope": "app", "view": view, "took": fmt.Sprintf("%.2f s", took.Seconds()),
		"observed_sample": filtered,
		"missing":         []string{"total", "facets", "histogram", "scatter_population"},
	})
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
	httpapi.WriteJSON(w, 200, traceDetailPayload(id, selected))
}

func traceDetailPayload(id string, selected []connectors.TraceSpan) map[string]any {
	sort.Slice(selected, func(i, j int) bool { return selected[i].Start.Before(selected[j].Start) })
	root := selected[0]
	last := root.Start.Add(time.Duration(root.DurationMS * float64(time.Millisecond)))
	for _, span := range selected {
		if span.ParentSpanID == "" && root.ParentSpanID != "" {
			root = span
		}
		if end := span.Start.Add(time.Duration(span.DurationMS * float64(time.Millisecond))); end.After(last) {
			last = end
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
		start := span.Start.Sub(selected[0].Start).Seconds() * 1000
		item := map[string]any{"id": span.SpanID, "depth": depth(span), "service": span.Service, "op": span.Name,
			"start": start, "end": start + span.DurationMS, "status": map[bool]string{true: "error", false: "ok"}[span.Error]}
		if len(span.Attributes) > 0 {
			attrs := make([]map[string]string, 0, len(span.Attributes))
			for _, attr := range span.Attributes {
				attrs = append(attrs, map[string]string{"k": attr.Key, "v": attr.Value})
			}
			item["attrs"] = attrs
		}
		if len(span.Events) > 0 {
			events := make([]map[string]string, 0, len(span.Events))
			for _, event := range span.Events {
				events = append(events, map[string]string{"at": event.At.UTC().Format(time.RFC3339Nano), "name": event.Name, "text": event.Text})
			}
			item["events"] = events
		}
		out = append(out, item)
		if _, seen := serviceIndex[span.Service]; !seen && span.Service != "" {
			serviceIndex[span.Service] = len(nodes)
			nodes = append(nodes, map[string]any{"id": span.Service, "x": 20 + 220*(len(nodes)%4), "y": 40 + 110*(len(nodes)/4),
				"sub": fmt.Sprintf("%.0f ms", span.DurationMS), "danger": span.Error})
		}
		if parent, found := byID[span.ParentSpanID]; found && parent.Service != span.Service && parent.Service != "" && span.Service != "" {
			edges = append(edges, map[string]any{"from": parent.Service, "to": span.Service, "label": span.Name, "danger": span.Error})
		}
	}
	return map[string]any{"id": id, "trace_id": id, "op": root.Name, "duration_ms": last.Sub(selected[0].Start).Seconds() * 1000,
		"time": selected[0].Start.UTC().Format(time.RFC3339Nano), "version": "", "colors": map[string]string{}, "spans": out,
		"partial": true, "missing": []string{"complete_trace", "critical_path", "linked_logs", "linked_changes"},
		"flame_note": "仅展示所选环境与服务授权范围内的 Span；跨服务链路可能不完整。", "graph": map[string]any{"nodes": nodes, "edges": edges}}
}
