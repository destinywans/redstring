package settings

import (
	"context"
	"errors"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"net/http"
)

func respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		httpapi.WriteJSON(w, 200, v)
		return
	}
	switch {
	case errors.Is(e, ErrInvalid):
		httpapi.Error(w, 422, "invalid_settings", "Check field lengths, timezone, language, and team owner membership")
	case errors.Is(e, store.ErrConflict):
		httpapi.Error(w, 409, "settings_conflict", "Workspace changed; reload before saving")
	case errors.Is(e, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Settings record not found")
	default:
		httpapi.Error(w, 503, "settings_unavailable", "Settings storage is unavailable")
	}
}
func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/settings/overview", "viewer", func(w http.ResponseWriter, r *http.Request) {
		workspace, e := s.Workspace(r.Context())
		if e != nil {
			respond(w, nil, e)
			return
		}
		integrations, e := s.integrationSummary(r.Context(), api, httpapi.CurrentUser(r).Role == "admin")
		if e != nil {
			respond(w, nil, e)
			return
		}
		version := s.Version
		if version == "" {
			version = "development (unversioned)"
		}
		httpapi.WriteJSON(w, 200, map[string]any{"workspace": workspace, "version": version, "integrations": integrations, "can_manage": s.Store.AuthorizeCapability(r.Context(), httpapi.CurrentUser(r), "manage", "management", "settings") == nil, "capabilities": map[string]bool{"custom_roles": true, "resource_acl": true, "team_owners": true}})
	})
	api.Register("PATCH /api/v1/settings/overview", "admin", func(w http.ResponseWriter, r *http.Request) {
		var p WorkspacePatch
		if !httpapi.Decode(w, r, &p) {
			return
		}
		v, e := s.UpdateWorkspace(r.Context(), p)
		respond(w, v, e)
	})
	api.Register("GET /api/v1/settings/directory", "admin", func(w http.ResponseWriter, r *http.Request) { v, e := s.Directory(r.Context()); respond(w, v, e) })
	api.Register("PATCH /api/v1/settings/teams/{id}/profile", "admin", func(w http.ResponseWriter, r *http.Request) {
		var p TeamProfilePatch
		if !httpapi.Decode(w, r, &p) {
			return
		}
		v, e := s.UpdateTeamProfile(r.Context(), r.PathValue("id"), p)
		respond(w, v, e)
	})
}
func (s *Service) integrationSummary(ctx context.Context, api *httpapi.Server, admin bool) ([]IntegrationSummary, error) {
	result := []IntegrationSummary{{Kind: "model", State: "not_configured"}, {Kind: "mcp", State: "unavailable"}, {Kind: "connector", State: "not_configured"}, {Kind: "embedding", State: "not_configured"}}
	if api.Runtime != nil {
		result[0].Total = len(api.Runtime.Models())
		result[0].Configured = result[0].Total
	}
	for _, item := range api.Integrations {
		index := -1
		switch item.Kind {
		case "connector":
			index = 2
		case "embedding", "retrieval", "vector":
			index = 3
		}
		if index >= 0 {
			result[index].Total++
			if item.Configured {
				result[index].Configured++
			}
		}
	}
	// MCP metadata is administrator-only elsewhere. Preserve that boundary here.
	if admin {
		var exists bool
		if e := s.Store.DB.QueryRow(ctx, `SELECT to_regclass('ai_extensions') IS NOT NULL`).Scan(&exists); e != nil {
			return nil, e
		}
		if exists {
			e := s.Store.DB.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE record->>'enabled'='true') FROM ai_extensions WHERE kind='mcp'`).Scan(&result[1].Total, &result[1].Configured)
			if e != nil {
				return nil, e
			}
			result[1].State = "not_configured"
		}
	} else {
		result[1].State = "restricted"
	}
	for i := range result {
		if result[i].Configured > 0 {
			result[i].State = "configured_unverified"
		}
	}
	return result, nil
}
