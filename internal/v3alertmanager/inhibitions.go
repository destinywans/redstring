package v3alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

type InhibitionSource interface {
	ListInhibitionRules(context.Context, string) ([]InhibitionRule, error)
}

type matcherView struct {
	K  string `json:"k"`
	V  string `json:"v"`
	Op string `json:"-"`
}

type inhibitionView struct {
	ID     string        `json:"id"`
	Source []matcherView `json:"source"`
	Target []matcherView `json:"target"`
	Equal  []string      `json:"equal"`
	Stat   string        `json:"stat"`
	// `active` is intentionally omitted: /api/v2/status publishes configured
	// rules, not per-rule activity or historical hit counts. This endpoint is
	// partial until a trustworthy hit source exists.
}

var inhibitionMatcher = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)(=~|!~|!=|=)(.*)$`)
var ErrInvalidMatcher = errors.New("invalid Alertmanager inhibition matcher")

func (s *Service) Inhibitions(w http.ResponseWriter, r *http.Request) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	if s == nil || s.Source == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unconfigured", "Alertmanager is not configured")
		return
	}
	source, ok := s.Source.(InhibitionSource)
	if !ok {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_source_unavailable", "Inhibition configuration is unavailable")
		return
	}
	items := []inhibitionView{}
	unavailable := []string{}
	unscopedPresent := false
	for _, env := range scope.EnvironmentIDs {
		label := s.EnvironmentLabels[env]
		if label == "" {
			unavailable = append(unavailable, env)
			continue
		}
		rules, err := source.ListInhibitionRules(r.Context(), env)
		if err != nil {
			unavailable = append(unavailable, env)
			continue
		}
		for index, rule := range rules {
			src, err := ruleMatchers(rule.SourceMatchers, rule.SourceMatch, rule.SourceMatchRE)
			if err != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "invalid_inhibition_source", "Alertmanager inhibition configuration cannot be interpreted")
				return
			}
			dst, err := ruleMatchers(rule.TargetMatchers, rule.TargetMatch, rule.TargetMatchRE)
			if err != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "invalid_inhibition_source", "Alertmanager inhibition configuration cannot be interpreted")
				return
			}
			sourceScope := classifyMatchers(src, label, env, s.EnvironmentLabels)
			targetScope := classifyMatchers(dst, label, env, s.EnvironmentLabels)
			if sourceScope == scopeOther || targetScope == scopeOther {
				continue
			}
			if sourceScope != scopeOwn || targetScope != scopeOwn {
				unscopedPresent = true
				continue
			}
			if len(items) == maxRecords {
				httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_limit", "Inhibition result exceeds limit")
				return
			}
			bytes, _ := json.Marshal(struct {
				Environment string
				Index       int
				Rule        InhibitionRule
			}{env, index, rule})
			hash := sha256.Sum256(bytes)
			stat := "状态与命中数未接入"
			if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
				stat = "Activity and hits unavailable"
			}
			items = append(items, inhibitionView{ID: hex.EncodeToString(hash[:12]), Source: src, Target: dst, Equal: append([]string{}, rule.Equal...), Stat: stat})
		}
	}
	if len(unavailable) == len(scope.EnvironmentIDs) {
		httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "No authorized Alertmanager source is available")
		return
	}
	status := http.StatusPartialContent
	if len(items) == 0 && len(unavailable) == 0 && !unscopedPresent {
		status = http.StatusOK
	}
	httpapi.WriteJSON(w, status, map[string]any{"items": items, "partial": status == http.StatusPartialContent,
		"unavailable": []string{"activity", "hit_counts"}, "unavailable_environments": unavailable, "unscoped_present": unscopedPresent})
}

func ruleMatchers(values []string, exact, regex map[string]string) ([]matcherView, error) {
	result := make([]matcherView, 0, len(values)+len(exact)+len(regex))
	for _, raw := range values {
		parts := inhibitionMatcher.FindStringSubmatch(strings.TrimSpace(raw))
		if parts == nil {
			return nil, ErrInvalidMatcher
		}
		value := strings.TrimSpace(parts[3])
		if strings.HasPrefix(value, `"`) {
			parsed, err := strconv.Unquote(value)
			if err != nil {
				return nil, ErrInvalidMatcher
			}
			value = parsed
		}
		switch parts[2] {
		case "=~":
			value = "~ " + value
		case "!~":
			value = "!~ " + value
		case "!=":
			value = "! " + value
		}
		result = append(result, matcherView{K: parts[1], V: value, Op: parts[2]})
	}
	keys := make([]string, 0, len(exact))
	for key := range exact {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, matcherView{K: key, V: exact[key], Op: "="})
	}
	keys = keys[:0]
	for key := range regex {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, matcherView{K: key, V: "~ " + regex[key], Op: "=~"})
	}
	return result, nil
}

func classifyMatchers(values []matcherView, label, env string, labels map[string]string) recordScope {
	own := false
	other := false
	for _, matcher := range values {
		if matcher.Op != "=" {
			continue
		}
		if matcher.K == label {
			if matcher.V == env {
				own = true
			} else {
				other = true
			}
		}
		for otherEnv, otherLabel := range labels {
			if otherEnv != env && matcher.K == otherLabel && matcher.V == otherEnv {
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
