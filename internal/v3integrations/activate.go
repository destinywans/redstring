package v3integrations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/jackc/pgx/v5"
)

// Activation is separate from connector creation. The wizard's Webhook toggle
// is only a recorded request. GitLab additionally requires point-in-time
// project GET checks; neither check proves an upstream token's permission scope.
type activateRequest struct {
	CredentialRef string `json:"credential_ref"`
}

type activationTarget struct {
	ID        string
	ProjectID string
}

const maxActivationProjects = 20

func (s *Service) activationSnapshot(ctx context.Context, tx pgx.Tx, id, environment string, lock bool) (connector, []activationTarget, error) {
	query := "SELECT " + columns + " FROM v3_connectors WHERE id=$1 AND environment_id=$2"
	if lock {
		query += " FOR UPDATE"
	}
	c, err := scanConnector(tx.QueryRow(ctx, query, id, environment))
	if err != nil {
		return c, nil, err
	}
	if c.Provider != "gitlab" {
		return c, nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT t.id,t.target_kind,t.external_id,
 COALESCE(NOT a.archived,false) AND ve.application_id IS NOT NULL AND COALESCE(NOT e.archived,false)
 FROM v3_connector_targets t
 LEFT JOIN applications a ON a.id=t.application_id
 LEFT JOIN v3_application_environments ve ON ve.application_id=t.application_id AND ve.environment_id=t.environment_id
 LEFT JOIN application_environments e ON e.application_id=t.application_id AND e.key=t.environment_id
 WHERE t.connector_id=$1 AND t.environment_id=$2
 ORDER BY t.id LIMIT $3`, id, environment, maxActivationProjects+1)
	if err != nil {
		return c, nil, err
	}
	defer rows.Close()
	targets := make([]activationTarget, 0)
	for rows.Next() {
		var t activationTarget
		var kind string
		var active bool
		if err = rows.Scan(&t.ID, &kind, &t.ProjectID, &active); err != nil {
			return c, nil, err
		}
		if !active {
			return c, nil, errInactiveActivationTarget
		}
		if !validExternalTarget("gitlab", kind, t.ProjectID) {
			return c, nil, errInvalidActivationTarget
		}
		targets = append(targets, t)
	}
	return c, targets, rows.Err()
}

var errInvalidActivationTarget = errors.New("invalid activation target")
var errInactiveActivationTarget = errors.New("inactive activation target")
var errProjectUnavailable = errors.New("GitLab project unavailable")

// activationFailure says why a project could not be verified, for the
// service log only: a class, the upstream status and a transport or TLS
// message without the request URL. It never holds a credential.
type activationFailure struct {
	class  string // endpoint, ca_invalid, tls, timeout, connection, upstream_status, invalid_response, project_mismatch
	status int
	detail string
}

func (f *activationFailure) Error() string { return "GitLab project unavailable: " + f.class }
func (f *activationFailure) Unwrap() error { return errProjectUnavailable }

// transportFailure classifies a client error. The URL wrapper is dropped:
// only the underlying TLS, timeout or connection message is kept.
func transportFailure(err error) *activationFailure {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	detail := err.Error()
	if len(detail) > 300 {
		detail = detail[:300]
	}
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var netErr net.Error
	switch {
	case errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &hostname) ||
		errors.As(err, &invalid) || errors.As(err, &record) || strings.Contains(detail, "tls:") || strings.Contains(detail, "x509:"):
		return &activationFailure{class: "tls", detail: detail}
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return &activationFailure{class: "timeout", detail: detail}
	}
	return &activationFailure{class: "connection", detail: detail}
}

func (s *Service) verifyGitLabProject(ctx context.Context, c connector, projectID, token, credentialRef string) error {
	base, err := validEndpoint(c.Endpoint)
	if err != nil || (c.AuthMethod != "pat" && c.AuthMethod != "oauth") {
		return &activationFailure{class: "endpoint"}
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v4/projects/" + url.PathEscape(projectID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return &activationFailure{class: "endpoint"}
	}
	request.Header.Set("Accept", "application/json")
	if c.AuthMethod == "pat" {
		request.Header.Set("PRIVATE-TOKEN", token)
	} else {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	// D11: trust only the private CA kept next to the token, like the probe
	// and the change reader; ctx carries the credential use.
	copyClient, err := PrivateCAClient(ctx, s.Prober.Resolver, credentialRef, base, s.Prober.Client)
	if err != nil {
		return &activationFailure{class: "ca_invalid"}
	}
	if copyClient.Timeout == 0 || copyClient.Timeout > 5*time.Second {
		copyClient.Timeout = 5 * time.Second
	}
	response, err := copyClient.Do(request)
	if err != nil {
		return transportFailure(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &activationFailure{class: "upstream_status", status: response.StatusCode}
	}
	var project struct {
		ID int64 `json:"id"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&project); err != nil || project.ID <= 0 {
		return &activationFailure{class: "invalid_response", status: response.StatusCode}
	}
	if strconv.FormatInt(project.ID, 10) != projectID {
		return &activationFailure{class: "project_mismatch", status: response.StatusCode}
	}
	return nil
}

