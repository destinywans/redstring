// Package v3webhooks reads the registered inbound webhook sources and their
// accepted receipts. It does not provision hooks or replay events.
package v3webhooks

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/delivery"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB           *pgxpool.Pool
	Access       Access
	AlertSources []operations.Source
	GitLabs      []delivery.GitLabConfig
}

func New(db *pgxpool.Pool, access Access, alerts []operations.Source, gitlabs []delivery.GitLabConfig) *Service {
	return &Service{DB: db, Access: access, AlertSources: alerts, GitLabs: gitlabs}
}

func (s *Service) Register(api *httpapi.Server) {
	const pattern = "GET /api/v1/webhooks"
	api.Register(pattern, "viewer", s.List)
	api.MarkV3ObjectRoute(pattern, "connectors", "view")
}

type inbound struct {
	ID     string `json:"id"`
	Brand  string `json:"brand"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Verify string `json:"verify"`
	Count  string `json:"count"`
	Last   string `json:"last"`
	Status string `json:"status"`
}

type deliveryRecord struct {
	Time  string `json:"time"`
	Event string `json:"event"`
	Code  string `json:"code"`
	MS    string `json:"ms"`
}

type tokenRecord struct {
	Name    string   `json:"name"`
	Scopes  []string `json:"scopes"`
	Used    string   `json:"used"`
	Expires string   `json:"expires"`
}

type outboundRecord struct {
	Event  string `json:"event"`
	Target string `json:"target"`
}

type response struct {
	Inbound    []inbound        `json:"inbound"`
	Deliveries []deliveryRecord `json:"deliveries"`
	Tokens     []tokenRecord    `json:"tokens"`
	MCP        struct {
		Enabled bool     `json:"enabled"`
		URL     string   `json:"url"`
		Tools   []string `json:"tools"`
		Clients string   `json:"clients"`
	} `json:"mcp"`
	Outbound     []outboundRecord  `json:"outbound"`
	Availability map[string]string `json:"availability"`
}

func emptyResponse() response {
	out := response{
		Inbound: []inbound{}, Deliveries: []deliveryRecord{}, Tokens: []tokenRecord{},
		Outbound: []outboundRecord{},
		Availability: map[string]string{
			"inbound":         "configured_sources_only_health_unknown",
			"gitlab_receipts": "historical_application_scope_unavailable",
			"deliveries":      "source_specific_delivery_history_unavailable",
			"tokens":          "api_token_registry_unavailable",
			"mcp":             "outbound_mcp_service_unavailable",
			"outbound":        "outbound_webhook_delivery_disabled",
		},
	}
	out.MCP.Tools = []string{}
	return out
}

func (s *Service) List(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "webhook_registry_unavailable", "Webhook storage or policy is unavailable")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "webhook_registry_unavailable", "Webhook receipts are unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize the grant and mapping reads with V3 governance changes. Keep
	// READ COMMITTED so a wait on these locks cannot retain a stale snapshot.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			unavailable(w)
			return
		}
	}
	if !activeSession(w, r.Context(), tx, cookie.Value, user.ID) {
		return
	}
	out := emptyResponse()
	for _, source := range s.AlertSources {
		// A source may map many label sets to one application. Count each
		// authorized application/environment pair once.
		seen := map[string]bool{}
		var count int64
		var last *time.Time
		visible := false
		for _, mapping := range source.Mappings {
			key := mapping.ApplicationID + "\x00" + mapping.EnvironmentID
			if seen[key] {
				continue
			}
			seen[key] = true
			env, valid, err := activeMapping(r.Context(), tx, mapping.ApplicationID, mapping.EnvironmentID, scope.EnvironmentIDs)
			if err != nil {
				unavailable(w)
				return
			}
			if !valid {
				continue
			}
			allowed, err := s.allowed(r.Context(), tx, user, env, mapping.ApplicationID)
			if err != nil {
				unavailable(w)
				return
			}
			if !allowed {
				continue
			}
			visible = true
			var n int64
			var newest *time.Time
			err = tx.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE o.received_at >= now() - interval '24 hours'),max(o.received_at)
			 FROM ops_incidents i JOIN ops_alert_occurrences o ON o.incident_id=i.id
			 WHERE i.source_id=$1 AND i.application_id=$2 AND i.environment_id=$3`, source.ID, mapping.ApplicationID, mapping.EnvironmentID).Scan(&n, &newest)
			if err != nil {
				unavailable(w)
				return
			}
			count += n
			last = latest(last, newest)
		}
		if visible {
			out.Inbound = append(out.Inbound, inbound{ID: "alertmanager:" + source.ID, Brand: "prometheus", Name: source.ID,
				URL: "/api/v1/webhooks/alertmanager/" + source.ID, Verify: "Bearer", Count: strconv.FormatInt(count, 10),
				Last: lastAccepted(last), Status: "unknown"})
		}
	}
	for _, source := range s.GitLabs {
		seen := map[int64]bool{}
		visible := false
		for _, binding := range source.Bindings {
			if seen[binding.ProjectID] {
				continue
			}
			seen[binding.ProjectID] = true
			env, valid, err := activeMapping(r.Context(), tx, binding.ApplicationID, binding.EnvironmentID, scope.EnvironmentIDs)
			if err != nil {
				unavailable(w)
				return
			}
			if !valid {
				continue
			}
			allowed, err := s.allowed(r.Context(), tx, user, env, binding.ApplicationID)
			if err != nil {
				unavailable(w)
				return
			}
			if !allowed {
				continue
			}
			visible = true
		}
		if visible {
			name := source.Name
			if name == "" {
				name = source.ID
			}
			out.Inbound = append(out.Inbound, inbound{ID: "gitlab:" + source.ID, Brand: "gitlab", Name: name,
				URL: "/api/v1/webhooks/gitlab/" + source.ID, Verify: "X-Gitlab-Token", Count: "",
				Last: "", Status: "unknown"})
		}
	}
	sort.Slice(out.Inbound, func(i, j int) bool { return out.Inbound[i].ID < out.Inbound[j].ID })
	if !activeSession(w, r.Context(), tx, cookie.Value, user.ID) {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

func activeSession(w http.ResponseWriter, ctx context.Context, tx pgx.Tx, token, userID string) bool {
	err := store.LockActiveSession(ctx, tx, token, userID)
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return false
	}
	if err != nil {
		unavailable(w)
		return false
	}
	return true
}

func activeMapping(ctx context.Context, tx pgx.Tx, app, environmentID string, scope []string) (string, bool, error) {
	var env string
	err := tx.QueryRow(ctx, `SELECT e.key FROM application_environments e
	 JOIN applications a ON a.id=e.application_id AND NOT a.archived
	 JOIN v3_application_environments ve ON ve.application_id=e.application_id AND ve.environment_id=e.key
	 WHERE e.id=$1 AND e.application_id=$2 AND NOT e.archived AND e.key=ANY($3::text[])`, environmentID, app, scope).Scan(&env)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return env, err == nil, err
}

func (s *Service) allowed(ctx context.Context, tx pgx.Tx, user core.User, env, app string) (bool, error) {
	err := s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "connectors", Action: "view", EnvironmentID: env, ApplicationID: app})
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

func latest(a, b *time.Time) *time.Time {
	if b != nil && (a == nil || b.After(*a)) {
		return b
	}
	return a
}

func lastAccepted(v *time.Time) string {
	if v == nil {
		return ""
	}
	return "202 · " + v.UTC().Format(time.RFC3339)
}

func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "webhook_registry_unavailable", "Webhook receipts or authorization are unavailable")
}
