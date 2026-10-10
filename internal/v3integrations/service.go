// Package v3integrations stores V3 connector registrations and performs bounded,
// read-only connection checks. It never stores credentials or provisions webhooks.
package v3integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3interactionaudit"
)

type Service struct {
	DB     *pgxpool.Pool
	Tier   string // "production" or "nonprod"; one database per instance
	Prober Prober
	// Workloads, when set, projects a Kubernetes connector's workload
	// targets and capabilities on its detail (PA-72). v3rollouts supplies it,
	// so write approvals stay out of this package.
	Workloads func(ctx context.Context, connectorID, environmentID string) (ConnectorWorkloads, error)
	// RegistryTools shows the saved tool switches on the connector detail
	// (PA-19 P3 stage 2 step 7, the registrations reader).
	RegistryTools bool
}

// ConnectorWorkloads is the read-only rollback view of a Kubernetes
// connector: its workload targets with the reviewed rollback setting and the
// deployer's write approval, and what the connector can do.
type ConnectorWorkloads struct {
	Workloads    []ConnectorWorkload `json:"workloads"`
	Capabilities []string            `json:"capabilities"`
}

type ConnectorWorkload struct {
	Key           string                   `json:"key"`
	Namespace     string                   `json:"namespace"`
	Deployment    string                   `json:"deployment"`
	Service       *string                  `json:"service"`
	Rollback      *ConnectorWorkloadPolicy `json:"rollback"`
	WriteApproved *bool                    `json:"write_approved"`
}

type ConnectorWorkloadPolicy struct {
	Allowed            bool `json:"allowed"`
	ObservationSeconds *int `json:"observation_seconds"`
}

func New(db *pgxpool.Pool, tier string, resolver CredentialResolver) *Service {
	return &Service{DB: db, Tier: tier, Prober: Prober{Resolver: resolver}}
}

type tool struct {
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	Enabled bool   `json:"enabled"`
}

type connector struct {
	ID            string
	Revision      int64
	EnvironmentID string
	Provider      string
	Name          string
	Category      string
	Endpoint      string
	AuthMethod    string
	CredentialRef string
	Webhook       bool
	Enabled       bool
	Tools         []tool
	Status        string
	Checked       *time.Time
	LatencyMS     *int
	ErrorCode     string
}

type createRequest struct {
	Type          string   `json:"type"`
	URL           string   `json:"url"`
	Auth          string   `json:"auth"`
	CredentialRef string   `json:"credential_ref"`
	Tools         []string `json:"tools"`
	Webhook       bool     `json:"webhook"`
}

type patchRequest struct {
	Tools []struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	} `json:"tools"`
}

func (s *Service) scope(w http.ResponseWriter, r *http.Request, write bool) ([]string, bool) {
	bound, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || bound.Tier != s.Tier || len(bound.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	if write && bound.All {
		httpapi.Error(w, 409, "env_view_only", "All non-production environments are read-only")
		return nil, false
	}
	if write && len(bound.EnvironmentIDs) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Choose one environment")
		return nil, false
	}
	return bound.EnvironmentIDs, true
}

