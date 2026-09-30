package v3canvas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type investigationReadTarget struct {
	application string
	environment string
	team        string
}

// The owner team is read from the persisted application, never the request.
func (s *Service) authorizeInvestigationRead(ctx context.Context, tx pgx.Tx, user core.User, target investigationReadTarget) error {
	policy := &rbac.V3Policy{Store: s.Store}
	err := policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
		Module: "canvas", Action: "view", EnvironmentID: target.environment, ApplicationID: target.application,
	})
	if errors.Is(err, store.ErrForbidden) && target.team != "" {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
			Module: "canvas", Action: "view", EnvironmentID: target.environment, TeamID: target.team,
		})
	}
	return err
}

// This auxiliary route exposes only the current persisted branch context and
// revision. It does not synthesize graph nodes or request model inference.
func (s *Service) readInvestigationNodeMessages(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	environments, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	defer tx.Rollback(ctx)
	if failure := s.canvasPrincipal(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	target, failure := s.canvasTarget(ctx, tx, r.PathValue("id"), false)
	if failure != nil {
		failure.write(w)
		return
	}
	if !slicesContains(environments, target.environment) {
		graphNotFound().write(w)
		return
	}
	if failure = s.canvasPermission(ctx, tx, httpapi.CurrentUser(r), target, "view"); failure != nil {
		failure.write(w)
		return
	}
	node := r.PathValue("node")
	var revision int64
	err = tx.QueryRow(ctx, `SELECT revision FROM branches WHERE id=$1 AND investigation_id=$2`, node, target.id).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		investigationReadError(w, 404)
		return
	}
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	var raw []byte
	// The SQL predicate bounds the context transferred to Go. PostgreSQL still
	// measures the stored JSONB value, so this does not bound database work.
	err = tx.QueryRow(ctx, `SELECT messages FROM context_revisions
 WHERE branch_id=$1 AND revision=$2 AND octet_length(messages::text)<=$3`,
		node, revision, maxNodeContextBytes).Scan(&raw)
	if err != nil || len(raw) > maxNodeContextBytes {
		investigationReadError(w, 503)
		return
	}
	var messages []json.RawMessage
	if err = json.Unmarshal(raw, &messages); err != nil || messages == nil {
		investigationReadError(w, 503)
		return
	}
	rows, err := tx.Query(ctx, `SELECT id,user_text,
 CASE WHEN state='running' AND lease_expires_at<=clock_timestamp() THEN 'failed' ELSE state END,
 CASE WHEN state='running' AND lease_expires_at<=clock_timestamp() THEN 'lease_expired' ELSE failure_code END,
 created_at FROM v3_investigation_node_replies
 WHERE branch_id=$1 AND state IN ('running','failed') ORDER BY created_at DESC,id DESC LIMIT $2`,
		node, maxInvestigationReplyAttempts+1)
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	attempts := []investigationReplyAttempt{}
	for rows.Next() {
		var attempt investigationReplyAttempt
		if err = rows.Scan(&attempt.ID, &attempt.Text, &attempt.State, &attempt.ErrorCode, &attempt.CreatedAt); err != nil {
			break
		}
		attempts = append(attempts, attempt)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	hasMoreAttempts := len(attempts) > maxInvestigationReplyAttempts
	if hasMoreAttempts {
		attempts = attempts[:maxInvestigationReplyAttempts]
	}
	if err = tx.Commit(ctx); err != nil {
		investigationReadError(w, 503)
		return
	}
	// RawMessage retains omitted legacy fields as omitted; decoding through
	// core.Message would turn absent created_at into a fabricated zero time.
	httpapi.WriteJSON(w, 200, map[string]any{"branch_id": node, "revision": revision, "messages": json.RawMessage(raw),
		"attempts": attempts, "attempts_has_more": hasMoreAttempts})
}

func investigationReadError(w http.ResponseWriter, status int) {
	switch status {
	case 404:
		httpapi.Error(w, 404, "not_found", "Investigation node not found")
	default:
		httpapi.Error(w, 503, "investigation_node_unavailable", "Investigation node unavailable")
	}
}

func slicesContains(items []string, item string) bool {
	for _, v := range items {
		if v == item {
			return true
		}
	}
	return false
}
