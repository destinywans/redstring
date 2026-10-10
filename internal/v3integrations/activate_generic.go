package v3integrations

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

// activateGeneric completes a disabled, non-GitLab registration with a
// point-in-time read-only connection probe. It never provisions a webhook or
// enables registered tools. Tool preferences can be changed separately after
// the connector is enabled.
func (s *Service) activateGeneric(w http.ResponseWriter, r *http.Request, initial connector, in activateRequest) {
	if initial.Enabled {
		httpapi.Error(w, 409, "connector_enabled", "Connector is already enabled")
		return
	}
	d, known := descriptors[initial.Provider]
	if !known || initial.Provider == "gitlab" || !knownMethod(d, initial.AuthMethod) ||
		!validRegisteredTools(initial, d) {
		httpapi.Error(w, 409, "connector_changed", "Connector configuration cannot be activated")
		return
	}
	if _, err := validEndpoint(initial.Endpoint); err != nil {
		httpapi.Error(w, 422, "invalid_endpoint", "Connector endpoint cannot be tested")
		return
	}
	credentialRef := in.CredentialRef
	if initial.AuthMethod == "none" {
		if credentialRef != "" || initial.CredentialRef != "" {
			httpapi.Error(w, 422, "invalid_credential_ref", "Unauthenticated connectors do not take credentials")
			return
		}
	} else {
		if credentialRef == "" {
			credentialRef = initial.CredentialRef
		}
		if validateReference(credentialRef) != nil {
			httpapi.Error(w, 422, "invalid_credential_ref", "Specify a Vault credential field")
			return
		}
		if !s.credentialBound(initial, credentialRef) {
			httpapi.Error(w, 503, "credential_not_authorized", "Connector credential has no server-side binding")
			return
		}
	}
	probeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	probeCtx = WithCredentialUse(probeCtx, CredentialUse{
		Tier: s.Tier, EnvironmentID: initial.EnvironmentID, ConnectorID: initial.ID,
		Provider: initial.Provider, Endpoint: initial.Endpoint,
	})
	if initial.AuthMethod == "basic" {
		probeCtx = WithBasicUsername(probeCtx, s.connectorUsername(probeCtx, initial.ID, initial.EnvironmentID))
	}
	result, err := s.Prober.Check(probeCtx, initial.Provider, initial.Endpoint, initial.AuthMethod, credentialRef)
	if errors.Is(err, ErrCredentialUnavailable) {
		if !s.auditGenericActivationFailure(w, r, initial, "credential_unavailable") {
			return
		}
		httpapi.Error(w, 503, "credential_unavailable", "Vault credential is unavailable")
		return
	}
	if err != nil {
		if !s.auditGenericActivationFailure(w, r, initial, "invalid_connector") {
			return
		}
		httpapi.Error(w, 422, "invalid_connector", "Connector settings cannot be tested")
		return
	}
	if !result.OK {
		if !s.auditGenericActivationFailure(w, r, initial, "read_probe_failed") {
			return
		}
		httpapi.Error(w, 502, "connector_test_failed", "Connector endpoint did not pass its read-only check")
		return
	}

	// The probe ran outside a write transaction. Only its exact registration
	// may be activated; another edit or concurrent activation must be rejected.
	writeCtx, writeCancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer writeCancel()
	r = r.WithContext(writeCtx)
	tx, err := s.DB.Begin(writeCtx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer rollbackConnectorWrite(tx)
	current, err := scanConnector(tx.QueryRow(writeCtx, "SELECT "+columns+" FROM v3_connectors WHERE id=$1 AND environment_id=$2 FOR UPDATE", initial.ID, initial.EnvironmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "connector_changed", "Connector scope changed during connection check")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if !s.authorizeLockedWriteTx(w, r, tx, initial.EnvironmentID) {
		return
	}
	if !sameGenericActivationRegistration(current, initial) {
		httpapi.Error(w, 409, "connector_changed", "Connector configuration changed during connection check")
		return
	}
	_, err = tx.Exec(writeCtx, `UPDATE v3_connectors c SET credential_ref=$1,enabled=true,status='ok',
 last_checked_at=now(),last_latency_ms=$2,last_error_code='',updated_at=now(),
 tools=COALESCE((SELECT jsonb_agg(jsonb_set(value,'{enabled}','false'::jsonb,true))
 FROM jsonb_array_elements(c.tools) AS item(value)),'[]'::jsonb)
 WHERE id=$3 AND environment_id=$4`, credentialRef, result.LatencyMS, current.ID, current.EnvironmentID)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if err = audit(writeCtx, tx, "connector.activated", current.ID, map[string]any{
		"environment": current.EnvironmentID, "provider": current.Provider,
		"verification": "connection_probe", "webhook_active": false,
	}); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Could not audit connector activation")
		return
	}
	if err = tx.Commit(writeCtx); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"enabled": true, "status": "ok", "verified_projects": 0,
		"latency_ms": result.LatencyMS, "webhook_active": false,
		"credential_verified": false,
	})
}

func sameGenericActivationRegistration(current, initial connector) bool {
	return !current.Enabled && current.Revision == initial.Revision && current.Provider == initial.Provider && current.Name == initial.Name &&
		current.Category == initial.Category && current.Endpoint == initial.Endpoint &&
		current.AuthMethod == initial.AuthMethod && current.CredentialRef == initial.CredentialRef &&
		current.Webhook == initial.Webhook && reflect.DeepEqual(current.Tools, initial.Tools)
}

// A failed probe must leave the registration disabled but still produce a
// durable attempt record. Recheck the exact target and current authority after
// network I/O; only fixed reason codes enter the audit, never a Vault reference,
// token, resolver error or upstream response body.
func (s *Service) auditGenericActivationFailure(w http.ResponseWriter, r *http.Request, initial connector, reason string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return false
	}
	defer rollbackConnectorWrite(tx)
	current, err := scanConnector(tx.QueryRow(ctx, "SELECT "+columns+" FROM v3_connectors WHERE id=$1 AND environment_id=$2 FOR UPDATE", initial.ID, initial.EnvironmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "connector_changed", "Connector scope changed during connection check")
		return false
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return false
	}
	if !s.authorizeLockedWriteTx(w, r, tx, initial.EnvironmentID) {
		return false
	}
	if !sameGenericActivationRegistration(current, initial) {
		httpapi.Error(w, 409, "connector_changed", "Connector configuration changed during connection check")
		return false
	}
	if err = auditOutcome(ctx, tx, "connector.activation.attempted", current.ID, "failure", map[string]any{
		"environment": current.EnvironmentID, "provider": current.Provider, "reason": reason,
	}); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Could not audit connector activation attempt")
		return false
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return false
	}
	return true
}
