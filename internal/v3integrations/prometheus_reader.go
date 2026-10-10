package v3integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// PrometheusQueryKey identifies an operator-reviewed expression for one exact
// connector, service, environment and bound resource. The catalog owner must
// also verify that the expression itself filters to this application and
// environment (or an upstream source isolated to that exact scope). Key
// equality alone cannot constrain data inside arbitrary PromQL.
type PrometheusQueryKey struct {
	ConnectorID   string
	ApplicationID string
	EnvironmentID string
	ResourceID    string
	QueryID       string
}

var prometheusQueryID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// PrometheusReader bridges V3 registrations to a bounded, read-only query.
// Its catalog is copied at construction and cannot be changed by a request.
// The composition root must supply reviewed expressions and a Vault resolver.
type PrometheusReader struct {
	DB       *pgxpool.Pool
	Tier     string
	Resolver CredentialResolver
	Client   *http.Client
	queries  map[PrometheusQueryKey]string
}

type PrometheusMetricEvidence struct {
	ApplicationID string                    `json:"application_id"`
	EnvironmentID string                    `json:"environment_id"`
	BindingID     string                    `json:"binding_id"`
	ConnectorID   string                    `json:"connector_id"`
	ResourceID    string                    `json:"resource_id"`
	QueryID       string                    `json:"query_id"`
	CapturedAt    time.Time                 `json:"captured_at"`
	Series        []connectors.MetricSeries `json:"series"`
	Empty         bool                      `json:"empty"`
	// SourceFingerprint binds private connector configuration and the
	// reviewed expression to an observed snapshot. It is deliberately
	// omitted from public evidence JSON.
	SourceFingerprint string `json:"-"`
}

// PrometheusReadError has a stable, non-sensitive code for callers. It never
// includes the endpoint, credential reference, raw expression or upstream body.
type PrometheusReadError struct{ Code string }

func (e *PrometheusReadError) Error() string { return e.Code }

func metricReadError(code string) error { return &PrometheusReadError{Code: code} }

func NewPrometheusReader(db *pgxpool.Pool, tier string, resolver CredentialResolver, catalog map[PrometheusQueryKey]string) (*PrometheusReader, error) {
	if tier != "production" && tier != "nonprod" {
		return nil, metricReadError("invalid_catalog")
	}
	copied := make(map[PrometheusQueryKey]string, len(catalog))
	for key, expression := range catalog {
		if !PrometheusQueryValid(key, expression) {
			return nil, metricReadError("invalid_catalog")
		}
		copied[key] = expression
	}
	return &PrometheusReader{DB: db, Tier: tier, Resolver: resolver, queries: copied}, nil
}

type prometheusSource struct {
	BindingID, ConnectorID, ResourceID  string
	Endpoint, AuthMethod, CredentialRef string
	ConfigFingerprint                   string
}

