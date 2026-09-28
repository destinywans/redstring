// Package v3knowledgesources exposes the document lineage that is actually
// persisted today. A connector/sync source registry does not yet exist.
package v3knowledgesources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

const maxDocuments = 200
const maxCandidates = 2001

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

type document struct {
	ID, Title, ApplicationID, TeamID string
	EnvironmentIDs                   []string
}

type repository interface {
	Documents(context.Context, string, []string) ([]document, error)
}

type Service struct {
	repository repository
	access     Access
}

func New(db *pgxpool.Pool, access Access) *Service {
	return &Service{repository: pgRepository{db}, access: access}
}

func (s *Service) Register(api *httpapi.Server) {
	const route = "GET /api/v1/knowledge-sources/observed-documents"
	api.Register(route, "viewer", s.list)
	api.MarkV3ObjectRoute(route, "knowledge", "view")
}

// DocumentSource is a partial projection, one item per authorized published
// document. A document is not claimed to be a configured external connector.
// Null fields explicitly distinguish unknown sync/index state from success.
type DocumentSource struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	Docs   *int    `json:"docs"`
	Sync   *string `json:"sync"`
	Chunks *string `json:"chunks"`
	Last   *string `json:"last"`
	Status *string `json:"status"`
}

type response struct {
	Items     []DocumentSource `json:"items"`
	Index     any              `json:"index"`
	Partial   bool             `json:"partial"`
	Reason    string           `json:"reason"`
	Truncated bool             `json:"truncated"`
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.repository == nil || s.access == nil {
		httpapi.Error(w, 503, "knowledge_sources_unavailable", "Knowledge storage or policy is unavailable")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	items, truncated, err := s.visible(r.Context(), user, scope.EnvironmentIDs)
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Knowledge access denied")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "knowledge_sources_unavailable", "Knowledge sources unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, response{Items: items, Index: nil, Partial: true, Reason: "source_registry_unavailable", Truncated: truncated})
}

func (s *Service) visible(ctx context.Context, user core.User, environments []string) ([]DocumentSource, bool, error) {
	if len(environments) == 0 || user.ID == "" || s == nil || s.repository == nil || s.access == nil {
		return nil, false, store.ErrForbidden
	}
	// Object-scoped grants can be application-only. Query the already validated
	// V3 environment set, then authorize each persisted document separately.
	docs, err := s.repository.Documents(ctx, user.ID, environments)
	if err != nil {
		return nil, false, err
	}
	items := make([]DocumentSource, 0, min(len(docs), maxDocuments))
	for _, d := range docs {
		// Historical documents without explicit environment attribution are
		// ambiguous in V3 and must not cross an environment boundary.
		if d.ID == "" || d.Title == "" || len(d.EnvironmentIDs) == 0 {
			continue
		}
		for _, env := range environments {
			if !has(d.EnvironmentIDs, env) {
				continue
			}
			err = s.access.Authorize(ctx, user, rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: env, ApplicationID: d.ApplicationID})
			if err == nil && d.TeamID != "" {
				err = s.access.Authorize(ctx, user, rbac.V3Decision{Module: "knowledge", Action: "view", EnvironmentID: env, TeamID: d.TeamID})
			}
			if err == nil {
				if len(items) == maxDocuments {
					return items, true, nil
				}
				items = append(items, DocumentSource{ID: d.ID, Name: d.Title, Kind: "document"})
				break
			}
			if !errors.Is(err, store.ErrForbidden) {
				return nil, false, err
			}
		}
	}
	// Truncation reflects authorized items only. The response is always marked
	// partial because this bounded projection is not a source registry.
	return items, false, nil
}

func has(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type pgRepository struct{ db *pgxpool.Pool }

func (p pgRepository) Documents(ctx context.Context, userID string, environments []string) ([]document, error) {
	if p.db == nil {
		return nil, errors.New("knowledge database unavailable")
	}
	rows, err := p.db.Query(ctx, `SELECT id,title,COALESCE(application_id,''),COALESCE(metadata->>'team_id',''),metadata->'environment_ids'
FROM knowledge_documents
WHERE lifecycle='published'
  AND jsonb_typeof(metadata->'environment_ids')='array'
  AND EXISTS (
    SELECT 1 FROM unnest($2::text[]) selected(environment_id)
    WHERE metadata->'environment_ids' ? selected.environment_id
      AND EXISTS (
        SELECT 1 FROM v3_effective_role_grants g
        WHERE g.user_id=$1 AND g.module='environment' AND g.action='view'
          AND ((g.scope_kind='global' AND g.scope_id='*')
            OR (g.scope_kind='environment' AND g.scope_id=selected.environment_id))
      )
      AND (application_id IS NULL OR EXISTS (
        SELECT 1 FROM v3_application_environments ae
        WHERE ae.application_id=knowledge_documents.application_id
          AND ae.environment_id=selected.environment_id
      ))
      AND EXISTS (
        SELECT 1 FROM v3_effective_role_grants g
        WHERE g.user_id=$1 AND g.module='knowledge' AND g.action='view'
          AND ((g.scope_kind='global' AND g.scope_id='*')
            OR (g.scope_kind='environment' AND g.scope_id=selected.environment_id)
            OR (application_id IS NOT NULL AND g.scope_kind='application' AND g.scope_id=application_id))
      )
      AND (COALESCE(metadata->>'team_id','')='' OR EXISTS (
        SELECT 1 FROM v3_effective_role_grants g
        WHERE g.user_id=$1 AND g.module='knowledge' AND g.action='view'
          AND ((g.scope_kind='global' AND g.scope_id='*')
            OR (g.scope_kind='environment' AND g.scope_id=selected.environment_id)
            OR (g.scope_kind='team' AND g.scope_id=metadata->>'team_id'))
      ))
  )
ORDER BY created_at DESC,id LIMIT $3`, userID, environments, maxCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs := make([]document, 0)
	for rows.Next() {
		var d document
		var raw []byte
		if err = rows.Scan(&d.ID, &d.Title, &d.ApplicationID, &d.TeamID, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &d.EnvironmentIDs); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}
