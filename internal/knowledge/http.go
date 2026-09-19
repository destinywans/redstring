package knowledge

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) Register(api *httpapi.Server) {
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
	api.Register("POST /api/v1/knowledge/search", "viewer", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ApplicationID string `json:"application_id"`
			Query         string `json:"query"`
			Limit         int    `json:"limit"`
		}
		if !decode(w, r, &input) {
			return
		}
		hits, e := s.Search(r.Context(), input.ApplicationID, input.Query, input.Limit)
		respond(w, map[string]any{"items": hits, "embedding": s.Embedding.Descriptor()}, e)
	})
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
		if errors.Is(e, ErrInvalid) {
			status = 422
			code = "invalid_input"
			message = e.Error()
		} else if errors.Is(e, store.ErrConflict) {
			status = 409
			code = "conflict"
			message = "Close the investigation before confirming this experience."
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
