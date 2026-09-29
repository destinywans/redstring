// Package v3mcp exposes scoped MCP registration metadata. Discovery snapshots
// are untrusted observations; health, tool review and execution require
// separate V3-scoped trust decisions.
package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

// DiscoverFunc returns untrusted MCP tool declarations. The application wires
// the shared bounded protocol client and its private credential root.
type DiscoverFunc func(context.Context, aiextensions.MCPConfig) ([]aiextensions.ToolInfo, error)

type Service struct {
	DB            *pgxpool.Pool
	Access        Access
	DiscoverTools DiscoverFunc
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
		action  string
	}{
		{"GET /api/v1/mcp-servers", s.list, "view"},
		{"GET /api/v1/mcp-servers/{id}", s.detail, "view"},
		{"PATCH /api/v1/mcp-servers/{id}/tools/{name}", s.toolChange, "manage"},
		{"POST /api/v1/mcp-servers/{id}/review", s.review, "manage"},
		{"POST /api/v1/mcp-servers/{id}/discover", s.discover, "manage"},
		{"GET /api/v1/mcp-servers/{id}/review-candidate", s.reviewCandidate, "manage"},
		{"POST /api/v1/mcp-servers/{id}/review-version", s.reviewVersion, "manage"},
		{"PATCH /api/v1/mcp-servers/{id}/reviewed-tools/{name}", s.changeReviewedTool, "manage"},
		{"PUT /api/v1/mcp-servers/{id}/environment-binding", s.bindEnvironment, "manage"},
		{"DELETE /api/v1/mcp-servers/{id}/environment-binding", s.unbindEnvironment, "manage"},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3ObjectRoute(route.pattern, "connectors", route.action)
	}
}

type registration struct {
	ID            string
	ApplicationID string
	Application   string
	EnvironmentID string
	raw           []byte
	Record        aiextensions.Record
}

const registeredMCP = `SELECT e.id,e.application_id,a.name,b.environment_id,e.record
 FROM ai_extensions e
 JOIN applications a ON a.id=e.application_id AND NOT a.archived
 JOIN v3_mcp_environment_bindings b ON b.extension_id=e.id AND b.application_id=a.id
 JOIN v3_application_environments ae ON ae.application_id=a.id AND ae.environment_id=b.environment_id
 JOIN application_environments app_env ON app_env.application_id=a.id
  AND app_env.key=b.environment_id AND NOT app_env.archived
 WHERE e.kind='mcp' AND b.environment_id=ANY($1::text[])`

// Keep the work done inside one list request bounded even when most registered
// servers are hidden by object policy. The response remains all-or-error.
const maxListCandidates = 2000

func scan(row pgx.Row) (registration, error) {
	var out registration
	err := row.Scan(&out.ID, &out.ApplicationID, &out.Application, &out.EnvironmentID, &out.raw)
	if err != nil {
		return out, err
	}
	return out, nil
}

func (out *registration) validateRecord() error {
	if err := json.Unmarshal(out.raw, &out.Record); err != nil {
		return err
	}
	// Never trust identity fields from mutable JSON over the relational keys.
	if out.Record.ID != out.ID || out.Record.ApplicationID != out.ApplicationID || out.Record.Kind != "mcp" || out.Record.MCP == nil {
		return errors.New("inconsistent MCP registration")
	}
	for _, b := range out.Record.MCP.Bindings {
		if !b.ReadOnlyConfirmed {
			return errors.New("unreviewed MCP binding")
		}
	}
	return nil
}

func (s *Service) ready(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP storage or policy is unavailable")
		return nil, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	return scope.EnvironmentIDs, true
}

func (s *Service) allowedTx(r *http.Request, tx pgx.Tx, v registration, action string) error {
	return s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "connectors", Action: action,
		EnvironmentID: v.EnvironmentID, ApplicationID: v.ApplicationID,
	})
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	// Keep the registration and authorization reads in one snapshot. Materialize
	// each bounded page and close it before policy queries use the same connection.
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registrations are unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	items := make([]map[string]any, 0)
	lang := r.Header.Get("Accept-Language")
	seen := map[string]bool{}
	toolCount := 0
	candidates := 0
	for offset := 0; ; offset += 200 {
		rows, queryErr := tx.Query(r.Context(), registeredMCP+" ORDER BY e.updated_at DESC,e.id,b.environment_id LIMIT 200 OFFSET $2", envs, offset)
		if queryErr != nil {
			httpapi.Error(w, 503, "mcp_unavailable", "MCP registrations are unavailable")
			return
		}
		batch := make([]registration, 0, 200)
		for rows.Next() {
			v, scanErr := scan(rows)
			if scanErr != nil {
				queryErr = scanErr
				break
			}
			batch = append(batch, v)
		}
		if queryErr == nil {
			queryErr = rows.Err()
		}
		rows.Close()
		if queryErr != nil {
			httpapi.Error(w, 503, "mcp_unavailable", "MCP registrations are unavailable")
			return
		}
		if candidates+len(batch) > maxListCandidates {
			httpapi.Error(w, 413, "candidate_scan_limit", "MCP registration candidates exceed the list scan limit")
			return
		}
		candidates += len(batch)
		for _, v := range batch {
			if err := s.allowedTx(r, tx, v, "view"); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				httpapi.Error(w, 503, "authorization_unavailable", "MCP authorization is unavailable")
				return
			}
			if err := v.validateRecord(); err != nil {
				httpapi.Error(w, 503, "mcp_unavailable", "An accessible MCP registration is inconsistent")
				return
			}
			if seen[v.ID] {
				continue
			}
			if len(items) == 1000 {
				httpapi.Error(w, 413, "scope_too_large", "MCP registrations exceed the unpaged response limit")
				return
			}
			seen[v.ID] = true
			items = append(items, summary(v, lang))
			if v.Record.MCP != nil {
				toolCount += len(v.Record.MCP.Bindings)
			}
		}
		if len(batch) < 200 {
			break
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registrations are unavailable")
		return
	}
	// These are registration counts, not discovered tools, health or call volume.
	kpis := []map[string]any{
		{"id": "servers", "label": mcpLocalized(lang, "已绑定登记", "Bound registrations"), "value": strconv.Itoa(len(items)), "sub": mcpLocalized(lang, "仅统计当前可见的显式环境绑定", "Only explicit environment bindings visible to you"), "series": []int{}},
		{"id": "tools", "label": mcpLocalized(lang, "已登记只读绑定", "Registered read-only bindings"), "value": strconv.Itoa(toolCount), "sub": mcpLocalized(lang, "不表示 V3 已审核或可执行", "Not V3 reviewed or executable"), "series": []int{}},
	}
	httpapi.WriteJSON(w, 200, map[string]any{"kpis": kpis, "items": items, "complete_v3_dto": false,
		"unavailable_fields": []string{"unbound_legacy_registrations", "connection_health", "discovered_capabilities", "v3_tool_review", "agent_authorized_apps", "call_metrics", "pending_change"}})
}

