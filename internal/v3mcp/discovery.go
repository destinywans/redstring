package v3mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

const maxDiscoveredTools = 128
const maxDiscoveryBytes = 512 << 10

var errInconsistentDiscoveryRegistration = errors.New("inconsistent MCP registration")

type discoveryRegistration struct {
	registration
	version           int
	hash              string
	bindingInstanceID string
}

func digestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableJSON(value []byte) any {
	if value == nil {
		return nil
	}
	return json.RawMessage(value)
}

// loadDiscoveryRegistration resolves only relational identity and raw bytes.
// Callers must authorize that identity before decoding the mutable record or
// revealing whether its contents are malformed. The lock variant is used only
// after the network call.
func loadDiscoveryRegistration(ctx context.Context, tx pgx.Tx, id, env string, lock bool) (discoveryRegistration, error) {
	var out discoveryRegistration
	out.EnvironmentID = env
	query := `SELECT e.id,e.application_id,a.name,COALESCE(a.owner_team_id,''),e.record,e.version,b.binding_instance_id
 FROM ai_extensions e
 JOIN applications a ON a.id=e.application_id AND NOT a.archived
 JOIN v3_mcp_environment_bindings b ON b.extension_id=e.id AND b.application_id=a.id AND b.environment_id=$2
 JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=b.environment_id
 JOIN application_environments app_env ON app_env.application_id=a.id
  AND app_env.key=b.environment_id AND NOT app_env.archived
 WHERE e.id=$1 AND e.kind='mcp'`
	if lock {
		query += ` FOR UPDATE OF e,a,b,ae,app_env`
	}
	err := tx.QueryRow(ctx, query, id, env).Scan(&out.ID, &out.ApplicationID, &out.Application, &out.OwnerTeamID, &out.raw, &out.version, &out.bindingInstanceID)
	if err != nil {
		return out, err
	}
	out.Version = out.version
	out.BindingInstanceID = out.bindingInstanceID
	out.hash = digestBytes(out.raw)
	return out, nil
}

func (out *discoveryRegistration) validateDiscoveryRegistration() error {
	var origin struct {
		RegistrationSource string `json:"registration_source"`
	}
	if json.Unmarshal(out.raw, &origin) != nil || (origin.RegistrationSource != "" && origin.RegistrationSource != "v3") ||
		out.validateRecord() != nil || !validMCPRegistration(out.Record, origin.RegistrationSource == "") || out.version != out.Record.Version {
		return errInconsistentDiscoveryRegistration
	}
	return nil
}

// validateDiscovery normalizes untrusted remote declarations to bounded JSON.
// read_only_hint is retained solely as a claim made by the remote server.
func validateDiscovery(tools []aiextensions.ToolInfo) ([]byte, error) {
	if len(tools) > maxDiscoveredTools {
		return nil, errors.New("too many discovered tools")
	}
	normalized := make([]aiextensions.ToolInfo, 0, len(tools))
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if !mcpIdentifier.MatchString(tool.Name) || seen[tool.Name] ||
			!utf8.ValidString(tool.Description) || len(tool.Description) > 2000 {
			return nil, errors.New("invalid discovered tool")
		}
		seen[tool.Name] = true
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil || len(schema) > 16<<10 || len(schema) < 2 {
			return nil, errors.New("invalid discovered input schema")
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(schema, &object) != nil || object == nil {
			return nil, errors.New("invalid discovered input schema")
		}
		var kind string
		if json.Unmarshal(object["type"], &kind) != nil || kind != "object" {
			return nil, errors.New("invalid discovered input schema")
		}
		if raw, present := object["properties"]; present {
			var properties map[string]json.RawMessage
			if json.Unmarshal(raw, &properties) != nil || properties == nil {
				return nil, errors.New("invalid discovered input schema")
			}
		}
		if raw, present := object["required"]; present {
			var required []string
			if json.Unmarshal(raw, &required) != nil {
				return nil, errors.New("invalid discovered input schema")
			}
			for _, name := range required {
				if name == "" || len(name) > 100 {
					return nil, errors.New("invalid discovered input schema")
				}
			}
		}
		normalized = append(normalized, aiextensions.ToolInfo{
			Name: tool.Name, Description: tool.Description, ReadOnlyHint: tool.ReadOnlyHint, InputSchema: json.RawMessage(schema),
		})
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Name < normalized[j].Name })
	payload, err := json.Marshal(normalized)
	if err != nil || len(payload) > maxDiscoveryBytes {
		return nil, errors.New("discovery payload exceeds limit")
	}
	return payload, nil
}

