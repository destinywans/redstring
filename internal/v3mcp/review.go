package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

// These auxiliary routes deliberately require an explicit discovery version.
// The unversioned main UI routes cannot express a human review decision yet.
type reviewSnapshot struct {
	id, extensionID, bindingInstanceID, applicationID, environmentID string
	registrationHash, toolsHash                                      string
	registrationVersion                                              int
	toolsRaw                                                         []byte
	toolCount                                                        int
	capturedAt                                                       time.Time
}

type observedTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	ReadOnlyHint bool            `json:"read_only_hint"`
	InputSchema  json.RawMessage `json:"input_schema"`
	ToolHash     string          `json:"tool_hash"`
	Decision     string          `json:"decision"`
	Enabled      bool            `json:"enabled"`
	Revision     int             `json:"revision"`
}

func loadLatestReviewSnapshot(ctx context.Context, tx pgx.Tx, extensionID, env string) (reviewSnapshot, error) {
	var out reviewSnapshot
	err := tx.QueryRow(ctx, `SELECT id,extension_id,binding_instance_id,application_id,environment_id,
 registration_version,registration_hash,tools_hash,tools,tool_count,captured_at
 FROM v3_mcp_discovery_snapshots WHERE extension_id=$1 AND environment_id=$2
 ORDER BY snapshot_seq DESC LIMIT 1`, extensionID, env).Scan(
		&out.id, &out.extensionID, &out.bindingInstanceID, &out.applicationID, &out.environmentID,
		&out.registrationVersion, &out.registrationHash, &out.toolsHash, &out.toolsRaw,
		&out.toolCount, &out.capturedAt)
	return out, err
}

func (snap reviewSnapshot) matches(current discoveryRegistration) bool {
	return snap.extensionID == current.ID && snap.applicationID == current.ApplicationID &&
		snap.environmentID == current.EnvironmentID && snap.bindingInstanceID == current.bindingInstanceID &&
		snap.registrationVersion == current.version && snap.registrationHash == current.hash
}

func (snap reviewSnapshot) tools() ([]observedTool, error) {
	if snap.toolsHash != digestBytes(snap.toolsRaw) || len(snap.toolsRaw) > maxDiscoveryBytes {
		return nil, errors.New("MCP discovery snapshot digest mismatch")
	}
	var rawTools []json.RawMessage
	if err := json.Unmarshal(snap.toolsRaw, &rawTools); err != nil || len(rawTools) != snap.toolCount || len(rawTools) > maxDiscoveredTools {
		return nil, errors.New("MCP discovery snapshot is inconsistent")
	}
	tools := make([]observedTool, 0, len(rawTools))
	seen := make(map[string]bool, len(rawTools))
	for _, raw := range rawTools {
		var tool observedTool
		if err := json.Unmarshal(raw, &tool); err != nil || !mcpIdentifier.MatchString(tool.Name) ||
			seen[tool.Name] || len(tool.InputSchema) == 0 {
			return nil, errors.New("MCP discovery snapshot tool is inconsistent")
		}
		tool.ToolHash = digestBytes(raw)
		seen[tool.Name] = true
		tools = append(tools, tool)
	}
	return tools, nil
}

