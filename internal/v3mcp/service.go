// Package v3mcp exposes the metadata that can be proven from legacy MCP
// registrations. Discovery, health, tool review and write-tool approval need
// a V3-scoped trust record before they can be exposed as successful actions.
package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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

type Service struct {
	DB     *pgxpool.Pool
	Access Access
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
	seen := map[string]bool{}
	toolCount := 0
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
			items = append(items, summary(v))
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
		{"id": "servers", "label": "已绑定服务器", "value": strconv.Itoa(len(items)), "sub": "未绑定环境的旧登记不展示", "series": []int{}},
		{"id": "tools", "label": "已配置只读工具", "value": strconv.Itoa(toolCount), "sub": "未执行实时发现", "series": []int{}},
	}
	httpapi.WriteJSON(w, 200, map[string]any{"kpis": kpis, "items": items, "complete_v3_dto": false,
		"unavailable_fields": []string{"unbound_legacy_registrations", "connection_health", "discovered_tools", "call_metrics", "pending_change"}})
}

func summary(v registration) map[string]any {
	bindings := 0
	if v.Record.MCP != nil {
		bindings = len(v.Record.MCP.Bindings)
	}
	return map[string]any{
		"id": v.ID, "brand": "mcp", "name": v.Record.Title,
		"desc": "已登记的应用 MCP 扩展", "transport": "Streamable HTTP",
		"caps":   strconv.Itoa(bindings) + " 个已配置只读工具",
		"trust":  map[string]string{"label": "待验证", "tone": "warn"},
		"apps":   []map[string]string{{"label": v.Application}},
		"status": "pending", // No live connection check has been persisted.
		"last":   v.Record.UpdatedAt.UTC().Format(time.RFC3339),
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
	result := summary(v)
	tools := make([]map[string]any, 0)
	if v.Record.MCP != nil {
		for _, b := range v.Record.MCP.Bindings {
			tools = append(tools, map[string]any{
				"name": b.Name, "desc": b.Description, "write": false,
				"enabled": v.Record.Enabled, "calls": "未接入",
			})
		}
	}
	result["version"] = strconv.Itoa(v.Record.Version)
	result["counts"] = map[string]int{"tools": len(tools), "resources": 0, "prompts": 0}
	result["tools"] = tools
	result["resources"] = []any{}
	result["prompts"] = []any{}
	result["calls"] = []any{}
	result["trust_info"] = map[string]any{
		"readonly_check": "管理员登记只读固定参数；实时 readOnlyHint 尚未验证",
		"scope":          []string{v.Application + " · " + v.EnvironmentID},
		"limit":          "单应用最多 8 个已登记工具", "auth": "凭据不在此接口展示",
	}
	result["complete_v3_dto"] = false
	result["unavailable_fields"] = []string{"connection_health", "discovered_resources", "discovered_prompts", "call_history", "version_review"}
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
