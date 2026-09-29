package v3alertrules

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

const (
	dryRunWindow     = 6 * time.Hour
	dryRunMaximumFor = time.Hour
	dryRunMaxPoints  = 2000
	dryRunMaxWindows = 500
)

// RangeQuery is optional on a rule source. The rule-list reader remains usable
// when a provider cannot run historical Prometheus queries.
type RangeQuery interface {
	QueryRange(context.Context, string, string, time.Time, time.Time, time.Duration) ([]RangeSeries, error)
}

// DryRun simulates the current, authorized rule expression on real Prometheus
// range samples. The request cannot submit a different PromQL expression.
func (s *Service) DryRun(w http.ResponseWriter, r *http.Request) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment for a rule dry run")
		return
	}
	var input struct {
		Expr string `json:"expr"`
		For  string `json:"for"`
	}
	if !httpapi.Decode(w, r, &input) {
		return
	}
	forDuration, err := time.ParseDuration(input.For)
	if len(input.For) > 32 || forDuration < 0 || forDuration > dryRunMaximumFor || err != nil || len(input.Expr) == 0 || len(input.Expr) > 8192 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_dry_run", "Use the current rule expression and a for duration from 0s to 1h")
		return
	}
	if s == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "dry_run_unavailable", "Alert rule source is unavailable")
		return
	}
	item, lookupError := s.findRule(r, r.PathValue("id"))
	if lookupError != nil {
		httpapi.Error(w, lookupError.status, lookupError.code, lookupError.message)
		return
	}
	if input.Expr != item.Expr {
		httpapi.Error(w, http.StatusUnprocessableEntity, "expression_mismatch", "Dry run expression must equal the current authorized rule")
		return
	}
	query, ok := s.Source.(RangeQuery)
	if !ok {
		httpapi.Error(w, http.StatusServiceUnavailable, "dry_run_unavailable", "Historical Prometheus query is not configured")
		return
	}
	user := httpapi.CurrentUser(r)
	owner, lookupError := s.dryRunOwner(r.Context(), user, item)
	if lookupError != nil {
		httpapi.Error(w, lookupError.status, lookupError.code, lookupError.message)
		return
	}
	step := item.evaluationInterval
	if step == 0 {
		step = time.Minute // provider omitted its group interval; sampled fallback
	}
	if step < 15*time.Second || step > 5*time.Minute {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_interval_unavailable", "Rule evaluation interval is unsupported")
		return
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	end := now.Truncate(step)
	if item.LastEvaluation != "" {
		last, parseErr := time.Parse(time.RFC3339Nano, item.LastEvaluation)
		if parseErr != nil || last.After(now.Add(step)) || now.Sub(last) > 5*time.Minute {
			httpapi.Error(w, http.StatusServiceUnavailable, "rule_evaluation_stale", "Recent rule evaluation time is unavailable")
			return
		}
		end = last.UTC()
	}
	windowStart := end.Add(-dryRunWindow)
	preRollSteps := int((forDuration+step-1)/step) + 1
	start := windowStart.Add(-time.Duration(preRollSteps) * step)
	if int(end.Sub(start)/step)+1 > dryRunMaxPoints {
		httpapi.Error(w, http.StatusUnprocessableEntity, "dry_run_range_limit", "Rule interval and for duration exceed the bounded query range")
		return
	}
	series, err := query.QueryRange(r.Context(), item.EnvironmentID, item.Expr, start, end, step)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_query_unavailable", "Prometheus historical rule query is unavailable")
		return
	}
	// Query evaluation is a separate provider read. Resolve the same rule again
	// and recheck its current application/team/environment permission and owner.
	current, lookupError := s.findRule(r, r.PathValue("id"))
	if lookupError != nil {
		httpapi.Error(w, lookupError.status, lookupError.code, lookupError.message)
		return
	}
	currentOwner, lookupError := s.dryRunOwner(r.Context(), user, current)
	if lookupError != nil {
		httpapi.Error(w, lookupError.status, lookupError.code, lookupError.message)
		return
	}
	if currentOwner != owner || !sameDryRunRule(item, current) {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_scope_changed", "Rule identity or scope changed during dry run")
		return
	}
	// Rule labels are added after expression evaluation. A trusted rule may
	// still produce vector elements outside its bound application/environment;
	// never count or silently discard those elements as an in-scope result.
	for _, row := range series {
		if row.Labels["environment"] != item.EnvironmentID || row.Labels["application"] != item.ApplicationID {
			httpapi.Error(w, http.StatusServiceUnavailable, "rule_query_scope_unavailable", "Historical rule query returned unscoped series")
			return
		}
	}
	windows, err := firingWindows(series, start, windowStart, end, step, forDuration)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_query_invalid", "Prometheus historical rule samples are invalid or exceed the bound")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"would_fire": len(windows), "windows": windows})
}

