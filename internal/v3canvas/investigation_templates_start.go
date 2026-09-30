package v3canvas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type investigationTemplateStartRequest struct {
	Inputs struct {
		ApplicationID   string `json:"application_id"`
		TemplateVersion string `json:"template_version"`
		RequestID       string `json:"request_id"`
	} `json:"inputs"`
}

func (s *Service) startInvestigationTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, true)
	if !ok {
		return
	}
	var request investigationTemplateStartRequest
	if !httpapi.Decode(w, r, &request) {
		return
	}
	inputs := request.Inputs
	if inputs.ApplicationID == "" || len(inputs.ApplicationID) > 200 ||
		strings.TrimSpace(inputs.ApplicationID) != inputs.ApplicationID ||
		len(inputs.RequestID) < 8 || len(inputs.RequestID) > 128 ||
		strings.TrimSpace(inputs.RequestID) != inputs.RequestID ||
		len(inputs.TemplateVersion) < 2 || len(inputs.TemplateVersion) > 20 ||
		inputs.TemplateVersion[0] != 'v' {
		httpapi.Error(w, 422, "invalid_template_start", "Select an application and supply the listed template version and a request ID")
		return
	}
	version, err := strconv.Atoi(inputs.TemplateVersion[1:])
	if err != nil || version < 1 || inputs.TemplateVersion != "v"+strconv.Itoa(version) {
		httpapi.Error(w, 422, "invalid_template_start", "Invalid template version")
		return
	}
	templateID := r.PathValue("id")
	if templateID == "" || len(templateID) > 200 {
		httpapi.Error(w, 404, "not_found", "Investigation template not found")
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	ctx, user, env := r.Context(), httpapi.CurrentUser(r), envs[0]
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	defer tx.Rollback(ctx)
	creation := createRequest{Kind: "investigation", Services: []string{inputs.ApplicationID}}
	service, failure := s.prepareCreation(ctx, tx, cookie.Value, user, env, creation)
	if failure != nil {
		failure.write(w)
		return
	}
	// Serialize this actor's request ID before looking for an earlier commit.
	// A retry that arrived while the first transaction was still running waits
	// here, then observes its committed investigation in the next statement.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 7255))`,
		user.ID+":"+inputs.RequestID); err != nil {
		creationUnavailable().write(w)
		return
	}
	var existingID, existingTemplate, existingApp, existingEnv string
	var existingVersion int
	err = tx.QueryRow(ctx, `SELECT investigation_id,template_id,template_version,application_id,environment_id
 FROM v3_investigation_template_starts WHERE actor_id=$1 AND request_id=$2 FOR SHARE`, user.ID, inputs.RequestID).
		Scan(&existingID, &existingTemplate, &existingVersion, &existingApp, &existingEnv)
	if err == nil {
		if existingTemplate != templateID || existingVersion != version || existingApp != inputs.ApplicationID || existingEnv != env {
			httpapi.Error(w, 409, "template_request_conflict", "Request ID was used with different template inputs")
			return
		}
		if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, 401, "unauthorized", "Login required")
			return
		} else if err != nil {
			creationUnavailable().write(w)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			creationUnavailable().write(w)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]string{"investigation_id": existingID})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		creationUnavailable().write(w)
		return
	}
	tmpl, err := loadInvestigationTemplate(ctx, tx, templateID, env, true)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Investigation template not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Investigation template is unavailable")
		return
	}
	if version != tmpl.Version {
		httpapi.Error(w, 409, "template_version_changed", "Template changed; reload it before starting")
		return
	}
	if tmpl.ApplicationID != "" && tmpl.ApplicationID != inputs.ApplicationID ||
		tmpl.Owner == "team" && (tmpl.TeamID == "" || tmpl.TeamID != service.team) {
		httpapi.Error(w, 404, "not_found", "Investigation template not found for this application")
		return
	}
	if tmpl.Owner == "team" {
		var member string
		err = tx.QueryRow(ctx, `SELECT user_id FROM team_members WHERE team_id=$1 AND user_id=$2 FOR SHARE`,
			tmpl.TeamID, user.ID).Scan(&member)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "not_found", "Investigation template not found for this team")
			return
		}
		if err != nil {
			creationUnavailable().write(w)
			return
		}
	}
	if failure = s.checkTemplateDependencies(ctx, tx, user, env, inputs.ApplicationID, service.team, tmpl); failure != nil {
		failure.write(w)
		return
	}
	creation.Title, creation.Goal = tmpl.Name, tmpl.Description
	id, failure := s.createInvestigationCanvas(ctx, tx, user, env, creation, service, nil, "")
	if failure != nil {
		failure.write(w)
		return
	}
	var root string
	if err = tx.QueryRow(ctx, `SELECT id FROM branches WHERE investigation_id=$1 AND parent_id IS NULL`, id).Scan(&root); err != nil {
		creationUnavailable().write(w)
		return
	}
	for _, branch := range tmpl.Branches {
		branchID := store.ID()
		if _, err = tx.Exec(ctx, `INSERT INTO branches(id,investigation_id,parent_id,title,inherited_parent_revision,synced_parent_revision)
 VALUES($1,$2,$3,$4,0,0)`, branchID, id, root, branch.Name); err != nil {
			creationUnavailable().write(w)
			return
		}
		provenance := mustJSON(map[string]any{"kind": "investigation_template", "template_id": tmpl.ID,
			"template_version": inputs.TemplateVersion, "source_branch_id": root, "source_revision": 0,
			"step": branch.Step, "tools": branch.Tools, "runbooks": tmpl.Runbooks, "budget": tmpl.Budget})
		if _, err = tx.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 VALUES($1,0,'[]'::jsonb,$2::jsonb)`, branchID, provenance); err != nil {
			creationUnavailable().write(w)
			return
		}
		if _, err = tx.Exec(ctx, `INSERT INTO events(investigation_id,branch_id,type,payload)
 VALUES($1,$2,'branch.forked',$3::jsonb)`, id, branchID,
			mustJSON(map[string]any{"parent_id": root, "parent_revision": 0, "source": "investigation_template"})); err != nil {
			creationUnavailable().write(w)
			return
		}
	}
	snapshot := mustJSON(map[string]any{"template_id": tmpl.ID, "template_version": inputs.TemplateVersion,
		"name": tmpl.Name, "branches": tmpl.Branches, "tools": tmpl.Tools, "runbooks": tmpl.Runbooks,
		"budget": tmpl.Budget, "environment_id": env, "application_id": inputs.ApplicationID,
		"execution": "not_started"})
	tag, err := tx.Exec(ctx, `INSERT INTO v3_investigation_template_starts
 (actor_id,request_id,investigation_id,template_id,template_version,application_id,environment_id,snapshot)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb) ON CONFLICT (actor_id,request_id) DO NOTHING`,
		user.ID, inputs.RequestID, id, tmpl.ID, tmpl.Version, inputs.ApplicationID, env, snapshot)
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	if tag.RowsAffected() == 0 {
		// The advisory lock covers this handler. Retain the unique constraint
		// as protection against an independent importer using the same key.
		if err = tx.QueryRow(ctx, `SELECT investigation_id,template_id,template_version,application_id,environment_id
 FROM v3_investigation_template_starts WHERE actor_id=$1 AND request_id=$2`, user.ID, inputs.RequestID).
			Scan(&existingID, &existingTemplate, &existingVersion, &existingApp, &existingEnv); err != nil {
			creationUnavailable().write(w)
			return
		}
		if existingTemplate != tmpl.ID || existingVersion != tmpl.Version || existingApp != inputs.ApplicationID || existingEnv != env {
			httpapi.Error(w, 409, "template_request_conflict", "Request ID was used with different template inputs")
			return
		}
		// Roll back the speculative investigation; only the first committed
		// request retains its rows and audit record.
		if err = tx.Rollback(ctx); err != nil {
			creationUnavailable().write(w)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]string{"investigation_id": existingID})
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE v3_investigation_templates SET uses=uses+1 WHERE id=$1`, tmpl.ID); err != nil {
		creationUnavailable().write(w)
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.investigation_template.started',$3,'success',$4::jsonb)`, user.ID, inputs.ApplicationID, id,
		mustJSON(map[string]any{"template_id": tmpl.ID, "template_version": inputs.TemplateVersion,
			"environment_id": env, "request_id": inputs.RequestID, "branch_count": len(tmpl.Branches),
			"tool_count": len(tmpl.Tools), "runbook_count": len(tmpl.Runbooks), "budget": tmpl.Budget})); err != nil {
		(&creationFailure{503, "audit_unavailable", "Template start audit unavailable"}).write(w)
		return
	}
	// A long wait on a reviewed tool or Runbook lock can outlive the session.
	// The grant and source rows remain pinned; check database-clock expiry again
	// immediately before accepting the durable start.
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	} else if err != nil {
		creationUnavailable().write(w)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		creationUnavailable().write(w)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"investigation_id": id})
}

