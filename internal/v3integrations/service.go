// Package v3integrations stores V3 connector registrations and performs bounded,
// read-only connection checks. It never stores credentials or provisions webhooks.
package v3integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
)

type Service struct {
	DB     *pgxpool.Pool
	Tier   string // "production" or "nonprod"; one database per instance
	Prober Prober
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
	EnvironmentID string
	Provider      string
	Name          string
	Category      string
	Endpoint      string
	AuthMethod    string
	CredentialRef string
	Webhook       bool
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
		Enabled bool   `json:"enabled"`
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
	api.Register("GET /api/v1/connector-types/{type}", "viewer", s.connectorType)
	api.Register("GET /api/v1/connectors", "admin", s.list)
	api.Register("GET /api/v1/connectors/{id}", "admin", s.detail)
	api.Register("POST /api/v1/connectors", "admin", s.create)
	api.Register("PATCH /api/v1/connectors/{id}", "admin", s.patch)
	api.Register("POST /api/v1/connectors/{id}/test", "admin", s.test)
}

func (s *Service) connectorType(w http.ResponseWriter, r *http.Request) {
	d, ok := descriptors[r.PathValue("type")]
	if !ok {
		httpapi.Error(w, 404, "not_found", "Connector type is not available")
		return
	}
	lang := r.Header.Get("Accept-Language")
	auth := make([]map[string]string, 0, len(d.Methods))
	for _, method := range d.Methods {
		auth = append(auth, map[string]string{"id": method, "label": method, "desc": "Credential is read from a Vault reference when required"})
	}
	tools := make([]map[string]string, 0, len(d.Tools))
	for _, name := range d.Tools {
		tools = append(tools, map[string]string{"name": name, "desc": localized(lang, "只读", "Read-only")})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"brand": d.Provider, "name": d.Name, "kind": d.Kind, "version": "v3", "auth_methods": auth, "scopes": []any{}, "tools": tools, "webhook_url": ""})
}

func scanConnector(rows pgx.Row) (connector, error) {
	var c connector
	var raw []byte
	err := rows.Scan(&c.ID, &c.EnvironmentID, &c.Provider, &c.Name, &c.Category, &c.Endpoint, &c.AuthMethod, &c.CredentialRef, &c.Webhook, &raw, &c.Status, &c.Checked, &c.LatencyMS, &c.ErrorCode)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c.Tools); err != nil {
		return c, err
	}
	return c, nil
}

const columns = "id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,webhook_requested,tools,status,last_checked_at,last_latency_ms,last_error_code"

func (s *Service) lookup(ctx context.Context, id string, environments []string) (connector, error) {
	return scanConnector(s.DB.QueryRow(ctx, "SELECT "+columns+" FROM v3_connectors WHERE id=$1 AND environment_id = ANY($2::text[])", id, environments))
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
	rows, err := s.DB.Query(r.Context(), "SELECT "+columns+" FROM v3_connectors WHERE environment_id = ANY($1::text[]) ORDER BY name,id", env)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	counts := map[string]int{"ok": 0, "unverified": 0, "error": 0}
	categoryCounts := map[string]int{"all": 0}
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
			return
		}
		counts[c.Status]++
		categoryCounts["all"]++
		categoryCounts[c.Category]++
		if category != "all" && c.Category != category {
			continue
		}
		if query != "" && !searchtext.Match(c.Name+" "+c.Provider+" "+c.Endpoint, query) {
			continue
		}
		items = append(items, summary(c))
	}
	if rows.Err() != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	cats := make([]map[string]any, 0, len(categories))
	for _, id := range categories {
		cats = append(cats, map[string]any{"id": id, "label": categoryLabel(id, r.Header.Get("Accept-Language")), "count": categoryCounts[id]})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts, "categories": cats})
}