func prometheusFingerprint(fields ...string) string {
	// JSON encoding is length-delimited, unlike concatenating raw identifiers.
	encoded, _ := json.Marshal(fields)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (p *PrometheusReader) loadSource(ctx context.Context, user core.User, app, env, bindingID string) (prometheusSource, error) {
	var source prometheusSource
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return source, metricReadError("storage_unavailable")
	}
	defer tx.Rollback(ctx)
	svc := Service{DB: p.DB}
	team, err := svc.targetServiceTx(ctx, tx, app, env, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return source, metricReadError("source_not_found")
	}
	if err != nil {
		return source, metricReadError("storage_unavailable")
	}
	if err = svc.authorizeTargetTx(ctx, tx, user, app, env, team, "view"); err != nil {
		if errors.Is(err, errTargetServiceForbidden) || errors.Is(err, store.ErrForbidden) {
			return source, metricReadError("forbidden")
		}
		return source, metricReadError("authorization_unavailable")
	}
	var enabled bool
	var tools []byte
	err = tx.QueryRow(ctx, `SELECT t.id,t.connector_id,t.external_id,c.endpoint,c.auth_method,c.credential_ref,
 c.enabled,c.tools
 FROM v3_connector_targets t JOIN v3_connectors c
  ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.id=$1 AND t.application_id=$2 AND t.environment_id=$3
  AND t.target_kind='metric_resource' AND c.provider='prometheus'`, bindingID, app, env).
		Scan(&source.BindingID, &source.ConnectorID, &source.ResourceID, &source.Endpoint,
			&source.AuthMethod, &source.CredentialRef, &enabled, &tools)
	if errors.Is(err, pgx.ErrNoRows) {
		return prometheusSource{}, metricReadError("source_not_found")
	}
	if err != nil {
		return prometheusSource{}, metricReadError("storage_unavailable")
	}
	if !enabled || !toolEnabled(tools, "query_metrics") ||
		!validExternalTarget("prometheus", "metric_resource", source.ResourceID) {
		return prometheusSource{}, metricReadError("source_unhealthy")
	}
	if _, err = validEndpoint(source.Endpoint); err != nil ||
		(source.AuthMethod != "none" && source.AuthMethod != "bearer") ||
		(source.AuthMethod == "none" && source.CredentialRef != "") ||
		(source.AuthMethod == "bearer" && validateReference(source.CredentialRef) != nil) {
		return prometheusSource{}, metricReadError("source_unhealthy")
	}
	source.ConfigFingerprint = prometheusFingerprint(source.Endpoint, source.AuthMethod, source.CredentialRef,
		string(tools))
	if err = tx.Commit(ctx); err != nil {
		return prometheusSource{}, metricReadError("storage_unavailable")
	}
	return source, nil
}

func toolEnabled(raw []byte, name string) bool {
	var items []tool
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	for _, item := range items {
		if item.Name == name && item.Enabled {
			return true
		}
	}
	return false
}

// Query first checks the selected instance scope, persisted service/connector
// grants, explicit binding, enabled reader and reviewed query ID. A connection
// probe is only historical metadata; the live range query proves read health.
// It resolves the bearer only for the live request, then rechecks all source
// and authorization state before returning samples to the caller.
func (p *PrometheusReader) Query(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User,
	app, env, bindingID, queryID string, window connectors.TimeWindow) (PrometheusMetricEvidence, error) {
	var out PrometheusMetricEvidence
	if p == nil || p.DB == nil {
		return out, metricReadError("storage_unavailable")
	}
	if scope.Tier != p.Tier || app == "" || env == "" || bindingID == "" || !prometheusQueryID.MatchString(queryID) {
		return out, metricReadError("invalid_scope")
	}
	selected := false
	for _, id := range scope.EnvironmentIDs {
		if id == env {
			selected = true
			break
		}
	}
	if !selected {
		return out, metricReadError("source_not_found")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	source, err := p.loadSource(ctx, user, app, env, bindingID)
	if err != nil {
		return out, err
	}
	key := PrometheusQueryKey{ConnectorID: source.ConnectorID, ApplicationID: app, EnvironmentID: env,
		ResourceID: source.ResourceID, QueryID: queryID}
	expression, ok := p.queries[key]
	if !ok {
		return out, metricReadError("query_unregistered")
	}
	credential := ""
	if source.AuthMethod == "bearer" {
		if p.Resolver == nil {
			return out, metricReadError("credential_unavailable")
		}
		credentialCtx := WithCredentialUse(ctx, CredentialUse{
			Tier: p.Tier, EnvironmentID: env, ConnectorID: source.ConnectorID,
			Provider: "prometheus", Endpoint: source.Endpoint,
		})
		credential, err = p.Resolver.Resolve(credentialCtx, source.CredentialRef)
		if err != nil || credential == "" || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n\x00") {
			return out, metricReadError("credential_unavailable")
		}
	}
	series, err := queryRegisteredPrometheus(ctx, source.Endpoint, source.AuthMethod, credential, expression, window, p.Client)
	if err != nil {
		return out, err
	}
	current, err := p.loadSource(ctx, user, app, env, bindingID)
	if err != nil {
		return out, err
	}
	if !reflect.DeepEqual(current, source) || p.queries[key] != expression {
		return out, metricReadError("source_changed")
	}
	out = PrometheusMetricEvidence{ApplicationID: app, EnvironmentID: env,
		BindingID: bindingID, ConnectorID: source.ConnectorID, ResourceID: source.ResourceID,
		QueryID: queryID, CapturedAt: time.Now().UTC(), Series: series, Empty: true,
		SourceFingerprint: prometheusFingerprint(source.ConfigFingerprint, expression)}
	for _, item := range series {
		if len(item.Samples) > 0 {
			out.Empty = false
			break
		}
	}
	return out, nil
}

