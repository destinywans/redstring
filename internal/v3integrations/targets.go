package v3integrations

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A target is an administrator-reviewed external object. A connector's
// successful connection check alone never assigns any of its objects to a
// service. The IDs here are metadata, not URLs, credentials or tool inputs.
type externalTarget struct {
	ID            string `json:"id"`
	ConnectorID   string `json:"connector_id"`
	Provider      string `json:"provider"`
	TargetKind    string `json:"target_kind"`
	ExternalID    string `json:"external_id"`
	EnvironmentID string `json:"environment_id"`
}

type externalTargetInput struct {
	ConnectorID string `json:"connector_id"`
	TargetKind  string `json:"target_kind"`
	ExternalID  string `json:"external_id"`
}

var errTargetServiceForbidden = errors.New("service target is outside caller scope")

func (s *Service) registerTargets(api *httpapi.Server) {
	api.Register("GET /api/v1/services/{id}/external-targets", "viewer", s.listTargets)
	api.Register("POST /api/v1/services/{id}/external-targets", "viewer", s.createTarget)
	api.Register("DELETE /api/v1/services/{id}/external-targets/{bindingId}", "viewer", s.deleteTarget)
	// Both permissions depend on the persisted service and connector. An
	// environment-only route check would reject valid application/team grants.
	api.MarkV3ObjectRoute("GET /api/v1/services/{id}/external-targets", "services", "view")
	api.MarkV3ObjectRoute("POST /api/v1/services/{id}/external-targets", "services", "change")
	api.MarkV3ObjectRoute("DELETE /api/v1/services/{id}/external-targets/{bindingId}", "services", "change")
}

func targetServiceID(id string) (string, string, bool) {
	if id == "" || len(id) > 1024 {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		base64.RawURLEncoding.EncodeToString(raw) != id {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func selectedTargetService(w http.ResponseWriter, r *http.Request, envs []string) (string, string, bool) {
	app, env, ok := targetServiceID(r.PathValue("id"))
	if !ok {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return "", "", false
	}
	for _, selected := range envs {
		if selected == env {
			return app, env, true
		}
	}
	httpapi.Error(w, 404, "not_found", "Service not found")
	return "", "", false
}

func (s *Service) targetPolicy() *rbac.V3Policy {
	return &rbac.V3Policy{Store: &store.Store{DB: s.DB}}
}

func (s *Service) targetServiceTx(ctx context.Context, tx pgx.Tx, app, env string, lock bool) (string, error) {
	query := `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived`
	if lock {
		query += " FOR UPDATE OF a,ve,e"
	}
	var team string
	err := tx.QueryRow(ctx, query, app, env).Scan(&team)
	return team, err
}

func (s *Service) authorizeTargetTx(ctx context.Context, tx pgx.Tx, user core.User, app, env, team, action string) error {
	policy := s.targetPolicy()
	err := policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
		Module: "services", Action: action, EnvironmentID: env, ApplicationID: app,
	})
	if errors.Is(err, store.ErrForbidden) && team != "" {
		err = policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
			Module: "services", Action: action, EnvironmentID: env, TeamID: team,
		})
	}
	if errors.Is(err, store.ErrForbidden) {
		return errTargetServiceForbidden
	}
	if err != nil {
		return err
	}
	connectorAction := "view"
	if action != "view" {
		connectorAction = "manage"
	}
	return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
		Module: "connectors", Action: connectorAction, EnvironmentID: env,
	})
}

func targetAuthorizationError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errTargetServiceForbidden) {
		httpapi.Error(w, 404, "not_found", "Service not found")
	} else if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Service or connector permission required")
	} else {
		httpapi.Error(w, 503, "authorization_unavailable", "Target authorization unavailable")
	}
	return true
}

