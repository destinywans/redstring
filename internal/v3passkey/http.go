package v3passkey

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
)

// Register exposes account-owned ceremonies only. The integration layer must
// configure V3 environment middleware before serving these routes. No route
// issues proposal approval assurance.
func (s *Service) Register(api *httpapi.Server) {
	routes := []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/me/passkeys", s.listHTTP},
		{"POST /api/v1/me/passkeys/registration/options", s.beginRegistrationHTTP},
		{"POST /api/v1/me/passkeys/registration/finish", s.finishRegistrationHTTP},
		{"POST /api/v1/me/passkeys/assertion/options", s.beginAssertionHTTP},
		{"POST /api/v1/me/passkeys/assertion/finish", s.finishAssertionHTTP},
		{"POST /api/v1/me/passkeys/deletion/options", s.beginDeletionHTTP},
		{"POST /api/v1/me/passkeys/deletion/finish", s.finishDeletionHTTP},
	}
	for _, route := range routes {
		handler := route.handler
		api.Register(route.pattern, "viewer", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && !s.sameOriginWrite(w, r, api.AllowedOrigin, api.SecureCookies) {
				return
			}
			handler(w, r)
		})
		api.MarkV3Route(route.pattern, "", "") // self account, not a matrix grant
	}
}

func passkeyActor(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] == "" {
		httpapi.Error(w, 422, "passkey_environment_required", "Select one environment")
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

func (s *Service) sameOriginWrite(w http.ResponseWriter, r *http.Request, allowedOrigin string, secureCookies bool) bool {
	if s == nil || s.origin == "" {
		httpapi.Error(w, 403, "passkey_secure_origin_required", "Use the secure passkey application")
		return false
	}
	// cmd/aiops serves HTTP behind a TLS-terminating proxy. In that topology,
	// require both a configured exact HTTPS public origin and Secure cookies;
	// never trust a request-supplied X-Forwarded-Proto header as TLS evidence.
	proxyTLS := secureCookies && allowedOrigin == s.origin && strings.HasPrefix(s.origin, "https://")
	localFixture := s.tier == "nonprod" && (s.origin == "http://localhost" || strings.HasPrefix(s.origin, "http://localhost:"))
	if r.TLS == nil && !proxyTLS && !localFixture {
		httpapi.Error(w, 403, "passkey_secure_origin_required", "Use the secure passkey application")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if origin == s.origin {
			return true
		}
	} else if r.Header.Get("Sec-Fetch-Site") == "same-origin" && allowedOrigin == s.origin {
		return true
	}
	httpapi.Error(w, 403, "passkey_origin_required", "Use the authenticated same-origin application")
	return false
}

func passkeyJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Body == nil || mediaErr != nil || mediaType != "application/json" {
		httpapi.Error(w, 422, "passkey_invalid_input", "Expected one JSON object")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResponseBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		httpapi.Error(w, 422, "passkey_invalid_input", "Expected one valid JSON object")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		httpapi.Error(w, 422, "passkey_invalid_input", "Expected one JSON object")
		return false
	}
	return true
}

func passkeyEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2)
	var one [1]byte
	n, err := r.Body.Read(one[:])
	if n == 0 && err == io.EOF {
		return true
	}
	httpapi.Error(w, 422, "passkey_invalid_input", "This operation does not accept a body")
	return false
}

func passkeyHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrForbidden):
		httpapi.Error(w, 403, "passkey_forbidden", "Account, session or environment is unavailable")
	case errors.Is(err, ErrFreshLoginRequired):
		httpapi.Error(w, 403, "passkey_recent_login_required", "Log in again before changing a passkey")
	case errors.Is(err, ErrNoCredential):
		httpapi.Error(w, 409, "passkey_not_enrolled", "No active passkey credential")
	case errors.Is(err, ErrConflict):
		httpapi.Error(w, 409, "passkey_conflict", "Passkey state changed")
	case errors.Is(err, ErrRateLimited):
		httpapi.Error(w, 429, "passkey_rate_limited", "Try again later")
	case errors.Is(err, ErrChallengeInvalid):
		httpapi.Error(w, 409, "passkey_challenge_invalid", "Passkey challenge expired or was already used")
	case errors.Is(err, ErrInvalidResponse):
		httpapi.Error(w, 422, "passkey_invalid_response", "Passkey response could not be verified")
	case errors.Is(err, ErrProposalDigestUnavailable):
		httpapi.Error(w, 503, "passkey_proposal_unavailable", "Proposal approval is unavailable")
	default:
		httpapi.Error(w, 503, "passkey_unavailable", "Passkey service unavailable")
	}
}

