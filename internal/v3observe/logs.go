package v3observe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type logLine struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Service string `json:"service"`
	Message string `json:"message"`
	Trace   string `json:"trace,omitempty"`
}

type logPatternKey struct {
	Template string
	Level    string
}

type logPatternCounts struct {
	Count   int64
	Buckets map[int64]int64
}

type logPattern struct {
	Template string  `json:"template"`
	Level    string  `json:"level"`
	Count    string  `json:"count"`
	Trend    []int64 `json:"trend"`
	CountNum int64   `json:"-"`
}

// logSearch is LogSearch with explicit unknowns. Items are the bounded, level
// checked lines the sources returned; total, errors, histogram and patterns
// are null with a reason in Unavailable when no source proved them complete.
type logSearch struct {
	Total       *string           `json:"total"`
	Errors      *string           `json:"errors"`
	Histogram   [][3]int64        `json:"histogram"`
	Items       []logLine         `json:"items"`
	Patterns    []logPattern      `json:"patterns"`
	Unavailable map[string]string `json:"unavailable"`
}

// logReason merges the reasons of several sources for one field. A missing
// registration is reported over a transient failure because retrying cannot fix it.
func logReason(current, next string) string {
	if next != "not_configured" {
		next = "unavailable"
	}
	if current == "not_configured" {
		return current
	}
	return next
}

type logReader interface {
	QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error)
}