func validExternalTarget(provider, kind, id string) bool {
	// The resource is an opaque, administrator-reviewed catalog key. It is
	// never interpolated into a URL or a PromQL expression.
	if provider == "prometheus" && kind == "metric_resource" {
		return prometheusResourceID.MatchString(id)
	}
	// A workload is also a reviewed catalog key; the namespace and Deployment
	// come from the catalog (PA-08).
	if provider == "kubernetes" && kind == "workload" {
		return prometheusResourceID.MatchString(id)
	}
	// Jenkins currently accepts only server-configured aliases, but this
	// service has no verified alias registry. Harbor has no safe reader yet.
	// Keep both unavailable until those execution boundaries are connected.
	if provider != "gitlab" || kind != "project" || id == "" || len(id) > 19 || id[0] == '0' {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return false
		}
	}
	value, err := strconv.ParseInt(id, 10, 64)
	return err == nil && value > 0
}

var prometheusResourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func (s *Service) listTargets(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, false)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	app, env, ok := selectedTargetService(w, r, envs)
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	team, err := s.targetServiceTx(r.Context(), tx, app, env, false)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	if targetAuthorizationError(w, s.authorizeTargetTx(r.Context(), tx, httpapi.CurrentUser(r), app, env, team, "view")) {
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT t.id,t.connector_id,c.provider,t.target_kind,t.external_id,t.environment_id
 FROM v3_connector_targets t JOIN v3_connectors c
  ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.application_id=$1 AND t.environment_id=$2
 ORDER BY c.provider,t.connector_id,t.target_kind,t.external_id LIMIT 101`, app, env)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	items := []externalTarget{}
	for rows.Next() {
		var item externalTarget
		if err = rows.Scan(&item.ID, &item.ConnectorID, &item.Provider, &item.TargetKind, &item.ExternalID, &item.EnvironmentID); err != nil {
			break
		}
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	if len(items) > 100 {
		httpapi.Error(w, 422, "result_limit", "Too many targets")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (s *Service) createTarget(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	app, env, ok := selectedTargetService(w, r, envs)
	if !ok {
		return
	}
	var in externalTargetInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.ConnectorID == "" || len(in.ConnectorID) > 128 || strings.TrimSpace(in.ConnectorID) != in.ConnectorID ||
		in.TargetKind == "" || in.ExternalID == "" || len(in.ExternalID) > 512 {
		httpapi.Error(w, 422, "invalid_target", "Invalid external target")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	team, err := s.targetServiceTx(r.Context(), tx, app, env, true)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	if targetAuthorizationError(w, s.authorizeTargetTx(r.Context(), tx, httpapi.CurrentUser(r), app, env, team, "change")) {
		return
	}
	var provider string
	var connectorEnabled bool
	err = tx.QueryRow(r.Context(), `SELECT provider,enabled FROM v3_connectors
 WHERE id=$1 AND environment_id=$2 FOR UPDATE`, in.ConnectorID, env).Scan(&provider, &connectorEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	if !validExternalTarget(provider, in.TargetKind, in.ExternalID) {
		httpapi.Error(w, 422, "target_unavailable", "Provider target cannot be safely bound")
		return
	}
	// The service row is locked above, so concurrent writes to this service
	// cannot push its target list past the bounded read contract.
	var targetCount int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM v3_connector_targets
 WHERE application_id=$1 AND environment_id=$2`, app, env).Scan(&targetCount); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	if targetCount >= 100 {
		httpapi.Error(w, 422, "target_limit", "Service target limit reached")
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, id, app, env, in.ConnectorID, in.TargetKind, in.ExternalID, httpapi.CurrentUser(r).ID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			httpapi.Error(w, 409, "target_exists", "Target already bound")
			return
		}
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	// A new binding expands the scope that was verified during GitLab
	// activation. Make the connector inert until the complete target set is
	// verified again; otherwise the new project could be read immediately.
	if provider == "gitlab" && connectorEnabled {
		_, err = tx.Exec(r.Context(), `UPDATE v3_connectors c SET enabled=false,status='unverified',
 last_checked_at=NULL,last_latency_ms=NULL,last_error_code='',updated_at=now(),
 tools=COALESCE((SELECT jsonb_agg(jsonb_set(value,'{enabled}','false'::jsonb,true))
 FROM jsonb_array_elements(c.tools) AS item(value)),'[]'::jsonb)
 WHERE id=$1 AND environment_id=$2`, in.ConnectorID, env)
		if err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Connector storage unavailable")
			return
		}
		if err = audit(r.Context(), tx, "connector.deactivated", in.ConnectorID, map[string]any{
			"environment": env, "reason": "project_scope_changed",
		}); err != nil {
			httpapi.Error(w, 503, "audit_unavailable", "Connector audit unavailable")
			return
		}
	}
	if err = s.auditTargetTx(r.Context(), tx, httpapi.CurrentUser(r).ID, app, env, "v3.connector.target_bound", id, in.ConnectorID, in.TargetKind, in.ExternalID); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Target audit unavailable")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	httpapi.WriteJSON(w, 201, externalTarget{ID: id, ConnectorID: in.ConnectorID, Provider: provider,
		TargetKind: in.TargetKind, ExternalID: in.ExternalID, EnvironmentID: env})
}

