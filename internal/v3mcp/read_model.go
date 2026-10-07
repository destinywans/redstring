package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// An observation is usable only for the exact current registration and
// environment-binding instance. Remote declarations remain untrusted.
type mcpObservation struct {
	snapshot           reviewSnapshot
	tools              []observedTool
	resources, prompts any
	dismissed          bool
	reviewed           bool
	allowed, enabled   int
}

func loadMCPObservation(ctx context.Context, tx pgx.Tx, v registration) (*mcpObservation, error) {
	snap, err := loadLatestReviewSnapshot(ctx, tx, v.ID, v.EnvironmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if snap.extensionID != v.ID || snap.applicationID != v.ApplicationID || snap.environmentID != v.EnvironmentID ||
		snap.bindingInstanceID != v.BindingInstanceID || snap.registrationVersion != v.Version || snap.registrationHash != digestBytes(v.raw) {
		return nil, nil
	}
	tools, err := snap.tools()
	if err != nil {
		return nil, err
	}
	if snap.toolsState != "supported" && snap.toolsState != "unsupported" && snap.toolsState != "unknown" {
		return nil, errors.New("invalid tools state")
	}
	if snap.toolsState == "unsupported" && len(tools) != 0 {
		return nil, errors.New("invalid unsupported tools")
	}
	obs := &mcpObservation{snapshot: snap, tools: tools}
	obs.resources, err = observedList(snap.resourcesState, snap.resourcesRaw)
	if err != nil {
		return nil, err
	}
	obs.prompts, err = observedList(snap.promptsState, snap.promptsRaw)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_mcp_tool_reviews
 WHERE snapshot_id=$1 AND extension_id=$2 AND binding_instance_id=$3 AND application_id=$4
 AND environment_id=$5 AND registration_version=$6 AND registration_hash=$7 AND tools_hash=$8)`,
		snap.id, v.ID, v.BindingInstanceID, v.ApplicationID, v.EnvironmentID, v.Version, digestBytes(v.raw), snap.toolsHash).Scan(&obs.reviewed)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_mcp_review_dismissals WHERE snapshot_id=$1)`, snap.id).Scan(&obs.dismissed); err != nil {
		return nil, err
	}
	if obs.reviewed {
		decisions, err := loadCompleteReview(ctx, tx, snap.id, tools)
		if err != nil {
			return nil, err
		}
		for i := range obs.tools {
			decision := decisions[obs.tools[i].Name]
			obs.tools[i].Decision, obs.tools[i].Enabled, obs.tools[i].Revision = decision.Decision, decision.Enabled, decision.Revision
			if decision.Decision == "allow_read_only" {
				obs.allowed++
			}
			if decision.Enabled {
				obs.enabled++
			}
		}
	}
	return obs, nil
}

func observedList(state string, raw []byte) (any, error) {
	if state == "unknown" || state == "unsupported" {
		if len(raw) != 0 {
			return nil, errors.New("unexpected capability metadata")
		}
		return nil, nil
	}
	if state != "supported" || len(raw) == 0 {
		return nil, errors.New("missing capability metadata")
	}
	var values []map[string]any
	if err := json.Unmarshal(raw, &values); err != nil || values == nil || len(values) > 128 {
		return nil, errors.New("invalid capability metadata")
	}
	return values, nil
}

func observedCount(state string, items any, tools []observedTool) any {
	if state != "supported" {
		return nil
	}
	if tools != nil {
		return len(tools)
	}
	if values, ok := items.([]map[string]any); ok {
		return len(values)
	}
	return nil
}

