package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wans/aiops-canvas/internal/core"
	"net/http"
	"strconv"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Register omits the historical search projection when a V3 instance installs
// the V3 knowledge search handler at the same route.
func (s *Service) Register(api *httpapi.Server, v3Search ...bool) {
	if s.Authorize == nil {
		s.Authorize = func(ctx context.Context, user, app, permission string) error {
			return api.AuthorizeApplication(ctx, core.User{ID: user, Role: PrincipalFromContext(ctx).Role}, app, permission)
		}
	}
	if s.VisibleApplications == nil {
		s.VisibleApplications = func(ctx context.Context, user string) ([]string, error) {
			return api.VisibleApplicationIDs(ctx, core.User{ID: user})
		}
	}
	if repo, ok := s.Repository.(*PGRepository); ok {
		s.ValidateEnvironment = func(ctx context.Context, app, env string) error {
			var exists bool
			err := repo.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM application_environments WHERE id=$1 AND application_id=$2 AND NOT archived)", env, app).Scan(&exists)
			if err != nil {
				return err
			}
			if !exists {
				return ErrInvalid
			}
			return nil
		}
		repo.ValidateScope = func(ctx context.Context, app string, m Metadata) error {
			for _, env := range m.EnvironmentIDs {
				if err := s.ValidateEnvironment(ctx, app, env); err != nil {
					return err
				}
			}
			if m.TeamID != "" {
				var exists bool
				if err := repo.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM teams WHERE id=$1)", m.TeamID).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return ErrInvalid
				}
			}
			return nil
		}
	}
	api.Register("GET /api/v1/knowledge/templates", "viewer", func(w http.ResponseWriter, r *http.Request) { respond(w, map[string]any{"items": Templates()}, nil) })
	api.Register("PUT /api/v1/knowledge/{id}", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in UpdateInput
		if !decode(w, r, &in) {
			return
		}
		d, e := s.Update(r.Context(), r.PathValue("id"), in, httpapi.CurrentUser(r).ID)
		respond(w, d, e)
	})
	api.Register("POST /api/v1/knowledge/{id}/transition", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in TransitionInput
		if !decode(w, r, &in) {
			return
		}
		d, e := s.Transition(r.Context(), r.PathValue("id"), in, httpapi.CurrentUser(r).ID)
		respond(w, d, e)
	})
	api.Register("GET /api/v1/knowledge/{id}/revisions", "viewer", func(w http.ResponseWriter, r *http.Request) {
		ds, e := s.Revisions(r.Context(), r.PathValue("id"))
		respond(w, map[string]any{"items": ds}, e)
	})
	api.Register("GET /api/v1/knowledge/{id}/revisions/{version}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		version, e := strconv.Atoi(r.PathValue("version"))
		if e != nil {
			respond(w, nil, ErrInvalid)
			return
		}
		d, e := s.Revision(r.Context(), r.PathValue("id"), version)
		respond(w, d, e)
	})
	api.Register("GET /api/v1/knowledge/{id}/export", "viewer", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Export(r.Context(), r.PathValue("id"))
		respond(w, d, e)
	})
	api.Register("POST /api/v1/investigations/{id}/knowledge-drafts/generate", "operator", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.GenerateExperienceDraft(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, false)
		respond(w, d, e)
	})

	api.Register("POST /api/v1/investigations/{id}/knowledge-drafts", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in DraftInput
		if !decode(w, r, &in) {
			return
		}
		d, e := s.CreateDraft(r.Context(), r.PathValue("id"), in, httpapi.CurrentUser(r).ID)
		respond(w, d, e)
	})
	api.Register("GET /api/v1/investigations/{id}/knowledge-drafts", "viewer", func(w http.ResponseWriter, r *http.Request) {
		items, e := s.Drafts(r.Context(), r.PathValue("id"))
		respond(w, map[string]any{"items": items}, e)
	})
	api.Register("POST /api/v1/knowledge-drafts/{id}/confirm", "operator", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.ConfirmDraft(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID)
		respond(w, d, e)
	})
	api.Register("GET /api/v1/knowledge", "viewer", func(w http.ResponseWriter, r *http.Request) {
		items, e := s.List(r.Context(), r.URL.Query().Get("application_id"))
		respond(w, map[string]any{"items": items}, e)
	})
	api.Register("GET /api/v1/knowledge/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Get(r.Context(), r.PathValue("id"))
		respond(w, d, e)
	})
	api.Register("POST /api/v1/knowledge", "operator", func(w http.ResponseWriter, r *http.Request) {
		var d Document
		if !decode(w, r, &d) {
			return
		}
		d, e := s.Import(r.Context(), d, httpapi.CurrentUser(r).ID)
		respond(w, d, e)
	})
	api.Register("POST /api/v1/knowledge/{id}/reindex", "operator", func(w http.ResponseWriter, r *http.Request) {
		e := s.Index(r.Context(), r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		d, e := s.Get(r.Context(), r.PathValue("id"))
		respond(w, d, e)
	})
	if len(v3Search) == 0 || !v3Search[0] {
		api.Register("POST /api/v1/knowledge/search", "viewer", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				ApplicationID string `json:"application_id"`
				EnvironmentID string `json:"environment_id"`
				Query         string `json:"query"`
				Limit         int    `json:"limit"`
			}
			if !decode(w, r, &input) {
				return
			}
			result, e := s.SearchScoped(r.Context(), input.ApplicationID, input.EnvironmentID, input.Query, input.Limit)
			respond(w, result, e)
		})
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invalid_input", "message": "invalid JSON or fields"}})
		return false
	}
	return true
}
func respond(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		status := 503
		code := "dependency_error"
		message := "Knowledge operation failed; check embedding availability and retry."
		if errors.Is(e, ErrForbidden) || errors.Is(e, store.ErrForbidden) {
			status = 403
			code = "forbidden"
			message = "Knowledge access denied."
		} else if errors.Is(e, ErrInvalid) {
			status = 422
			code = "invalid_input"
			message = e.Error()
		} else if errors.Is(e, store.ErrConflict) {
			status = 409
			code = "conflict"
			message = "Version or lifecycle changed; reload the document, or close the investigation before confirming experience."
		} else if errors.Is(e, store.ErrNotFound) {
			status = 404
			code = "not_found"
			message = "Document or application does not exist."
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
