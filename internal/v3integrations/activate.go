package v3integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

// Activation is separate from connector creation. The wizard's Webhook toggle
// is only a recorded request. This enables the platform's read-only GitLab
// reader after point-in-time project GET checks; token scope is not proven.
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

func (s *Service) verifyGitLabProject(ctx context.Context, c connector, projectID, token string) error {
	base, err := validEndpoint(c.Endpoint)
	if err != nil || (c.AuthMethod != "pat" && c.AuthMethod != "oauth") {
		return errProjectUnavailable
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v4/projects/" + url.PathEscape(projectID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errProjectUnavailable
	}
	request.Header.Set("Accept", "application/json")
	if c.AuthMethod == "pat" {
		request.Header.Set("PRIVATE-TOKEN", token)
	} else {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := s.Prober.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 5*time.Second {
		copyClient.Timeout = 5 * time.Second
	}
	response, err := copyClient.Do(request)
	if err != nil {
		return errProjectUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errProjectUnavailable
	}
	var project struct {
		ID int64 `json:"id"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&project); err != nil ||
		project.ID <= 0 || strconv.FormatInt(project.ID, 10) != projectID {
		return errProjectUnavailable
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
	if validateReference(in.CredentialRef) != nil {
		httpapi.Error(w, 422, "invalid_credential_ref", "Specify a Vault credential field")
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
		httpapi.Error(w, 503, "credential_unavailable", "Vault credential is unavailable")
		return
	}
	token, err := s.Prober.Resolver.Resolve(ctx, in.CredentialRef)
	if err != nil || len(token) == 0 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		httpapi.Error(w, 503, "credential_unavailable", "Vault credential is unavailable")
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, target := range targets {
		if err = s.verifyGitLabProject(probeCtx, c, target.ProjectID, token); err != nil {
			httpapi.Error(w, 502, "project_unavailable", "GitLab project could not be verified")
			return
		}
	}
	tx, err = s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if !s.authorizeWriteTx(w, r, tx, env[0]) {
		return
	}
	current, currentTargets, err := s.activationSnapshot(ctx, tx, c.ID, env[0], true)
	if activationLookupError(w, err) {
		return
	}
	if current.Enabled || current.Provider != c.Provider || current.Endpoint != c.Endpoint ||
		current.AuthMethod != c.AuthMethod || current.CredentialRef != c.CredentialRef ||
		!reflect.DeepEqual(currentTargets, targets) {
		httpapi.Error(w, 409, "connector_changed", "Connector or project scope changed during verification")
		return
	}
	_, err = tx.Exec(ctx, `UPDATE v3_connectors c SET credential_ref=$1,enabled=true,status='ok',
 last_checked_at=now(),last_error_code='',last_latency_ms=NULL,updated_at=now(),
 tools=COALESCE((SELECT jsonb_agg(jsonb_set(value,'{enabled}','false'::jsonb,true))
 FROM jsonb_array_elements(c.tools) AS item(value)),'[]'::jsonb)
 WHERE id=$2 AND environment_id=$3`, in.CredentialRef, c.ID, c.EnvironmentID)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if err = audit(ctx, tx, "connector.activated", c.ID, map[string]any{
		"environment": c.EnvironmentID, "provider": c.Provider,
		"verified_projects": len(targets), "webhook_active": false,
	}); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Could not audit connector activation")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"enabled": true, "status": "ok", "verified_projects": len(targets), "webhook_active": false})
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
