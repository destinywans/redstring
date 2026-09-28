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
	tx, err := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	defer tx.Rollback(ctx)

	var target investigationReadTarget
	var actualInvestigation string
	node := r.PathValue("node")
	err = tx.QueryRow(ctx, `SELECT b.investigation_id,i.application_id,ae.key,COALESCE(a.owner_team_id,'')
 FROM branches b JOIN investigations i ON i.id=b.investigation_id
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments ae ON ae.id=i.environment_id AND ae.application_id=i.application_id AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=i.application_id AND vae.environment_id=ae.key
 WHERE b.id=$1`, node).Scan(&actualInvestigation, &target.application, &target.environment, &target.team)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (actualInvestigation != r.PathValue("id") || !slicesContains(environments, target.environment)) {
		investigationReadError(w, 404)
		return
	}
	if err != nil {
		investigationReadError(w, 503)
		return
	}
	if err = s.authorizeInvestigationRead(ctx, tx, httpapi.CurrentUser(r), target); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			// Keep inaccessible object IDs indistinguishable from absent IDs.
			investigationReadError(w, 404)
		} else {
			investigationReadError(w, 503)
		}
		return
	}
	var revision int64
	var raw []byte
	// The SQL predicate bounds the context transferred to Go. PostgreSQL still
	// measures the stored JSONB value, so this does not bound database work.
	err = tx.QueryRow(ctx, `SELECT b.revision,r.messages FROM branches b JOIN context_revisions r
 ON r.branch_id=b.id AND r.revision=b.revision WHERE b.id=$1 AND octet_length(r.messages::text)<=$2`,
		node, maxNodeContextBytes).Scan(&revision, &raw)
	if err != nil || len(raw) > maxNodeContextBytes {
		investigationReadError(w, 503)
		return
	}
	var messages []json.RawMessage
	if err = json.Unmarshal(raw, &messages); err != nil || messages == nil {
		investigationReadError(w, 503)
		return
	}
	// RawMessage retains omitted legacy fields as omitted; decoding through
	// core.Message would turn absent created_at into a fabricated zero time.
	httpapi.WriteJSON(w, 200, map[string]any{"branch_id": node, "revision": revision, "messages": json.RawMessage(raw)})
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