func (s *Service) Register(api *httpapi.Server) {
	s.registerTargets(api)
	api.Register("GET /api/v1/connector-types/{type}", "viewer", s.connectorType)
	api.Register("GET /api/v1/connectors", "admin", s.list)
	api.Register("GET /api/v1/connectors/{id}", "admin", s.detail)
	api.Register("GET /api/v1/connectors/{id}/tool-settings", "admin", s.toolSettings)
	api.Register("POST /api/v1/connectors", "admin", s.create)
	api.Register("POST /api/v1/connectors/{id}/activate", "admin", s.activate)
	api.Register("PATCH /api/v1/connectors/{id}", "admin", s.patch)
	api.Register("POST /api/v1/connectors/{id}/test", "admin", s.test)
	v3interactionaudit.Mark(api, s.DB, "PUT /api/v1/connectors/{id}/settings", "v3.connector.settings_updated")
	api.Register("PUT /api/v1/connectors/{id}/settings", "admin", s.settings)
	v3interactionaudit.Mark(api, s.DB, "POST /api/v1/connectors/{id}/disable", "v3.connector.disabled")
	api.Register("POST /api/v1/connectors/{id}/disable", "admin", s.disable)
	v3interactionaudit.Mark(api, s.DB, "POST /api/v1/webhooks", "v3.webhook.registration_unavailable")
	api.Register("POST /api/v1/webhooks", "admin", s.createWebhook)
	for _, pattern := range []string{"GET /api/v1/connector-types/{type}", "GET /api/v1/connectors", "GET /api/v1/connectors/{id}", "GET /api/v1/connectors/{id}/tool-settings"} {
		api.MarkV3Route(pattern, "connectors", "view")
	}
	for _, pattern := range []string{"POST /api/v1/connectors", "POST /api/v1/connectors/{id}/activate", "PATCH /api/v1/connectors/{id}", "POST /api/v1/connectors/{id}/test", "PUT /api/v1/connectors/{id}/settings", "POST /api/v1/connectors/{id}/disable", "POST /api/v1/webhooks"} {
		api.MarkV3Route(pattern, "connectors", "manage")
	}
}

func (s *Service) connectorType(w http.ResponseWriter, r *http.Request) {
	lang := r.Header.Get("Accept-Language")
	d, ok := descriptors[r.PathValue("type")]
	if !ok {
		httpapi.Error(w, 404, "not_found", localized(lang, "连接器类型不可用", "Connector type is not available"))
		return
	}
	auth := make([]map[string]string, 0, len(d.Methods))
	for _, method := range d.Methods {
		label, desc := authMethodCopy(method, lang)
		auth = append(auth, map[string]string{"id": method, "label": label, "desc": desc})
	}
	// This is a provider type, not a connected instance: its version and token
	// scopes cannot be verified here. Webhook provisioning is unsupported.
	// Descriptor tools are accepted registration preferences, not Agent tools.
	// No connector-backed Agent execution is wired, so do not advertise them.
	httpapi.WriteJSON(w, 200, map[string]any{"brand": d.Provider, "name": d.Name, "kind": kindCopy(d.Kind, lang), "version": "", "auth_methods": auth, "scopes": []any{}, "tools": []any{}, "webhook_url": ""})
}

func scanConnector(rows pgx.Row) (connector, error) {
	var c connector
	var raw []byte
	err := rows.Scan(&c.ID, &c.EnvironmentID, &c.Provider, &c.Name, &c.Category, &c.Endpoint, &c.AuthMethod, &c.CredentialRef, &c.Webhook, &c.Enabled, &raw, &c.Status, &c.Checked, &c.LatencyMS, &c.ErrorCode, &c.Revision)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c.Tools); err != nil {
		return c, err
	}
	return c, nil
}

const columns = "id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,webhook_requested,enabled,tools,status,last_checked_at,last_latency_ms,last_error_code,revision"

// A connection test is a point-in-time probe, not ongoing health monitoring.
// The list only calls it current for this short window; older results retain
// their timestamp but do not advertise the connector as connected or broken.
const listProbeFreshness = 15 * time.Minute

func (s *Service) lookup(ctx context.Context, id string, environments []string) (connector, error) {
	return scanConnector(s.DB.QueryRow(ctx, "SELECT "+columns+" FROM v3_connectors WHERE id=$1 AND environment_id = ANY($2::text[])", id, environments))
}

func (s *Service) authorizeWriteTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx, environment string) bool {
	policy := &rbac.V3Policy{Store: &store.Store{DB: s.DB}}
	err := policy.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "connectors", Action: "manage", EnvironmentID: environment,
	})
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Connector management permission required")
		return false
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Connector authorization unavailable")
		return false
	}
	return true
}

// Pin the permission sources until this transaction commits. The V3 route
// check and a plain policy read alone cannot protect a write from a concurrent
// role, grant, account or session revoke.
func (s *Service) authorizeLockedWriteTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx, environment string) bool {
	if err := lockConnectorAuthority(r.Context(), tx, r, environment); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Connector management permission required")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Connector authorization unavailable")
		}
		return false
	}
	return s.authorizeWriteTx(w, r, tx, environment)
}

