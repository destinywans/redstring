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
	"github.com/wans/aiops-canvas/internal/rbac"
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
	if s == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_scope_unavailable", "Silence authorization is unavailable")
		return
	}
	allowedApps, admin, readable, ok := s.inhibitionAccess(w, r, scope.EnvironmentIDs)
	if !ok {
		return
	}
	if s.Source == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unconfigured", "Alertmanager is not configured")
		return
	}
	source, ok := s.Source.(InhibitionSource)
	if !ok {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_source_unavailable", "Inhibition configuration is unavailable")
		return
	}
	items := []inhibitionView{}
	unscopedPresent := false
	for _, env := range scope.EnvironmentIDs {
		if !readable[env] {
			continue
		}
		label := s.EnvironmentLabels[env]
		if label == "" {
			httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "An authorized Alertmanager source is unavailable")
			return
		}
		rules, err := source.ListInhibitionRules(r.Context(), env)
		if err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "An authorized Alertmanager source is unavailable")
			return
		}
		if rules == nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "invalid_inhibition_source", "Alertmanager inhibition configuration is unavailable")
			return
		}
		if len(rules) > maxRecords {
			httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_limit", "Inhibition source exceeds limit")
			return
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
			if targetScope == scopeOther {
				continue
			}
			if sourceScope == scopeOther {
				// A foreign source can still inhibit a target in this
				// environment. Its rule is hidden, but administrators need
				// to know the visible result is incomplete.
				if admin[env] {
					unscopedPresent = true
				}
				continue
			}
			if sourceScope != scopeOwn || targetScope != scopeOwn {
				if admin[env] {
					unscopedPresent = true
				}
				continue
			}
			sourceApp, sourceExact, sourceAmbiguous := exactInhibitionApplication(src)
			targetApp, targetExact, targetAmbiguous := exactInhibitionApplication(dst)
			if sourceAmbiguous || targetAmbiguous {
				if admin[env] {
					unscopedPresent = true
				}
				continue
			}
			if (sourceExact && !allowedApps[env][sourceApp]) || (targetExact && !allowedApps[env][targetApp]) {
				// A stale or hidden application binding cannot establish
				// ownership. Only an environment administrator may learn
				// that such a configured rule was omitted.
				if admin[env] {
					unscopedPresent = true
				}
				continue
			}
			if (!sourceExact || !targetExact) && !admin[env] {
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
	// Configured rules have no trustworthy per-rule activity or hit source.
	httpapi.WriteJSON(w, http.StatusPartialContent, map[string]any{"items": items, "partial": true,
		"unavailable": []string{"activity", "hit_counts"}, "unavailable_environments": []string{}, "unscoped_present": unscopedPresent})
}

// Resolve current persisted application ownership before inspecting a shared
// upstream. An environment-wide reader also needs the current manage grant
// to see rules that can cross application boundaries.
func (s *Service) inhibitionAccess(w http.ResponseWriter, r *http.Request, environments []string) (map[string]map[string]bool, map[string]bool, map[string]bool, bool) {
	if s.Catalog == nil || s.Access == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_scope_unavailable", "Silence authorization is unavailable")
		return nil, nil, nil, false
	}
	apps, err := s.Catalog.Applications(r.Context(), environments)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application catalog is unavailable")
		return nil, nil, nil, false
	}
	current := make(map[string]map[string]Application)
	for _, app := range apps {
		if app.ID == "" || !containsSilenceEnvironment(environments, app.EnvironmentID) {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application catalog contains invalid ownership")
			return nil, nil, nil, false
		}
		if current[app.EnvironmentID] == nil {
			current[app.EnvironmentID] = make(map[string]Application)
		}
		if _, duplicate := current[app.EnvironmentID][app.ID]; duplicate {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application identity is not unique in its environment")
			return nil, nil, nil, false
		}
		current[app.EnvironmentID][app.ID] = app
	}
	user := httpapi.CurrentUser(r)
	allowedApps := make(map[string]map[string]bool)
	admin := make(map[string]bool)
	readable := make(map[string]bool)
	anyReadable := false
	for _, env := range environments {
		allowedApps[env] = make(map[string]bool)
		for labelValue, id := range s.ApplicationBindings[env] {
			app, active := current[env][id]
			if labelValue == "" || id == "" || !active {
				continue
			}
			decision := rbac.V3Decision{Module: "silences", Action: "view", EnvironmentID: env, ApplicationID: app.ID}
			allowed, authErr := silenceAllowed(r.Context(), s.Access, user, decision)
			if authErr == nil && !allowed && app.OwnerTeamID != "" {
				decision.ApplicationID = ""
				decision.TeamID = app.OwnerTeamID
				allowed, authErr = silenceAllowed(r.Context(), s.Access, user, decision)
			}
			if authErr != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
				return nil, nil, nil, false
			}
			allowedApps[env][labelValue] = allowed
			readable[env] = readable[env] || allowed
		}
		broadAllowed, authErr := silenceAllowed(r.Context(), s.Access, user,
			rbac.V3Decision{Module: "silences", Action: "view", EnvironmentID: env})
		if authErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
			return nil, nil, nil, false
		}
		grant, catalogErr := s.Catalog.EnvironmentAdmin(r.Context(), user, env)
		if catalogErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Silence grants are unavailable")
			return nil, nil, nil, false
		}
		admin[env] = broadAllowed && grant
		readable[env] = readable[env] || admin[env]
		anyReadable = anyReadable || readable[env]
	}
	if !anyReadable {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Silence access is denied")
		return nil, nil, nil, false
	}
	return allowedApps, admin, readable, true
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
	mixed := false
	for _, matcher := range values {
		if matcher.K == label {
			switch {
			case matcher.Op != "=":
				mixed = true
			case matcher.V == env:
				own = true
			default:
				other = true
			}
		}
		for otherEnv, otherLabel := range labels {
			if otherEnv != env && matcher.K == otherLabel {
				if matcher.Op == "=" && matcher.V == otherEnv {
					other = true
				} else if otherLabel != label {
					mixed = true
				}
			}
		}
	}
	if other {
		return scopeOther
	}
	if mixed {
		return scopeBroad
	}
	if own {
		return scopeOwn
	}
	return scopeBroad
}

// A rule can be attributed to one application only when every constraint on
// that label is the same positive equality. Regex and negative constraints
// can overlap another team's application even alongside an exact matcher.
func exactInhibitionApplication(matchers []matcherView) (string, bool, bool) {
	id := ""
	otherConstraint := false
	for _, matcher := range matchers {
		if matcher.K != "application" {
			continue
		}
		if matcher.Op != "=" || matcher.V == "" {
			otherConstraint = true
			continue
		}
		if id != "" && id != matcher.V {
			return "", false, true
		}
		id = matcher.V
	}
	if otherConstraint {
		return "", false, true
	}
	return id, id != "", false
}