func decorateMCPSummary(result map[string]any, v registration, obs *mcpObservation, lang string) {
	result["enabled"] = v.Record.Enabled
	result["application_id"] = v.ApplicationID
	result["environment_id"] = v.EnvironmentID
	result["binding_instance_id"] = v.BindingInstanceID
	result["owner_team_id"] = nullableText(v.OwnerTeamID)
	result["registration_version"] = v.Version
	credentialRegistered := v.Record.MCP != nil && v.Record.MCP.CredentialRef != ""
	credentialSource := "none"
	if credentialRegistered {
		credentialSource = "private_file"
	}
	result["credential"] = map[string]any{"source": credentialSource, "reference_registered": credentialRegistered,
		"current_validity": nil, "vault_state": "not_integrated"}
	result["connection"] = map[string]any{"state": "unknown", "last_success_at": nil, "health": nil}
	result["discovery"] = nil
	result["review"] = map[string]any{"reviewed": false, "allowed_read_only_count": nil, "enabled_count": nil}
	result["call_metrics"] = nil
	if obs == nil {
		return
	}
	snap := obs.snapshot
	result["connection"] = map[string]any{"state": "observed_success", "last_success_at": snap.capturedAt.UTC().Format(time.RFC3339Nano), "health": nil}
	result["caps"] = mcpLocalized(lang, "已观察 MCP 声明，未经信任", "MCP declarations observed, untrusted")
	result["discovery"] = map[string]any{
		"snapshot_id": snap.id, "captured_at": snap.capturedAt.UTC().Format(time.RFC3339Nano),
		"trust": "untrusted_observation", "server_name": snap.serverName, "server_version": snap.serverVersion,
		"protocol_version": snap.protocolVersion,
		"tools":            map[string]any{"state": snap.toolsState, "count": observedCount(snap.toolsState, nil, obs.tools)},
		"resources":        map[string]any{"state": snap.resourcesState, "count": observedCount(snap.resourcesState, obs.resources, nil)},
		"prompts":          map[string]any{"state": snap.promptsState, "count": observedCount(snap.promptsState, obs.prompts, nil)},
	}
	result["review"] = map[string]any{"reviewed": obs.reviewed, "allowed_read_only_count": nil, "enabled_count": nil}
	if obs.reviewed {
		result["review"] = map[string]any{"reviewed": true, "allowed_read_only_count": obs.allowed, "enabled_count": obs.enabled}
		result["trust"] = map[string]string{"label": mcpLocalized(lang, "已审核发现版本", "Discovery version reviewed"), "tone": "warn"}
	}
}

func decorateMCPDetail(result map[string]any, obs *mcpObservation, lang string, credentialRegistered bool) {
	result["version"] = nil
	result["counts"] = map[string]any{"tools": nil, "resources": nil, "prompts": nil}
	result["tools"] = nil
	result["resources"] = nil
	result["prompts"] = nil
	result["calls"] = nil
	result["review_candidate"] = nil
	auth := mcpLocalized(lang, "未登记凭据引用", "No credential reference registered")
	if credentialRegistered {
		auth = mcpLocalized(lang, "已登记私有文件凭据引用，当前有效性未验证", "Private-file credential reference registered; current validity unverified")
	}
	result["trust_info"] = map[string]any{
		"readonly_check": mcpLocalized(lang, "尚无当前版本人工审核", "No human review of current version"),
		"scope":          []string{}, "limit": mcpLocalized(lang, "V3 Agent 调用尚不可用", "V3 Agent execution unavailable"), "auth": auth,
	}
	if obs == nil {
		return
	}
	snap := obs.snapshot
	if !obs.reviewed && !obs.dismissed {
		result["review_candidate"] = map[string]any{"candidate_version": snap.id, "snapshot_id": snap.id, "registration_version": snap.registrationVersion, "tools_hash": snap.toolsHash}
	}
	result["version"] = snap.serverVersion
	result["counts"] = map[string]any{
		"tools":     observedCount(snap.toolsState, nil, obs.tools),
		"resources": observedCount(snap.resourcesState, obs.resources, nil),
		"prompts":   observedCount(snap.promptsState, obs.prompts, nil),
	}
	if snap.toolsState == "supported" {
		tools := make([]map[string]any, 0, len(obs.tools))
		for _, tool := range obs.tools {
			decision := any(nil)
			if obs.reviewed {
				decision = tool.Decision
			}
			tools = append(tools, map[string]any{"name": tool.Name, "desc": tool.Description, "read_only_hint": tool.ReadOnlyHint,
				"decision": decision, "enabled": tool.Enabled, "revision": nullableRevision(tool.Revision), "trust": "untrusted_observation", "calls": nil})
		}
		result["tools"] = tools
	}
	result["resources"] = obs.resources
	result["prompts"] = obs.prompts
	if obs.reviewed {
		result["trust_info"].(map[string]any)["readonly_check"] = mcpLocalized(lang, "当前发现版本已由人工逐项审核", "Current discovery version reviewed by a human")
	}
}

func nullableRevision(revision int) any {
	if revision < 1 {
		return nil
	}
	return revision
}
