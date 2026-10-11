package v3traces

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3agents"
)

// Agent records live in the platform, independently of application Tempo
// bindings. The existing composition root already supplies transactional RBAC.
func (s *Service) listAgent(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	for _, key := range []string{"scope", "view", "q", "service"} {
		if len(r.URL.Query()[key]) > 1 {
			httpapi.Error(w, 422, "invalid_query", "Select one value for each Agent trace parameter")
			return
		}
	}
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "all"
	}
	switch view {
	case "all", "errors", "slow", "blocked":
	default:
		httpapi.Error(w, 422, "invalid_view", "Unknown Agent trace view")
		return
	}
	filters, err := parseAgentTraceFilter(r.URL.Query().Get("q"))
	if err != nil {
		httpapi.Error(w, 422, "invalid_query", "Use bounded agent, model, status, version or trigger equality and duration comparisons")
		return
	}
	if s == nil || s.Store == nil || s.Store.DB == nil {
		httpapi.Error(w, 503, "traces_unavailable", "Agent run storage is unavailable")
		return
	}
	access, ok := s.Access.(v3agents.Access)
	if !ok {
		httpapi.Error(w, 503, "trace_policy_unavailable", "Transactional Agent trace authorization is unavailable")
		return
	}
	source, err := v3agents.New(s.Store.DB, access).ReadTraceRunObservations(r)
	if err != nil {
		switch {
		case errors.Is(err, v3agents.ErrTraceRunInvalidRequest):
			httpapi.Error(w, 422, "invalid_query", "Agent trace request parameters are invalid")
		case errors.Is(err, v3agents.ErrTraceRunScopeLimit):
			httpapi.Error(w, 413, "trace_scope_limit", "Agent trace population exceeds the bounded read limit")
		case errors.Is(err, store.ErrSessionRevoked):
			httpapi.Error(w, 401, "unauthorized", "Session is no longer active")
		case errors.Is(err, store.ErrForbidden):
			httpapi.Error(w, 403, "forbidden", "Agent traces are outside your authorized scope")
		default:
			httpapi.Error(w, 503, "agent_trace_unavailable", "Persisted Agent trace metadata is unavailable")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, buildAgentTraceList(source, filters, view, time.Since(started)))
}

type agentTraceFilter struct {
	key, op, value string
	durationMS     float64
}

var agentEquality = regexp.MustCompile(`^\s*(agent|model|status|version|trigger)\s*=\s*("(?:[^"\\]|\\.)*")`)
var agentDuration = regexp.MustCompile(`^\s*duration\s*(>=|<=|>|<|=)\s*([0-9]+(?:\.[0-9]+)?)\s*(ms|s)`)

// This is a small metadata grammar, not TraceQL or executable SQL. Parse each
// quoted value before consuming && so that a literal containing && stays data.
func parseAgentTraceFilter(query string) ([]agentTraceFilter, error) {
	if len(query) > 2048 {
		return nil, errors.New("filter too long")
	}
	rest := strings.TrimSpace(query)
	filters := []agentTraceFilter{}
	for rest != "" {
		if len(filters) == 8 {
			return nil, errors.New("too many terms")
		}
		var f agentTraceFilter
		if match := agentEquality.FindStringSubmatch(rest); match != nil {
			value, err := strconv.Unquote(match[2])
			if err != nil || len(value) == 0 || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
				return nil, errors.New("invalid equality")
			}
			f = agentTraceFilter{key: match[1], op: "=", value: value}
			if f.key == "status" && value != "success" && value != "failed" && value != "blocked" {
				return nil, errors.New("unknown terminal status")
			}
			if f.key == "trigger" && value != "manual" && value != "alert" && value != "scheduled" && value != "api" {
				return nil, errors.New("unknown trigger")
			}
			rest = rest[len(match[0]):]
		} else if match := agentDuration.FindStringSubmatch(rest); match != nil {
			value, err := strconv.ParseFloat(match[2], 64)
			if match[3] == "s" {
				value *= 1000
			}
			if err != nil || math.IsInf(value, 0) || value > float64((24*time.Hour)/time.Millisecond) {
				return nil, errors.New("invalid duration")
			}
			f = agentTraceFilter{key: "duration", op: match[1], durationMS: value}
			rest = rest[len(match[0]):]
		} else {
			return nil, errors.New("unsupported expression")
		}
		filters = append(filters, f)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, "&&") {
			return nil, errors.New("expected conjunction")
		}
		rest = strings.TrimSpace(rest[2:])
		if rest == "" {
			return nil, errors.New("missing term")
		}
	}
	return filters, nil
}

