package v3mfa

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/destinywans/redstring/internal/httpapi"
)

// Register exposes only account-owned auxiliary routes. The integration
// layer must configure V3 environment middleware before serving requests.
// None of these routes issues passkey assurance or changes approval policy.
func (s *Service) Register(api *httpapi.Server) {
	s.allowedOrigin = api.AllowedOrigin
	routes := []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/me/mfa/totp", s.statusHTTP},
		{"POST /api/v1/me/mfa/totp/enrollment", s.beginHTTP},
		{"POST /api/v1/me/mfa/totp/confirm", s.confirmHTTP},
		{"DELETE /api/v1/me/mfa/totp/enrollment", s.cancelEnrollmentHTTP},
		{"POST /api/v1/me/mfa/verify", s.verifyHTTP},
		{"DELETE /api/v1/me/mfa/totp", s.cancelHTTP},
	}
	for _, route := range routes {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3Route(route.pattern, "", "") // self account, not a matrix grant
	}
}

func actorFromRequest(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] == "" {
		httpapi.Error(w, 422, "mfa_environment_required", "Select one environment")
		return Actor{}, false
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return Actor{}, false
	}
	return Actor{UserID: httpapi.CurrentUser(r).ID, SessionToken: cookie.Value,
		Tier: scope.Tier, EnvironmentID: scope.EnvironmentIDs[0]}, true
}

func (s *Service) sameOriginWrite(w http.ResponseWriter, r *http.Request) bool {
	// Session cookies authenticate these factor-changing endpoints. Require a
	// browser origin signal even where the common handler permits both Origin
	// and Fetch Metadata to be absent for non-browser API clients.
	origin := r.Header.Get("Origin")
	if origin == "" {
		if r.Header.Get("Sec-Fetch-Site") == "same-origin" {
			return true
		}
	} else {
		expected := s.allowedOrigin
		if expected == "" {
			expected = "http://" + r.Host
			if r.TLS != nil {
				expected = "https://" + r.Host
			}
		}
		if origin == expected {
			return true
		}
	}
	httpapi.Error(w, 403, "mfa_origin_required", "Use the authenticated same-origin application")
	return false
}

func readCode(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Body == nil {
		httpapi.Error(w, 422, "mfa_invalid_input", "Enter one authenticator code")
		return "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Code string `json:"code"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		httpapi.Error(w, 422, "mfa_invalid_input", "Enter one authenticator code")
		return "", false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		httpapi.Error(w, 422, "mfa_invalid_input", "Expected one code object")
		return "", false
	}
	if input.Code == "" || len(input.Code) > 64 || strings.TrimSpace(input.Code) != input.Code {
		httpapi.Error(w, 422, "mfa_invalid_input", "Enter one authenticator code")
		return "", false
	}
	return input.Code, true
}

func requireEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	var b [1]byte
	n, err := r.Body.Read(b[:])
	if n != 0 || err != io.EOF {
		httpapi.Error(w, 422, "mfa_invalid_input", "This operation does not accept a body")
		return false
	}
	return true
}

func (s *Service) cancelEnrollmentHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginWrite(w, r) || !requireEmptyBody(w, r) {
		return
	}
	a, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.CancelEnrollment(r.Context(), a); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpapi.Error(w, 403, "mfa_forbidden", "Account, session or environment is unavailable")
	case errors.Is(err, ErrFreshLoginRequired):
		httpapi.Error(w, 403, "mfa_recent_login_required", "Log in again before changing an authenticator")
	case errors.Is(err, ErrConflict):
		httpapi.Error(w, 409, "mfa_conflict", "Authenticator state changed or enrollment is pending")
	case errors.Is(err, ErrNoFactor):
		httpapi.Error(w, 409, "mfa_not_enrolled", "No active authenticator")
	case errors.Is(err, ErrInvalidCode), errors.Is(err, ErrInvalidInput):
		httpapi.Error(w, 422, "mfa_invalid_code", "Invalid or already used authenticator code")
	case errors.Is(err, ErrRateLimited):
		httpapi.Error(w, 429, "mfa_rate_limited", "Wait before trying another authenticator code")
	default:
		httpapi.Error(w, 503, "mfa_unavailable", "Authenticator storage or encryption unavailable")
	}
}

func (s *Service) statusHTTP(w http.ResponseWriter, r *http.Request) {
	a, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	status, err := s.Status(r.Context(), a)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, status)
}

func (s *Service) beginHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginWrite(w, r) || !requireEmptyBody(w, r) {
		return
	}
	a, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	enrollment, err := s.BeginEnrollment(r.Context(), a)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, enrollment)
}

func (s *Service) confirmHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginWrite(w, r) {
		return
	}
	code, ok := readCode(w, r)
	if !ok {
		return
	}
	a, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	result, err := s.ConfirmEnrollment(r.Context(), a, code)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, result)
}

func (s *Service) verifyHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginWrite(w, r) {
		return
	}
	code, ok := readCode(w, r)
	if !ok {
		return
	}
	a, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	result, err := s.Verify(r.Context(), a, code)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, result)
}

func (s *Service) cancelHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginWrite(w, r) {
		return
	}
	code, ok := readCode(w, r)
	if !ok {
		return
	}
	a, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.Cancel(r.Context(), a, code); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}