func (s *Service) dryRunOwner(ctx context.Context, user core.User, item Detail) (Application, *ruleLookupError) {
	allowed, status, err := s.authorizedApplications(ctx, user, []string{item.EnvironmentID})
	if err != nil {
		return Application{}, &ruleLookupError{status, errorCode(status), "Alert rule authorization is unavailable"}
	}
	app, ok := allowed[item.EnvironmentID][item.ApplicationID]
	if !ok {
		return Application{}, &ruleLookupError{http.StatusNotFound, "not_found", "Alert rule not found"}
	}
	return app, nil
}

func sameDryRunRule(a, b Detail) bool {
	return a.ID == b.ID && a.EnvironmentID == b.EnvironmentID && a.ApplicationID == b.ApplicationID &&
		a.Expr == b.Expr && a.ForSeconds == b.ForSeconds && a.evaluationInterval == b.evaluationInterval &&
		reflect.DeepEqual(a.Labels, b.Labels) && reflect.DeepEqual(a.Annotations, b.Annotations)
}

// Each returned vector element is active regardless of its numeric value.
// One uninterrupted run that reaches the requested for duration counts as one
// firing episode. Windows are UTC ISO intervals, one per series episode.
func firingWindows(series []RangeSeries, queryStart, windowStart, end time.Time, step, forDuration time.Duration) ([]string, error) {
	if len(series) > 100 || step <= 0 {
		return nil, errors.New("range series limit exceeded")
	}
	windows := []string{}
	needed := time.Duration((forDuration+step-1)/step) * step
	for _, seriesItem := range series {
		if len(seriesItem.Samples) > dryRunMaxPoints {
			return nil, errors.New("range sample limit exceeded")
		}
		var first, previous time.Time
		addRun := func() error {
			if first.IsZero() || previous.Before(first.Add(needed)) {
				return nil
			}
			firingAt := first.Add(needed)
			if firingAt.Before(windowStart) || firingAt.After(end) {
				return nil
			}
			if len(windows) == dryRunMaxWindows {
				return errors.New("firing window limit exceeded")
			}
			windows = append(windows, firingAt.UTC().Format(time.RFC3339Nano)+"/"+previous.UTC().Format(time.RFC3339Nano))
			return nil
		}
		for _, at := range seriesItem.Samples {
			// Prometheus serializes timestamps as floating-point seconds. Snap
			// sub-50 ms representation noise back onto the requested grid.
			if at.Before(queryStart.Add(-50*time.Millisecond)) || at.After(end.Add(50*time.Millisecond)) {
				return nil, errors.New("out of range sample")
			}
			index := (at.Sub(queryStart) + step/2) / step
			aligned := queryStart.Add(index * step)
			if drift := at.Sub(aligned); drift < -50*time.Millisecond || drift > 50*time.Millisecond {
				return nil, errors.New("off-grid sample")
			}
			if !previous.IsZero() && !aligned.After(previous) {
				return nil, errors.New("out of range or unordered sample")
			}
			if first.IsZero() || aligned.Sub(previous) != step {
				if err := addRun(); err != nil {
					return nil, err
				}
				first = aligned
			}
			previous = aligned
		}
		if err := addRun(); err != nil {
			return nil, err
		}
	}
	sort.Strings(windows)
	return windows, nil
}