func validateCatalog(c aiextensions.MCPCatalog) (tools, resources, prompts []byte, err error) {
	if c.ToolsState != "supported" && c.ToolsState != "unsupported" ||
		c.ResourcesState != "supported" && c.ResourcesState != "unsupported" && c.ResourcesState != "unknown" ||
		c.PromptsState != "supported" && c.PromptsState != "unsupported" && c.PromptsState != "unknown" ||
		(c.ToolsState == "unsupported" && len(c.Tools) != 0) ||
		(c.ResourcesState != "supported" && len(c.Resources) != 0) ||
		(c.PromptsState != "supported" && len(c.Prompts) != 0) ||
		len(c.ServerName) > 160 || len(c.ServerVersion) > 160 || len(c.ProtocolVersion) > 80 ||
		!utf8.ValidString(c.ServerName) || !utf8.ValidString(c.ServerVersion) || !utf8.ValidString(c.ProtocolVersion) {
		return nil, nil, nil, errors.New("invalid MCP catalog state")
	}
	tools, err = validateDiscovery(c.Tools)
	if err != nil {
		return nil, nil, nil, err
	}
	if c.ResourcesState == "supported" {
		if len(c.Resources) > 128 {
			return nil, nil, nil, errors.New("too many MCP resources")
		}
		seen := map[string]bool{}
		for _, item := range c.Resources {
			parsed, parseErr := url.Parse(item.URI)
			if parseErr != nil || parsed.Scheme == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
				len(item.URI) > 2000 || len(item.Name) > 160 || len(item.Description) > 2000 ||
				!utf8.ValidString(item.URI) || !utf8.ValidString(item.Name) || !utf8.ValidString(item.Description) || seen[item.URI] {
				return nil, nil, nil, errors.New("invalid MCP resource")
			}
			seen[item.URI] = true
		}
		resources, err = json.Marshal(c.Resources)
		if err != nil || len(resources) > 256<<10 {
			return nil, nil, nil, errors.New("MCP resources exceed limit")
		}
	}
	if c.PromptsState == "supported" {
		if len(c.Prompts) > 128 {
			return nil, nil, nil, errors.New("too many MCP prompts")
		}
		seen := map[string]bool{}
		for _, item := range c.Prompts {
			if !mcpIdentifier.MatchString(item.Name) || len(item.Description) > 2000 || !utf8.ValidString(item.Description) || seen[item.Name] {
				return nil, nil, nil, errors.New("invalid MCP prompt")
			}
			seen[item.Name] = true
		}
		prompts, err = json.Marshal(c.Prompts)
		if err != nil || len(prompts) > 256<<10 {
			return nil, nil, nil, errors.New("MCP prompts exceed limit")
		}
	}
	for _, value := range []string{c.ServerName, c.ServerVersion, c.ProtocolVersion} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, nil, nil, errors.New("invalid MCP server metadata")
		}
	}
	return tools, resources, prompts, nil
}