// QueryService resolves exactly one explicit V3 Prometheus metric target for a
// service in the selected environment. It does not use legacy CMDB telemetry
// bindings. Query rechecks the chosen target and grants around the live read;
// this method also rechecks that the exact-one selection still holds afterward.
func (p *PrometheusReader) QueryService(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User,
	service, env, queryID string, window connectors.TimeWindow) (PrometheusMetricEvidence, error) {
	var empty PrometheusMetricEvidence
	if p == nil || p.DB == nil {
		return empty, metricReadError("storage_unavailable")
	}
	if scope.Tier != p.Tier || service == "" || env == "" {
		return empty, metricReadError("invalid_scope")
	}
	selected := false
	for _, id := range scope.EnvironmentIDs {
		selected = selected || id == env
	}
	if !selected || scope.All || len(scope.EnvironmentIDs) != 1 {
		return empty, metricReadError("invalid_scope")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	app, binding, err := p.resolveServiceMetricTarget(ctx, user, service, env)
	if err != nil {
		return empty, err
	}
	evidence, err := p.Query(ctx, scope, user, app, env, binding, queryID, window)
	if err != nil {
		var readErr *PrometheusReadError
		if errors.As(err, &readErr) && readErr.Code == "source_not_found" {
			return empty, metricReadError("source_changed")
		}
		return empty, err
	}
	currentApp, currentBinding, err := p.resolveServiceMetricTarget(ctx, user, service, env)
	if err != nil {
		var readErr *PrometheusReadError
		if errors.As(err, &readErr) && (readErr.Code == "source_not_found" || readErr.Code == "source_ambiguous") {
			return empty, metricReadError("source_changed")
		}
		return empty, err
	}
	if currentApp != app || currentBinding != binding {
		return empty, metricReadError("source_changed")
	}
	return evidence, nil
}

func (p *PrometheusReader) resolveServiceMetricTarget(ctx context.Context, user core.User, service, env string) (string, string, error) {
	tx, err := p.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", "", metricReadError("storage_unavailable")
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT a.id,COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE (a.id=$1 OR a.key=$1) AND NOT a.archived LIMIT 2`, service, env)
	if err != nil {
		return "", "", metricReadError("storage_unavailable")
	}
	type candidate struct{ app, team string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.app, &item.team); err != nil {
			break
		}
		candidates = append(candidates, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", "", metricReadError("storage_unavailable")
	}
	svc := Service{DB: p.DB}
	var visible []candidate
	for _, item := range candidates {
		if err = svc.authorizeTargetTx(ctx, tx, user, item.app, env, item.team, "view"); err != nil {
			if errors.Is(err, errTargetServiceForbidden) {
				continue
			}
			// The service is visible; deny access before looking up bindings
			// so source existence stays undisclosed without connectors.view.
			if errors.Is(err, store.ErrForbidden) {
				return "", "", metricReadError("forbidden")
			}
			return "", "", metricReadError("authorization_unavailable")
		}
		visible = append(visible, item)
	}
	if len(visible) == 0 {
		return "", "", metricReadError("source_not_found")
	}
	if len(visible) != 1 {
		return "", "", metricReadError("source_ambiguous")
	}
	app := visible[0].app
	bindings, err := tx.Query(ctx, `SELECT t.id FROM v3_connector_targets t JOIN v3_connectors c
  ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.application_id=$1 AND t.environment_id=$2
  AND t.target_kind='metric_resource' AND c.provider='prometheus' LIMIT 2`, app, env)
	if err != nil {
		return "", "", metricReadError("storage_unavailable")
	}
	var binding string
	count := 0
	for bindings.Next() {
		if err = bindings.Scan(&binding); err != nil {
			break
		}
		count++
	}
	if err == nil {
		err = bindings.Err()
	}
	bindings.Close()
	if err != nil {
		return "", "", metricReadError("storage_unavailable")
	}
	if count == 0 {
		return "", "", metricReadError("source_not_found")
	}
	if count != 1 {
		return "", "", metricReadError("source_ambiguous")
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", metricReadError("storage_unavailable")
	}
	return app, binding, nil
}

// QueryForExecutor reads one reviewed query for the rollback executor
// (PA-08). The executor acts on an approved proposal, not for a viewer, so
// there is no user check; instead the metric target must still be the
// service's bound metric_resource on this connector and resource, enabled
// for query_metrics, and the query must be in the reviewed catalog.
func (p *PrometheusReader) QueryForExecutor(ctx context.Context, app, env, connectorID, resourceID, queryID string,
	window connectors.TimeWindow) ([]connectors.MetricSeries, error) {
	if p == nil || p.DB == nil {
		return nil, metricReadError("storage_unavailable")
	}
	if app == "" || env == "" || env == "all" || connectorID == "" || !validExternalTarget("prometheus", "metric_resource", resourceID) ||
		!prometheusQueryID.MatchString(queryID) {
		return nil, metricReadError("invalid_scope")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var source prometheusSource
	var enabled bool
	var tools []byte
	err := p.DB.QueryRow(ctx, `SELECT t.id,t.connector_id,t.external_id,c.endpoint,c.auth_method,c.credential_ref,c.enabled,c.tools
 FROM v3_connector_targets t JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 JOIN applications a ON a.id=t.application_id AND NOT a.archived
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.connector_id=$3 AND t.external_id=$4
  AND t.target_kind='metric_resource' AND c.provider='prometheus'`, app, env, connectorID, resourceID).
		Scan(&source.BindingID, &source.ConnectorID, &source.ResourceID, &source.Endpoint, &source.AuthMethod,
			&source.CredentialRef, &enabled, &tools)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, metricReadError("source_not_found")
	}
	if err != nil {
		return nil, metricReadError("storage_unavailable")
	}
	if !enabled || !toolEnabled(tools, "query_metrics") {
		return nil, metricReadError("source_unhealthy")
	}
	if _, err = validEndpoint(source.Endpoint); err != nil ||
		(source.AuthMethod != "none" && source.AuthMethod != "bearer") ||
		(source.AuthMethod == "bearer" && validateReference(source.CredentialRef) != nil) {
		return nil, metricReadError("source_unhealthy")
	}
	expression, ok := p.queries[PrometheusQueryKey{ConnectorID: connectorID, ApplicationID: app, EnvironmentID: env,
		ResourceID: resourceID, QueryID: queryID}]
	if !ok {
		return nil, metricReadError("query_unregistered")
	}
	credential := ""
	if source.AuthMethod == "bearer" {
		if p.Resolver == nil {
			return nil, metricReadError("credential_unavailable")
		}
		credential, err = p.Resolver.Resolve(WithCredentialUse(ctx, CredentialUse{Tier: p.Tier, EnvironmentID: env,
			ConnectorID: connectorID, Provider: "prometheus", Endpoint: source.Endpoint}), source.CredentialRef)
		if err != nil || credential == "" || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n\x00") {
			return nil, metricReadError("credential_unavailable")
		}
	}
	return queryRegisteredPrometheus(ctx, source.Endpoint, source.AuthMethod, credential, expression, window, p.Client)
}
