package profile

import (
	"errors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
)

func respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		httpapi.WriteJSON(w, 200, v)
		return
	}
	switch {
	case errors.Is(e, ErrInvalid):
		httpapi.Error(w, 422, "invalid_profile", "Check avatar format or password requirements")
	case errors.Is(e, ErrPasswordRequirements):
		httpapi.Error(w, 422, "invalid_password", "New password must be 8–128 characters and include uppercase and lowercase letters, numbers and symbols")
	case errors.Is(e, ErrPassword):
		httpapi.Error(w, 422, "incorrect_password", "Current password is incorrect")
	case errors.Is(e, ErrNoPassword):
		httpapi.Error(w, 422, "no_local_password", "No local password is configured")
	case errors.Is(e, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Personal profile access required")
	case errors.Is(e, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Personal profile not found")
	default:
		httpapi.Error(w, 503, "profile_unavailable", "Personal profile storage is unavailable")
	}
}
func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/profile", "viewer", func(w http.ResponseWriter, r *http.Request) { v, e := s.Get(r.Context()); respond(w, v, e) })
	api.Register("PATCH /api/v1/profile", "viewer", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Avatar *string `json:"avatar_data_url"`
		}
		if !httpapi.Decode(w, r, &p) {
			return
		}
		if p.Avatar == nil {
			respond(w, nil, ErrInvalid)
			return
		}
		v, e := s.SetAvatar(r.Context(), *p.Avatar)
		respond(w, v, e)
	})
	api.Register("POST /api/v1/profile/password", "viewer", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Current string `json:"current_password"`
			Next    string `json:"new_password"`
		}
		if !httpapi.Decode(w, r, &p) {
			return
		}
		if e := s.ChangePassword(r.Context(), p.Current, p.Next); e != nil {
			respond(w, nil, e)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "aiops_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: api.SecureCookies, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(http.StatusNoContent)
	})
}
