package v3tokens

import (
	"errors"
	"net/http"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Register exposes human-only credential management. The common V3 HTTP gate
// still needs a separate bearer integration before issued tokens can read any
// product resource.
func (s *Service) Register(api *httpapi.Server) {
	api.Register("POST /api/v1/api-tokens", "viewer", s.issueHTTP)
	api.MarkV3Route("POST /api/v1/api-tokens", "connectors", "manage")
	api.Register("GET /api/v1/api-tokens", "viewer", s.listHTTP)
	api.MarkV3Route("GET /api/v1/api-tokens", "", "")
	api.Register("DELETE /api/v1/api-tokens/{id}", "viewer", s.revokeHTTP)
	api.MarkV3Route("DELETE /api/v1/api-tokens/{id}", "", "")
}

func tokenActor(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "environment_required", "Select one environment")
		return Actor{}, false
	}
	if scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "env_view_only", "Select one environment for token management")
		return Actor{}, false
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Human login required")
		return Actor{}, false
	}
	return Actor{User: httpapi.CurrentUser(r), SessionToken: cookie.Value,
		EnvironmentID: scope.EnvironmentIDs[0]}, true
}

func tokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_token_request", "Choose a valid name, read scopes and TTL")
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Human session expired")
	case errors.Is(err, ErrForbidden), errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Token operation denied")
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, http.StatusNotFound, "not_found", "Token not found")
	case errors.Is(err, ErrTokenLimit):
		httpapi.Error(w, http.StatusConflict, "token_limit", "Active token limit reached")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "tokens_unavailable", "Token service unavailable")
	}
}

func (s *Service) issueHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := tokenActor(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var in IssueInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	result, err := s.Issue(r.Context(), actor, in)
	if err != nil {
		tokenError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusCreated, result)
}

func (s *Service) listHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := tokenActor(w, r)
	if !ok {
		return
	}
	result, err := s.List(r.Context(), actor)
	if err != nil {
		tokenError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (s *Service) revokeHTTP(w http.ResponseWriter, r *http.Request) {
	actor, ok := tokenActor(w, r)
	if !ok {
		return
	}
	if err := s.Revoke(r.Context(), actor, r.PathValue("id")); err != nil {
		tokenError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
