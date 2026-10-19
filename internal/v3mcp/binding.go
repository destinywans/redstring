package v3mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/destinywans/redstring/internal/aiextensions"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

var mcpIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)

// A V3 binding is an explicit enrollment of an existing application-scoped
// registration into exactly one environment. The environment is supplied by
// the authenticated V3 scope, never by a body or a legacy JSON field.
func (s *Service) bindEnvironment(w http.ResponseWriter, r *http.Request) {
	s.changeBinding(w, r, true)
}

func (s *Service) unbindEnvironment(w http.ResponseWriter, r *http.Request) {
	s.changeBinding(w, r, false)
}

func (s *Service) changeBinding(w http.ResponseWriter, r *http.Request, bind bool) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	if scope.All {
		httpapi.Error(w, http.StatusConflict, "env_view_only", "All non-production environments are read-only")
		return
	}
	if len(envs) != 1 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_environment", "Choose one environment")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httpapi.Error(w, http.StatusNotFound, "not_found", "MCP registration not found")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP storage is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	var v registration
	v.EnvironmentID = envs[0]
	sourceErr := tx.QueryRow(r.Context(), `SELECT e.id,e.application_id,a.name,COALESCE(a.owner_team_id,''),e.version,e.record
 FROM ai_extensions e
 JOIN applications a ON a.id=e.application_id AND NOT a.archived
 JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=$2
 JOIN application_environments app_env ON app_env.application_id=a.id
  AND app_env.key=$2 AND NOT app_env.archived
 WHERE e.id=$1 AND e.kind='mcp'
 FOR UPDATE OF e,a,ae,app_env`, id, v.EnvironmentID).Scan(&v.ID, &v.ApplicationID, &v.Application, &v.OwnerTeamID, &v.Version, &v.raw)
	// Use the same source -> account -> session -> grant lock order as review
	// and discovery. Even a missing source is reported only after the caller's
	// current session and role sources have been checked.
	actorID := httpapi.CurrentUser(r).ID
	if err = lockReviewerAccount(r.Context(), tx, actorID); err == nil {
		err = lockReviewerWriteSession(r, tx, actorID)
	}
	if err == nil {
		err = lockReviewerRoleGrantSources(r.Context(), tx, actorID)
	}
	if err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if errors.Is(sourceErr, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "MCP server not found")
		return
	}
	if sourceErr != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registration is unavailable")
		return
	}
	if err = s.allowedTx(r, tx, v, "manage"); err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if bind {
		var origin struct {
			RegistrationSource string `json:"registration_source"`
		}
		if err = json.Unmarshal(v.raw, &origin); err != nil ||
			(origin.RegistrationSource != "" && origin.RegistrationSource != "v3") {
			httpapi.Error(w, http.StatusUnprocessableEntity, "mcp_registration_unreviewed", "MCP registration source is invalid")
			return
		}
		if err = v.validateRecord(); err != nil || !validMCPRegistration(v.Record, origin.RegistrationSource == "") {
			httpapi.Error(w, http.StatusUnprocessableEntity, "mcp_registration_unreviewed", "MCP registration is malformed, disabled or not reviewed for read-only tools")
			return
		}
		if !s.registrationEndpointAllowed(r.Context(), v) {
			registrationEndpointError(w)
			return
		}
	}
	changed := false
	if bind {
		var existingApp string
		err = tx.QueryRow(r.Context(), `SELECT application_id FROM v3_mcp_environment_bindings
 WHERE extension_id=$1 AND environment_id=$2 FOR UPDATE`, v.ID, v.EnvironmentID).Scan(&existingApp)
		if err == nil && existingApp != v.ApplicationID {
			httpapi.Error(w, http.StatusConflict, "mcp_binding_conflict", "MCP registration is bound to a different application")
			return
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP binding is unavailable")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = tx.Exec(r.Context(), `INSERT INTO v3_mcp_environment_bindings
 (extension_id,application_id,environment_id,bound_by)
 VALUES($1,$2,$3,$4)`, v.ID, v.ApplicationID, v.EnvironmentID, httpapi.CurrentUser(r).ID)
			changed = err == nil
		}
	} else {
		tag, deleteErr := tx.Exec(r.Context(), `DELETE FROM v3_mcp_environment_bindings
 WHERE extension_id=$1 AND application_id=$2 AND environment_id=$3`, v.ID, v.ApplicationID, v.EnvironmentID)
		err = deleteErr
		changed = tag.RowsAffected() == 1
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP binding could not be saved")
		return
	}
	if changed {
		action := "v3.mcp.environment_unbound"
		if bind {
			action = "v3.mcp.environment_bound"
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,'success',jsonb_build_object('environment_id',$5::text))`,
			httpapi.CurrentUser(r).ID, v.ApplicationID, action, v.ID, v.EnvironmentID)
		if err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP binding audit is unavailable")
			return
		}
	}
	if err = lockReviewerWriteSession(r, tx, actorID); err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP binding could not be committed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"id": v.ID, "application_id": v.ApplicationID,
		"environment_id": v.EnvironmentID, "bound": bind, "changed": changed,
	})
}

// Legacy JSON may predate the current writer's validation. Explicit enrollment
// is limited to active, reviewed read-only records; this does not attest to
// live MCP discovery, health or the server's advertised readOnlyHint.
func bindableMCP(r aiextensions.Record) bool {
	return validMCPRegistration(r, true)
}

// Discovery does not require legacy fixed-argument bindings. New V3 records
// start with no tools; only a later version-bound human review can approve them.
func validMCPRegistration(r aiextensions.Record, requireBindings bool) bool {
	if !r.Enabled || strings.TrimSpace(r.Title) == "" || len(r.Title) > 160 ||
		strings.TrimSpace(r.Maintainer) == "" || len(r.Maintainer) > 160 || r.Version < 1 || r.MCP == nil ||
		(requireBindings && len(r.MCP.Bindings) == 0) || len(r.MCP.Bindings) > 8 {
		return false
	}
	u, err := url.Parse(r.MCP.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(r.MCP.Endpoint, "\\\r\n\x00#") || len(r.MCP.Endpoint) > 2000 ||
		(u.Scheme != "https" && !(u.Scheme == "http" && r.MCP.AllowHTTP &&
			(u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return false
	}
	if r.MCP.CredentialRef != "" && (!mcpIdentifier.MatchString(r.MCP.CredentialRef) ||
		r.MCP.CredentialRef == "." || r.MCP.CredentialRef == "..") {
		return false
	}
	seen := map[string]bool{}
	for _, b := range r.MCP.Bindings {
		if !mcpIdentifier.MatchString(b.Name) || !mcpIdentifier.MatchString(b.Tool) ||
			!b.ReadOnlyConfirmed || seen[b.Name] || b.Arguments == nil || len(b.Description) > 1000 {
			return false
		}
		seen[b.Name] = true
		raw, err := json.Marshal(b.Arguments)
		if err != nil || len(raw) > 16384 {
			return false
		}
	}
	return true
}