func (s *Service) credentialBound(c connector, reference string) bool {
	if c.AuthMethod == "none" {
		return true
	}
	if s.Prober.Resolver == nil || reference == "" {
		return false
	}
	// Production supplies a BoundResolver. Other resolvers are limited to
	// isolated adapter tests; an unbound production registration is projected
	// as disabled and cannot reach Vault or its upstream endpoint.
	if policy, ok := s.Prober.Resolver.(interface {
		CredentialAllowed(CredentialUse, string) bool
	}); ok {
		return policy.CredentialAllowed(CredentialUse{
			Tier: s.Tier, EnvironmentID: c.EnvironmentID, ConnectorID: c.ID,
			Provider: c.Provider, Endpoint: c.Endpoint,
		}, reference)
	}
	return true
}

func (s *Service) effectiveConnector(c connector) connector {
	if !s.credentialBound(c, c.CredentialRef) {
		c.Enabled = false
		c.Status = "unverified"
	}
	return c
}

func (s *Service) connectorSummary(c connector, lang string, now time.Time) map[string]any {
	result := observedSummary(s.effectiveConnector(c), lang, now)
	if c.AuthMethod != "none" && c.CredentialRef != "" && !s.credentialBound(c, c.CredentialRef) {
		result["meta"] = localized(lang, "未启用 · 服务端凭据绑定未配置", "Disabled · server credential binding not configured")
	}
	return result
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scope(w, r, false)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	category := r.URL.Query().Get("category")
	if category == "" {
		category = "all"
	}
	valid := false
	for _, c := range categories {
		if c == category {
			valid = true
			break
		}
	}
	if !valid {
		httpapi.Error(w, 422, "invalid_category", "Unknown connector category")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		httpapi.Error(w, 422, "invalid_query", "Search is too long")
		return
	}
	// The frontend contract has no page cursor. Scan in bounded batches, then
	// either return complete counts or fail instead of silently truncating them.
	// A read-only snapshot also keeps the list and its authorization decisions
	// on one connection (including with a single-connection pool).
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	policy := &rbac.V3Policy{Store: &store.Store{DB: s.DB}}
	user := httpapi.CurrentUser(r)
	s.listInTx(w, r, tx, env, category, query, false, func(ctx context.Context, tx pgx.Tx, c connector) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
			Module: "connectors", Action: "view", EnvironmentID: c.EnvironmentID,
		})
	}, nil)
}

