package httpapi

import (
	"context"
	"encoding/json"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"net/http"
	"strings"
)

func (s *Server) RegisterPublic(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }
func (s *Server) AuthorizeApplication(ctx context.Context, u core.User, app, permission string) error {
	p, ok := s.Store.(store.PlatformRepository)
	if !ok {
		return store.ErrForbidden
	}
	return p.AuthorizeApplication(ctx, u, app, permission)
}
func (s *Server) VisibleApplicationIDs(ctx context.Context, u core.User) ([]string, error) {
	p, ok := s.Store.(store.PlatformRepository)
	if !ok {
		return nil, store.ErrForbidden
	}
	return p.VisibleApplicationIDs(ctx, u)
}
func AuthorizationError(w http.ResponseWriter, e error) { fail(w, e) }
func (s *Server) authorizeRoute(r *http.Request) error {
	p, ok := s.Store.(store.PlatformRepository)
	path := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(path) < 4 {
		return nil
	}
	kind, id := path[2], path[3]
	switch kind {
	case "applications", "environments", "investigations", "branches", "runs", "action-plans", "executions":
	default:
		return nil
	}
	if !ok {
		return store.ErrForbidden
	}
	app, e := p.ResourceApplication(r.Context(), kind, id)
	if e != nil {
		return e
	}
	permission := "view"
	if r.Method != "GET" && r.Method != "HEAD" {
		permission = "investigate"
	}
	if r.Method != "GET" && r.Method != "HEAD" && kind == "action-plans" && len(path) > 4 {
		switch path[4] {
		case "approve", "reject", "revoke", "execute":
			// The action service authorizes the exact immutable CMDB target.
			// An application-level grant must not gate or widen that decision.
			return nil
		}
	}
	// These POST endpoints only execute registered, bounded read queries. Their
	// JSON bodies do not grant write authority; viewers retain application ACLs.
	if r.Method == "POST" && kind == "applications" && len(path) == 5 && (path[4] == "metrics" || path[4] == "logs") {
		permission = "view"
	}
	if strings.HasSuffix(r.URL.Path, "/layout") || ((kind == "applications" || kind == "environments") && r.Method != "GET") {
		permission = "view"
	}
	return p.AuthorizeApplication(r.Context(), CurrentUser(r), app, permission)
}
func (s *Server) platformRoutes(v3Mode bool) {
	p, ok := s.Store.(store.PlatformRepository)
	if !ok {
		return
	}
	s.Register("GET /api/v1/applications/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := p.Application(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		s.enrichApplication(&v)
		WriteJSON(w, 200, v)
	})
	s.Register("PATCH /api/v1/applications/{id}", "admin", func(w http.ResponseWriter, r *http.Request) {
		a, e := p.Application(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		var patch map[string]json.RawMessage
		if !Decode(w, r, &patch) {
			return
		}
		for k := range patch {
			switch k {
			case "name", "description", "key", "owner_team_id", "maintainer_id", "tags", "repository_url", "runbook_url", "profile":
			default:
				Error(w, 422, "invalid_field", "Unsupported application field")
				return
			}
		}
		raw, _ := json.Marshal(patch)
		if json.Unmarshal(raw, &a) != nil {
			Error(w, 422, "invalid_body", "Invalid field value")
			return
		}
		a, e = p.UpdateApplication(r.Context(), a)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, a)
	})
	s.Register("POST /api/v1/applications/{id}/archive", "admin", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Archived bool `json:"archived"`
		}
		if !Decode(w, r, &v) {
			return
		}
		a, e := p.Application(r.Context(), r.PathValue("id"))
		if e == nil {
			a.Archived = v.Archived
			a, e = p.UpdateApplication(r.Context(), a)
		}
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, a)
	})
	s.Register("GET /api/v1/applications/{id}/environments", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := p.Environments(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		for i := range v {
			if s.EnvironmentCapabilities != nil {
				v[i].Capabilities = s.EnvironmentCapabilities(v[i])
			}
		}
		WriteJSON(w, 200, map[string]any{"items": v})
	})
	saveEnv := func(w http.ResponseWriter, r *http.Request) {
		var v core.Environment
		if !Decode(w, r, &v) {
			return
		}
		if r.Method == "POST" {
			v.ID = ""
			v.ApplicationID = r.PathValue("id")
		} else {
			v.ID = r.PathValue("id")
			app, e := p.ResourceApplication(r.Context(), "environments", v.ID)
			if e != nil {
				fail(w, e)
				return
			}
			if v.ApplicationID != "" && app != v.ApplicationID {
				fail(w, store.ErrInvalidReference)
				return
			}
			v.ApplicationID = app
		}
		if s.ValidateBindings != nil {
			if e := s.ValidateBindings(v.Bindings); e != nil {
				fail(w, e)
				return
			}
		} else if len(v.Bindings) > 0 {
			fail(w, store.ErrInvalidReference)
			return
		}
		v, e := p.SaveEnvironment(r.Context(), v)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, v)
	}
	s.Register("POST /api/v1/applications/{id}/environments", "admin", saveEnv)
	s.Register("PATCH /api/v1/environments/{id}", "admin", saveEnv)
	s.Register("GET /api/v1/teams", "admin", func(w http.ResponseWriter, r *http.Request) {
		v, e := p.Teams(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"items": v})
	})
	saveTeam := func(w http.ResponseWriter, r *http.Request) {
		var v core.Team
		if !Decode(w, r, &v) {
			return
		}
		v.ID = r.PathValue("id")
		v, e := p.SaveTeam(r.Context(), v)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, v)
	}
	s.Register("POST /api/v1/teams", "admin", saveTeam)
	s.Register("PATCH /api/v1/teams/{id}", "admin", saveTeam)
	s.Register("GET /api/v1/teams/{id}/members", "admin", func(w http.ResponseWriter, r *http.Request) {
		v, e := p.TeamMembers(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"items": v})
	})
	s.Register("PUT /api/v1/teams/{id}/members", "admin", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			IDs []string `json:"user_ids"`
		}
		if !Decode(w, r, &v) {
			return
		}
		if e := p.SetTeamMembers(r.Context(), r.PathValue("id"), v.IDs); e != nil {
			memberError(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"items": v.IDs})
	})
	s.Register("GET /api/v1/applications/{id}/grants", "admin", func(w http.ResponseWriter, r *http.Request) {
		v, e := p.Grants(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"items": v})
	})
	s.Register("PUT /api/v1/applications/{id}/grants", "admin", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Items []core.Grant `json:"items"`
		}
		if !Decode(w, r, &v) {
			return
		}
		if e := p.SetGrants(r.Context(), r.PathValue("id"), v.Items); e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, v)
	})
	if !v3Mode {
		s.Register("GET /api/v1/audit", "admin", s.auditEntries)
	}
}

func (s *Server) enrichApplication(a *core.Application) {
	if s.EnvironmentCapabilities == nil {
		return
	}
	for i := range a.Environments {
		a.Environments[i].Capabilities = s.EnvironmentCapabilities(a.Environments[i])
	}
}
