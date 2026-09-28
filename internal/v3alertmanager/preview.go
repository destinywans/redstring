package v3alertmanager

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
)

// The preview counts a fresh Alertmanager snapshot in the selected environment.
// It never creates a silence. A future create path must itself append and
// enforce exact environment and application scope before any upstream write.
type previewMatcher struct {
	K  string `json:"k"`
	Op string `json:"op"`
	V  string `json:"v"`
}

type compiledPreviewMatcher struct {
	previewMatcher
	pattern *regexp.Regexp
}

var alertLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func compilePreviewMatchers(input []previewMatcher, environmentLabel, environment string) ([]compiledPreviewMatcher, string, error) {
	if len(input) == 0 || len(input) > 20 {
		return nil, "", errors.New("expected 1 to 20 matchers")
	}
	result := make([]compiledPreviewMatcher, 0, len(input))
	application := ""
	for _, m := range input {
		if !alertLabelName.MatchString(m.K) || len(m.V) == 0 || len(m.V) > 256 ||
			(m.Op != "=" && m.Op != "!=" && m.Op != "=~" && m.Op != "!~") {
			return nil, "", errors.New("invalid matcher")
		}
		// Scope labels cannot be broadened by a negative or regex matcher. The
		// selected environment is an implicit positive matcher even when the UI
		// does not send it.
		if m.K == environmentLabel && (m.Op != "=" || m.V != environment) {
			return nil, "", errors.New("environment matcher conflicts with selected environment")
		}
		if m.K == "application" {
			if m.Op != "=" || application != "" && application != m.V {
				return nil, "", errors.New("application matcher must be exact")
			}
			application = m.V
		}
		compiled := compiledPreviewMatcher{previewMatcher: m}
		if m.Op == "=~" || m.Op == "!~" {
			pattern, err := regexp.Compile("^(?:" + m.V + ")$")
			if err != nil {
				return nil, "", errors.New("invalid regular expression")
			}
			compiled.pattern = pattern
		}
		result = append(result, compiled)
	}
	return result, application, nil
}

func previewMatches(labels map[string]string, matchers []compiledPreviewMatcher) bool {
	for _, m := range matchers {
		value := labels[m.K]
		match := false
		switch m.Op {
		case "=", "!=":
			match = value == m.V
		case "=~", "!~":
			match = m.pattern.MatchString(value)
		}
		if m.Op == "!=" || m.Op == "!~" {
			match = !match
		}
		if !match {
			return false
		}
	}
	return true
}

func (s *Service) Preview(w http.ResponseWriter, r *http.Request) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment for a silence preview")
		return
	}
	if s == nil || s.Source == nil || s.Catalog == nil || s.Access == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_preview_unavailable", "Silence preview source or policy is unavailable")
		return
	}
	env := scope.EnvironmentIDs[0]
	envLabel := s.EnvironmentLabels[env]
	if envLabel == "" {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_scope_unavailable", "Environment label binding is unavailable")
		return
	}
	var input struct {
		Matchers []previewMatcher `json:"matchers"`
	}
	if !httpapi.Decode(w, r, &input) {
		return
	}
	matchers, providerApp, err := compilePreviewMatchers(input.Matchers, envLabel, env)
	if err != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_matchers", err.Error())
		return
	}
	user := httpapi.CurrentUser(r)
	if providerApp == "" {
		// Without an exact application matcher, this request could affect any
		// application in the environment, including one with no current alerts.
		// Require the environment-wide view AND manage grant.
		allowed, authErr := silenceAllowed(r.Context(), s.Access, user,
			rbac.V3Decision{Module: "silences", Action: "view", EnvironmentID: env})
		if authErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
			return
		}
		admin, catalogErr := s.Catalog.EnvironmentAdmin(r.Context(), user, env)
		if catalogErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Silence grants are unavailable")
			return
		}
		if !allowed || !admin {
			httpapi.Error(w, http.StatusForbidden, "silence_scope_forbidden", "Exact application matcher or environment-wide silence grant required")
			return
		}
	} else if !s.previewApplicationAllowed(r.Context(), user, env, providerApp, w) {
		return
	}
	alerts, err := s.Source.ListAlerts(r.Context(), env)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "alertmanager_unavailable", "Current Alertmanager alerts are unavailable")
		return
	}
	affects := 0
	inEnvironment := 0
	samples := map[string]bool{}
	for _, alert := range alerts {
		if alert.Labels[envLabel] != env {
			continue
		}
		inEnvironment++
		if inEnvironment > maxRecords {
			httpapi.Error(w, http.StatusServiceUnavailable, "alert_limit", "Current alert snapshot exceeds limit")
			return
		}
		if !previewMatches(alert.Labels, matchers) {
			continue
		}
		affects++
		name := alert.Labels["alertname"]
		if name != "" {
			samples[name] = true
		}
	}
	names := make([]string, 0, len(samples))
	for name := range samples {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 5 {
		names = names[:5]
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"affects": affects, "sample": names})
}

func (s *Service) previewApplicationAllowed(ctx context.Context, user core.User, env, providerApp string, w http.ResponseWriter) bool {
	appID := s.ApplicationBindings[env][providerApp]
	if appID == "" {
		httpapi.Error(w, http.StatusNotFound, "application_not_found", "Application is not bound to this alert source")
		return false
	}
	apps, err := s.Catalog.Applications(ctx, []string{env})
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application catalog is unavailable")
		return false
	}
	var app Application
	found := false
	for _, current := range apps {
		if current.ID == appID && current.EnvironmentID == env {
			if found {
				httpapi.Error(w, http.StatusServiceUnavailable, "silence_catalog_unavailable", "Application identity is ambiguous")
				return false
			}
			app, found = current, true
		}
	}
	if !found {
		httpapi.Error(w, http.StatusNotFound, "application_not_found", "Application is not active in this environment")
		return false
	}
	decision := rbac.V3Decision{Module: "silences", Action: "view", EnvironmentID: env, ApplicationID: app.ID}
	allowed, err := silenceAllowed(ctx, s.Access, user, decision)
	if err == nil && !allowed && app.OwnerTeamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = app.OwnerTeamID
		allowed, err = silenceAllowed(ctx, s.Access, user, decision)
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "silence_authorization_unavailable", "Silence authorization is unavailable")
		return false
	}
	if !allowed {
		// Do not distinguish a hidden bound application from an unknown
		// provider label. The route-level object marker has no application
		// identity until this function resolves the binding.
		httpapi.Error(w, http.StatusNotFound, "application_not_found", "Application is not bound to this alert source")
		return false
	}
	return true
}