// listInTx projects the existing connector-list contract after its caller has
// established an authorization boundary on this same transaction. Neither
// the HTTP authentication mode nor its credential enters the response.
func (s *Service) listInTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx, env []string,
	category, query string, singleSnapshot bool, authorize func(context.Context, pgx.Tx, connector) error,
	finalize func(context.Context, pgx.Tx) error) {
	items := make([]map[string]any, 0)
	counts := map[string]int{"ok": 0, "unverified": 0, "error": 0}
	categoryCounts := map[string]int{"all": 0}
	now := time.Now()
	lastName, lastID := "", ""
	for {
		statement := "SELECT " + columns + " FROM v3_connectors WHERE environment_id = ANY($1::text[]) AND (name,id)>($2,$3) ORDER BY name,id LIMIT 201"
		if singleSnapshot {
			// The Bearer path uses READ COMMITTED for revocation safety. One
			// statement snapshot prevents a concurrent connector update from
			// changing the collection or counts between pages.
			statement = "SELECT " + columns + " FROM v3_connectors WHERE environment_id = ANY($1::text[]) ORDER BY name,id LIMIT 1001"
		}
		var rows pgx.Rows
		var err error
		if singleSnapshot {
			rows, err = tx.Query(r.Context(), statement, env)
		} else {
			rows, err = tx.Query(r.Context(), statement, env, lastName, lastID)
		}
		if err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
			return
		}
		batch := make([]connector, 0, 201)
		for rows.Next() {
			c, scanErr := scanConnector(rows)
			if scanErr != nil {
				err = scanErr
				break
			}
			batch = append(batch, c)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
			return
		}
		for _, c := range batch {
			// A connector registration belongs to exactly one environment. It
			// carries no application/team owner; those scopes apply separately to
			// its explicit target bindings, never to the registration itself.
			err := authorize(r.Context(), tx, c)
			if errors.Is(err, store.ErrForbidden) {
				continue
			}
			if err != nil {
				httpapi.Error(w, 503, "authorization_unavailable", "Connector access could not be checked")
				return
			}
			if !validConnectorProjection(c) {
				httpapi.Error(w, 503, "connector_state_unavailable", "Connector state cannot be represented by the V3 contract")
				return
			}
			if categoryCounts["all"] == 1000 {
				httpapi.Error(w, 413, "scope_too_large", "Connector collection exceeds the unpaged V3 response limit")
				return
			}
			projected := s.connectorSummary(c, r.Header.Get("Accept-Language"), now)
			counts[projected["status"].(string)]++
			categoryCounts["all"]++
			categoryCounts[c.Category]++
			if category != "all" && c.Category != category {
				continue
			}
			if query != "" && !searchtext.Match(c.ID+" "+c.Name+" "+c.Provider, query) {
				continue
			}
			items = append(items, projected)
		}
		if singleSnapshot || len(batch) < 201 {
			break
		}
		lastName, lastID = batch[len(batch)-1].Name, batch[len(batch)-1].ID
	}
	cats := make([]map[string]any, 0, len(categories))
	for _, id := range categories {
		cats = append(cats, map[string]any{"id": id, "label": categoryLabel(id, r.Header.Get("Accept-Language")), "count": categoryCounts[id]})
	}
	if finalize != nil {
		if err := finalize(r.Context(), tx); err != nil {
			bearerListError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts, "categories": cats})
}

func validConnectorProjection(c connector) bool {
	validCategory := false
	for _, known := range categories {
		validCategory = validCategory || (known != "all" && known == c.Category)
	}
	return validCategory && (c.Status == "ok" || c.Status == "unverified" || c.Status == "error") &&
		(c.Status == "unverified" || c.Checked != nil)
}

// observedSummary projects the most recent bounded connection test. Its
// status is never a claim that the upstream remains healthy between tests.
// Call only after validConnectorProjection, which checks Checked before use.
func observedSummary(c connector, lang string, now time.Time) map[string]any {
	if c.AuthMethod != "none" && c.CredentialRef == "" {
		// A stored successful probe cannot be current when the selected
		// authentication method has no credential reference to resolve.
		c.Status = "unverified"
		result := summary(c, lang)
		if c.Enabled {
			result["meta"] = localized(lang, "凭据未配置", "Credentials not configured")
		} else {
			result["meta"] = localized(lang, "未启用 · 凭据未配置", "Disabled · credentials not configured")
		}
		return result
	}
	if !c.Enabled {
		c.Status = "unverified"
		result := summary(c, lang)
		result["meta"] = localized(lang, "未启用 · 完成凭据和范围配置后可启用", "Disabled · configure credentials and scope before activation")
		return result
	}
	if c.Status == "unverified" {
		return summary(c, lang)
	}
	age := now.Sub(*c.Checked)
	if age >= 0 && age <= listProbeFreshness {
		result := summary(c, lang)
		if c.Status == "error" {
			result["meta"] = localized(lang, "连接检查失败 ", "Connection test failed ") + c.Checked.UTC().Format(time.RFC3339) + ": " + c.ErrorCode
		}
		return result
	}
	last := c.Checked.UTC().Format(time.RFC3339)
	c.Status = "unverified"
	result := summary(c, lang)
	result["meta"] = localized(lang, "上次连接检查 ", "Last connection test ") + last + localized(lang, " · 已过期", " · expired")
	return result
}