// Ordinary connectors.manage is available to machine identities. A V3 tool
// review and its enable switch require a human identity holding that grant
// through a non-machine role at the time of the write.
func humanMCPReviewer(ctx context.Context, tx pgx.Tx, actor, applicationID, environmentID string) (bool, error) {
	var permitted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$1 AND g.v3_identity_kind='human' AND g.role_kind<>'machine'
 AND g.module='connectors' AND g.action='manage'
 AND ((g.scope_kind='global' AND g.scope_id='*')
   OR (g.scope_kind='environment' AND g.scope_id=$2)
   OR (g.scope_kind='application' AND g.scope_id=$3))
 )`, actor, environmentID, applicationID).Scan(&permitted)
	return permitted, err
}

func requireHumanMCPReviewer(w http.ResponseWriter, r *http.Request, tx pgx.Tx, current discoveryRegistration) bool {
	permitted, err := humanMCPReviewer(r.Context(), tx, httpapi.CurrentUser(r).ID,
		current.ApplicationID, current.EnvironmentID)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "MCP reviewer identity is unavailable")
		return false
	}
	if !permitted {
		httpapi.Error(w, http.StatusForbidden, "human_review_required", "A human connector manager must review and change tool switches")
		return false
	}
	return true
}

func loadCompleteReview(ctx context.Context, tx pgx.Tx, snapshotID string, tools []observedTool) (map[string]observedTool, error) {
	rows, err := tx.Query(ctx, `SELECT tool_name,tool_hash,decision,enabled,revision
 FROM v3_mcp_tool_review_decisions WHERE snapshot_id=$1`, snapshotID)
	if err != nil {
		return nil, err
	}
	decisions := make(map[string]observedTool, len(tools))
	for rows.Next() {
		var item observedTool
		if err = rows.Scan(&item.Name, &item.ToolHash, &item.Decision, &item.Enabled, &item.Revision); err != nil {
			break
		}
		decisions[item.Name] = item
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(decisions) != len(tools) {
		return nil, errors.New("MCP review decisions are incomplete")
	}
	for _, tool := range tools {
		item, exists := decisions[tool.Name]
		if !exists || item.ToolHash != tool.ToolHash ||
			(item.Decision != "allow_read_only" && item.Decision != "deny") ||
			(item.Enabled && item.Decision != "allow_read_only") || item.Revision < 1 {
			return nil, errors.New("MCP review decision does not match snapshot")
		}
	}
	return decisions, nil
}

// reviewCurrent holds the registration/binding lock for every write. Discover
// takes the same lock before assigning snapshot_seq, so a concurrent discovery
// cannot silently replace a candidate during a review or switch update.
func (s *Service) reviewCurrent(w http.ResponseWriter, r *http.Request, write bool) (pgx.Tx, discoveryRegistration, reviewSnapshot, bool) {
	envs, ok := s.ready(w, r)
	if !ok {
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	if scope.All {
		httpapi.Error(w, http.StatusConflict, "env_view_only", "All non-production environments are read-only")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	if len(envs) != 1 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_environment", "Choose one environment")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	if r.PathValue("id") == "" {
		httpapi.Error(w, http.StatusNotFound, "not_found", "MCP server not found")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if write {
		options = pgx.TxOptions{}
	}
	tx, err := s.DB.BeginTx(r.Context(), options)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review storage is unavailable")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	current, err := loadDiscoveryRegistration(r.Context(), tx, r.PathValue("id"), envs[0], write)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(r.Context())
		httpapi.Error(w, http.StatusNotFound, "not_found", "MCP server not found")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	if err != nil {
		_ = tx.Rollback(r.Context())
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP registration is unavailable")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	if write {
		if err = lockReviewerGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); err != nil {
			_ = tx.Rollback(r.Context())
			writeDiscoveryAuthorizationError(w, err, true)
			return nil, discoveryRegistration{}, reviewSnapshot{}, false
		}
	}
	if err = s.allowedTx(r, tx, current.registration, "manage"); err != nil {
		_ = tx.Rollback(r.Context())
		writeDiscoveryAuthorizationError(w, err, true)
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	if err = current.validateDiscoveryRegistration(); err != nil {
		_ = tx.Rollback(r.Context())
		httpapi.Error(w, http.StatusConflict, "mcp_registration_changed", "MCP registration requires a new discovery")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	snap, err := loadLatestReviewSnapshot(r.Context(), tx, current.ID, current.EnvironmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(r.Context())
		httpapi.Error(w, http.StatusConflict, "mcp_discovery_required", "Discover MCP tools before review")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	if err != nil {
		_ = tx.Rollback(r.Context())
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP discovery snapshot is unavailable")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	if !snap.matches(current) {
		_ = tx.Rollback(r.Context())
		httpapi.Error(w, http.StatusConflict, "mcp_discovery_stale", "MCP registration or environment binding changed; discover again")
		return nil, discoveryRegistration{}, reviewSnapshot{}, false
	}
	return tx, current, snap, true
}

func (s *Service) reviewCandidate(w http.ResponseWriter, r *http.Request) {
	tx, current, snap, ok := s.reviewCurrent(w, r, false)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	tools, err := snap.tools()
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP discovery snapshot is inconsistent")
		return
	}
	var reviewed bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_mcp_tool_reviews
 WHERE snapshot_id=$1 AND extension_id=$2 AND binding_instance_id=$3 AND application_id=$4
 AND environment_id=$5 AND registration_version=$6 AND registration_hash=$7 AND tools_hash=$8)`,
		snap.id, current.ID, current.bindingInstanceID, current.ApplicationID, current.EnvironmentID,
		current.version, current.hash, snap.toolsHash).Scan(&reviewed)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is unavailable")
		return
	}
	if reviewed {
		decisions, decisionErr := loadCompleteReview(r.Context(), tx, snap.id, tools)
		if decisionErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is inconsistent")
			return
		}
		for i := range tools {
			item := decisions[tools[i].Name]
			tools[i].Decision, tools[i].Enabled, tools[i].Revision = item.Decision, item.Enabled, item.Revision
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is unavailable")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"snapshot_id": snap.id, "mcp_server_id": current.ID, "application_id": current.ApplicationID,
		"environment_id": current.EnvironmentID, "registration_version": current.version,
		"registration_hash": current.hash, "tools_hash": snap.toolsHash, "tool_count": len(tools),
		"captured_at": snap.capturedAt.UTC().Format(time.RFC3339Nano), "tools": tools,
		"trust": "untrusted_observation", "reviewed": reviewed,
		"health_verified": false, "execution_available": false,
	})
}