func matchesAgentTrace(v v3agents.TraceRunObservation, filters []agentTraceFilter) bool {
	for _, f := range filters {
		switch f.key {
		case "agent":
			if v.AgentID != f.value && v.AgentName != f.value {
				return false
			}
		case "model":
			found := false
			for _, model := range v.ModelIDs {
				if model == f.value {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		case "status":
			if v.Status != f.value {
				return false
			}
		case "version":
			if v.Version != f.value {
				return false
			}
		case "trigger":
			if v.Trigger != f.value {
				return false
			}
		case "duration":
			ms := float64(v.FinishedAt.Sub(v.StartedAt)) / float64(time.Millisecond)
			switch f.op {
			case ">":
				if !(ms > f.durationMS) {
					return false
				}
			case ">=":
				if !(ms >= f.durationMS) {
					return false
				}
			case "<":
				if !(ms < f.durationMS) {
					return false
				}
			case "<=":
				if !(ms <= f.durationMS) {
					return false
				}
			case "=":
				if ms != f.durationMS {
					return false
				}
			}
		}
	}
	return true
}

func agentTraceHasError(v v3agents.TraceRunObservation) bool {
	return v.Status == "failed" || v.Status == "blocked" || v.ErrorSpanCount > 0
}

// All totals and facets are calculated from this bounded, authorized persisted
// population. Reported span counts do not claim complete runtime instrumentation.
func buildAgentTraceList(source v3agents.TraceRunObservations, filters []agentTraceFilter, view string, took time.Duration) TraceList {
	result := TraceList{
		Scope: "agent", Took: fmt.Sprintf("%d ms", took.Milliseconds()), Colors: map[string]string{},
		Facets: []TraceFacet{}, Items: []TraceRow{}, Scatter: []TraceScatter{},
		Source: "v3_agent_runs_and_reported_spans", AggregationScope: "authorized_persisted_finished_runs",
		WindowStartAt: source.WindowStartAt.UTC().Format(time.RFC3339Nano),
		WindowEndAt:   source.WindowEndAt.UTC().Format(time.RFC3339Nano), ExcludedRunning: &source.ExcludedRunning,
		Partial: true, Missing: []string{"producer_coverage", "complete_span_coverage", "cost"},
		HistogramStates: []string{"failed", "blocked", "reported_span_error"},
		Unavailable:     map[string]string{},
	}
	population := make([]v3agents.TraceRunObservation, 0, len(source.Items))
	errorCount, slowCount, blockedCount := 0, 0, 0
	for _, v := range source.Items {
		if !matchesAgentTrace(v, filters) {
			continue
		}
		population = append(population, v)
		if agentTraceHasError(v) {
			errorCount++
		}
		if v.Status == "blocked" {
			blockedCount++
		}
		if v.FinishedAt.Sub(v.StartedAt) >= time.Second {
			slowCount++
		}
	}
	result.Views = []TraceViewOption{
		{ID: "all", Label: "All", Count: count(len(population)), Query: ""},
		{ID: "errors", Label: "Failed, blocked or reported errors", Count: count(errorCount), Query: ""},
		{ID: "slow", Label: "Slow", Count: count(slowCount), Query: "duration >= 1s"},
		{ID: "blocked", Label: "Blocked", Count: count(blockedCount), Query: `status = "blocked"`},
	}
	selected := make([]v3agents.TraceRunObservation, 0, len(population))
	for _, v := range population {
		duration := v.FinishedAt.Sub(v.StartedAt)
		if view == "errors" && !agentTraceHasError(v) || view == "slow" && duration < time.Second || view == "blocked" && v.Status != "blocked" {
			continue
		}
		selected = append(selected, v)
	}
	sort.Slice(selected, func(i, j int) bool {
		if !selected[i].StartedAt.Equal(selected[j].StartedAt) {
			return selected[i].StartedAt.After(selected[j].StartedAt)
		}
		return selected[i].ID < selected[j].ID
	})
	result.Total = len(selected)
	agents, models, statuses, versions, services := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	result.Histogram.Counts = make([][2]int, 12)
	result.Histogram.AxisTimes = make([]string, 12)
	width := source.WindowEndAt.Sub(source.WindowStartAt)
	for i := range result.Histogram.AxisTimes {
		result.Histogram.AxisTimes[i] = source.WindowStartAt.UTC().Add(time.Duration(i) * width / 12).Format(time.RFC3339)
	}
	maxMS := 1.0
	for _, v := range selected {
		maxMS = math.Max(maxMS, float64(v.FinishedAt.Sub(v.StartedAt))/float64(time.Millisecond))
	}
	for _, v := range selected {
		durationMS := float64(v.FinishedAt.Sub(v.StartedAt)) / float64(time.Millisecond)
		row := TraceRow{ID: v.ID, Time: v.StartedAt.UTC().Format(time.RFC3339Nano), Op: v.AgentName + " / " + v.Trigger,
			Services: []string{v.ApplicationName}, DurationMS: durationMS, Spans: v.SpanCount, Errors: v.ErrorSpanCount,
			Href: "/agents/runs/" + url.PathEscape(v.ID), Version: v.Version, Agent: v.AgentName, AgentID: v.AgentID,
			Status: v.Status, ApplicationID: v.ApplicationID, EnvironmentID: v.EnvironmentID,
			Missing: []string{"complete_span_coverage", "cost"}}
		if v.TokenInput != nil && v.TokenOutput != nil {
			row.Tokens = strconv.FormatInt(int64(*v.TokenInput)+int64(*v.TokenOutput), 10)
		} else {
			row.Missing = append(row.Missing, "tokens")
		}
		if len(v.ModelIDs) > 0 {
			row.Model = strings.Join(v.ModelIDs, ", ")
		} else {
			row.Missing = append(row.Missing, "model")
		}
		result.Items = append(result.Items, row)
		agents[v.AgentName]++
		statuses[v.Status]++
		versions[v.Version]++
		services[v.ApplicationName]++
		for _, model := range v.ModelIDs {
			models[model]++
		}
		fraction := float64(v.StartedAt.Sub(source.WindowStartAt)) / float64(width)
		fraction = math.Max(0, math.Min(0.999999, fraction))
		bucket := int(fraction * 12)
		hasError := agentTraceHasError(v)
		if hasError {
			result.Histogram.Counts[bucket][1]++
		} else {
			result.Histogram.Counts[bucket][0]++
		}
		result.Scatter = append(result.Scatter, TraceScatter{X: 4 + 432*fraction, Y: 105 - 100*durationMS/maxMS, Error: hasError, ID: v.ID})
	}
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		result.Colors[name] = palette[i%len(palette)]
	}
	result.Facets = []TraceFacet{facet("agent", "Agent", agents), facet("model", "Model", models), facet("status", "Status", statuses), facet("version", "Version", versions), facet("service", "Service", services)}
	return result
}