func summary(c connector, lang string) map[string]any {
	// Persisted tool choices are registration preferences. They are not
	// connector-backed Agent grants and must not appear as capability badges.
	tags := []map[string]string{}
	meta := localized(lang, "尚未检查连接", "Connection not tested")
	if c.Status == "ok" && c.Checked != nil {
		meta = localized(lang, "已检查 ", "Checked ") + c.Checked.UTC().Format(time.RFC3339)
	}
	if c.Status == "error" {
		meta = localized(lang, "连接检查失败：", "Connection check failed: ") + c.ErrorCode
	}
	kind := descriptors[c.Provider].Kind
	if kind == "" {
		kind = c.Provider
	}
	return map[string]any{"id": c.ID, "brand": c.Provider, "name": c.Name, "kind": kindCopy(kind, lang), "category": c.Category, "status": c.Status, "tags": tags, "meta": meta}
}

func displayTools(items []tool, lang string) []tool {
	tools := make([]tool, 0, len(items))
	for _, item := range items {
		item.Desc = toolCopy(item.Name, lang)
		tools = append(tools, item)
	}
	return tools
}

func validRegisteredTools(c connector, d descriptor) bool {
	if c.Tools == nil {
		return false
	}
	seen := make(map[string]bool, len(c.Tools))
	for _, item := range c.Tools {
		if seen[item.Name] || !knownTool(d, item.Name) {
			return false
		}
		seen[item.Name] = true
	}
	return true
}

var errConnectorServiceLimit = errors.New("connector service result limit exceeded")

// connectorServices projects only reviewed external targets belonging to this
// exact connector instance and environment. Every service is checked against
// its persisted application/team V3 grant before its key enters the response.
func (s *Service) connectorServices(ctx context.Context, c connector, user core.User) ([]string, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	services, err := s.connectorServicesTx(ctx, tx, c, user)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return services, nil
}

func (s *Service) connectorServicesTx(ctx context.Context, tx pgx.Tx, c connector, user core.User) ([]string, error) {
	services := []string{}
	afterID := ""
	for {
		rows, err := tx.Query(ctx, `SELECT DISTINCT a.id,a.key,COALESCE(a.owner_team_id,'')
 FROM v3_connector_targets t
 JOIN applications a ON a.id=t.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=t.environment_id
 JOIN application_environments e ON e.application_id=a.id AND e.key=t.environment_id AND NOT e.archived
 WHERE t.connector_id=$1 AND t.environment_id=$2 AND a.id>$3
 ORDER BY a.id LIMIT 100`, c.ID, c.EnvironmentID, afterID)
		if err != nil {
			return nil, err
		}
		type candidate struct{ id, key, team string }
		batch := make([]candidate, 0, 100)
		for rows.Next() {
			var item candidate
			if err = rows.Scan(&item.id, &item.key, &item.team); err != nil {
				break
			}
			batch = append(batch, item)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, item := range batch {
			err = s.authorizeTargetTx(ctx, tx, user, item.id, c.EnvironmentID, item.team, "view")
			if errors.Is(err, errTargetServiceForbidden) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if len(services) == 1000 {
				return nil, errConnectorServiceLimit
			}
			services = append(services, item.key)
		}
		if len(batch) < 100 {
			break
		}
		afterID = batch[len(batch)-1].id
	}
	return services, nil
}

func (s *Service) detail(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scope(w, r, false)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	c, err := s.lookup(r.Context(), r.PathValue("id"), env)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if !validConnectorProjection(c) {
		httpapi.Error(w, 503, "connector_state_unavailable", "Connector state cannot be represented by the V3 contract")
		return
	}
	services, err := s.connectorServices(r.Context(), c, httpapi.CurrentUser(r))
	if errors.Is(err, errConnectorServiceLimit) {
		httpapi.Error(w, 422, "result_limit", "Too many services for connector detail")
		return
	}
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Connector permission required")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector service bindings are unavailable")
		return
	}
	result := s.detailProjection(c, services, r.Header.Get("Accept-Language"))
	if c.Provider == "kubernetes" && s.Workloads != nil {
		view, err := s.Workloads(r.Context(), c.ID, c.EnvironmentID)
		if err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Connector workloads are unavailable")
			return
		}
		result["workloads"], result["capabilities"] = view.Workloads, view.Capabilities
	}
	httpapi.WriteJSON(w, 200, result)
}