type reviewVersionRequest struct {
	SnapshotID          string `json:"snapshot_id"`
	RegistrationVersion int    `json:"registration_version"`
	RegistrationHash    string `json:"registration_hash"`
	ToolsHash           string `json:"tools_hash"`
	Decisions           []struct {
		Name     string `json:"name"`
		ToolHash string `json:"tool_hash"`
		Decision string `json:"decision"`
	} `json:"decisions"`
}

func (s *Service) reviewVersion(w http.ResponseWriter, r *http.Request) {
	var request reviewVersionRequest
	if !httpapi.Decode(w, r, &request) {
		return
	}
	tx, current, snap, ok := s.reviewCurrent(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if !requireHumanMCPReviewer(w, r, tx, current) {
		return
	}
	if request.SnapshotID != snap.id || request.RegistrationVersion != current.version ||
		request.RegistrationHash != current.hash || request.ToolsHash != snap.toolsHash {
		httpapi.Error(w, http.StatusConflict, "mcp_discovery_stale", "Review the current MCP discovery snapshot")
		return
	}
	tools, err := snap.tools()
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP discovery snapshot is inconsistent")
		return
	}
	if request.Decisions == nil || len(request.Decisions) != len(tools) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_review", "Review every discovered tool explicitly")
		return
	}
	lookup := make(map[string]string, len(tools))
	for _, tool := range tools {
		lookup[tool.Name] = tool.ToolHash
	}
	decisions := make(map[string]string, len(tools))
	for _, item := range request.Decisions {
		if lookup[item.Name] == "" || lookup[item.Name] != item.ToolHash ||
			(item.Decision != "allow_read_only" && item.Decision != "deny") || decisions[item.Name] != "" {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_review", "Review decisions must match every discovered tool")
			return
		}
		decisions[item.Name] = item.Decision
	}
	actor := httpapi.CurrentUser(r).ID
	var alreadyReviewed bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_mcp_tool_reviews WHERE snapshot_id=$1)`, snap.id).Scan(&alreadyReviewed)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is unavailable")
		return
	}
	if alreadyReviewed {
		httpapi.Error(w, http.StatusConflict, "mcp_already_reviewed", "MCP tool version is already reviewed")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_mcp_tool_reviews
 (snapshot_id,extension_id,binding_instance_id,application_id,environment_id,
  registration_version,registration_hash,tools_hash,reviewed_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, snap.id, current.ID, current.bindingInstanceID,
		current.ApplicationID, current.EnvironmentID, current.version, current.hash, snap.toolsHash, actor)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review could not be saved")
		return
	}
	allowed := 0
	for _, tool := range tools {
		decision := decisions[tool.Name]
		if decision == "allow_read_only" {
			allowed++
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_mcp_tool_review_decisions
 (snapshot_id,tool_name,tool_hash,decision,updated_by) VALUES($1,$2,$3,$4,$5)`,
			snap.id, tool.Name, tool.ToolHash, decision, actor)
		if err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review could not be saved")
			return
		}
	}
	changes, err := json.Marshal(map[string]any{
		"environment_id": current.EnvironmentID, "snapshot_id": snap.id,
		"registration_version": current.version, "registration_hash": current.hash,
		"tools_hash": snap.toolsHash, "tool_count": len(tools), "allowed_read_only_count": allowed,
	})
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP review audit is unavailable")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
	 (actor_id,application_id,action,target,outcome,changes)
	 VALUES($1,$2,'v3.mcp.tool_version_reviewed',$3,'success',$4::jsonb)`,
		actor, current.ApplicationID, current.ID, changes)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP review audit is unavailable")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review could not be committed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"snapshot_id": snap.id, "reviewed": true, "tool_count": len(tools),
		"allowed_read_only_count": allowed, "enabled_count": 0, "execution_available": false,
	})
}

type changeReviewedToolRequest struct {
	SnapshotID       string `json:"snapshot_id"`
	ToolsHash        string `json:"tools_hash"`
	ToolHash         string `json:"tool_hash"`
	ExpectedRevision int    `json:"expected_revision"`
	Enabled          *bool  `json:"enabled"`
}

func (s *Service) changeReviewedTool(w http.ResponseWriter, r *http.Request) {
	var request changeReviewedToolRequest
	if !httpapi.Decode(w, r, &request) {
		return
	}
	name := r.PathValue("name")
	if !mcpIdentifier.MatchString(name) || request.ExpectedRevision < 1 || request.Enabled == nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_tool_change", "A reviewed tool and expected revision are required")
		return
	}
	tx, current, snap, ok := s.reviewCurrent(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if !requireHumanMCPReviewer(w, r, tx, current) {
		return
	}
	if request.SnapshotID != snap.id || request.ToolsHash != snap.toolsHash {
		httpapi.Error(w, http.StatusConflict, "mcp_discovery_stale", "Tool change requires the current reviewed snapshot")
		return
	}
	tools, err := snap.tools()
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP discovery snapshot is inconsistent")
		return
	}
	actualHash := ""
	for _, tool := range tools {
		if tool.Name == name {
			actualHash = tool.ToolHash
			break
		}
	}
	if actualHash == "" {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Reviewed MCP tool not found")
		return
	}
	if request.ToolHash != actualHash {
		httpapi.Error(w, http.StatusConflict, "mcp_tool_changed", "Tool declaration changed; review the current version")
		return
	}
	var reviewExists bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_mcp_tool_reviews
 WHERE snapshot_id=$1 AND extension_id=$2 AND binding_instance_id=$3 AND application_id=$4
 AND environment_id=$5 AND registration_version=$6 AND registration_hash=$7 AND tools_hash=$8)`,
		snap.id, current.ID, current.bindingInstanceID, current.ApplicationID, current.EnvironmentID,
		current.version, current.hash, snap.toolsHash).Scan(&reviewExists)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is unavailable")
		return
	}
	if !reviewExists {
		httpapi.Error(w, http.StatusConflict, "mcp_review_required", "Review the current MCP tool version first")
		return
	}
	if _, err = loadCompleteReview(r.Context(), tx, snap.id, tools); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is inconsistent")
		return
	}
	var savedHash, decision string
	var enabled bool
	var revision int
	err = tx.QueryRow(r.Context(), `SELECT tool_hash,decision,enabled,revision
 FROM v3_mcp_tool_review_decisions WHERE snapshot_id=$1 AND tool_name=$2 FOR UPDATE`,
		snap.id, name).Scan(&savedHash, &decision, &enabled, &revision)
	if errors.Is(err, pgx.ErrNoRows) || savedHash != actualHash {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is inconsistent")
		return
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP review is unavailable")
		return
	}
	if request.ExpectedRevision != revision {
		httpapi.Error(w, http.StatusConflict, "mcp_tool_revision_changed", "Tool switch changed; reload the reviewed version")
		return
	}
	if *request.Enabled && decision != "allow_read_only" {
		httpapi.Error(w, http.StatusUnprocessableEntity, "mcp_tool_denied", "Denied or write-capable tools cannot be enabled")
		return
	}
	changed := enabled != *request.Enabled
	if changed {
		revision++
		_, err = tx.Exec(r.Context(), `UPDATE v3_mcp_tool_review_decisions
 SET enabled=$3,revision=$4,updated_by=$5,updated_at=now()
 WHERE snapshot_id=$1 AND tool_name=$2`, snap.id, name, *request.Enabled, revision, httpapi.CurrentUser(r).ID)
		if err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP tool switch could not be saved")
			return
		}
		changes, marshalErr := json.Marshal(map[string]any{
			"environment_id": current.EnvironmentID, "snapshot_id": snap.id,
			"tools_hash": snap.toolsHash, "tool_name": name, "tool_hash": actualHash,
			"enabled": *request.Enabled, "revision": revision,
		})
		if marshalErr != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP tool switch audit is unavailable")
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
		 (actor_id,application_id,action,target,outcome,changes)
		 VALUES($1,$2,'v3.mcp.reviewed_tool_changed',$3,'success',$4::jsonb)`,
			httpapi.CurrentUser(r).ID, current.ApplicationID, current.ID, changes)
		if err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "MCP tool switch audit is unavailable")
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "mcp_unavailable", "MCP tool switch could not be committed")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"snapshot_id": snap.id, "tool_name": name, "enabled": *request.Enabled,
		"revision": revision, "changed": changed, "execution_available": false,
	})
}
