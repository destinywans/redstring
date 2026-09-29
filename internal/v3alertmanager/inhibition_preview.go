package v3alertmanager

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type inhibitionDraftInput struct {
	ApplicationID string              `json:"application_id"`
	Source        []routeDraftMatcher `json:"source"`
	Target        []routeDraftMatcher `json:"target"`
	Equal         []string            `json:"equal"`
}

type compiledInhibitionDraft struct {
	source, target  []policyMatcher
	effectiveSource []routeDraftMatcher
	effectiveTarget []routeDraftMatcher
}

var nonP1TargetRegex = regexp.MustCompile(`^P[234](\|P[234])*$`)

func compileInhibitionDraft(input inhibitionDraftInput, envLabel, env, appLabel string) (compiledInhibitionDraft, error) {
	if input.ApplicationID == "" || len(input.ApplicationID) > 128 || envLabel == "" ||
		!policyLabelName.MatchString(envLabel) || envLabel == "application" || appLabel == "" ||
		len(input.Source) == 0 || len(input.Source) > 20 || len(input.Target) == 0 || len(input.Target) > 20 ||
		len(input.Equal) == 0 || len(input.Equal) > 10 {
		return compiledInhibitionDraft{}, errors.New("invalid bounded inhibition rule")
	}
	result := compiledInhibitionDraft{}
	base := []routeDraftMatcher{{K: envLabel, Op: "=", V: env}, {K: "application", Op: "=", V: appLabel}}
	result.effectiveSource = append(append([]routeDraftMatcher{}, base...), input.Source...)
	result.effectiveTarget = append(append([]routeDraftMatcher{}, base...), input.Target...)
	for side, values := range [][]routeDraftMatcher{result.effectiveSource, result.effectiveTarget} {
		seen := make(map[string]bool)
		compiled := make([]policyMatcher, 0, len(values))
		for i, value := range values {
			if value.K == "" || len(value.V) == 0 || len(value.V) > 256 ||
				strings.ContainsAny(value.V, "\x00\r\n") || seen[value.K] {
				return compiledInhibitionDraft{}, errors.New("invalid or duplicate matcher")
			}
			if i >= 2 && (value.K == envLabel || value.K == "application") {
				return compiledInhibitionDraft{}, errors.New("scope matchers are server owned")
			}
			if side == 1 && value.K == "severity" && !allowedInhibitionTargetSeverity(value) {
				return compiledInhibitionDraft{}, errors.New("target severity must exclude P1")
			}
			matcher, err := newPolicyMatcher(value.K, value.Op, value.V)
			if err != nil {
				return compiledInhibitionDraft{}, err
			}
			seen[value.K] = true
			compiled = append(compiled, matcher)
		}
		if side == 1 && !seen["severity"] {
			return compiledInhibitionDraft{}, errors.New("target severity is required")
		}
		if side == 0 {
			result.source = compiled
		} else {
			result.target = compiled
		}
	}
	seenEqual := make(map[string]bool)
	for _, name := range input.Equal {
		if !policyLabelName.MatchString(name) || name == envLabel || name == "application" ||
			name == "severity" || seenEqual[name] {
			return compiledInhibitionDraft{}, errors.New("invalid equal label")
		}
		seenEqual[name] = true
	}
	return result, nil
}

func allowedInhibitionTargetSeverity(m routeDraftMatcher) bool {
	if m.Op == "=" {
		return m.V == "P2" || m.V == "P3" || m.V == "P4"
	}
	if m.Op != "=~" || !nonP1TargetRegex.MatchString(m.V) {
		return false
	}
	seen := make(map[string]bool)
	for _, value := range strings.Split(m.V, "|") {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func inhibitionMatches(labels map[string]string, matchers []policyMatcher) bool {
	for _, matcher := range matchers {
		if !matcher.matches(labels) {
			return false
		}
	}
	return true
}

func (s *Service) inhibitionManager(r *http.Request, env string) (int, error) {
	if s == nil || s.Access == nil || s.Catalog == nil {
		return http.StatusServiceUnavailable, errors.New("inhibition authorization unavailable")
	}
	user := httpapi.CurrentUser(r)
	for _, action := range []string{"view", "manage"} {
		if err := s.Access.Authorize(r.Context(), user, rbac.V3Decision{Module: "silences", Action: action, EnvironmentID: env}); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				return http.StatusForbidden, err
			}
			return http.StatusServiceUnavailable, err
		}
	}
	admin, err := s.Catalog.EnvironmentAdmin(r.Context(), user, env)
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	if !admin {
		return http.StatusForbidden, store.ErrForbidden
	}
	return http.StatusOK, nil
}