func (s *Service) detailProjection(c connector, services []string, lang string) map[string]any {
	result := s.connectorSummary(c, lang, time.Now())
	checked := localized(lang, "尚未检查", "Never checked")
	if c.Checked != nil {
		checked = c.Checked.UTC().Format(time.RFC3339)
	}
	result["revision"] = c.Revision
	result["enabled"] = s.effectiveConnector(c).Enabled
	result["scope"] = c.EnvironmentID
	result["address"] = c.Endpoint
	if c.AuthMethod == "none" {
		result["auth"] = map[string]string{"text": localized(lang, "无需认证", "No authentication")}
	} else if c.CredentialRef == "" {
		label, _ := authMethodCopy(c.AuthMethod, lang)
		result["auth"] = map[string]string{"text": label + localized(lang, " · 凭据未配置", " · credentials not configured")}
	} else if !s.credentialBound(c, c.CredentialRef) {
		label, _ := authMethodCopy(c.AuthMethod, lang)
		result["auth"] = map[string]string{"text": label + localized(lang, " · 服务端绑定未配置", " · server binding not configured")}
	} else {
		label, _ := authMethodCopy(c.AuthMethod, lang)
		result["auth"] = map[string]string{"brand": "vault", "text": label + localized(lang, " · Vault 引用已登记", " · Vault reference registered")}
	}
	result["checked"] = checked
	result["services"] = services
	// Query IDs are not yet bound to a V3 connector instance. Tool settings
	// only describe desired configuration and do not grant execution.
	result["queries"] = []string{}
	// Connector-backed Agent execution is not wired. Keep registration
	// preferences in storage, but expose no executable tools in this DTO;
	// with the registrations reader switched (PA-19 P3 stage 2 step 7) the
	// saved switches are shown, since they decide whether the registry's
	// readers may read this connector.
	result["tools"] = []tool{}
	if s.RegistryTools {
		result["tools"] = displayTools(c.Tools, lang)
	}
	result["agent_execution_available"] = false
	return result
}

// toolSettings reads the stored connector tool switches separately from the
// Agent-facing detail DTO. Some scoped source readers consult these switches;
// they do not grant connector-backed Agent execution.
func (s *Service) toolSettings(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scope(w, r, false)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	c, err := s.lookup(r.Context(), r.PathValue("id"), env)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	d, known := descriptors[c.Provider]
	if !known || !validRegisteredTools(c, d) {
		httpapi.Error(w, 503, "connector_state_unavailable", "Connector tool settings are inconsistent with the current catalog")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"connector_id": c.ID, "environment_id": c.EnvironmentID, "connector_enabled": s.effectiveConnector(c).Enabled,
		"registered_tools":          displayTools(c.Tools, r.Header.Get("Accept-Language")),
		"agent_execution_available": false,
	})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	var in createRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	d, known := descriptors[in.Type]
	if !known || !knownMethod(d, in.Auth) {
		httpapi.Error(w, 422, "invalid_connector", "Unsupported connector type or authentication method")
		return
	}
	if _, err := validEndpoint(in.URL); err != nil {
		httpapi.Error(w, 422, "invalid_endpoint", "Use an HTTPS endpoint, or a local loopback HTTP endpoint")
		return
	}
	if in.Auth != "none" {
		if in.CredentialRef != "" && validateReference(in.CredentialRef) != nil {
			httpapi.Error(w, 422, "invalid_credential_ref", "Credential reference must use a Vault field")
			return
		}
	} else if in.CredentialRef != "" {
		httpapi.Error(w, 422, "invalid_connector", "Unauthenticated connectors do not take credentials")
		return
	}
	// GitLab's project scope is a separately reviewed target binding. The
	// current wizard sends neither that scope nor a Vault reference, so its
	// Webhook toggle only records intent; no upstream Webhook is created.
	// A connector with an explicit environment scope and complete credentials
	// can retain the existing read-only registration behavior for providers
	// that do not require project enrollment.
	// A Vault reference supplied by the caller is not an authorization to
	// read it. Credentialed registrations stay disabled until their generated
	// ID is bound to an exact server-managed credential policy and activation
	// performs a bounded check. Unauthenticated read-only sources can start
	// without that separate approval.
	enabled := d.Provider != "gitlab" && !in.Webhook && in.Auth == "none"
	seen := map[string]bool{}
	tools := make([]tool, 0, len(in.Tools))
	for _, name := range in.Tools {
		if seen[name] || !knownTool(d, name) {
			httpapi.Error(w, 422, "invalid_tools", "Choose distinct supported read-only tools")
			return
		}
		seen[name] = true
		tools = append(tools, tool{Name: name, Desc: "Read-only", Enabled: enabled})
	}
	raw, _ := json.Marshal(tools)
	id := store.ID()
	writeCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(writeCtx)
	tx, err := s.DB.Begin(writeCtx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer rollbackConnectorWrite(tx)
	if !s.authorizeWriteTx(w, r, tx, env[0]) {
		return
	}
	if !s.authorizeLockedWriteTx(w, r, tx, env[0]) {
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,webhook_requested,enabled,tools) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, env[0], d.Provider, d.Name, d.Category, strings.TrimRight(strings.TrimSpace(in.URL), "/"), in.Auth, in.CredentialRef, in.Webhook, enabled, raw)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			httpapi.Error(w, 409, "connector_exists", "This connector is already registered")
			return
		}
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if err = audit(r.Context(), tx, "connector.created", id, map[string]any{"environment": env[0], "provider": d.Provider, "auth_method": in.Auth, "enabled": enabled, "webhook_requested": in.Webhook}); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Could not audit connector creation")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"id": id})
}