type passkeyCredentialJSON struct {
	ID             string     `json:"id"`
	Label          string     `json:"label,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	BackupEligible bool       `json:"backup_eligible"`
	BackupState    bool       `json:"backup_state"`
}

func passkeyPublicCredential(c CredentialInfo) passkeyCredentialJSON {
	return passkeyCredentialJSON{ID: base64.RawURLEncoding.EncodeToString(c.ID), Label: c.Label,
		CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt,
		BackupEligible: c.BackupEligible, BackupState: c.BackupState}
}

func passkeyDecodeID(value string) ([]byte, bool) {
	if value == "" || len(value) > 1364 || strings.Contains(value, "=") {
		return nil, false
	}
	id, err := base64.RawURLEncoding.DecodeString(value)
	return id, err == nil && len(id) > 0 && len(id) <= 1023 &&
		base64.RawURLEncoding.EncodeToString(id) == value
}

func (s *Service) listHTTP(w http.ResponseWriter, r *http.Request) {
	a, ok := passkeyActor(w, r)
	if !ok {
		return
	}
	credentials, err := s.List(r.Context(), a)
	if err != nil {
		passkeyHTTPError(w, err)
		return
	}
	out := make([]passkeyCredentialJSON, 0, len(credentials))
	for _, c := range credentials {
		out = append(out, passkeyPublicCredential(c))
	}
	httpapi.WriteJSON(w, 200, map[string]any{"credentials": out})
}

func (s *Service) beginRegistrationHTTP(w http.ResponseWriter, r *http.Request) {
	if !passkeyEmptyBody(w, r) {
		return
	}
	a, ok := passkeyActor(w, r)
	if !ok {
		return
	}
	ceremony, err := s.BeginRegistration(r.Context(), a)
	if err != nil {
		passkeyHTTPError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, ceremony)
}

func (s *Service) beginAssertionHTTP(w http.ResponseWriter, r *http.Request) {
	if !passkeyEmptyBody(w, r) {
		return
	}
	a, ok := passkeyActor(w, r)
	if !ok {
		return
	}
	ceremony, err := s.BeginAssertion(r.Context(), a)
	if err != nil {
		passkeyHTTPError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, ceremony)
}

type passkeyFinishInput struct {
	ChallengeID string          `json:"challenge_id"`
	Credential  json.RawMessage `json:"credential"`
	Label       string          `json:"label,omitempty"`
}

func passkeyReadFinish(w http.ResponseWriter, r *http.Request, withLabel bool) (passkeyFinishInput, bool) {
	var input passkeyFinishInput
	if !passkeyJSON(w, r, &input) {
		return input, false
	}
	if len(input.ChallengeID) != 32 || len(input.Credential) == 0 ||
		!withLabel && input.Label != "" {
		httpapi.Error(w, 422, "passkey_invalid_input", "Expected challenge and credential response")
		return input, false
	}
	return input, true
}

func (s *Service) finishRegistrationHTTP(w http.ResponseWriter, r *http.Request) {
	input, ok := passkeyReadFinish(w, r, true)
	if !ok {
		return
	}
	a, ok := passkeyActor(w, r)
	if !ok {
		return
	}
	credential, err := s.FinishRegistration(r.Context(), a, input.ChallengeID, input.Credential, input.Label)
	if err != nil {
		passkeyHTTPError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, passkeyPublicCredential(credential))
}

func (s *Service) finishAssertionHTTP(w http.ResponseWriter, r *http.Request) {
	input, ok := passkeyReadFinish(w, r, false)
	if !ok {
		return
	}
	a, ok := passkeyActor(w, r)
	if !ok {
		return
	}
	result, err := s.FinishAssertion(r.Context(), a, input.ChallengeID, input.Credential)
	if err != nil {
		passkeyHTTPError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"credential_id": base64.RawURLEncoding.EncodeToString(result.CredentialID),
		"verified":      result.Verified, "scope": "account_only",
	})
}

func (s *Service) beginDeletionHTTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CredentialID string `json:"credential_id"`
	}
	if !passkeyJSON(w, r, &input) {
		return
	}
	id, ok := passkeyDecodeID(input.CredentialID)
	if !ok {
		httpapi.Error(w, 422, "passkey_invalid_input", "Expected a passkey credential ID")
		return
	}
	a, ok := passkeyActor(w, r)
	if !ok {
		return
	}
	ceremony, err := s.BeginDeletion(r.Context(), a, id)
	if err != nil {
		passkeyHTTPError(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, ceremony)
}

func (s *Service) finishDeletionHTTP(w http.ResponseWriter, r *http.Request) {
	input, ok := passkeyReadFinish(w, r, false)
	if !ok {
		return
	}
	a, ok := passkeyActor(w, r)
	if !ok {
		return
	}
	if err := s.FinishDeletion(r.Context(), a, input.ChallengeID, input.Credential); err != nil {
		passkeyHTTPError(w, err)
		return
	}
	w.WriteHeader(204)
}
