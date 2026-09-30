package v3integrations

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// AuthorizeMetricSource checks that a previously captured metric still belongs
// to the sole reviewed Prometheus target for this service. This convenience
// form owns a transaction; writes that attach evidence to another record must
// use AuthorizeMetricSourceTx in that record's transaction instead.
func (p *PrometheusReader) AuthorizeMetricSource(ctx context.Context, ownerID, env, app, binding, connector, resource, query string) error {
	return p.authorizeMetricSource(ctx, ownerID, env, app, binding, connector, resource, query, "")
}

// AuthorizeMetricSourceVersion additionally requires the connector settings
// and reviewed expression to match the version captured with the samples.
func (p *PrometheusReader) AuthorizeMetricSourceVersion(ctx context.Context, ownerID, env, app, binding, connector, resource, query, fingerprint string) error {
	if fingerprint == "" {
		return metricReadError("invalid_scope")
	}
	return p.authorizeMetricSource(ctx, ownerID, env, app, binding, connector, resource, query, fingerprint)
}

func (p *PrometheusReader) authorizeMetricSource(ctx context.Context, ownerID, env, app, binding, connector, resource, query, fingerprint string) error {
	if p == nil || p.DB == nil {
		return metricReadError("storage_unavailable")
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return metricReadError("storage_unavailable")
	}
	defer tx.Rollback(ctx)
	if err = p.authorizeMetricSourceTx(ctx, tx, ownerID, env, app, binding, connector, resource, query, fingerprint); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return metricReadError("storage_unavailable")
	}
	return nil
}

// AuthorizeMetricSourceTx pins the authorization and source rows until the
// caller commits its conversation/question write. The caller must use a
// read-write transaction at READ COMMITTED isolation: a snapshot established
// before a concurrent revoke could otherwise continue to see stale grants.
// A session credential is checked by the HTTP/conversation caller, not here.
func (p *PrometheusReader) AuthorizeMetricSourceTx(ctx context.Context, tx pgx.Tx, ownerID, env, app, binding, connector, resource, query string) error {
	return p.authorizeMetricSourceTx(ctx, tx, ownerID, env, app, binding, connector, resource, query, "")
}

// AuthorizeMetricSourceVersionTx is the write-transaction form for evidence
// accepted earlier. It fails if connector settings or the reviewed expression
// changed without changing the source identifiers.
func (p *PrometheusReader) AuthorizeMetricSourceVersionTx(ctx context.Context, tx pgx.Tx, ownerID, env, app, binding, connector, resource, query, fingerprint string) error {
	if fingerprint == "" {
		return metricReadError("invalid_scope")
	}
	return p.authorizeMetricSourceTx(ctx, tx, ownerID, env, app, binding, connector, resource, query, fingerprint)
}

