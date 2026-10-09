package v3security

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func sessionToken(r *http.Request) string {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func selectedEnvironment(r *http.Request) (string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		return "", false
	}
	return scope.EnvironmentIDs[0], true
}

// withoutEmptyUnavailable drops the echoed GET "unavailable" key. Only an
// empty map can come from a writable (complete) view.
func withoutEmptyUnavailable(raw json.RawMessage) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw, true
	}
	value, present := fields["unavailable"]
	if !present {
		return raw, true
	}
	var reasons map[string]string
	if json.Unmarshal(value, &reasons) != nil || reasons == nil || len(reasons) != 0 {
		return nil, false
	}
	delete(fields, "unavailable")
	stripped, err := json.Marshal(fields)
	return stripped, err == nil
}

func decodeSecurityView(w http.ResponseWriter, r *http.Request) (SecurityView, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	var raw json.RawMessage
	err := decoder.Decode(&raw)
	ok := err == nil && decoder.Decode(new(any)) == io.EOF && rejectDuplicateJSONKeys(raw) == nil
	if ok {
		raw, ok = withoutEmptyUnavailable(raw)
	}
	if !ok || validateSecurityView(raw) != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_security_view", "Expected one complete security settings object")
		return SecurityView{}, false
	}
	var view SecurityView
	if decodeStrict(raw, &view) != nil || validView(view) != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_security_view", "Security settings are incomplete or invalid")
		return SecurityView{}, false
	}
	return view, true
}

func respondSecurity(w http.ResponseWriter, r *http.Request, snapshot RuntimeSnapshot, err error) {
	if err == nil {
		w.Header().Set("ETag", securityETag(RuntimeScope{InstanceID: snapshot.InstanceID, EnvironmentID: snapshot.EnvironmentID}, snapshot.Revision))
		w.Header().Set("Cache-Control", "private, no-store")
		httpapi.WriteJSON(w, http.StatusOK, securityResponse{SecurityView: snapshot.View, Unavailable: map[string]string{}})
		return
	}
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Platform administrator permission required")
	case errors.Is(err, errInvalidView), errors.Is(err, errReadOnlyChange):
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_security_view", "Security settings are invalid or include a read-only change")
	case errors.Is(err, ErrRuntimeRejected):
		httpapi.Error(w, http.StatusUnprocessableEntity, "security_control_unsupported", "Security authority cannot enforce a requested control")
	case errors.Is(err, errSecurityChanged), errors.Is(err, ErrRuntimeConflict):
		httpapi.Error(w, http.StatusPreconditionFailed, "security_changed", "Security policy changed; refresh before saving")
	case errors.Is(err, errOperationPending):
		if r.Method == http.MethodGet {
			httpapi.Error(w, http.StatusServiceUnavailable, "security_reconciliation_required", "Security policy operation requires reconciliation")
		} else {
			httpapi.Error(w, http.StatusConflict, "security_reconciliation_required", "Security policy operation requires reconciliation")
		}
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "security_authority_unavailable", "Effective security policy is unavailable")
	}
}

// Register installs the domain routes. WAN-117 must call this after the V3
// environment and policy middleware are configured, with a real authority.
func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/security", "admin", func(w http.ResponseWriter, r *http.Request) {
		environmentID, ok := selectedEnvironment(r)
		if !ok {
			httpapi.Error(w, http.StatusUnprocessableEntity, "environment_required", "Select one environment to inspect security policy")
			return
		}
		view, reason, err := s.read(r.Context(), httpapi.CurrentUser(r), sessionToken(r), environmentID)
		if err == nil && reason != "" {
			// No authority: every field is unknown and there is no revision,
			// so no ETag is issued and PUT stays unavailable.
			w.Header().Set("Cache-Control", "private, no-store")
			httpapi.WriteJSON(w, http.StatusOK, newUnknownSecurityView(reason))
			return
		}
		respondSecurity(w, r, view, err)
	})
	api.Register("PUT /api/v1/security", "admin", func(w http.ResponseWriter, r *http.Request) {
		environmentID, ok := selectedEnvironment(r)
		if !ok {
			httpapi.Error(w, http.StatusUnprocessableEntity, "environment_required", "Select one environment to change security policy")
			return
		}
		scope, err := s.runtimeScope(environmentID)
		if err != nil {
			respondSecurity(w, r, RuntimeSnapshot{}, err)
			return
		}
		values := r.Header.Values("If-Match")
		if len(values) == 0 || strings.TrimSpace(r.Header.Get("If-Match")) == "" {
			httpapi.Error(w, http.StatusPreconditionRequired, "security_precondition_required", "Read the current security policy before saving")
			return
		}
		if len(values) != 1 {
			httpapi.Error(w, http.StatusBadRequest, "invalid_precondition", "Exactly one security revision tag is required")
			return
		}
		expected, ok := parseSecurityIfMatch(values[0], scope)
		if !ok {
			httpapi.Error(w, http.StatusBadRequest, "invalid_precondition", "If-Match must be a security revision tag")
			return
		}
		view, ok := decodeSecurityView(w, r)
		if !ok {
			return
		}
		updated, err := s.write(r.Context(), httpapi.CurrentUser(r), sessionToken(r), environmentID, expected, view)
		respondSecurity(w, r, updated, err)
	})
	api.MarkV3Route("GET /api/v1/security", "workspace", "manage")
	api.MarkV3Route("PUT /api/v1/security", "workspace", "manage")
}
