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
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type InhibitionSource interface {
	ListInhibitionRules(context.Context, string) ([]InhibitionRule, error)
}

type matcherView struct {
	K             string `json:"k"`
	V             string `json:"v,omitempty"`
	Op            string `json:"op"`
	ValueRedacted bool   `json:"value_redacted,omitempty"`
}

type inhibitionView struct {
	ID     string        `json:"id"`
	Source []matcherView `json:"source"`
	Target []matcherView `json:"target"`
	Equal  []string      `json:"equal"`
	Stat   string        `json:"stat"`
	Active any           `json:"active"`
	Hits7D any           `json:"hits_7d"`
	// Status publishes configured rules, not per-rule activity or hit counts.
}

var inhibitionMatcher = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)[ \t\n]*(=~|!~|!=|=)[ \t\n]*(?s:(.*))$`)
var ErrInvalidMatcher = errors.New("invalid Alertmanager inhibition matcher")

func (s *Service) Inhibitions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	if s == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_scope_unavailable", "Silence authorization is unavailable")
		return
	}
	allowedApps, admin, readable, ok := s.inhibitionAccess(w, r, scope.EnvironmentIDs, true)
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
	revisions := make(map[string]string)
	observedAt := make(map[string]string)
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
		var rules []InhibitionRule
		var err error
		if observing, supported := s.Source.(inhibitionObservationSource); supported {
			observation, observeErr := observing.ObserveInhibitions(r.Context(), env)
			err = observeErr
			rules = observation.Rules
			if err == nil {
				if len(observation.ConfigSHA256) != 64 || observation.ObservedAt.IsZero() {
					httpapi.Error(w, http.StatusServiceUnavailable, "invalid_inhibition_source", "Alertmanager inhibition configuration is incomplete")
					return
				}
				revisions[env] = "sha256:" + observation.ConfigSHA256
				observedAt[env] = observation.ObservedAt.UTC().Format(time.RFC3339Nano)
			}
		} else {
			rules, err = source.ListInhibitionRules(r.Context(), env)
		}
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
			items = append(items, inhibitionView{ID: hex.EncodeToString(hash[:12]), Source: redactedInhibitionMatchers(src),
				Target: redactedInhibitionMatchers(dst), Equal: append([]string{}, rule.Equal...), Stat: stat})
		}
	}
	// Re-read current ownership and grants after the shared upstream access.
	// A changed projection is never returned under an earlier grant.
	currentApps, currentAdmin, currentReadable, ok := s.inhibitionAccess(w, r, scope.EnvironmentIDs, false)
	if !ok {
		return
	}
	if !equalInhibitionAccess(allowedApps, admin, readable, currentApps, currentAdmin, currentReadable) {
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_scope_changed", "Inhibition access changed during configuration read")
		return
	}
	if err := inhibitionSessionCurrent(r, s); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_scope_unavailable", "Inhibition session could not be confirmed")
		}
		return
	}
	if len(revisions) == 1 && len(scope.EnvironmentIDs) == 1 {
		w.Header().Set("ETag", `"`+revisions[scope.EnvironmentIDs[0]]+`"`)
	}
	// Configured rules have no trustworthy per-rule activity or hit source.
	unavailable := []string{"activity", "hit_counts"}
	if len(scope.EnvironmentIDs) == 1 && !s.inhibitionDraftsReady(scope.EnvironmentIDs[0]) {
		unavailable = append(unavailable, "inhibition_drafts")
	}
	httpapi.WriteJSON(w, http.StatusPartialContent, map[string]any{"items": items, "partial": true,
		"unavailable": unavailable, "unavailable_environments": []string{}, "unscoped_present": unscopedPresent,
		"config_revisions": revisions, "observed_at": observedAt, "empty_reason": nil})
}

func redactedInhibitionMatchers(values []matcherView) []matcherView {
	redacted := make([]matcherView, len(values))
	for i, value := range values {
		redacted[i] = matcherView{K: value.K, Op: value.Op, ValueRedacted: true}
	}
	return redacted
}

func equalInhibitionAccess(a map[string]map[string]bool, b, c map[string]bool,
	x map[string]map[string]bool, y, z map[string]bool) bool {
	if len(a) != len(x) || len(b) != len(y) || len(c) != len(z) {
		return false
	}
	for env, apps := range a {
		if len(apps) != len(x[env]) || b[env] != y[env] || c[env] != z[env] {
			return false
		}
		for app, allowed := range apps {
			currentAllowed, present := x[env][app]
			if !present || currentAllowed != allowed {
				return false
			}
		}
	}
	return true
}

func inhibitionSessionCurrent(r *http.Request, s *Service) error {
	db, _ := routeTestAuthority(s.Catalog, s.Access)
	if db == nil {
		if _, production := s.Source.(*Client); production {
			return store.ErrSessionRevoked
		}
		return nil
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	tx, err := db.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	user := httpapi.CurrentUser(r)
	if err := store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		return err
	}
	var unexpired bool
	err = tx.QueryRow(r.Context(), `SELECT expires_at>clock_timestamp() FROM sessions WHERE token_hash=$1 AND user_id=$2`,
		store.Hash(cookie.Value), user.ID).Scan(&unexpired)
	if err != nil {
		return err
	}
	if !unexpired {
		return store.ErrSessionRevoked
	}
	return tx.Commit(r.Context())
}

// Resolve current persisted application ownership before inspecting a shared
// upstream. An environment-wide reader also needs the current manage grant
// to see rules that can cross application boundaries.
// With firstRead, an empty readable scope is answered as an empty list
// (PA-09); the recheck after the upstream read keeps failing closed.
func (s *Service) inhibitionAccess(w http.ResponseWriter, r *http.Request, environments []string, firstRead bool) (map[string]map[string]bool, map[string]bool, map[string]bool, bool) {
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
	if !anyReadable && firstRead {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": []inhibitionView{}, "partial": false,
			"unavailable": []string{}, "unavailable_environments": []string{}, "unscoped_present": false,
			"config_revisions": map[string]string{}, "observed_at": map[string]string{}, "empty_reason": "no_authorized_target"})
		return nil, nil, nil, false
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
		} else if strings.ContainsAny(value, "{},\"'`\\\t\n\r !~=") {
			// A string may contain multiple Alertmanager matchers. Never
			// treat an unsupported composition as one label value: it can
			// hide an environment or application constraint.
			return nil, ErrInvalidMatcher
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
