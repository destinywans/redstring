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
	result, err := s.Prober.Check(probeCtx, initial.Provider, initial.Endpoint, initial.AuthMethod, credentialRef)
	if errors.Is(err, ErrCredentialUnavailable) {
		httpapi.Error(w, 503, "credential_unavailable", "Vault credential is unavailable")
		return
	}
	if err != nil {
		httpapi.Error(w, 422, "invalid_connector", "Connector settings cannot be tested")
		return
	}
	if !result.OK {
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
	if current.Enabled || current.Provider != initial.Provider || current.Name != initial.Name ||
		current.Category != initial.Category || current.Endpoint != initial.Endpoint ||
		current.AuthMethod != initial.AuthMethod || current.CredentialRef != initial.CredentialRef ||
		current.Webhook != initial.Webhook || !reflect.DeepEqual(current.Tools, initial.Tools) {
		httpapi.Error(w, 409, "connector_changed", "Connector configuration changed during connection check")
		return
	}
	_, err = tx.Exec(writeCtx, `UPDATE v3_connectors SET credential_ref=$1,enabled=true,status='ok',
 last_checked_at=now(),last_latency_ms=$2,last_error_code='',updated_at=now()
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
