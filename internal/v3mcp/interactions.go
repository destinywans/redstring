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
	"github.com/wans/aiops-canvas/internal/httpapi"
)

func (s *Service) disable(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	if scope.All {
		httpapi.Error(w, 409, "env_view_only", "Select one environment")
		return
	}
	if len(envs) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Select one environment")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil && r.ContentLength != 0 && !httpapi.Decode(w, r, &in) {
		return
	}
	if !utf8.ValidString(in.Reason) || len(in.Reason) > 1000 || strings.ContainsAny(in.Reason, "\x00\r\n") {
		httpapi.Error(w, 422, "invalid_reason", "Reason exceeds its bounds")
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
	current, err := loadDiscoveryRegistration(ctx, tx, r.PathValue("id"), envs[0], true)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "MCP server not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is unavailable")
		return
	}
	if !lockMCPRead(w, r, tx) {
		return
	}
	if err = s.allowedTx(r, tx, current.registration, "manage"); err != nil {
		writeDiscoveryAuthorizationError(w, err, true)
		return
	}
	if err = current.validateDiscoveryRegistration(); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is inconsistent")
		return
	}
	if !requireHumanMCPReviewer(w, r, tx, current) {
		return
	}
	changed := current.Record.Enabled
	// Preserve transport/configuration and invalidate all version-bound grants.
	if changed {
		var record map[string]json.RawMessage
		if err = json.Unmarshal(current.raw, &record); err != nil {
			httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is inconsistent")
			return
		}

		current.version++
		shape := current.Record
		shape.Enabled = false
		shape.Version = current.version
		shape.ContentHash = ""
		shape.UpdatedAt = time.Time{}
		shape.UpdatedBy = ""
		var hashBytes []byte
		var source string
		_ = json.Unmarshal(record["registration_source"], &source)
		if source == "v3" {
			hashBytes, _ = json.Marshal(v3RegistrationRecord{Record: shape, RegistrationSource: source})
		} else {
			hashBytes, _ = json.Marshal(shape)
		}
		record["content_hash"], _ = json.Marshal(digestBytes(hashBytes))
		record["updated_by"], _ = json.Marshal(httpapi.CurrentUser(r).ID)
		record["enabled"] = json.RawMessage("false")
		record["version"], _ = json.Marshal(current.version)
		record["updated_at"], _ = json.Marshal(time.Now().UTC())
		raw, _ := json.Marshal(record)
		_, err = tx.Exec(ctx, `UPDATE ai_extensions SET record=$2,version=$3,updated_at=now() WHERE id=$1`, current.ID, raw, current.version)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO ai_extension_revisions(id,version,record) VALUES($1,$2,$3)`, current.ID, current.version, raw)
		}
		if err != nil {
			httpapi.Error(w, 503, "mcp_unavailable", "MCP registration could not be saved")
			return
		}
	}
	_, err = tx.Exec(ctx, `UPDATE v3_mcp_tool_review_decisions d SET enabled=false,revision=revision+1,updated_by=$2,updated_at=now() FROM v3_mcp_tool_reviews v WHERE d.snapshot_id=v.snapshot_id AND v.extension_id=$1 AND d.enabled`, current.ID, httpapi.CurrentUser(r).ID)
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP switches could not be revoked")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'v3.mcp.disabled',$3,'success',jsonb_build_object('environment_id',$4::text,'registration_version',$5::int,'changed',$6::boolean,'reason_supplied',$7::boolean))`, httpapi.CurrentUser(r).ID, current.ApplicationID, current.ID, current.EnvironmentID, current.version, changed, in.Reason != "")
	if err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "MCP audit is unavailable")
		return
	}
	if !freshMCPReadSession(w, r, tx) {
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP disable could not be committed")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": current.ID, "enabled": false, "registration_version": current.version, "environment_id": current.EnvironmentID, "execution_available": false})
}

func (s *Service) reviewDismiss(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var in struct {
		CandidateVersion string `json:"candidate_version"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.CandidateVersion == "" {
		httpapi.Error(w, 422, "invalid_candidate_version", "A candidate version is required")
		return
	}
	tx, current, snap, ok := s.reviewCurrent(w, r, true)
	if !ok {
		return
	}
	defer rollbackCandidateRead(tx)
	if !requireHumanMCPReviewer(w, r, tx, current) {
		return
	}
	if in.CandidateVersion != snap.id {
		httpapi.Error(w, 409, "mcp_discovery_stale", "Reload the current candidate")
		return
	}
	if _, err := snap.tools(); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP snapshot is inconsistent")
		return
	}
	// Dismissal is a separate acknowledgement, never a tool review/grant.
	tag, err := tx.Exec(r.Context(), `INSERT INTO v3_mcp_review_dismissals(snapshot_id,dismissed_by) VALUES($1,$2) ON CONFLICT(snapshot_id) DO NOTHING`, snap.id, httpapi.CurrentUser(r).ID)
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP dismissal could not be saved")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'v3.mcp.review_dismissed',$3,'success',jsonb_build_object('environment_id',$4::text,'snapshot_id',$5::text,'changed',$6::boolean))`, httpapi.CurrentUser(r).ID, current.ApplicationID, current.ID, current.EnvironmentID, snap.id, tag.RowsAffected() > 0); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "MCP audit is unavailable")
		return
	}
	if !freshMCPReadSession(w, r, tx) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP dismissal could not be committed")
		return
	}
	w.WriteHeader(204)
}
