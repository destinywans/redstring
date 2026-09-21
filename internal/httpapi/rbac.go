package httpapi

import (
	"context"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"strings"
)

type capabilityAuthorizer interface {
	AuthorizeCapability(context.Context, core.User, string, string, string) error
}

// authorizeRole preserves legacy-only modules but opens implemented RBAC surfaces
// using their specific capability. It never changes or elevates the user's role.
func (s *Server) authorizeRole(r *http.Request, u core.User, minRole string) error {
	a, ok := s.Store.(capabilityAuthorizer)
	if !ok {
		if rank(u.Role) < rank(minRole) {
			return store.ErrForbidden
		}
		return nil
	}
	path := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(path) < 3 {
		return store.ErrForbidden
	}
	kind := path[2]
	if minRole == "viewer" {
		return nil
	}
	if minRole == "admin" {
		target := map[string]string{"rbac": "rbac", "users": "users", "teams": "teams", "settings": "settings", "applications": "applications", "environments": "applications", "audit": "audit", "resources": "resources"}[kind]
		if kind == "settings" && len(path) > 3 && path[3] == "teams" {
			target = "teams"
		}
		if target != "" {
			return a.AuthorizeCapability(r.Context(), u, "manage", "management", target)
		}
	}
	if minRole == "operator" {
		switch kind {
		case "investigations", "branches", "runs", "action-plans", "executions":
			return nil
		}
	}
	if rank(u.Role) < rank(minRole) {
		return store.ErrForbidden
	}
	return nil
}
