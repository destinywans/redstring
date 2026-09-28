package v3alertmanager

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

// Source is a trusted, environment-bound Alertmanager client. The service
// still requires an exact environment matcher on each returned silence, so a
// shared Alertmanager cannot leak another environment's broad silence.
type Source interface {
	ListSilences(context.Context, string) ([]Silence, error)
	ListAlerts(context.Context, string) ([]Alert, error)
}

type Service struct {
	Source Source
	// EnvironmentLabels maps a V3 environment to the Alertmanager label used
	// for its exact scope. Values come from trusted server configuration.
	EnvironmentLabels map[string]string
	Now               func() time.Time
}

type silenceView struct {
	ID        string              `json:"id"`
	Matchers  []map[string]string `json:"matchers"`
	State     string              `json:"state"`
	Window    string              `json:"window"`
	Remaining *remainingView      `json:"remaining,omitempty"`
	Affects   int                 `json:"affects"`
	Creator   string              `json:"creator"`
	Reason    string              `json:"reason"`
}

type remainingView struct {
	Pct  int    `json:"pct"`
	Text string `json:"text"`
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/silences", "viewer", s.List)
	api.MarkV3Route("GET /api/v1/silences", "silences", "view")
	api.Register("GET /api/v1/inhibitions", "viewer", s.Inhibitions)
	api.MarkV3Route("GET /api/v1/inhibitions", "silences", "view")
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	filter := "active"
	if values, present := r.URL.Query()["state"]; present {
		if len(values) != 1 || (values[0] != "active" && values[0] != "planned" && values[0] != "expired" && values[0] != "all") {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_state", "Select one known silence state")
			return
		}
		filter = values[0]
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	if s == nil || s.Source == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unconfigured", "Alertmanager is not configured")
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	all := []silenceView{}
	unavailable := []string{}
	unscopedPresent := false
	for _, env := range scope.EnvironmentIDs {
		label := s.EnvironmentLabels[env]
		if label == "" {
			unavailable = append(unavailable, env)
			continue
		}
		silences, err := s.Source.ListSilences(r.Context(), env)
		if err != nil {
			unavailable = append(unavailable, env)
			continue
		}
		scoped := make([]Silence, 0, len(silences))
		for _, silence := range silences {
			switch classifySilence(silence, label, env, s.EnvironmentLabels) {
			case scopeOwn:
				scoped = append(scoped, silence)
			case scopeBroad:
				unscopedPresent = true
			}
		}
		alerts := []Alert{}
		if len(scoped) > 0 {
			alerts, err = s.Source.ListAlerts(r.Context(), env)
			if err != nil {
				unavailable = append(unavailable, env)
				continue
			}
		}
		for _, silence := range scoped {
			if len(all) == maxRecords {
				httpapi.Error(w, http.StatusServiceUnavailable, "silence_limit", "Silence result exceeds limit")
				return
			}
			all = append(all, projectSilence(silence, alerts, label, env, now, r.Header.Get("Accept-Language")))
		}
	}
	if len(unavailable) == len(scope.EnvironmentIDs) {
		httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "No authorized Alertmanager source is available")
		return
	}
	counts := map[string]int{"active": 0, "planned": 0, "expired": 0}
	items := []silenceView{}
	for _, item := range all {
		counts[item.State]++
		if filter == "all" || filter == item.State {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	status := http.StatusOK
	if len(unavailable) > 0 || unscopedPresent {
		status = http.StatusPartialContent
	}
	// A foreign exact-environment record never changes the response status or
	// coverage flag. Truly broad records only set a boolean, never a count.
	httpapi.WriteJSON(w, status, map[string]any{"items": items, "counts": counts, "partial": status == http.StatusPartialContent,
		"unavailable_environments": unavailable, "unscoped_present": unscopedPresent})
}

type recordScope uint8

const (
	scopeBroad recordScope = iota
	scopeOwn
	scopeOther
)

func classifySilence(s Silence, label, env string, labels map[string]string) recordScope {
	own := false
	other := false
	for _, matcher := range s.Matchers {
		if !matcher.IsEqual || matcher.IsRegex {
			continue
		}
		if matcher.Name == label {
			if matcher.Value == env {
				own = true
			} else {
				other = true
			}
		}
		for otherEnv, otherLabel := range labels {
			if otherEnv != env && matcher.Name == otherLabel && matcher.Value == otherEnv {
				other = true
			}
		}
	}
	if other {
		return scopeOther
	}
	if own {
		return scopeOwn
	}
	return scopeBroad
}

func projectSilence(s Silence, alerts []Alert, label, env string, now time.Time, language string) silenceView {
	state := s.Status.State
	if state == "pending" {
		state = "planned"
	}
	matchers := make([]map[string]string, 0, len(s.Matchers))
	for _, matcher := range s.Matchers {
		value := matcher.Value
		if matcher.IsRegex {
			value = "~ " + value
		} else if !matcher.IsEqual {
			value = "! " + value
		}
		matchers = append(matchers, map[string]string{"k": matcher.Name, "v": value})
	}
	view := silenceView{ID: s.ID, Matchers: matchers, State: state,
		Window:  s.StartsAt.Format("2006-01-02 15:04 -07:00") + " – " + s.EndsAt.Format("2006-01-02 15:04 -07:00"),
		Creator: s.CreatedBy, Reason: s.Comment}
	if state != "active" {
		return view
	}
	for _, alert := range alerts {
		if alert.Labels[label] != env {
			continue
		}
		for _, silenceID := range alert.Status.SilencedBy {
			if silenceID == s.ID {
				view.Affects++
				break
			}
		}
	}
	duration := s.EndsAt.Sub(s.StartsAt)
	if duration > 0 {
		pct := int(100 * now.Sub(s.StartsAt).Seconds() / duration.Seconds())
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		left := s.EndsAt.Sub(now)
		if left < 0 {
			left = 0
		}
		en := strings.HasPrefix(strings.ToLower(language), "en")
		text := "不到 1 分钟"
		if en {
			text = "< 1 m"
		}
		minutes := int(left.Minutes())
		if minutes >= 60 {
			if en {
				text = fmtDurationEN(minutes)
			} else {
				text = fmtDurationZH(minutes)
			}
		} else if minutes > 0 {
			if en {
				text = fmtInt(minutes) + " m"
			} else {
				text = fmtInt(minutes) + " 分"
			}
		}
		view.Remaining = &remainingView{Pct: pct, Text: text}
	}
	return view
}

func fmtDurationEN(minutes int) string { return fmtInt(minutes/60) + " h " + fmtInt(minutes%60) + " m" }
func fmtDurationZH(minutes int) string {
	return fmtInt(minutes/60) + " 小时 " + fmtInt(minutes%60) + " 分"
}
func fmtInt(n int) string { return strconv.Itoa(n) }