// PreviewInhibition simulates potential suppression against a complete,
// environment-bound seven-day history. It does not claim actual historical
// Alertmanager inhibition, notification delivery, or running config changes.
func (s *Service) PreviewInhibition(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment for inhibition preview")
		return
	}
	env := scope.EnvironmentIDs[0]
	if status, err := s.inhibitionManager(r, env); err != nil {
		httpapi.Error(w, status, "inhibition_scope_unavailable", "Inhibition access is unavailable or denied")
		return
	}
	var input inhibitionDraftInput
	if !httpapi.Decode(w, r, &input) {
		return
	}
	appLabel, app, status, err := s.routeDraftApplication(r.Context(), env, input.ApplicationID)
	if err != nil {
		httpapi.Error(w, status, "application_not_found", "Bound application is unavailable")
		return
	}
	envLabel := s.EnvironmentLabels[env]
	compiled, err := compileInhibitionDraft(input, envLabel, env, appLabel)
	if err != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_inhibition_draft", "Provide bounded source, non-P1 target, and equal matchers")
		return
	}
	historySource := s.InhibitionHistory
	configSource, configConnected := s.Source.(inhibitionObservationSource)
	if historySource == nil || !configConnected || configSource == nil {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "A complete seven-day history and current configuration source are required")
		return
	}
	from, through := s.inhibitionWindow()
	observation, err := configSource.ObserveInhibitions(r.Context(), env)
	if err != nil {
		writeInhibitionSourceError(w, err)
		return
	}
	if len(observation.ConfigSHA256) != 64 || observation.ObservedAt.IsZero() || observation.Rules == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_source_unavailable", "Current inhibition configuration is incomplete")
		return
	}
	history, err := historySource.ReadInhibitionHistory(r.Context(), env, from, through)
	if err != nil {
		writeInhibitionSourceError(w, err)
		return
	}
	if !validInhibitionHistory(history, env, envLabel, from, through) {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_history_incomplete", "Seven-day alert coverage cannot be proved")
		return
	}
	if s.EnvironmentLabels[env] != envLabel || s.ApplicationBindings[env][appLabel] != app.ID {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_scope_changed", "Inhibition scope changed during history read")
		return
	}
	if err := inhibitionFinalAuthority(r, s, env, app); err != nil {
		writeInhibitionAuthorityError(w, err)
		return
	}
	count, samples := evaluateInhibitionHistory(history.Alerts, compiled, input.Equal, from, through)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"environment_id": env, "application_id": app.ID, "config_revision": "sha256:" + observation.ConfigSHA256,
		"window_start": from.Format(time.RFC3339Nano), "window_end": through.Format(time.RFC3339Nano),
		"basis": "complete_history_simulation", "potentially_suppressed": count, "sample": samples,
		"actual_inhibition_hits": nil, "history_complete": true,
	})
}

func (s *Service) inhibitionWindow() (time.Time, time.Time) {
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	return now.Add(-7 * 24 * time.Hour), now
}

func validInhibitionHistory(history InhibitionHistory, env, envLabel string, from, through time.Time) bool {
	if !history.Complete || history.EnvironmentID != env || history.SourceID == "" ||
		history.CoverageStart.IsZero() || history.CoverageEnd.IsZero() ||
		!history.CoverageStart.Before(history.CoverageEnd) ||
		history.CoverageStart.After(from) || history.CoverageEnd.Before(through) ||
		history.Alerts == nil || len(history.Alerts) > maxRecords {
		return false
	}
	seen := make(map[string]bool)
	for _, alert := range history.Alerts {
		if alert.ID == "" || len(alert.ID) > 256 || seen[alert.ID] || !alert.FiringInterval ||
			alert.StartsAt.IsZero() || alert.StartsAt.After(through) || len(alert.Labels) == 0 || len(alert.Labels) > 100 ||
			alert.Labels[envLabel] != env || alert.Labels["application"] == "" ||
			alert.Labels["severity"] == "" || alert.EndsAt != nil && !alert.EndsAt.After(alert.StartsAt) {
			return false
		}
		seen[alert.ID] = true
		for name, value := range alert.Labels {
			if !alertLabelName.MatchString(name) || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
				return false
			}
		}
	}
	return true
}

func evaluateInhibitionHistory(alerts []InhibitionHistoryAlert, rule compiledInhibitionDraft, equal []string,
	from, through time.Time) (int, []string) {
	count := 0
	samples := make([]string, 0, 10)
	seenNames := make(map[string]bool)
	for _, target := range alerts {
		if !inhibitionMatches(target.Labels, rule.target) || !overlapsInhibitionWindow(target, from, through) {
			continue
		}
		matched := false
		for _, source := range alerts {
			if source.ID == target.ID || !inhibitionMatches(source.Labels, rule.source) ||
				!overlapsInhibitionAlerts(source, target, from, through) ||
				inhibitionMatches(source.Labels, rule.target) && inhibitionMatches(target.Labels, rule.source) {
				continue
			}
			equalLabels := true
			for _, name := range equal {
				// Missing and empty labels are equivalent in Alertmanager.
				if source.Labels[name] != target.Labels[name] {
					equalLabels = false
					break
				}
			}
			if equalLabels {
				matched = true
				break
			}
		}
		if matched {
			count++
			name := target.Labels["alertname"]
			if name != "" && !seenNames[name] {
				seenNames[name] = true
				samples = append(samples, name)
			}
		}
	}
	sort.Strings(samples)
	if len(samples) > 10 {
		samples = samples[:10]
	}
	return count, samples
}

func overlapsInhibitionWindow(alert InhibitionHistoryAlert, from, through time.Time) bool {
	return alert.StartsAt.Before(through) && (alert.EndsAt == nil || alert.EndsAt.After(from))
}

func overlapsInhibitionAlerts(a, b InhibitionHistoryAlert, from, through time.Time) bool {
	if !overlapsInhibitionWindow(a, from, through) || !overlapsInhibitionWindow(b, from, through) {
		return false
	}
	start := a.StartsAt
	if b.StartsAt.After(start) {
		start = b.StartsAt
	}
	if from.After(start) {
		start = from
	}
	end := through
	if a.EndsAt != nil && a.EndsAt.Before(end) {
		end = *a.EndsAt
	}
	if b.EndsAt != nil && b.EndsAt.Before(end) {
		end = *b.EndsAt
	}
	return start.Before(end)
}

func writeInhibitionSourceError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotConfigured) {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "An inhibition read source is not connected")
		return
	}
	httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_source_unavailable", "Inhibition source is unavailable or incomplete")
}
