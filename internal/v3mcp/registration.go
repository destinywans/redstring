package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type registrationRequest struct {
	ApplicationID string `json:"application_id"`
	Title         string `json:"title"`
	Maintainer    string `json:"maintainer"`
	Endpoint      string `json:"endpoint"`
	CredentialRef string `json:"credential_ref"`
	AllowHTTP     bool   `json:"allow_http"`
}

type v3RegistrationRecord struct {
	aiextensions.Record
	RegistrationSource string `json:"registration_source"`
}

func (s *Service) registrationEndpointAllowed(ctx context.Context, v registration) bool {
	var source struct {
		RegistrationSource string `json:"registration_source"`
	}
	if json.Unmarshal(v.raw, &source) != nil {
		return false
	}
	if source.RegistrationSource == "" {
		return true
	} // Existing legacy enrollment.
	return source.RegistrationSource == "v3" && s.AuthorizeRegistrationEndpoint != nil && v.Record.MCP != nil &&
		s.AuthorizeRegistrationEndpoint(ctx, v.ApplicationID, v.EnvironmentID, *v.Record.MCP) == nil
}

func registrationEndpointError(w http.ResponseWriter) {
	httpapi.Error(w, 503, "mcp_registration_endpoint_unavailable", "MCP transport is not admitted for this application and environment")
}

func (in registrationRequest) record() (aiextensions.Record, bool) {
	r := aiextensions.Record{ApplicationID: in.ApplicationID, Kind: "mcp", Title: in.Title,
		Maintainer: in.Maintainer, Enabled: true, Version: 1,
		MCP: &aiextensions.MCPConfig{Endpoint: in.Endpoint, CredentialRef: in.CredentialRef,
			AllowHTTP: in.AllowHTTP, Bindings: []aiextensions.Binding{}},
	}
	for _, value := range []string{in.ApplicationID, in.Title, in.Maintainer, in.Endpoint, in.CredentialRef} {
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") || strings.TrimSpace(value) != value {
			return r, false
		}
	}
	return r, mcpIdentifier.MatchString(in.ApplicationID) && validMCPRegistration(r, false)
}

// Registration is metadata only: it neither contacts the endpoint nor accepts
// tool declarations, approvals, credentials, environment overrides or Agent grants.
func (s *Service) createRegistration(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	if scope.All {
		httpapi.Error(w, 409, "env_view_only", "All non-production environments are read-only")
		return
	}
	if len(envs) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Choose one environment")
		return
	}
	var request registrationRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if !httpapi.Decode(w, r, &request) {
		return
	}
	record, valid := request.record()
	if !valid {
		httpapi.Error(w, 422, "invalid_mcp_registration", "Invalid MCP registration configuration")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP storage is unavailable")
		return
	}
	defer rollbackCandidateRead(tx)
	v := registration{ApplicationID: request.ApplicationID, EnvironmentID: envs[0]}
	// Same source -> account -> session -> grant order as other MCP mutations.
	sourceErr := tx.QueryRow(ctx, `SELECT a.name,COALESCE(a.owner_team_id,'')
 FROM applications a
 JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=$2
 JOIN application_environments app_env ON app_env.application_id=a.id AND app_env.key=$2 AND NOT app_env.archived
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ae,app_env`, v.ApplicationID, v.EnvironmentID).Scan(&v.Application, &v.OwnerTeamID)
	actor := httpapi.CurrentUser(r).ID
	if err = lockReviewerAccount(ctx, tx, actor); err == nil {
		err = lockReviewerWriteSession(r, tx, actor)
	}
	if err == nil {
		err = lockReviewerRoleGrantSources(ctx, tx, actor)
	}
	if err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if errors.Is(sourceErr, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "MCP server not found")
		return
	}
	if sourceErr != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP application scope is unavailable")
		return
	}
	if err = s.allowedTx(r, tx, v, "manage"); err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if s.AuthorizeRegistrationEndpoint == nil || s.AuthorizeRegistrationEndpoint(ctx, v.ApplicationID, v.EnvironmentID, *record.MCP) != nil {
		registrationEndpointError(w)
		return
	}
	record.ID = "mcp-" + store.ID()
	// Keep the legacy content hash convention so the registration and immutable
	// revision remain usable by readers; updated metadata is excluded from it.
	persisted := v3RegistrationRecord{Record: record, RegistrationSource: "v3"}
	hashBytes, _ := json.Marshal(persisted)
	record.ContentHash = digestBytes(hashBytes)
	record.UpdatedBy = actor
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&record.UpdatedAt); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration could not be saved")
		return
	}
	persisted.Record = record
	raw, _ := json.Marshal(persisted)
	if _, err = tx.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record) VALUES($1,$2,'mcp',1,$3)`, record.ID, record.ApplicationID, raw); err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO ai_extension_revisions(id,version,record) VALUES($1,1,$2)`, record.ID, raw)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings(extension_id,application_id,environment_id,bound_by) VALUES($1,$2,$3,$4)`, record.ID, record.ApplicationID, v.EnvironmentID, actor)
	}
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration could not be saved")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.mcp.registered',$3,'success',jsonb_build_object('environment_id',$4::text,'version',1,'reviewed',false,'execution_available',false))`, actor, record.ApplicationID, record.ID, v.EnvironmentID)
	if err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "MCP registration audit is unavailable")
		return
	}
	if err = lockReviewerWriteSession(r, tx, actor); err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration could not be committed")
		return
	}
	w.Header().Set("Location", "/api/v1/mcp-servers/"+record.ID)
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"id": record.ID, "application_id": record.ApplicationID,
		"environment_id": v.EnvironmentID, "version": 1, "status": "pending", "bound": true,
		"reviewed": false, "execution_available": false, "health_verified": false})
}