func mcpLocalized(lang, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(lang), "zh") {
		return zh
	}
	return en
}

func summary(v registration, lang string) map[string]any {
	return map[string]any{
		"id": v.ID, "brand": "mcp", "name": v.Record.Title,
		"desc": mcpLocalized(lang, "应用 "+v.Application+" 已绑定环境 "+v.EnvironmentID, "Application "+v.Application+" has a binding in "+v.EnvironmentID), "transport": "Streamable HTTP",
		"caps":  mcpLocalized(lang, "MCP 能力未验证", "MCP capabilities unverified"),
		"trust": map[string]string{"label": mcpLocalized(lang, "V3 待审核", "V3 review pending"), "tone": "warn"},
		// The owning application and environment binding do not grant Agent use.
		"apps":   []map[string]string{},
		"status": "pending", // No live connection check has been persisted.
		"last":   mcpLocalized(lang, "V3 调用记录未接入", "V3 call history unavailable"),
	}
}

func (s *Service) find(w http.ResponseWriter, r *http.Request, action string) (registration, bool) {
	envs, ok := s.ready(w, r)
	if !ok {
		return registration{}, false
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is unavailable")
		return registration{}, false
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), registeredMCP+" AND e.id=$2 ORDER BY b.environment_id", envs, r.PathValue("id"))
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is unavailable")
		return registration{}, false
	}
	registrations := make([]registration, 0, len(envs))
	for rows.Next() {
		v, scanErr := scan(rows)
		if scanErr != nil {
			err = scanErr
			break
		}
		registrations = append(registrations, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is unavailable")
		return registration{}, false
	}
	var selected registration
	for _, v := range registrations {
		if err = s.allowedTx(r, tx, v, action); err == nil {
			if err = v.validateRecord(); err != nil {
				httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is inconsistent")
				return registration{}, false
			}
			selected = v
			break
		}
		if !errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 503, "authorization_unavailable", "MCP authorization is unavailable")
			return registration{}, false
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "mcp_unavailable", "MCP registration is unavailable")
		return registration{}, false
	}
	if selected.ID == "" {
		httpapi.Error(w, 404, "not_found", "MCP server not found")
		return registration{}, false
	}
	return selected, true
}

func (s *Service) detail(w http.ResponseWriter, r *http.Request) {
	v, ok := s.find(w, r, "view")
	if !ok {
		return
	}
	lang := r.Header.Get("Accept-Language")
	result := summary(v, lang)
	// Legacy fixed-argument bindings and discovery observations are not V3
	// reviewed Agent grants. This DTO can expose no callable tool yet.
	result["version"] = mcpLocalized(lang, "服务版本未验证", "Server version unverified")
	result["counts"] = map[string]int{"tools": 0, "resources": 0, "prompts": 0} // DTO placeholders, not observed zero counts.
	result["tools"] = []any{}
	result["resources"] = []any{}
	result["prompts"] = []any{}
	result["calls"] = []any{}
	auth := mcpLocalized(lang, "未登记凭据引用", "No credential reference registered")
	if v.Record.MCP != nil && v.Record.MCP.CredentialRef != "" {
		auth = mcpLocalized(lang, "凭据引用已登记，未验证", "Credential reference registered, unverified")
	}
	result["trust_info"] = map[string]any{
		"readonly_check": mcpLocalized(lang, "V3 只读审核未完成", "V3 read-only review unavailable"),
		"scope":          []string{}, // The environment binding is not an Agent grant.
		"limit":          mcpLocalized(lang, "V3 调用尚不可用", "V3 calls unavailable"), "auth": auth,
	}
	result["complete_v3_dto"] = false
	result["unavailable_fields"] = []string{"connection_health", "server_version", "v3_tool_review", "agent_tool_grants", "agent_authorized_apps", "tool_count", "resource_count", "prompt_count", "discovered_resources", "discovered_prompts", "call_history", "call_limit"}
	httpapi.WriteJSON(w, 200, result)
}

func (s *Service) toolChange(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.find(w, r, "manage"); !ok {
		return
	}
	httpapi.Error(w, 503, "mcp_review_unavailable", "V3 tool review and environment-scoped trust records are not configured")
}

func (s *Service) review(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.find(w, r, "manage"); !ok {
		return
	}
	httpapi.Error(w, 503, "mcp_review_unavailable", "No verified discovery snapshot exists for review")
}
