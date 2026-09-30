package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/wans/aiops-canvas/internal/inference"
)

// v3VisibleDocumentSQL is deliberately stricter than historical knowledge
// visibility. $1 is the current user, $2 the exact application ID (or empty
// for an application-less workspace document), and $3 a concrete environment.
// Every V3 retrieval path applies this predicate before ranking and LIMIT.
// Source registrations remain excluded until their live observation and
// registration provenance can be checked in the same read snapshot.
const v3VisibleDocumentSQL = `
 d.lifecycle='published'
 AND jsonb_typeof(d.metadata)='object'
 AND NOT (d.metadata ? 'source_registration_id')
 AND CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array'
   THEN jsonb_array_length(d.metadata->'environment_ids')>0
    AND (d.metadata->'environment_ids') ? $3
    AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(d.metadata->'environment_ids') env_item
      WHERE jsonb_typeof(env_item)<>'string' OR btrim(env_item #>> '{}')='')
   ELSE false END
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants eg
   WHERE eg.user_id=$1 AND eg.module='environment' AND eg.action='view'
   AND ((eg.scope_kind='global' AND eg.scope_id='*')
     OR (eg.scope_kind='environment' AND eg.scope_id=$3)))
 AND (
   ($2<>'' AND d.application_id=$2 AND EXISTS (
     SELECT 1 FROM applications a
     JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=$3
     JOIN application_environments ae ON ae.application_id=a.id AND ae.key=$3 AND NOT ae.archived
     WHERE a.id=$2 AND NOT a.archived
       AND EXISTS (SELECT 1 FROM v3_effective_role_grants kg
         WHERE kg.user_id=$1 AND kg.module='knowledge' AND kg.action='view'
		 AND ((kg.scope_kind='global' AND kg.scope_id='*')
		   OR (kg.scope_kind='environment' AND kg.scope_id=$3)
		   OR (kg.scope_kind='application' AND kg.scope_id=a.id)))
       AND (NOT (d.metadata ? 'team_id') OR (
         jsonb_typeof(d.metadata->'team_id')='string'
         AND d.metadata->>'team_id'<>''
         AND d.metadata->>'team_id'=a.owner_team_id
         AND EXISTS (SELECT 1 FROM v3_effective_role_grants tg
           WHERE tg.user_id=$1 AND tg.module='knowledge' AND tg.action='view'
           AND ((tg.scope_kind='global' AND tg.scope_id='*')
             OR (tg.scope_kind='environment' AND tg.scope_id=$3)
             OR (tg.scope_kind='team' AND tg.scope_id=a.owner_team_id)))))
   ))
   OR ($2='' AND d.application_id IS NULL AND d.metadata->>'visibility'='workspace'
     AND NOT (d.metadata ? 'team_id')
     AND EXISTS (SELECT 1 FROM v3_effective_role_grants wg
       WHERE wg.user_id=$1 AND wg.module='knowledge' AND wg.action='view'
       AND ((wg.scope_kind='global' AND wg.scope_id='*')
         OR (wg.scope_kind='environment' AND wg.scope_id=$3))))
 )`

func validV3Scope(userID, app, env string, limit int) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(env) == "" || env == "all" || strings.TrimSpace(app) != app || limit < 1 || limit > 80 {
		return fmt.Errorf("%w: invalid V3 retrieval scope or limit", ErrInvalid)
	}
	return nil
}

// V3KeywordSearch returns only documents whose current metadata, application
// mapping and effective grants admit the actor in this exact environment.
func (s *PGRepository) V3KeywordSearch(ctx context.Context, userID, app, env, query string, limit int) ([]Hit, error) {
	if err := validV3Scope(userID, app, env, limit); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 8000 {
		return nil, fmt.Errorf("%w: invalid V3 keyword query", ErrInvalid)
	}
	terms := queryTerms(query)
	rows, err := s.DB.Query(ctx, `SELECT d.id,COALESCE(d.application_id,''),d.title,d.source,d.version,d.body,d.metadata
FROM knowledge_documents d WHERE `+v3VisibleDocumentSQL+`
AND EXISTS (SELECT 1 FROM unnest($4::text[]) term
 WHERE aiops_search_match(d.title || ' ' || d.body || ' ' || COALESCE(d.metadata->'tags','[]'::jsonb)::text,term))
ORDER BY d.id`, userID, app, env, terms)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []Hit{}
	for rows.Next() {
		var d Document
		if err = rows.Scan(&d.ID, &d.ApplicationID, &d.Title, &d.Source, &d.Version, &d.Body, &d.Metadata); err != nil {
			return nil, err
		}
		for ordinal, chunk := range chunkText(d.Body) {
			score := keywordScore(query, terms, d.Title, strings.Join(d.Metadata.Tags, " "), chunk)
			if score > 0 {
				hits = append(hits, Hit{DocumentID: d.ID, ApplicationID: d.ApplicationID, Title: d.Title, Source: d.Source, Version: d.Version, Chunk: ordinal, Content: chunk, Similarity: score})
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sortHits(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// V3SearchScoped applies the identical predicate to vector candidates before
// cosine ranking. It does not silently fall back to the legacy search scope.
func (p PGVector) V3SearchScoped(ctx context.Context, userID, app, env string, desc inference.EmbeddingDescriptor, v []float32, limit int) ([]Hit, error) {
	if err := validV3Scope(userID, app, env, limit); err != nil {
		return nil, err
	}
	encoded, err := vector(v)
	if err != nil {
		return nil, err
	}
	rows, err := p.DB.Query(ctx, `WITH eligible AS MATERIALIZED (
 SELECT d.id,COALESCE(d.application_id,'') application_id,d.title,d.source,d.version,
 c.ordinal,c.content,c.embedding,g.id generation_id
 FROM knowledge_documents d
 JOIN knowledge_generations g ON g.id=d.active_generation
 JOIN knowledge_chunks c ON c.generation_id=g.id
 WHERE `+v3VisibleDocumentSQL+`
 AND g.document_version=d.version AND g.status='ready'
 AND g.provider=$4 AND g.model=$5 AND g.dimensions=$6
) SELECT id,application_id,title,source,version,ordinal,content,
 1-(embedding <=> $7::vector),generation_id
 FROM eligible ORDER BY embedding <=> $7::vector,id,ordinal LIMIT $8`,
		userID, app, env, desc.Provider, desc.Model, len(v), encoded, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if err = rows.Scan(&h.DocumentID, &h.ApplicationID, &h.Title, &h.Source, &h.Version, &h.Chunk, &h.Content, &h.Similarity, &h.GenerationID); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// V3VisibleDocumentIDs is the final read-time recheck after embedding and
// ranking work. Callers must discard every hit absent from the returned set.
func (s *PGRepository) V3VisibleDocumentIDs(ctx context.Context, userID, app, env string, ids []string) (map[string]int, error) {
	out := make(map[string]int)
	if err := validV3Scope(userID, app, env, 1); err != nil {
		return nil, err
	}
	if len(ids) > 500 {
		return nil, fmt.Errorf("%w: too many V3 document IDs", ErrInvalid)
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT d.id,d.version FROM knowledge_documents d
 WHERE d.id=ANY($4::text[]) AND `+v3VisibleDocumentSQL, userID, app, env, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var version int
		if err = rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		out[id] = version
	}
	return out, rows.Err()
}