type logPatternTelemetry interface {
	QueryLogsWithPatterns(context.Context, string, string, string, string, connectors.TimeWindow, int) (connectors.LogEvidence, error)
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
	duration, err := v3pagequery.ParseRange(r.URL.Query(), 30*time.Minute)
	if err != nil {
		httpapi.Error(w, 422, "invalid_range", "Use one supported time range")
		return
	}
	if values, has := r.URL.Query()["source"]; has {
		if len(values) != 1 || (values[0] != "loki" && values[0] != "elasticsearch") {
			httpapi.Error(w, 422, "invalid_source", "Choose loki or elasticsearch")
			return
		}
		q := r.URL.Query()
		if q.Has("provider") && (len(q["provider"]) != 1 || q.Get("provider") != values[0]) {
			httpapi.Error(w, 422, "conflicting_source", "Source and provider must agree")
			return
		}
		q.Set("provider", values[0])
		r.URL.RawQuery = q.Encode()
	}

	envs, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return
	}
	_, unified := s.registrySurface("logs")
	if (!unified && s.Telemetry == nil) || (unified && (s.Registry == nil || s.Clients == nil)) {
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
	providers := []string{"loki", "elasticsearch"}
	if selected, specified := r.URL.Query()["provider"]; specified {
		if len(selected) != 1 || (selected[0] != "loki" && selected[0] != "elasticsearch") {
			httpapi.Error(w, 422, "invalid_provider", "Choose one registered log provider")
			return
		}
		providers = selected
	}
	// An omitted levels parameter uses the default set. An explicitly empty
	// selection (all UI level chips disabled) must not query every level.
	noLevels := r.URL.Query().Has("levels") && r.URL.Query().Get("levels") == ""
	levels, ok := requestedLogLevels(r.URL.Query().Get("levels"))
	if !ok {
		httpapi.Error(w, 422, "invalid_levels", "Choose ERROR, WARN or INFO")
		return
	}
	targets, err := s.observationTargets(r.Context(), httpapi.CurrentUser(r), envs, "logs", service, providers)
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
		// No log source is registered for this scope: a stable deployment
		// state rather than a failed read. Nothing is reported as zero or empty.
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, 200, logSearch{Unavailable: map[string]string{"total": "not_configured", "errors": "not_configured",
			"histogram": "not_configured", "items": "not_configured", "patterns": "not_configured"}})
		return
	}
	if len(targets) > 3 {
		httpapi.Error(w, 422, "scope_limit", "Select a service or narrow the environment")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if noLevels {
		zero := "0"
		httpapi.WriteJSON(w, 200, logSearch{Total: &zero, Errors: &zero, Histogram: [][3]int64{}, Items: []logLine{},
			Patterns: []logPattern{}, Unavailable: map[string]string{}})
		return
	}
	// Each target is read with the file-configured client, or once the logs
	// reader is switched, with its registered connector's client.
	readers := make([]logReader, len(targets))
	resources := make([]string, len(targets))
	for i, target := range targets {
		if !unified {
			readers[i], resources[i] = s.Telemetry, target.Resource
			continue
		}
		client, resource, err := s.registryClient(r.Context(), target, v3telemetry.Logs)
		var notReady sourceNotReadyError
		switch {
		case err == nil:
			readers[i], resources[i] = client, resource
			continue
		case errors.Is(err, errAmbiguousObservationBinding):
			httpapi.Error(w, 409, "ambiguous_binding", "Select one log provider for this service and environment")
		case errors.Is(err, errRegistrySourceChanged):
			httpapi.Error(w, 409, "log_scope_changed", "Log source or authorization changed during the query")
		case errors.As(err, &notReady):
			httpapi.Error(w, 503, "source_not_ready", "Log source is not ready: "+strings.Join(notReady.reasons, ", "))
		case errors.Is(err, errRegistryClient):
			httpapi.Error(w, 503, "not_configured", "Selected log provider is not configured")
		default:
			httpapi.Error(w, 503, "log_scope_unavailable", "Log scope lookup failed")
		}
		return
	}
	window := observationWindow(duration)
	items := []logLine{}
	total, errorsCount := int64(0), int64(0)
	patternCounts := map[logPatternKey]*logPatternCounts{}
	// A count, error count or pattern set is known only when every selected
	// source and level proved it. Lines remain usable evidence either way.
	countReason, errorsReason, patternReason := "", "", ""
	// Each level is queried separately so total and histogram refer to the
	// selected levels, not a misleading sample or an unrelated global count.
	buckets := map[int64][3]int64{}
	for i, target := range targets {
		for _, level := range levels {
			var result connectors.LogEvidence
			var err error
			if source, ok := readers[i].(logPatternTelemetry); ok {
				result, err = source.QueryLogsWithPatterns(r.Context(), resources[i], target.Provider, search, level, window, 100)
			} else {
				result, err = readers[i].QueryLogsOptions(r.Context(), resources[i], target.Provider, search, level, true, window, 100)
			}
			var telemetryError *connectors.TelemetryError
			if errors.As(err, &telemetryError) && telemetryError.Kind == "configuration" {
				httpapi.Error(w, 503, "not_configured", "Selected log provider is not configured")
				return
			}
			if unified && errors.As(err, &telemetryError) && telemetryError.Kind == "credentials" {
				// The approved Vault credential could not be read (an empty
				// Vault after a dev-mode restart, a revoked approval).
				httpapi.Error(w, 503, "source_unavailable", "Log source credential is unavailable")
				return
			}
			if err != nil || result.Source.Failure != nil {
				httpapi.Error(w, 503, "telemetry_unavailable", "Log query is unavailable")
				return
			}
			if result.Summary == nil || result.SummaryError != "" || result.Summary.Total < 0 {
				countReason = logReason(countReason, result.SummaryState)
				if level == "error" {
					errorsReason = logReason(errorsReason, result.SummaryState)
				}
			} else {
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
			}
			if result.PatternError != "" || result.PatternSummary == nil {
				// An exact zero count without a pattern error has no patterns
				// to account for; anything else leaves the pattern set unproven.
				if result.PatternError != "" || result.Summary == nil || result.Summary.Total != 0 {
					patternReason = logReason(patternReason, result.PatternState)
				}
			} else {
				for _, pattern := range result.PatternSummary.Patterns {
					key := logPatternKey{Template: pattern.Template, Level: strings.ToUpper(level)}
					counts := patternCounts[key]
					if counts == nil {
						counts = &logPatternCounts{Buckets: map[int64]int64{}}
						patternCounts[key] = counts
					}
					counts.Count += pattern.Count
					for _, bucket := range pattern.Buckets {
						counts.Buckets[bucket.Timestamp.UTC().Unix()] += bucket.Count
					}
				}
			}
			for _, entry := range result.Entries {
				if strings.ToLower(entry.Level) != level {
					httpapi.Error(w, 503, "telemetry_unavailable", "Log sample level does not match the scoped query")
					return
				}
				items = append(items, logLine{Time: entry.Timestamp.UTC().Format(time.RFC3339Nano), Level: strings.ToUpper(level),
					Service: target.ApplicationID, Message: entry.Line, Trace: entry.TraceID})
			}
		}
	}
	// An external aggregation can outlive a grant or binding revision. Resolve
	// the same persisted target set again before returning either full results
	// or partial evidence, so a changed scope cannot leak old-source lines.
	currentTargets, err := s.observationTargets(r.Context(), httpapi.CurrentUser(r), envs, "logs", service, providers)
	if err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Logs are outside your authorized scope")
		} else if errors.Is(err, errAmbiguousObservationBinding) || errors.Is(err, errObservationScopeLimit) {
			httpapi.Error(w, 409, "log_scope_changed", "Log source or authorization changed during the query")
		} else {
			httpapi.Error(w, 503, "log_scope_unavailable", "Log scope lookup failed")
		}
		return
	}
	if len(currentTargets) != len(targets) {
		httpapi.Error(w, 409, "log_scope_changed", "Log source or authorization changed during the query")
		return
	}
	for i := range targets {
		if currentTargets[i] != targets[i] {
			httpapi.Error(w, 409, "log_scope_changed", "Log source or authorization changed during the query")
			return
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
	out := logSearch{Items: items, Unavailable: map[string]string{}}
	if countReason == "" {
		value := fmt.Sprint(total)
		out.Total, out.Histogram = &value, histogram
	} else {
		out.Unavailable["total"], out.Unavailable["histogram"] = countReason, countReason
		patternReason = logReason(patternReason, countReason)
	}
	if errorsReason == "" {
		value := fmt.Sprint(errorsCount)
		out.Errors = &value
	} else {
		out.Unavailable["errors"] = errorsReason
	}
	patterns := make([]logPattern, 0, len(patternCounts))
	var patternedTotal int64
	for key, counts := range patternCounts {
		trend := make([]int64, 0, len(keys))
		var trendedTotal int64
		for _, stamp := range keys {
			value := counts.Buckets[stamp]
			trend = append(trend, value)
			trendedTotal += value
		}
		if (key.Template == "" || counts.Count < 0 || trendedTotal != counts.Count || len(counts.Buckets) > len(keys)) && patternReason == "" {
			patternReason = "unavailable"
		}
		patternedTotal += counts.Count
		patterns = append(patterns, logPattern{Template: key.Template, Level: key.Level, Count: fmt.Sprint(counts.Count), Trend: trend, CountNum: counts.Count})
	}
	if patternReason == "" && patternedTotal != total {
		patternReason = "unavailable"
	}
	if patternReason == "" {
		sort.Slice(patterns, func(i, j int) bool {
			if patterns[i].CountNum != patterns[j].CountNum {
				return patterns[i].CountNum > patterns[j].CountNum
			}
			if patterns[i].Level != patterns[j].Level {
				return patterns[i].Level < patterns[j].Level
			}
			return patterns[i].Template < patterns[j].Template
		})
		out.Patterns = patterns
	} else {
		// Pattern counts and trends must account for the exact total; a
		// partial set is not shown as the complete pattern list.
		out.Unavailable["patterns"] = patternReason
	}
	httpapi.WriteJSON(w, 200, out)
}