func (s *Service) patch(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	var in patchRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if len(in.Tools) == 0 {
		httpapi.Error(w, 422, "invalid_tools", "Choose at least one tool to update")
		return
	}
	// Bound the row and authorization lock waits while this write transaction
	// holds the connector. A canceled request must not leave its row locked.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer rollbackConnectorWrite(tx)
	if !s.authorizeWriteTx(w, r, tx, env[0]) {
		return
	}
	c, err := scanConnector(tx.QueryRow(r.Context(), "SELECT "+columns+" FROM v3_connectors WHERE id=$1 AND environment_id=$2 FOR UPDATE", r.PathValue("id"), env[0]))
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if !s.authorizeLockedWriteTx(w, r, tx, env[0]) {
		return
	}
	d, known := descriptors[c.Provider]
	if !known {
		httpapi.Error(w, 422, "invalid_connector", "Connector type is not supported")
		return
	}
	if !validRegisteredTools(c, d) {
		httpapi.Error(w, 409, "tool_catalog_changed", "Stored connector tools do not match the current catalog")
		return
	}
	seen := map[string]bool{}
	requested := make(map[string]bool, len(in.Tools))
	for _, item := range in.Tools {
		if item.Enabled == nil || seen[item.Name] || !knownTool(d, item.Name) {
			httpapi.Error(w, 422, "invalid_tools", "Choose distinct supported read-only tools")
			return
		}
		seen[item.Name] = true
		if *item.Enabled && (!c.Enabled || !s.credentialBound(c, c.CredentialRef)) {
			httpapi.Error(w, 409, "connector_disabled", "Complete connector credentials and scope before enabling tools")
			return
		}
		requested[item.Name] = *item.Enabled
	}
	updated := make([]tool, 0, len(c.Tools)+len(requested))
	for _, item := range c.Tools {
		if enabled, ok := requested[item.Name]; ok {
			item.Enabled = enabled
			delete(requested, item.Name)
		}
		updated = append(updated, item)
	}
	for _, item := range in.Tools {
		if enabled, ok := requested[item.Name]; ok {
			updated = append(updated, tool{Name: item.Name, Desc: toolCopy(item.Name, "en"), Enabled: enabled})
		}
	}
	raw, _ := json.Marshal(updated)
	_, err = tx.Exec(r.Context(), "UPDATE v3_connectors SET tools=$1,updated_at=now() WHERE id=$2 AND environment_id=$3", raw, c.ID, c.EnvironmentID)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if err = audit(r.Context(), tx, "connector.tools.updated", c.ID, map[string]any{"environment": c.EnvironmentID, "tools": updated}); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Could not audit connector change")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"ok": true, "tools": displayTools(updated, r.Header.Get("Accept-Language"))})
}