func summary(c connector) map[string]any {
	tags := make([]map[string]string, 0)
	for _, t := range c.Tools {
		if t.Enabled {
			tags = append(tags, map[string]string{"label": t.Name, "tone": "accent"})
		}
	}
	meta := "Connection not tested"
	if c.Status == "ok" && c.Checked != nil {
		meta = "Checked " + c.Checked.UTC().Format(time.RFC3339)
	}
	if c.Status == "error" {
		meta = "Connection check failed: " + c.ErrorCode
	}
	return map[string]any{"id": c.ID, "brand": c.Provider, "name": c.Name, "kind": descriptors[c.Provider].Kind, "category": c.Category, "status": c.Status, "tags": tags, "meta": meta}
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
	result := summary(c)
	checked := "Never checked"
	if c.Checked != nil {
		checked = c.Checked.UTC().Format(time.RFC3339)
	}
	result["scope"] = c.EnvironmentID
	result["address"] = c.Endpoint
	result["auth"] = map[string]string{"brand": "vault", "text": c.AuthMethod + " · Vault reference"}
	result["checked"] = checked
	result["services"] = []string{}
	result["queries"] = []string{}
	result["tools"] = c.Tools
	httpapi.WriteJSON(w, 200, result)
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
		if validateReference(in.CredentialRef) != nil {
			httpapi.Error(w, 422, "credential_ref_required", "A Vault credential reference is required")
			return
		}
	} else if in.CredentialRef != "" {
		httpapi.Error(w, 422, "invalid_connector", "Unauthenticated connectors do not take credentials")
		return
	}
	if in.Webhook {
		httpapi.Error(w, 422, "webhook_not_supported", "Webhook provisioning is not available for this connector type")
		return
	}
	seen := map[string]bool{}
	tools := make([]tool, 0, len(in.Tools))
	for _, name := range in.Tools {
		if seen[name] || !knownTool(d, name) {
			httpapi.Error(w, 422, "invalid_tools", "Choose distinct supported read-only tools")
			return
		}
		seen[name] = true
		tools = append(tools, tool{Name: name, Desc: "Read-only", Enabled: true})
	}
	raw, _ := json.Marshal(tools)
	id := store.ID()
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,tools) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, env[0], d.Provider, d.Name, d.Category, strings.TrimRight(strings.TrimSpace(in.URL), "/"), in.Auth, in.CredentialRef, raw)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			httpapi.Error(w, 409, "connector_exists", "This connector is already registered")
			return
		}
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if err = audit(r.Context(), tx, "connector.created", id, map[string]any{"environment": env[0], "provider": d.Provider, "endpoint": in.URL, "auth_method": in.Auth}); err != nil {
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
	c, err := s.lookup(r.Context(), r.PathValue("id"), env)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	d := descriptors[c.Provider]
	seen := map[string]bool{}
	updated := make([]tool, 0, len(in.Tools))
	for _, t := range in.Tools {
		if seen[t.Name] || !knownTool(d, t.Name) {
			httpapi.Error(w, 422, "invalid_tools", "Choose distinct supported read-only tools")
			return
		}
		seen[t.Name] = true
		updated = append(updated, tool{Name: t.Name, Desc: "Read-only", Enabled: t.Enabled})
	}
	raw, _ := json.Marshal(updated)
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
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
	httpapi.WriteJSON(w, 200, map[string]any{"ok": true, "tools": updated})
}

func audit(ctx context.Context, tx pgx.Tx, action, target string, changes any) error {
	raw, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',$4)", store.Principal(ctx).ID, action, target, raw)
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
	result, err := s.Prober.Check(r.Context(), c.Provider, c.Endpoint, c.AuthMethod, c.CredentialRef)
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
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), "UPDATE v3_connectors SET status=$1,last_checked_at=now(),last_latency_ms=$2,last_error_code=$3,updated_at=now() WHERE id=$4 AND environment_id=$5", status, result.LatencyMS, result.ErrorCode, c.ID, c.EnvironmentID)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if err = audit(r.Context(), tx, "connector.connection.tested", c.ID, map[string]any{"environment": c.EnvironmentID, "ok": result.OK, "latency_ms": result.LatencyMS, "error_code": result.ErrorCode}); err != nil {
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