func (s *Service) checkTemplateDependencies(ctx context.Context, tx pgx.Tx, user core.User, env, app, team string, tmpl investigationTemplateRecord) *creationFailure {
	policy := &rbac.V3Policy{Store: s.Store}
	for _, ref := range tmpl.Tools {
		if err := authorizeTemplateReference(ctx, tx, policy, user, "connectors", env, app, team); err != nil {
			return err
		}
		if err := checkTemplateMCPTool(ctx, tx, ref, env, app); err != nil {
			return err
		}
	}
	for _, ref := range tmpl.Runbooks {
		if err := authorizeTemplateReference(ctx, tx, policy, user, "knowledge", env, app, team); err != nil {
			return err
		}
		if err := checkTemplateRunbook(ctx, tx, policy, user, ref, env, app, team); err != nil {
			return err
		}
	}
	return nil
}

func authorizeTemplateReference(ctx context.Context, tx pgx.Tx, policy *rbac.V3Policy, user core.User, module, env, app, team string) *creationFailure {
	decision := rbac.V3Decision{Module: module, Action: "view", EnvironmentID: env, ApplicationID: app}
	err := policy.AuthorizeWithTx(ctx, tx, user, decision)
	if errors.Is(err, store.ErrForbidden) && team != "" {
		decision.ApplicationID, decision.TeamID = "", team
		err = policy.AuthorizeWithTx(ctx, tx, user, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		return &creationFailure{403, "template_reference_forbidden", "Current access to template references is required"}
	}
	if err != nil {
		return creationUnavailable()
	}
	return nil
}

// A template records reviewed read-only references; it never invokes an MCP
// tool. A new discovery, binding, registration or review switch invalidates
// the old reference at the next start.
func checkTemplateMCPTool(ctx context.Context, tx pgx.Tx, ref, env, app string) *creationFailure {
	extension, name, ok := strings.Cut(ref, ":")
	if !ok || extension == "" || name == "" {
		return &creationFailure{503, "template_unavailable", "Template tool reference is invalid"}
	}
	// Discovery locks the registration before inserting a snapshot. Lock that
	// same row first, then take a fresh READ COMMITTED statement snapshot to
	// select the latest discovery. A single joined SELECT could wait behind a
	// discovery and still read the prior approved snapshot.
	var raw []byte
	var version int
	var binding string
	err := tx.QueryRow(ctx, `SELECT e.record,e.version,b.binding_instance_id
 FROM ai_extensions e JOIN v3_mcp_environment_bindings b
   ON b.extension_id=e.id AND b.application_id=e.application_id AND b.environment_id=$3
 WHERE e.id=$1 AND e.application_id=$2 AND e.kind='mcp'
 FOR SHARE OF e,b`, extension, app, env).Scan(&raw, &version, &binding)
	if errors.Is(err, pgx.ErrNoRows) {
		return &creationFailure{409, "template_tool_unavailable", "Template tool is not currently reviewed for this application and environment"}
	}
	if err != nil {
		return creationUnavailable()
	}
	digest := sha256.Sum256(raw)
	actualHash := hex.EncodeToString(digest[:])
	var record aiextensions.Record
	if json.Unmarshal(raw, &record) != nil || record.ID != extension || record.ApplicationID != app ||
		record.Kind != "mcp" || record.Version != version || !record.Enabled || record.MCP == nil {
		return &creationFailure{409, "template_tool_unavailable", "Template MCP registration changed"}
	}
	readOnlyBinding := false
	for _, item := range record.MCP.Bindings {
		if item.Tool == name && item.ReadOnlyConfirmed {
			readOnlyBinding = true
		}
	}
	if !readOnlyBinding {
		return &creationFailure{409, "template_tool_unavailable", "Template tool is not a current read-only binding"}
	}
	var snapshotID string
	var toolsRaw []byte
	var toolCount, observedVersion, reviewedVersion int
	var observedApp, observedEnv, observedBinding, reviewedBinding, observedHash, reviewedHash, observedToolsHash, reviewedToolsHash string
	err = tx.QueryRow(ctx, `SELECT s.id,s.application_id,s.environment_id,s.registration_version,s.binding_instance_id,s.registration_hash,
 s.tools_hash,s.tools,s.tool_count,r.registration_version,r.binding_instance_id,
 r.registration_hash,r.tools_hash
 FROM v3_mcp_discovery_snapshots s
 JOIN v3_mcp_tool_reviews r ON r.snapshot_id=s.id AND r.extension_id=s.extension_id
   AND r.application_id=s.application_id AND r.environment_id=s.environment_id
 WHERE s.id=(SELECT id FROM v3_mcp_discovery_snapshots
   WHERE extension_id=$1 AND environment_id=$2 ORDER BY snapshot_seq DESC LIMIT 1)
 FOR SHARE OF s,r`, extension, env).Scan(&snapshotID, &observedApp, &observedEnv, &observedVersion, &observedBinding,
		&observedHash, &observedToolsHash, &toolsRaw, &toolCount, &reviewedVersion,
		&reviewedBinding, &reviewedHash, &reviewedToolsHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return &creationFailure{409, "template_tool_unavailable", "Template tool needs a current review"}
	}
	if err != nil {
		return creationUnavailable()
	}
	toolDigest := sha256.Sum256(toolsRaw)
	if observedApp != app || observedEnv != env || version != observedVersion || version != reviewedVersion || binding != observedBinding ||
		binding != reviewedBinding || actualHash != observedHash || actualHash != reviewedHash ||
		observedToolsHash != reviewedToolsHash || observedToolsHash != hex.EncodeToString(toolDigest[:]) ||
		len(toolsRaw) > 512<<10 || toolCount < 1 || toolCount > 128 {
		return &creationFailure{409, "template_tool_unavailable", "Template tool review or binding changed"}
	}
	var observed []json.RawMessage
	if json.Unmarshal(toolsRaw, &observed) != nil || len(observed) != toolCount {
		return &creationFailure{409, "template_tool_unavailable", "Template tool observation is invalid"}
	}
	expected := make(map[string]string, len(observed))
	for _, rawTool := range observed {
		var tool struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(rawTool, &tool) != nil || tool.Name == "" || len(tool.InputSchema) == 0 || expected[tool.Name] != "" {
			return &creationFailure{409, "template_tool_unavailable", "Template tool observation is invalid"}
		}
		itemDigest := sha256.Sum256(rawTool)
		expected[tool.Name] = hex.EncodeToString(itemDigest[:])
	}
	rows, err := tx.Query(ctx, `SELECT tool_name,tool_hash,decision,enabled
 FROM v3_mcp_tool_review_decisions WHERE snapshot_id=$1 FOR SHARE`, snapshotID)
	if err != nil {
		return creationUnavailable()
	}
	seen := map[string]bool{}
	selected := false
	for rows.Next() {
		var toolName, toolHash, decision string
		var enabled bool
		if err = rows.Scan(&toolName, &toolHash, &decision, &enabled); err != nil {
			break
		}
		if expected[toolName] == "" || expected[toolName] != toolHash || seen[toolName] ||
			(decision != "allow_read_only" && decision != "deny") || decision == "deny" && enabled {
			err = errors.New("inconsistent MCP review")
			break
		}
		seen[toolName] = true
		if toolName == name && decision == "allow_read_only" && enabled {
			selected = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(seen) != len(expected) || !selected {
		return &creationFailure{409, "template_tool_unavailable", "Template tool is not currently enabled by a complete read-only review"}
	}
	return nil
}

func checkTemplateRunbook(ctx context.Context, tx pgx.Tx, policy *rbac.V3Policy, user core.User, ref, env, app, team string) *creationFailure {
	var source, lifecycle, docApp string
	var metadataRaw []byte
	err := tx.QueryRow(ctx, `SELECT d.source,d.lifecycle,COALESCE(d.application_id,''),d.metadata
 FROM knowledge_documents d JOIN v3_runbooks vr ON vr.document_id=d.id
 WHERE d.id=$1 FOR SHARE OF d,vr`, ref).Scan(&source, &lifecycle, &docApp, &metadataRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return &creationFailure{409, "template_runbook_unavailable", "Template Runbook is unavailable"}
	}
	if err != nil {
		return creationUnavailable()
	}
	var metadata knowledge.Metadata
	var raw map[string]json.RawMessage
	if json.Unmarshal(metadataRaw, &metadata) != nil || !strings.HasPrefix(source, "runbook://") ||
		json.Unmarshal(metadataRaw, &raw) != nil || raw == nil || raw["source_registration_id"] != nil ||
		lifecycle != "published" || docApp != app || metadata.Visibility != "application" ||
		!containsTemplateEnvironment(metadata.EnvironmentIDs, env) ||
		(metadata.TeamID != "" && metadata.TeamID != team) {
		return &creationFailure{409, "template_runbook_unavailable", "Template Runbook scope or publication changed"}
	}
	for _, candidate := range metadata.EnvironmentIDs {
		if strings.TrimSpace(candidate) == "" {
			return &creationFailure{409, "template_runbook_unavailable", "Template Runbook environment scope is invalid"}
		}
	}
	if teamRaw, present := raw["team_id"]; present {
		var exactTeam string
		if json.Unmarshal(teamRaw, &exactTeam) != nil || exactTeam == "" || exactTeam != team {
			return &creationFailure{409, "template_runbook_unavailable", "Template Runbook team scope is invalid"}
		}
	}
	if metadata.TeamID != "" {
		decision := rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: env, TeamID: team}
		if err = policy.AuthorizeWithTx(ctx, tx, user, decision); errors.Is(err, store.ErrForbidden) {
			return &creationFailure{403, "template_reference_forbidden", "Current team access to template Runbook is required"}
		} else if err != nil {
			return creationUnavailable()
		}
	}
	return nil
}

func containsTemplateEnvironment(environments []string, env string) bool {
	for _, candidate := range environments {
		if candidate == env {
			return true
		}
	}
	return false
}