func (s *Service) activate(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	var in activateRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if !s.authorizeWriteTx(w, r, tx, env[0]) {
		return
	}
	c, targets, err := s.activationSnapshot(ctx, tx, r.PathValue("id"), env[0], false)
	if activationLookupError(w, err) {
		return
	}
	if c.Provider != "gitlab" {
		if err = tx.Commit(ctx); err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
			return
		}
		s.activateGeneric(w, r, c, in)
		return
	}
	credentialRef := in.CredentialRef
	if credentialRef == "" {
		credentialRef = c.CredentialRef
	}
	if validateReference(credentialRef) != nil {
		httpapi.Error(w, 422, "invalid_credential_ref", "Specify a Vault credential field")
		return
	}
	if !s.credentialBound(c, credentialRef) {
		httpapi.Error(w, 503, "credential_not_authorized", "Connector credential has no server-side binding")
		return
	}
	if c.Provider != "gitlab" || (c.AuthMethod != "pat" && c.AuthMethod != "oauth") {
		httpapi.Error(w, 422, "activation_unavailable", "Only GitLab project access can be activated here")
		return
	}
	if c.Enabled {
		httpapi.Error(w, 409, "connector_enabled", "Connector is already enabled")
		return
	}
	if len(targets) == 0 {
		httpapi.Error(w, 409, "target_required", "Bind at least one GitLab project before activation")
		return
	}
	if len(targets) > maxActivationProjects {
		httpapi.Error(w, 422, "scope_too_large", "Too many projects for one activation")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if s.Prober.Resolver == nil {
		if !s.auditGitLabActivationFailure(w, r, c, targets, "credential_unavailable") {
			return
		}
		httpapi.Error(w, 503, "credential_unavailable", "Vault credential is unavailable")
		return
	}
	credentialCtx := WithCredentialUse(ctx, CredentialUse{
		Tier: s.Tier, EnvironmentID: c.EnvironmentID, ConnectorID: c.ID,
		Provider: c.Provider, Endpoint: c.Endpoint,
	})
	token, err := s.Prober.Resolver.Resolve(credentialCtx, credentialRef)
	if err != nil || len(token) == 0 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		slog.Warn("V3 GitLab connector activation failed", "connector_id", c.ID, "environment", c.EnvironmentID,
			"class", "credential_unavailable")
		if !s.auditGitLabActivationFailure(w, r, c, targets, "credential_unavailable") {
			return
		}
		httpapi.Error(w, 503, "credential_unavailable", "Vault credential is unavailable")
		return
	}
	probeCtx, cancel := context.WithTimeout(credentialCtx, 15*time.Second)
	defer cancel()
	for _, target := range targets {
		if err = s.verifyGitLabProject(probeCtx, c, target.ProjectID, token, credentialRef); err != nil {
			failure := &activationFailure{class: "unknown"}
			errors.As(err, &failure)
			slog.Warn("V3 GitLab connector activation failed", "connector_id", c.ID, "environment", c.EnvironmentID,
				"project_id", target.ProjectID, "class", failure.class, "upstream_status", failure.status, "detail", failure.detail)
			if !s.auditGitLabActivationFailure(w, r, c, targets, "project_unavailable") {
				return
			}
			httpapi.Error(w, 502, "project_unavailable", "GitLab project could not be verified")
			return
		}
	}
	writeCtx, writeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer writeCancel()
	r = r.WithContext(writeCtx)
	tx, err = s.DB.Begin(writeCtx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer rollbackConnectorWrite(tx)
	if !s.authorizeWriteTx(w, r, tx, env[0]) {
		return
	}
	current, currentTargets, err := s.activationSnapshot(writeCtx, tx, c.ID, env[0], true)
	if activationLookupError(w, err) {
		return
	}
	if !s.authorizeLockedWriteTx(w, r, tx, c.EnvironmentID) {
		return
	}
	if !sameGitLabActivationSnapshot(current, currentTargets, c, targets) {
		httpapi.Error(w, 409, "connector_changed", "Connector or project scope changed during verification")
		return
	}
	_, err = tx.Exec(writeCtx, `UPDATE v3_connectors c SET credential_ref=$1,enabled=true,status='ok',
 last_checked_at=now(),last_error_code='',last_latency_ms=NULL,updated_at=now(),
 tools=COALESCE((SELECT jsonb_agg(jsonb_set(value,'{enabled}','false'::jsonb,true))
 FROM jsonb_array_elements(c.tools) AS item(value)),'[]'::jsonb)
 WHERE id=$2 AND environment_id=$3`, credentialRef, c.ID, c.EnvironmentID)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if err = audit(writeCtx, tx, "connector.activated", c.ID, map[string]any{
		"environment": c.EnvironmentID, "provider": c.Provider,
		"verified_projects": len(targets), "webhook_active": false,
	}); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Could not audit connector activation")
		return
	}
	if err = tx.Commit(writeCtx); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"enabled": true, "status": "ok", "verified_projects": len(targets), "webhook_active": false})
}

