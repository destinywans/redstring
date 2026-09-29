package v3ruleassistant

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

type previewInput struct {
	ApplicationID string  `json:"application_id,omitempty"`
	Query         string  `json:"query"`
	Op            string  `json:"op"`
	Threshold     float64 `json:"threshold"`
	ForMinutes    int     `json:"for_minutes"`
	Range         string  `json:"range"`
}

type sampleWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type previewPoint struct {
	At    string  `json:"at"`
	Value float64 `json:"value"`
}

type sampledBacktest struct {
	Status             string         `json:"status"`
	From               string         `json:"from"`
	To                 string         `json:"to"`
	StepSeconds        int            `json:"step_seconds"`
	SampledCount       int            `json:"sampled_count"`
	HistoricalCount    *int           `json:"count"`
	AvgMinutes         *float64       `json:"avg_minutes"`
	MaxMinutes         *float64       `json:"max_minutes"`
	Overlaps           any            `json:"overlaps"`
	Noise              any            `json:"noise"`
	SampledWindows     []sampleWindow `json:"sampled_windows"`
	CompletenessNotice string         `json:"completeness_notice"`
}

type previewResult struct {
	Valid            bool             `json:"valid"`
	SeriesCount      int              `json:"series_count"`
	Source           string           `json:"source"`
	ApplicationID    string           `json:"application_id"`
	EnvironmentID    string           `json:"environment_id"`
	QueryID          string           `json:"query_id"`
	Unit             string           `json:"unit"`
	Range            string           `json:"range"`
	Values           []float64        `json:"values"`
	Points           []previewPoint   `json:"points"`
	Current          *float64         `json:"current"`
	Fires            any              `json:"fires"`
	TriggerIntervals []sampleWindow   `json:"trigger_intervals"`
	Backtest         *sampledBacktest `json:"backtest"`
	Route            any              `json:"route"`
	RouteStatus      string           `json:"route_status"`
	Similar          any              `json:"similar"`
	SimilarStatus    string           `json:"similar_status"`
	Coverage         string           `json:"coverage"`
}

func comparePreview(value, threshold float64, op string) bool {
	if op == ">" {
		return value > threshold
	}
	return value < threshold
}