func (s *Service) discover(w http.ResponseWriter, r *http.Request) {
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
	if r.PathValue("id") == "" {
		httpapi.Error(w, http.StatusNotFound, "not_found", "MCP server not found")
		return
	}
	if s.DiscoverTools == nil && s.DiscoverCatalog == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_discovery_unavailable", "MCP discovery is unavailable")
		return
	}
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1))
		if err != nil || len(body) != 0 {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_request", "Discovery does not accept a request body")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	preTx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registration is unavailable")
		return
	}
	var before discoveryRegistration
	before, err = loadDiscoveryRegistration(ctx, preTx, r.PathValue("id"), envs[0], false)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = preTx.Rollback(ctx)
		httpapi.Error(w, http.StatusNotFound, "not_found", "MCP server not found")
		return
	}
	if err != nil {
		_ = preTx.Rollback(ctx)
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registration is unavailable")
		return
	}
	if err = s.allowedTx(r, preTx, before.registration, "manage"); err != nil {
		_ = preTx.Rollback(ctx)
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if err = before.validateDiscoveryRegistration(); err != nil {
		_ = preTx.Rollback(ctx)
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registration is inconsistent")
		return
	}
	if !s.registrationEndpointAllowed(ctx, before.registration) {
		_ = preTx.Rollback(ctx)
		registrationEndpointError(w)
		return
	}
	if err = preTx.Commit(ctx); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registration is unavailable")
		return
	}
	var catalog aiextensions.MCPCatalog
	var discoveryErr error
	if s.DiscoverCatalog != nil {
		catalog, discoveryErr = s.DiscoverCatalog(ctx, *before.Record.MCP)
	} else {
		catalog.Tools, discoveryErr = s.DiscoverTools(ctx, *before.Record.MCP)
		catalog.ToolsState, catalog.ResourcesState, catalog.PromptsState = "supported", "unknown", "unknown"
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP discovery could not be saved")
		return
	}
	defer tx.Rollback(ctx)
	after, loadErr := loadDiscoveryRegistration(ctx, tx, before.ID, before.EnvironmentID, true)
	actorID := httpapi.CurrentUser(r).ID
	if err = lockReviewerAccount(ctx, tx, actorID); err == nil {
		err = lockReviewerWriteSession(r, tx, actorID)
	}
	if err == nil {
		err = lockReviewerRoleGrantSources(ctx, tx, actorID)
	}
	if err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	// The caller was authorized before contacting the server. Recheck that
	// previous object first, so revoked grants do not reveal a changed source.
	if err = s.allowedTx(r, tx, before.registration, "manage"); err != nil {
		writeDiscoveryAuthorizationError(w, err, false)
		return
	}
	if errors.Is(loadErr, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusConflict, "mcp_registration_changed", "MCP registration or environment binding changed during discovery")
		return
	}
	if loadErr != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registration is unavailable")
		return
	}
	// A relational reassignment must not disclose the new object's state to a
	// caller who can only manage the old application.
	if err = s.allowedTx(r, tx, after.registration, "manage"); err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if err = after.validateDiscoveryRegistration(); err != nil {
		httpapi.Error(w, http.StatusConflict, "mcp_registration_changed", "MCP registration changed during discovery")
		return
	}
	if !s.registrationEndpointAllowed(ctx, after.registration) {
		registrationEndpointError(w)
		return
	}
	if after.ApplicationID != before.ApplicationID || after.bindingInstanceID != before.bindingInstanceID ||
		after.version != before.version || after.hash != before.hash ||
		after.Record.Enabled != before.Record.Enabled {
		httpapi.Error(w, http.StatusConflict, "mcp_registration_changed", "MCP registration changed during discovery")
		return
	}
	if discoveryErr != nil {
		// Upstream errors may contain endpoint URLs, credentials or remote text.
		if err = recordDiscoveryFailure(ctx, tx, r, after, "upstream"); err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP discovery failure audit is unavailable")
			return
		}
		httpapi.Error(w, http.StatusBadGateway, "mcp_discovery_failed", "MCP discovery failed")
		return
	}
	payload, resourcesPayload, promptsPayload, err := validateCatalog(catalog)
	if err != nil {
		if err = recordDiscoveryFailure(ctx, tx, r, after, "invalid_declaration"); err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP discovery failure audit is unavailable")
			return
		}
		httpapi.Error(w, http.StatusBadGateway, "mcp_discovery_invalid", "MCP discovery returned invalid tool declarations")
		return
	}
	id := store.ID()
	toolsHash := digestBytes(payload)
	var capturedAt time.Time
	err = tx.QueryRow(ctx, `INSERT INTO v3_mcp_discovery_snapshots
	 (id,extension_id,binding_instance_id,application_id,environment_id,registration_version,registration_hash,
  tools_hash,tools,tool_count,captured_by,server_name,server_version,protocol_version,tools_state,resources_state,prompts_state,resources,prompts)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19) RETURNING captured_at`,
		id, after.ID, after.bindingInstanceID, after.ApplicationID, after.EnvironmentID, after.version, after.hash,
		toolsHash, payload, len(catalog.Tools), httpapi.CurrentUser(r).ID,
		nullableText(catalog.ServerName), nullableText(catalog.ServerVersion), nullableText(catalog.ProtocolVersion),
		catalog.ToolsState, catalog.ResourcesState, catalog.PromptsState, nullableJSON(resourcesPayload), nullableJSON(promptsPayload)).Scan(&capturedAt)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP discovery could not be saved")
		return
	}
	changes, err := json.Marshal(map[string]any{
		"environment_id": after.EnvironmentID, "snapshot_id": id,
		"registration_version": after.version, "registration_hash": after.hash,
		"tools_hash": toolsHash, "tool_count": len(catalog.Tools),
	})
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP discovery audit is unavailable")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.mcp.discovery_observed',$3,'success',$4::jsonb)`,
		httpapi.CurrentUser(r).ID, after.ApplicationID, after.ID, changes)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP discovery audit is unavailable")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP discovery could not be committed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"snapshot_id": id, "mcp_server_id": after.ID, "application_id": after.ApplicationID,
		"environment_id": after.EnvironmentID, "registration_version": after.version,
		"registration_hash": after.hash, "tools_hash": toolsHash, "tool_count": len(catalog.Tools),
		"tools_state": catalog.ToolsState, "resources_state": catalog.ResourcesState, "prompts_state": catalog.PromptsState,
		"captured_at": capturedAt.UTC().Format(time.RFC3339Nano),
		"trust":       "untrusted_observation", "approved": false, "health_verified": false,
	})
}

func recordDiscoveryFailure(ctx context.Context, tx pgx.Tx, r *http.Request, current discoveryRegistration, kind string) error {
	changes, err := json.Marshal(map[string]any{
		"environment_id":       current.EnvironmentID,
		"registration_version": current.version,
		"failure_kind":         kind,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.mcp.discovery_failed',$3,'failure',$4::jsonb)`,
		httpapi.CurrentUser(r).ID, current.ApplicationID, current.ID, changes)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeDiscoveryAuthorizationError(w http.ResponseWriter, err error, hideObject bool) {
	if errors.Is(err, store.ErrForbidden) {
		if hideObject {
			httpapi.Error(w, http.StatusNotFound, "not_found", "MCP server not found")
			return
		}
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Insufficient connector permission")
		return
	}
	httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "MCP authorization is unavailable")
}
