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
	CurrentDocuments(context.Context, string, []string, []string) ([]document, error)
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
	httpapi.WriteJSON(w, 200, response{Items: items, Index: nil, Partial: true, Reason: "document_only_projection", Truncated: truncated})
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
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return []DocumentSource{}, false, nil
	}
	// Re-read only candidates with current document metadata, app mappings and
	// grants. Never emit the title or scope cached by the first query.
	docs, err = s.repository.CurrentDocuments(ctx, user.ID, environments, ids)
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
	return p.documents(ctx, userID, environments, nil)
}

func (p pgRepository) CurrentDocuments(ctx context.Context, userID string, environments, ids []string) ([]document, error) {
	if len(ids) == 0 {
		return []document{}, nil
	}
	return p.documents(ctx, userID, environments, ids)
}

func (p pgRepository) documents(ctx context.Context, userID string, environments, ids []string) ([]document, error) {
	if p.db == nil {
		return nil, errors.New("knowledge database unavailable")
	}
	rows, err := p.db.Query(ctx, `SELECT d.id,d.title,COALESCE(d.application_id,''),COALESCE(d.metadata->>'team_id',''),d.metadata->'environment_ids'
FROM knowledge_documents d
WHERE d.lifecycle='published'
  AND ($4::text[] IS NULL OR d.id=ANY($4::text[]))
  AND jsonb_typeof(d.metadata)='object'
  AND NOT (d.metadata ? 'source_registration_id')
  AND CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array'
    THEN jsonb_array_length(d.metadata->'environment_ids')>0
      AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(d.metadata->'environment_ids') env_item
        WHERE jsonb_typeof(env_item)<>'string' OR btrim(env_item #>> '{}')='')
    ELSE false END
  AND EXISTS (
    SELECT 1 FROM unnest($2::text[]) selected(environment_id)
    WHERE d.metadata->'environment_ids' ? selected.environment_id
      AND EXISTS (
        SELECT 1 FROM v3_effective_role_grants g
        WHERE g.user_id=$1 AND g.module='environment' AND g.action='view'
          AND ((g.scope_kind='global' AND g.scope_id='*')
            OR (g.scope_kind='environment' AND g.scope_id=selected.environment_id))
      )
      AND (
        (d.application_id IS NOT NULL AND EXISTS (
          SELECT 1 FROM applications a
          JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=selected.environment_id
          JOIN application_environments ae ON ae.application_id=a.id AND ae.key=selected.environment_id AND NOT ae.archived
          WHERE a.id=d.application_id AND NOT a.archived
            AND EXISTS (SELECT 1 FROM v3_effective_role_grants kg
              WHERE kg.user_id=$1 AND kg.module='knowledge' AND kg.action='view'
                AND ((kg.scope_kind='global' AND kg.scope_id='*')
                  OR (kg.scope_kind='environment' AND kg.scope_id=selected.environment_id)
                  OR (kg.scope_kind='application' AND kg.scope_id=a.id)))
            AND (NOT (d.metadata ? 'team_id') OR (
              jsonb_typeof(d.metadata->'team_id')='string'
              AND d.metadata->>'team_id'<>''
              AND d.metadata->>'team_id'=a.owner_team_id
              AND EXISTS (SELECT 1 FROM v3_effective_role_grants tg
                WHERE tg.user_id=$1 AND tg.module='knowledge' AND tg.action='view'
                  AND ((tg.scope_kind='global' AND tg.scope_id='*')
                    OR (tg.scope_kind='environment' AND tg.scope_id=selected.environment_id)
                    OR (tg.scope_kind='team' AND tg.scope_id=a.owner_team_id)))))
        ))
        OR (d.application_id IS NULL AND d.metadata->>'visibility'='workspace'
          AND NOT (d.metadata ? 'team_id')
          AND EXISTS (SELECT 1 FROM v3_effective_role_grants wg
            WHERE wg.user_id=$1 AND wg.module='knowledge' AND wg.action='view'
              AND ((wg.scope_kind='global' AND wg.scope_id='*')
                OR (wg.scope_kind='environment' AND wg.scope_id=selected.environment_id))))
      )
  )
ORDER BY d.created_at DESC,d.id LIMIT $3`, userID, environments, maxCandidates, ids)
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