func (s *Service) deleteTarget(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	app, env, ok := selectedTargetService(w, r, envs)
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	team, err := s.targetServiceTx(r.Context(), tx, app, env, true)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	if targetAuthorizationError(w, s.authorizeTargetTx(r.Context(), tx, httpapi.CurrentUser(r), app, env, team, "change")) {
		return
	}
	var connectorID, kind, externalID string
	err = tx.QueryRow(r.Context(), `SELECT connector_id,target_kind,external_id FROM v3_connector_targets
 WHERE id=$1 AND application_id=$2 AND environment_id=$3 FOR UPDATE`,
		r.PathValue("bindingId"), app, env).Scan(&connectorID, &kind, &externalID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Target not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	// Activation locks this connector row before checking the complete target
	// set. Taking the same lock before deletion prevents the final binding from
	// disappearing between activation's check and commit.
	var provider string
	var connectorEnabled bool
	if err = tx.QueryRow(r.Context(), `SELECT provider,enabled FROM v3_connectors
 WHERE id=$1 AND environment_id=$2 FOR UPDATE`, connectorID, env).Scan(&provider, &connectorEnabled); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage unavailable")
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM v3_connector_targets WHERE id=$1`, r.PathValue("bindingId")); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	if provider == "gitlab" {
		var remaining int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM v3_connector_targets
 WHERE connector_id=$1 AND environment_id=$2`, connectorID, env).Scan(&remaining); err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
			return
		}
		if remaining == 0 && connectorEnabled {
			_, err = tx.Exec(r.Context(), `UPDATE v3_connectors c SET enabled=false,status='unverified',
 last_checked_at=NULL,last_latency_ms=NULL,last_error_code='',updated_at=now(),
 tools=COALESCE((SELECT jsonb_agg(jsonb_set(value,'{enabled}','false'::jsonb,true))
 FROM jsonb_array_elements(c.tools) AS item(value)),'[]'::jsonb)
 WHERE id=$1 AND environment_id=$2`, connectorID, env)
			if err != nil {
				httpapi.Error(w, 503, "storage_unavailable", "Connector storage unavailable")
				return
			}
			if err = audit(r.Context(), tx, "connector.deactivated", connectorID, map[string]any{
				"environment": env, "reason": "last_project_unbound",
			}); err != nil {
				httpapi.Error(w, 503, "audit_unavailable", "Connector audit unavailable")
				return
			}
		}
	}
	if err = s.auditTargetTx(r.Context(), tx, httpapi.CurrentUser(r).ID, app, env, "v3.connector.target_unbound", r.PathValue("bindingId"), connectorID, kind, externalID); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Target audit unavailable")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Target storage unavailable")
		return
	}
	httpapi.WriteJSON(w, 204, nil)
}

func (s *Service) auditTargetTx(ctx context.Context, tx pgx.Tx, actor, app, env, action, target, connector, kind, external string) error {
	_, err := tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,$3,$4,'success',jsonb_build_object(
  'environment_id',$5::text,'connector_id',$6::text,'target_kind',$7::text,'external_id',$8::text))`,
		actor, app, action, target, env, connector, kind, external)
	return err
}
