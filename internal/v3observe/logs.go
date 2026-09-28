package v3observe

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type logLine struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Service string `json:"service"`
	Message string `json:"message"`
	Trace   string `json:"trace,omitempty"`
}

func requestedLogLevels(raw string) ([]string, bool) {
	if raw == "" {
		return []string{"error", "warn", "info"}, true
	}
	set := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		level := strings.ToLower(strings.TrimSpace(part))
		if level != "error" && level != "warn" && level != "info" {
			return nil, false
		}
		set[level] = true
	}
	if len(set) == 0 {
		return nil, false
	}
	result := []string{}
	for _, level := range []string{"error", "warn", "info"} {
		if set[level] {
			result = append(result, level)
		}
	}
	return result, true
}

func (s *Service) logs(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return
	}
	if s.Telemetry == nil {
		httpapi.Error(w, 503, "not_configured", "Log provider is not configured")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(search) > 256 {
		httpapi.Error(w, 422, "invalid_query", "Log query is too long")
		return
	}
	service := strings.TrimSpace(r.URL.Query().Get("service"))
	if len(service) > 200 {
		httpapi.Error(w, 422, "invalid_service", "Service filter is too long")
		return
	}
	// An omitted levels parameter uses the default set. An explicitly empty
	// selection (all UI level chips disabled) must not query every level.
	noLevels := r.URL.Query().Has("levels") && r.URL.Query().Get("levels") == ""
	levels, ok := requestedLogLevels(r.URL.Query().Get("levels"))
	if !ok {
		httpapi.Error(w, 422, "invalid_levels", "Choose ERROR, WARN or INFO")
		return
	}
	targets, err := s.observationTargets(r.Context(), httpapi.CurrentUser(r), envs, "logs", service, []string{"loki", "elasticsearch"})
	if err != nil {
		if errors.Is(err, errObservationScopeLimit) {
			httpapi.Error(w, 422, "scope_limit", "Narrow the service or environment")
			return
		}
		if errors.Is(err, errAmbiguousObservationBinding) {
			httpapi.Error(w, 409, "ambiguous_binding", "Select one log provider for this service and environment")
			return
		}
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Logs are outside your authorized scope")
			return
		}
		httpapi.Error(w, 503, "log_scope_unavailable", "Log scope lookup failed")
		return
	}
	if len(targets) == 0 {
		httpapi.Error(w, 503, "not_configured", "No authorized log binding is configured")
		return
	}
	if len(targets) > 3 {
		httpapi.Error(w, 422, "scope_limit", "Select a service or narrow the environment")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if noLevels {
		httpapi.WriteJSON(w, 200, map[string]any{"total": "0", "errors": "0", "histogram": [][3]int64{}, "items": []logLine{}, "patterns": []any{}})
		return
	}
	window := observationWindow(30 * time.Minute)
	items := []logLine{}
	total, errorsCount := int64(0), int64(0)
	// Each level is queried separately so total and histogram refer to the
	// selected levels, not a misleading sample or an unrelated global count.
	buckets := map[int64][3]int64{}
	for _, target := range targets {
		for _, level := range levels {
			result, err := s.Telemetry.QueryLogsOptions(r.Context(), target.Resource, target.Provider, search, level, true, window, 100)
			if err != nil || result.Source.Failure != nil || result.Summary == nil || result.SummaryError != "" {
				httpapi.Error(w, 503, "telemetry_unavailable", "Log query or complete count is unavailable")
				return
			}
			total += result.Summary.Total
			if level == "error" {
				errorsCount += result.Summary.Total
			}
			idx := map[string]int{"error": 0, "warn": 1, "info": 2}[level]
			for _, bucket := range result.Summary.Buckets {
				stamp := bucket.Timestamp.UTC().Unix()
				counts := buckets[stamp]
				counts[idx] += bucket.Count
				buckets[stamp] = counts
			}
			for _, entry := range result.Entries {
				if strings.ToLower(entry.Level) != level {
					continue
				}
				items = append(items, logLine{Time: entry.Timestamp.UTC().Format(time.RFC3339Nano), Level: strings.ToUpper(level),
					Service: target.ApplicationID, Message: entry.Line, Trace: entry.TraceID})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Time > items[j].Time })
	if len(items) > 100 {
		items = items[:100]
	}
	keys := make([]int64, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	histogram := make([][3]int64, 0, len(keys))
	for _, key := range keys {
		histogram = append(histogram, buckets[key])
	}
	// The providers return complete counts but only a bounded sample of lines.
	// A pattern count or trend derived from that sample would misrepresent the
	// 30-minute population. Fail the full LogSearch contract until a scoped
	// clustering source supplies complete pattern counts and trends.
	httpapi.WriteJSON(w, 503, map[string]any{
		"error": map[string]string{"code": "log_patterns_unavailable", "message": "Complete log pattern counts and trends are not available from registered sources"},
		"available": map[string]any{"total": fmt.Sprint(total), "errors": fmt.Sprint(errorsCount),
			"histogram": histogram, "items_sample": items},
		"missing": []string{"patterns", "pattern_counts", "pattern_trends"},
	})
}