func audit(ctx context.Context, tx pgx.Tx, action, target string, changes any) error {
	return auditOutcome(ctx, tx, action, target, "success", changes)
}

func auditOutcome(ctx context.Context, tx pgx.Tx, action, target, outcome string, changes any) error {
	raw, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,$4,$5)", store.Principal(ctx).ID, action, target, outcome, raw)
	return err
}

func (s *Service) test(w http.ResponseWriter, r *http.Request) {
	env, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	c, err := s.lookup(r.Context(), r.PathValue("id"), env)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if !c.Enabled {
		httpapi.Error(w, 409, "connector_disabled", "Complete connector credentials and scope before testing")
		return
	}
	if !s.credentialBound(c, c.CredentialRef) {
		httpapi.Error(w, 503, "credential_not_authorized", "Connector credential has no server-side binding")
		return
	}
	probeCtx := WithCredentialUse(r.Context(), CredentialUse{
		Tier: s.Tier, EnvironmentID: c.EnvironmentID, ConnectorID: c.ID,
		Provider: c.Provider, Endpoint: c.Endpoint,
	})
	if c.AuthMethod == "basic" {
		probeCtx = WithBasicUsername(probeCtx, s.connectorUsername(probeCtx, c.ID, c.EnvironmentID))
	}
	result, err := s.Prober.Check(probeCtx, c.Provider, c.Endpoint, c.AuthMethod, c.CredentialRef)
	if errors.Is(err, ErrCredentialUnavailable) {
		httpapi.Error(w, 503, "credential_unavailable", "Vault credential is unavailable")
		return
	}
	if err != nil {
		httpapi.Error(w, 422, "invalid_connector", "Connector settings cannot be tested")
		return
	}
	status := "error"
	if result.OK {
		status = "ok"
	}
	writeCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(writeCtx)
	tx, err := s.DB.Begin(writeCtx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer rollbackConnectorWrite(tx)
	current, err := scanConnector(tx.QueryRow(r.Context(), "SELECT "+columns+" FROM v3_connectors WHERE id=$1 AND environment_id=$2 FOR UPDATE", c.ID, c.EnvironmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "connector_changed", "Connector scope changed during connection check")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if !s.authorizeWriteTx(w, r, tx, c.EnvironmentID) {
		return
	}
	if !s.authorizeLockedWriteTx(w, r, tx, c.EnvironmentID) {
		return
	}
	if !current.Enabled || current.Revision != c.Revision || current.Provider != c.Provider || current.Name != c.Name ||
		current.Category != c.Category || current.Endpoint != c.Endpoint ||
		current.AuthMethod != c.AuthMethod || current.CredentialRef != c.CredentialRef ||
		current.Webhook != c.Webhook || !reflect.DeepEqual(current.Tools, c.Tools) {
		httpapi.Error(w, 409, "connector_changed", "Connector configuration changed during connection check")
		return
	}
	_, err = tx.Exec(r.Context(), "UPDATE v3_connectors SET status=$1,last_checked_at=now(),last_latency_ms=$2,last_error_code=$3,updated_at=now() WHERE id=$4 AND environment_id=$5", status, result.LatencyMS, result.ErrorCode, c.ID, c.EnvironmentID)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	outcome := "failure"
	if result.OK {
		outcome = "success"
	}
	if err = auditOutcome(r.Context(), tx, "connector.connection.tested", c.ID, outcome, map[string]any{"environment": c.EnvironmentID, "ok": result.OK, "latency_ms": result.LatencyMS, "error_code": result.ErrorCode}); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Could not audit connection check")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if !result.OK {
		httpapi.WriteJSON(w, 503, map[string]any{"error": map[string]any{"code": "connector_test_failed", "message": "Connector connection check failed", "details": map[string]any{"reason": result.ErrorCode, "latency_ms": result.LatencyMS}}})
		return
	}
	httpapi.WriteJSON(w, 200, result)
}
