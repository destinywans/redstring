package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// V3InstanceConfig is process configuration, not a client-selected tenant.
// Production and non-production instances must use different databases and secrets.
type V3InstanceConfig struct {
	Tier           string
	DefaultEnv     string
	EnvironmentIDs []string
}

type V3EnvironmentScope struct {
	Tier           string
	EnvironmentIDs []string
	All            bool
}

type v3EnvironmentContextKey struct{}

func V3ScopeFromContext(ctx context.Context) (V3EnvironmentScope, bool) {
	scope, ok := ctx.Value(v3EnvironmentContextKey{}).(V3EnvironmentScope)
	return scope, ok
}

// V3EnvironmentAuthorizer checks grants for each selected environment. It must
// resolve the authenticated user's current persisted roles, not a cookie claim.
type V3EnvironmentAuthorizer interface {
	AuthorizeEnvironment(context.Context, core.User, string) error
}

type V3SessionLookup func(context.Context, string) (core.User, error)

var ErrInvalidV3Instance = errors.New("invalid V3 instance configuration")

// NewV3EnvironmentHandler guards every V3 API operation before its handler.
// The returned scope is the only environment set that handlers and async work
// should pass to queries or tools. A headerless SSE, audit export or upload
// content GET has no request scope: its handler must bind to persisted scope
// and reauthorize it.
func NewV3EnvironmentHandler(cfg V3InstanceConfig, authorizer V3EnvironmentAuthorizer, session V3SessionLookup, next http.Handler) (http.Handler, error) {
	if authorizer == nil || session == nil || next == nil || (cfg.Tier != "production" && cfg.Tier != "nonprod") {
		return nil, ErrInvalidV3Instance
	}
	known := make(map[string]struct{}, len(cfg.EnvironmentIDs))
	for _, id := range cfg.EnvironmentIDs {
		if id == "" || strings.TrimSpace(id) != id || id == "all" {
			return nil, ErrInvalidV3Instance
		}
		if _, duplicate := known[id]; duplicate {
			return nil, ErrInvalidV3Instance
		}
		known[id] = struct{}{}
	}
	if cfg.Tier == "production" {
		if cfg.DefaultEnv != "prod" || len(known) != 1 {
			return nil, ErrInvalidV3Instance
		}
		if _, ok := known["prod"]; !ok {
			return nil, ErrInvalidV3Instance
		}
	} else {
		if _, ok := known[cfg.DefaultEnv]; !ok || cfg.DefaultEnv == "prod" {
			return nil, ErrInvalidV3Instance
		}
		if _, ok := known["prod"]; ok {
			return nil, ErrInvalidV3Instance
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") || v3EnvironmentExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("aiops_session")
		if err != nil {
			Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
			return
		}
		user, err := session(r.Context(), cookie.Value)
		if err != nil || user.ID == "" || user.Disabled {
			Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
			return
		}
		values := r.Header.Values("X-AIOps-Environment")
		if len(values) > 1 || (len(values) == 1 && (values[0] == "" || strings.TrimSpace(values[0]) != values[0] || strings.Contains(values[0], ","))) {
			Error(w, http.StatusUnprocessableEntity, "invalid_environment", "Select one known environment")
			return
		}
		env := ""
		if len(values) == 1 {
			env = values[0]
		}
		if env == "" {
			if cfg.Tier == "nonprod" && !v3PersistedScopeRead(r) {
				Error(w, http.StatusUnprocessableEntity, "environment_required", "Select an environment")
				return
			}
			if v3PersistedScopeRead(r) {
				next.ServeHTTP(w, r)
				return
			}
			env = cfg.DefaultEnv
		}
		if env == "all" {
			if cfg.Tier != "nonprod" {
				Error(w, http.StatusUnprocessableEntity, "invalid_environment", "All is only available on a non-production instance")
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !v3ReadOnlyPost(r) {
				Error(w, http.StatusConflict, "env_view_only", "All non-production environments are read-only")
				return
			}
			ids := make([]string, 0, len(known))
			for id := range known {
				err := authorizer.AuthorizeEnvironment(r.Context(), user, id)
				if err == nil {
					ids = append(ids, id)
				} else if !errors.Is(err, store.ErrForbidden) {
					Error(w, http.StatusServiceUnavailable, "environment_unavailable", "Environment authorization unavailable")
					return
				}
			}
			if len(ids) == 0 {
				Error(w, http.StatusForbidden, "forbidden", "No accessible environments")
				return
			}
			sort.Strings(ids)
			r = r.WithContext(context.WithValue(r.Context(), v3EnvironmentContextKey{}, V3EnvironmentScope{Tier: cfg.Tier, EnvironmentIDs: ids, All: true}))
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := known[env]; !ok {
			Error(w, http.StatusUnprocessableEntity, "invalid_environment", "Select one known environment")
			return
		}
		if err := authorizer.AuthorizeEnvironment(r.Context(), user, env); err != nil {
			if errors.Is(err, store.ErrForbidden) {
				Error(w, http.StatusForbidden, "forbidden", "Environment access denied")
			} else {
				Error(w, http.StatusServiceUnavailable, "environment_unavailable", "Environment authorization unavailable")
			}
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), v3EnvironmentContextKey{}, V3EnvironmentScope{Tier: cfg.Tier, EnvironmentIDs: []string{env}}))
		next.ServeHTTP(w, r)
	}), nil
}

// Some existing query APIs use POST for structured read input. Keep this
// allowlist narrow: an all-environment view must never turn a mutation into a
// write merely because its handler happens to return data.
func v3ReadOnlyPost(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/v1/knowledge/search"
}

func v3EnvironmentExempt(r *http.Request) bool {
	switch r.Method + " " + r.URL.Path {
	case "POST /api/v1/auth/login", "POST /api/v1/auth/logout", "GET /api/v1/auth/me", "GET /api/v1/instance", "GET /api/v1/auth/oidc/start", "GET /api/v1/auth/oidc/callback", "POST /api/v1/auth/invitations/accept":
		return true
	}
	// These handlers authenticate a configured source and bind it to a fixed
	// application and environment. They do not accept browser session scope.
	if v3SignedPushRoute(r) {
		return true
	}
	// Native inbound receivers authenticate their configured source tokens and
	// bind payloads to server-owned targets. They have no browser session or
	// selectable environment header. Use escaped path segments so a configured
	// source ID containing a slash remains one route parameter.
	if r.Method == http.MethodPost {
		parts := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
		return len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" &&
			parts[2] == "webhooks" && (parts[3] == "alertmanager" || parts[3] == "gitlab") && parts[4] != ""
	}
	return false
}

func v3SignedPushRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/events/alert", "/api/v1/events/change", "/api/v1/events/incident", "/api/v1/events/resource", "/api/v1/events/oncall":
		return true
	}
	return false
}

func v3PersistedScopeRead(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[3] == "" {
		return false
	}
	return (parts[2] == "conversations" && parts[4] == "events") ||
		(parts[2] == "audit" && parts[3] == "exports" && parts[4] != "") ||
		(parts[2] == "uploads" && parts[4] == "content")
}
