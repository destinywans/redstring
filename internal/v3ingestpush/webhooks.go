package v3ingestpush

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// WebhookView projects generic Push ingress and optionally injected native
// receiver registrations. User API tokens, public MCP serving and outbound
// delivery do not have a connected V3 registry here.
type webhookView struct {
	Inbound      []webhookInbound    `json:"inbound"`
	Deliveries   []webhookReceipt    `json:"deliveries"`
	Tokens       []any               `json:"tokens"`
	MCP          webhookMCP          `json:"mcp"`
	Outbound     []any               `json:"outbound"`
	Availability webhookAvailability `json:"availability"`
}

type webhookInbound struct {
	ID          string `json:"id"`
	SourceID    string `json:"source_id"`
	EventType   string `json:"event_type"`
	Brand       string `json:"brand"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Verify      string `json:"verify"`
	Count       string `json:"count"`
	Last        string `json:"last"`
	Status      string `json:"status"` // latest accepted/invalid receipt or unverified
	StatusBasis string `json:"status_basis"`
}

type webhookReceipt struct {
	Time  string `json:"time"`
	Event string `json:"event"`
	Code  string `json:"code"`
	MS    string `json:"ms"`
}

type webhookMCP struct {
	Enabled bool     `json:"enabled"`
	URL     string   `json:"url"`
	Tools   []string `json:"tools"`
	Clients string   `json:"clients"`
}

type webhookAvailability struct {
	Inbound       string `json:"inbound"`
	NativeInbound string `json:"native_inbound"`
	APITokens     string `json:"api_tokens"`
	PublicMCP     string `json:"public_mcp"`
	Outbound      string `json:"outbound"`
	ReceiptWindow string `json:"receipt_window"`
}

type webhookSourceEvent struct{ sourceID, environmentID, applicationID, eventType string }
type webhookReceiptSummary struct {
	count int64
	code  int
	at    time.Time
}

func (s *Service) webhooks(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.scope(w, r, false)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		webhooksUnavailable(w)
		return
	}
	defer tx.Rollback(context.Background())
	// Match the Push view's governance and session fence. Application grants
	// are checked against the selected source before any receipt is queried.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			webhooksUnavailable(w)
			return
		}
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if err = store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
		webhookSessionError(w, err)
		return
	}
	allowed := make([]string, 0, len(envs))
	for _, env := range envs {
		err = s.policy.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(r), rbac.V3Decision{
			Module: "connectors", Action: "view", EnvironmentID: env,
		})
		if errors.Is(err, store.ErrForbidden) && len(envs) > 1 {
			continue
		}
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Connector permission required")
			return
		}
		if err != nil {
			webhooksUnavailable(w)
			return
		}
		allowed = append(allowed, env)
	}
	if len(allowed) == 0 {
		httpapi.Error(w, 403, "forbidden", "Connector permission required")
		return
	}
	sources, err := s.visibleSources(ctx, tx, httpapi.CurrentUser(r), allowed, "view")
	if err != nil {
		webhooksUnavailable(w)
		return
	}
	selected := make([]webhookSourceEvent, 0)
	for _, source := range sources {
		for _, eventType := range source.Types {
			selected = append(selected, webhookSourceEvent{source.ID, source.EnvironmentID, source.ApplicationID, eventType})
		}
	}
	view := webhookView{
		Inbound: []webhookInbound{}, Deliveries: []webhookReceipt{}, Tokens: []any{},
		MCP: webhookMCP{Tools: []string{}, Clients: "unknown"}, Outbound: []any{},
		Availability: webhookAvailability{Inbound: "no_visible_sources", NativeInbound: "not_connected",
			APITokens: "not_connected", PublicMCP: "not_connected", Outbound: "not_connected", ReceiptWindow: "past_24_hours"},
	}
	if len(selected) > 0 {
		view.Availability.Inbound = "configured"
		stats, deliveries, err := readWebhookReceipts(ctx, tx, selected)
		if err != nil {
			webhooksUnavailable(w)
			return
		}
		view.Deliveries = deliveries
		for _, entry := range selected {
			key := entry.sourceID + ":" + entry.eventType
			stat := stats[key]
			item := webhookInbound{ID: key, SourceID: entry.sourceID, EventType: entry.eventType,
				Brand: "generic", Name: entry.sourceID + " · " + entry.eventType,
				URL: "/api/v1/events/" + entry.eventType, Verify: "Bearer + HMAC-SHA256",
				Count: strconv.FormatInt(stat.count, 10), Last: "unknown", Status: "unverified", StatusBasis: "no_receipt_in_window"}
			if !stat.at.IsZero() {
				item.Last = fmt.Sprintf("%d · %s", stat.code, stat.at.UTC().Format(time.RFC3339))
				item.StatusBasis = "latest_receipt_in_window"
				if stat.code == http.StatusAccepted {
					item.Status = "ok"
				} else if stat.code == http.StatusUnprocessableEntity {
					item.Status = "error"
				}
			}
			view.Inbound = append(view.Inbound, item)
		}
	}
	if len(s.nativeAlerts) > 0 || len(s.nativeGitLabs) > 0 {
		view.Availability.NativeInbound = "no_visible_sources"
	}
	native, err := s.nativeWebhookView(ctx, tx, httpapi.CurrentUser(r), allowed)
	if err != nil {
		webhooksUnavailable(w)
		return
	}
	if len(native) > 0 {
		view.Inbound = append(view.Inbound, native...)
		view.Availability.Inbound = "configured"
		view.Availability.NativeInbound = "configured_health_unverified"
	}
	if err = store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
		webhookSessionError(w, err)
		return
	}
	// Retain the read transaction and its authorization locks until the
	// bounded response is written, as the other source-scoped Push view does.
	httpapi.WriteJSON(w, 200, view)
}

func readWebhookReceipts(ctx context.Context, tx pgx.Tx, entries []webhookSourceEvent) (map[string]webhookReceiptSummary, []webhookReceipt, error) {
	ids, envs, apps, types := make([]string, 0, len(entries)), make([]string, 0, len(entries)), make([]string, 0, len(entries)), make([]string, 0, len(entries))
	for _, entry := range entries {
		ids, envs, apps, types = append(ids, entry.sourceID), append(envs, entry.environmentID), append(apps, entry.applicationID), append(types, entry.eventType)
	}
	visible := `WITH visible AS (SELECT * FROM unnest($1::text[],$2::text[],$3::text[],$4::text[]) AS v(source_id,environment_id,application_id,event_type))`
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	stats := make(map[string]webhookReceiptSummary, len(entries))
	rows, err := tx.Query(ctx, visible+` SELECT p.source_id,p.event_type,count(*) FILTER (WHERE p.code=202)
 FROM v3_push_receipts p JOIN visible v
 ON (p.source_id,p.environment_id,p.application_id,p.event_type)=(v.source_id,v.environment_id,v.application_id,v.event_type)
 WHERE NOT p.synthetic AND p.created_at >= $5
 GROUP BY p.source_id,p.event_type`, ids, envs, apps, types, cutoff)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, typ string
		var count int64
		if err = rows.Scan(&id, &typ, &count); err != nil {
			break
		}
		stat := stats[id+":"+typ]
		stat.count = count
		stats[id+":"+typ] = stat
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = tx.Query(ctx, visible+` SELECT DISTINCT ON (p.source_id,p.event_type) p.source_id,p.event_type,p.code,p.created_at
 FROM v3_push_receipts p JOIN visible v
 ON (p.source_id,p.environment_id,p.application_id,p.event_type)=(v.source_id,v.environment_id,v.application_id,v.event_type)
 WHERE NOT p.synthetic AND p.created_at >= $5
 ORDER BY p.source_id,p.event_type,p.created_at DESC,p.id DESC`, ids, envs, apps, types, cutoff)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, typ string
		var code int
		var at time.Time
		if err = rows.Scan(&id, &typ, &code, &at); err != nil {
			break
		}
		stat := stats[id+":"+typ]
		stat.code, stat.at = code, at
		stats[id+":"+typ] = stat
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = tx.Query(ctx, visible+` SELECT p.created_at,p.source_id,p.event_type,p.result,p.code,p.latency_ms
 FROM v3_push_receipts p JOIN visible v
 ON (p.source_id,p.environment_id,p.application_id,p.event_type)=(v.source_id,v.environment_id,v.application_id,v.event_type)
 WHERE NOT p.synthetic AND p.created_at >= $5
 ORDER BY p.created_at DESC,p.id DESC LIMIT 30`, ids, envs, apps, types, cutoff)
	if err != nil {
		return nil, nil, err
	}
	deliveries := make([]webhookReceipt, 0, 30)
	for rows.Next() {
		var at time.Time
		var id, typ, result string
		var code, ms int
		if err = rows.Scan(&at, &id, &typ, &result, &code, &ms); err != nil {
			break
		}
		deliveries = append(deliveries, webhookReceipt{
			Time: at.UTC().Format(time.RFC3339), Event: id + " · " + typ + " · " + result,
			Code: strconv.Itoa(code), MS: strconv.Itoa(ms) + " ms",
		})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return stats, deliveries, err
}

func webhookSessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	} else {
		webhooksUnavailable(w)
	}
}

func webhooksUnavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "webhook_view_unavailable", "Webhook view unavailable")
}
