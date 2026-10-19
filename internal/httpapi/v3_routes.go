package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
)

// V3RoutePermission is a stable authorization code, never a display label.
// An empty module is reserved for an authenticated user's own account data.
type V3RoutePermission struct {
	Module       string
	Action       string
	ObjectScoped bool
}

type V3PolicyFunc func(context.Context, core.User, string, string, string) error

// ConfigureV3Environment must be called once at startup. The middleware owns
// environment parsing and read-only all enforcement; route policy owns module
// grants. Keeping both on the server prevents a route from silently skipping
// environment checks when the V3 module is registered later in startup.
func (s *Server) ConfigureV3Environment(cfg V3InstanceConfig, authorizer V3EnvironmentAuthorizer, session V3SessionLookup, policy V3PolicyFunc) error {
	// Parse and authorize the environment before deciding whether a historical
	// route is available. In particular, an `all` write must always receive the
	// contract's 409 env_view_only, even when the route is not yet migrated.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			_, pattern := s.mux.Handler(r)
			if pattern != "" && !v3AuthOrInstance(pattern) && !(pattern == r.Method+" "+r.URL.Path && v3SignedPushRoute(r)) {
				if _, enabled := s.v3Routes[pattern]; !enabled {
					Error(w, http.StatusNotImplemented, "v3_not_implemented", "This operation is not available in V3 yet")
					return
				}
			}
		}
		s.mux.ServeHTTP(w, r)
	})
	handler, err := NewV3EnvironmentHandler(cfg, authorizer, session, next)
	if err != nil {
		return err
	}
	s.v3Environment = handler
	s.v3Policy = policy
	return nil
}

// MarkV3Route switches one registered route from legacy rank checks to the
// V3 matrix. Registration and marking happen before the server starts.
func (s *Server) MarkV3Route(pattern, module, action string) {
	s.v3Routes[pattern] = V3RoutePermission{Module: module, Action: action}
}

// MarkV3ObjectRoute is only for handlers that check the persisted target's
// application or team against V3Policy before returning data or changing it.
// A module grant scoped to one application cannot be checked at this route
// boundary, where no target has been resolved yet. Environment and session
// checks still run before the handler.
func (s *Server) MarkV3ObjectRoute(pattern, module, action string) {
	s.v3Routes[pattern] = V3RoutePermission{Module: module, Action: action, ObjectScoped: true}
}

func (s *Server) authorizeV3Route(w http.ResponseWriter, r *http.Request, user core.User, permission V3RoutePermission) (*http.Request, bool) {
	if permission.Module == "" || permission.ObjectScoped {
		return r, true
	}
	if s.v3Policy == nil {
		Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "V3 authorization is not configured")
		return nil, false
	}
	scope, ok := V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		Error(w, http.StatusUnprocessableEntity, "environment_required", "Select an environment")
		return nil, false
	}
	allowed := make([]string, 0, len(scope.EnvironmentIDs))
	for _, environmentID := range scope.EnvironmentIDs {
		err := s.v3Policy(r.Context(), user, permission.Module, permission.Action, environmentID)
		if err == nil {
			allowed = append(allowed, environmentID)
		} else if !errors.Is(err, store.ErrForbidden) {
			Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Authorization storage unavailable")
			return nil, false
		}
	}
	if len(allowed) == 0 {
		Error(w, http.StatusForbidden, "forbidden", "Insufficient permission")
		return nil, false
	}
	scope.EnvironmentIDs = allowed
	r = withV3EnvironmentScope(r, scope)
	return r, true
}