func (p *PrometheusReader) authorizeMetricSourceTx(ctx context.Context, tx pgx.Tx, ownerID, env, app, binding, connector, resource, query, fingerprint string) error {
	if p == nil || p.DB == nil || tx == nil {
		return metricReadError("storage_unavailable")
	}
	if ownerID == "" || env == "" || env == "all" || app == "" || binding == "" || connector == "" ||
		!prometheusResourceID.MatchString(resource) || !prometheusQueryID.MatchString(query) ||
		(p.Tier != "production" && p.Tier != "nonprod") {
		return metricReadError("invalid_scope")
	}
	var isolation string
	if err := tx.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil {
		return metricReadError("storage_unavailable")
	}
	if isolation != "read committed" {
		return metricReadError("invalid_scope")
	}
	key := PrometheusQueryKey{ConnectorID: connector, ApplicationID: app, EnvironmentID: env,
		ResourceID: resource, QueryID: query}
	expression, registered := p.queries[key]
	if !registered || expression == "" || strings.TrimSpace(expression) != expression || len(expression) > 2048 {
		return metricReadError("query_unregistered")
	}

	// Account writers update users before deleting sessions. Role management
	// and source writers also take user/role and service locks in this order.
	var activeID string
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled
 AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, ownerID).Scan(&activeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return metricReadError("forbidden")
	}
	if err != nil {
		return metricReadError("authorization_unavailable")
	}
	roles, err := connectorActorRoles(ctx, tx, ownerID, false)
	if err != nil {
		return metricReadError("authorization_unavailable")
	}
	rows, err := tx.Query(ctx, `SELECT id FROM rbac_roles WHERE id=ANY($1::text[])
 ORDER BY id FOR SHARE`, roles)
	if err != nil {
		return metricReadError("authorization_unavailable")
	}
	lockedRoles := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		lockedRoles++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return metricReadError("authorization_unavailable")
	}
	if lockedRoles != len(roles) {
		return metricReadError("forbidden")
	}
	lockedAssignments, err := connectorActorRoles(ctx, tx, ownerID, true)
	if err != nil {
		return metricReadError("authorization_unavailable")
	}
	if !slices.Equal(roles, lockedAssignments) {
		return metricReadError("forbidden")
	}

	// Derive a permission witness from locked rows. A role assignment that
	// appears after the scan cannot silently authorize this transaction.
	rows, err = tx.Query(ctx, `SELECT module,action,scope_kind,scope_id FROM v3_role_grants
 WHERE role_id=ANY($1::text[])
 AND ((module='environment' AND action='view')
   OR (module='services' AND action='view')
   OR (module='connectors' AND action='view'))
 ORDER BY role_id,module,action,scope_kind,scope_id FOR SHARE`, roles)
	if err != nil {
		return metricReadError("authorization_unavailable")
	}
	var environmentView, serviceView, connectorView bool
	teamGrantIDs := map[string]bool{}
	var team string
	for rows.Next() {
		var module, action, scopeKind, scopeID string
		if err = rows.Scan(&module, &action, &scopeKind, &scopeID); err != nil {
			break
		}
		global := scopeKind == "global" && scopeID == "*"
		environment := scopeKind == "environment" && scopeID == env
		switch module {
		case "environment":
			environmentView = environmentView || global || environment
		case "connectors":
			connectorView = connectorView || global || environment
		case "services":
			serviceView = serviceView || global || environment || (scopeKind == "application" && scopeID == app)
			if scopeKind == "team" {
				teamGrantIDs[scopeID] = true
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return metricReadError("authorization_unavailable")
	}
	if !environmentView || !connectorView || (!serviceView && len(teamGrantIDs) == 0) {
		return metricReadError("forbidden")
	}

	svc := Service{DB: p.DB}
	team, err = svc.targetServiceTx(ctx, tx, app, env, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return metricReadError("source_not_found")
	}
	if err != nil {
		return metricReadError("storage_unavailable")
	}
	if !serviceView && (team == "" || !teamGrantIDs[team]) {
		return metricReadError("source_not_found")
	}
	if err = svc.authorizeTargetTx(ctx, tx, core.User{ID: ownerID}, app, env, team, "view"); err != nil {
		if errors.Is(err, errTargetServiceForbidden) {
			return metricReadError("source_not_found")
		}
		if errors.Is(err, store.ErrForbidden) {
			return metricReadError("forbidden")
		}
		return metricReadError("authorization_unavailable")
	}

	// targetServiceTx locks the application, V3 mapping and legacy environment
	// mapping. Its V3 mapping lock also blocks new target inserts through the
	// foreign key, while target deletion follows the service-first lock order.
	var targetCount int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.target_kind='metric_resource'
 AND c.provider='prometheus'`, app, env).Scan(&targetCount)
	if err != nil {
		return metricReadError("storage_unavailable")
	}
	if targetCount != 1 {
		if targetCount == 0 {
			return metricReadError("source_changed")
		}
		return metricReadError("source_ambiguous")
	}
	var currentConnector, currentResource, endpoint, authMethod, credentialRef string
	var enabled bool
	var tools []byte
	err = tx.QueryRow(ctx, `SELECT t.connector_id,t.external_id,c.endpoint,c.auth_method,
 c.credential_ref,c.enabled,c.tools FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.id=$1 AND t.application_id=$2 AND t.environment_id=$3
 AND t.target_kind='metric_resource' AND c.provider='prometheus'
 FOR SHARE OF t,c`, binding, app, env).
		Scan(&currentConnector, &currentResource, &endpoint, &authMethod, &credentialRef, &enabled, &tools)
	if errors.Is(err, pgx.ErrNoRows) {
		return metricReadError("source_changed")
	}
	if err != nil {
		return metricReadError("storage_unavailable")
	}
	if currentConnector != connector || currentResource != resource {
		return metricReadError("source_changed")
	}
	if !enabled || !toolEnabled(tools, "query_metrics") ||
		!validExternalTarget("prometheus", "metric_resource", currentResource) ||
		(authMethod == "bearer" && (p.Resolver == nil || validateReference(credentialRef) != nil)) ||
		(authMethod == "none" && credentialRef != "") ||
		(authMethod != "none" && authMethod != "bearer") {
		return metricReadError("source_unhealthy")
	}
	if _, err = validEndpoint(endpoint); err != nil {
		return metricReadError("source_unhealthy")
	}
	if authMethod == "bearer" {
		// Production uses BoundResolver. Keep the capability optional for
		// isolated resolver fixtures, matching Service.effectiveConnector.
		if policy, ok := p.Resolver.(interface {
			CredentialAllowed(CredentialUse, string) bool
		}); ok && !policy.CredentialAllowed(CredentialUse{
			Tier: p.Tier, EnvironmentID: env, ConnectorID: connector,
			Provider: "prometheus", Endpoint: endpoint,
		}, credentialRef) {
			return metricReadError("source_unhealthy")
		}
	}
	if fingerprint != "" && fingerprint != prometheusFingerprint(
		prometheusFingerprint(endpoint, authMethod, credentialRef, string(tools)), expression) {
		return metricReadError("source_changed")
	}
	return nil
}