func sampledWindows(samples []PreviewSample, threshold float64, op string, forMinutes int) ([]sampleWindow, error) {
	const maxWindows = 120
	result := []sampleWindow{}
	needed := (forMinutes + 4) / 5
	first := -1
	add := func(last int) error {
		if first < 0 || last-first < needed {
			return nil
		}
		if len(result) == maxWindows {
			return errors.New("sampled trigger window limit exceeded")
		}
		result = append(result, sampleWindow{
			Start: samples[first+needed].At.UTC().Format(time.RFC3339),
			End:   samples[last].At.UTC().Format(time.RFC3339),
		})
		return nil
	}
	for index, sample := range samples {
		if comparePreview(sample.Value, threshold, op) {
			if first < 0 {
				first = index
			}
		} else if first >= 0 {
			if err := add(index - 1); err != nil {
				return nil, err
			}
			first = -1
		}
	}
	if first >= 0 {
		if err := add(len(samples) - 1); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// PreviewRule reads one configured, application-isolated Prometheus source.
// Trigger intervals are evaluations on a 5-minute grid, not historical alert
// events or a count of all events between samples.
func (s *Service) PreviewRule(w http.ResponseWriter, r *http.Request) {
	env, ok := singleEnvironment(w, r)
	if !ok {
		return
	}
	var input previewInput
	if !httpapi.Decode(w, r, &input) {
		return
	}
	if !boundedText(input.Query, 2048) ||
		!boundedText(input.ApplicationID, 128) ||
		(input.Op != ">" && input.Op != "<") || math.IsNaN(input.Threshold) ||
		math.IsInf(input.Threshold, 0) || math.Abs(input.Threshold) > 1e9 ||
		input.ForMinutes < 0 || input.ForMinutes > 60 ||
		input.Range != "1h" && input.Range != "24h" && input.Range != "7d" {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_rule_preview", "Query, condition or range is invalid")
		return
	}
	tx, ok := s.beginAuthorized(w, r, env, input.ApplicationID, "view")
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	selected, ok := s.selectBinding(w, env, input.ApplicationID, "")
	if !ok {
		return
	}
	app := selected.ApplicationID
	binding, ok := s.draftBinding(w, env, app, input.Query)
	if !ok {
		return
	}
	querySHA := sha256.Sum256([]byte(input.Query))
	command, err := tx.Exec(r.Context(), `INSERT INTO v3_rule_preview_budget
 (actor_id,application_id,environment_id,next_allowed_at)
 VALUES($1,$2,$3,clock_timestamp()+interval '10 seconds')
 ON CONFLICT (actor_id,application_id,environment_id) DO UPDATE
 SET next_allowed_at=clock_timestamp()+interval '10 seconds'
 WHERE v3_rule_preview_budget.next_allowed_at<=clock_timestamp()`,
		httpapi.CurrentUser(r).ID, app, env)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_unavailable", "Preview budget is unavailable")
		return
	}
	if command.RowsAffected() != 1 {
		httpapi.Error(w, http.StatusTooManyRequests, "rule_preview_rate_limited", "Wait before another 7-day preview")
		return
	}
	if err = audit(r.Context(), tx, app, httpapi.CurrentUser(r).ID, "v3.alert_rule.preview.requested", binding.QueryID,
		map[string]any{"environment_id": env, "query_sha256": hex.EncodeToString(querySHA[:]),
			"step_seconds": 300, "range": input.Range}); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_unavailable", "Preview audit is unavailable")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_unavailable", "Preview budget could not be confirmed")
		return
	}
	const step = 5 * time.Minute
	end := s.now().Truncate(step)
	start := end.Add(-7 * 24 * time.Hour)
	series, err := s.Preview.QueryRangeDraft(r.Context(), binding, input.Query, start, end, step)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_upstream_unavailable", "Bound Prometheus preview is unavailable")
		return
	}
	// Recheck current session, live app mapping and grants after provider I/O.
	finalTx, ok := s.beginAuthorized(w, r, env, app, "view")
	if !ok {
		return
	}
	defer finalTx.Rollback(r.Context())
	current := false
	for _, candidate := range s.choices(env, app) {
		if candidate == binding {
			current = true
			break
		}
	}
	if !current {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_scope_changed", "Reviewed query binding changed during preview")
		return
	}
	unit := binding.Unit
	if input.Query != binding.Query {
		unit = "unknown"
	}
	result := previewResult{Valid: true, SeriesCount: len(series), Source: binding.Source,
		ApplicationID: app, EnvironmentID: env, QueryID: binding.QueryID, Unit: unit,
		Range: input.Range, Values: []float64{}, Points: []previewPoint{}, Fires: nil,
		TriggerIntervals: []sampleWindow{}, Route: nil, RouteStatus: "unknown", Similar: nil,
		SimilarStatus: "unknown", Coverage: "no_observed_series"}
	if len(series) == 1 {
		samples := series[0].Samples
		windows, e := sampledWindows(samples, input.Threshold, input.Op, input.ForMinutes)
		if e != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_window_limit", "Sampled trigger window limit exceeded")
			return
		}
		result.Coverage = "sampled_5m"
		result.TriggerIntervals = windows
		from := end.Add(-time.Hour)
		if input.Range == "24h" {
			from = end.Add(-24 * time.Hour)
		} else if input.Range == "7d" {
			from = start
		}
		for _, sample := range samples {
			if sample.At.Before(from) {
				continue
			}
			result.Values = append(result.Values, sample.Value)
			result.Points = append(result.Points, previewPoint{At: sample.At.UTC().Format(time.RFC3339), Value: sample.Value})
		}
		last := samples[len(samples)-1].Value
		result.Current = &last
		result.Backtest = &sampledBacktest{Status: "sampled", From: start.Format(time.RFC3339),
			To: end.Format(time.RFC3339), StepSeconds: 300, SampledCount: len(windows),
			SampledWindows: windows, CompletenessNotice: "Five-minute sampled evaluations; historical total and between-sample alerts are unknown"}
	}
	if err = audit(r.Context(), finalTx, app, httpapi.CurrentUser(r).ID, "v3.alert_rule.preview.observed", binding.QueryID,
		map[string]any{"environment_id": env, "query_sha256": hex.EncodeToString(querySHA[:]),
			"series_count": len(series), "sampled_intervals": len(result.TriggerIntervals), "coverage": result.Coverage}); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_unavailable", "Preview audit is unavailable")
		return
	}
	if err = finalTx.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_preview_unavailable", "Preview scope could not be confirmed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}