func sameGitLabActivationSnapshot(current connector, currentTargets []activationTarget, initial connector, initialTargets []activationTarget) bool {
	return !current.Enabled && current.Revision == initial.Revision && current.Provider == initial.Provider && current.Name == initial.Name &&
		current.Category == initial.Category && current.Endpoint == initial.Endpoint &&
		current.AuthMethod == initial.AuthMethod && current.CredentialRef == initial.CredentialRef &&
		current.Webhook == initial.Webhook && reflect.DeepEqual(current.Tools, initial.Tools) &&
		reflect.DeepEqual(currentTargets, initialTargets)
}

// The credential resolver and project GET run outside the write transaction.
// A failed attempt is audited only after rechecking the exact connector and
// project scope with current manage authority. The fixed reason omits secret
// references, tokens, resolver errors and upstream response bodies.
func (s *Service) auditGitLabActivationFailure(w http.ResponseWriter, r *http.Request, initial connector, targets []activationTarget, reason string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return false
	}
	defer rollbackConnectorWrite(tx)
	current, currentTargets, err := s.activationSnapshot(ctx, tx, initial.ID, initial.EnvironmentID, true)
	if activationLookupError(w, err) {
		return false
	}
	if !s.authorizeLockedWriteTx(w, r, tx, initial.EnvironmentID) {
		return false
	}
	if !sameGitLabActivationSnapshot(current, currentTargets, initial, targets) {
		httpapi.Error(w, 409, "connector_changed", "Connector or project scope changed during verification")
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

func activationLookupError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
	} else if errors.Is(err, errInactiveActivationTarget) {
		httpapi.Error(w, 409, "inactive_target", "Remove or restore inactive project bindings before activation")
	} else if errors.Is(err, errInvalidActivationTarget) {
		httpapi.Error(w, 422, "invalid_target", "Connector has an invalid project binding")
	} else {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
	}
	return true
}
