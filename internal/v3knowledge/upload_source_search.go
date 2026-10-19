package v3knowledge

import (
	"context"
	"strings"

	"github.com/destinywans/redstring/internal/knowledge"
)

// Synced upload sources are keyword-searchable from their latest read (PA-66).
// Their bodies live in v3_external_source_documents, outside the legacy
// document routes. The predicate repeats the evidence proof: the newest read
// of a still-registered manual upload source with an unchanged binding, one
// observed document whose hashes match its observation and the owner's
// current, unrevoked upload, plus the same environment, application, team and
// knowledge grants as public Web pages. Other bound external types need the
// server's reader binding check and are not searched here.
const visibleUploadSourceSQL = `
 d.application_id=$2 AND d.environment_id=$3
 AND s.source_type='upload' AND src.source_type='upload'
 AND src.inherit_acl AND src.schedule='manual' AND src.spaces='[]'::jsonb
 AND src.environment_id=d.environment_id AND src.application_id=d.application_id
 AND COALESCE(src.team_id,'')=COALESCE(d.team_id,'')
 AND s.registration_id=src.id AND s.environment_id=src.environment_id
 AND s.binding_fingerprint=src.binding_fingerprint AND s.binding_snapshot=src.binding_snapshot
 AND s.document_count=1 AND s.indexed_chunk_count IS NULL
 AND rr.registration_id=src.id AND rr.environment_id=src.environment_id
 AND rr.reader_type='upload' AND rr.observed_count=1
 AND rr.id=(SELECT latest.id FROM v3_knowledge_source_reads latest
   WHERE latest.registration_id=src.id ORDER BY latest.observed_at DESC,latest.id DESC LIMIT 1)
 AND octet_length(d.body) BETWEEN 1 AND 131072
 AND d.content_hash=encode(sha256(convert_to(d.body,'UTF8')),'hex')
 AND d.item_hash=encode(sha256(convert_to(d.external_id,'UTF8')),'hex')
 AND d.upload_id=d.external_id
 AND d.source_version=(src.binding_snapshot->>'initial_source_version')
 AND o.content_hash=d.content_hash AND o.environment_id=d.environment_id
 AND o.application_id=d.application_id AND COALESCE(o.team_id,'')=COALESCE(d.team_id,'')
 AND up.owner_id=src.created_by AND up.environment_id=d.environment_id AND up.kind='file'
 AND up.name=d.title AND up.mime=d.upload_mime AND up.size_bytes=octet_length(d.body)
 AND NOT EXISTS (SELECT 1 FROM v3_upload_revocations revoked WHERE revoked.id=up.id)
 AND encode(sha256(up.content),'hex')=d.content_hash
 AND NOT owner.disabled AND owner.deleted_at IS NULL AND owner.source<>'settings_demo'
 AND NOT a.archived AND (src.team_id IS NULL OR a.owner_team_id=src.team_id)
 AND EXISTS (SELECT 1 FROM v3_application_environments vae
   WHERE vae.application_id=a.id AND vae.environment_id=$3)
 AND EXISTS (SELECT 1 FROM application_environments ae
   WHERE ae.application_id=a.id AND ae.key=$3 AND NOT ae.archived)
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants eg
   WHERE eg.user_id=$1 AND eg.module='environment' AND eg.action='view'
   AND ((eg.scope_kind='global' AND eg.scope_id='*')
      OR (eg.scope_kind='environment' AND eg.scope_id=$3)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants kg
   WHERE kg.user_id=$1 AND kg.module='knowledge' AND kg.action='view'
   AND ((kg.scope_kind='global' AND kg.scope_id='*')
      OR (kg.scope_kind='environment' AND kg.scope_id=$3)
      OR (kg.scope_kind='application' AND kg.scope_id=a.id)))
 AND (src.team_id IS NULL OR EXISTS (SELECT 1 FROM v3_effective_role_grants tg
   WHERE tg.user_id=$1 AND tg.module='knowledge' AND tg.action='view'
   AND ((tg.scope_kind='global' AND tg.scope_id='*')
      OR (tg.scope_kind='environment' AND tg.scope_id=$3)
      OR (tg.scope_kind='team' AND tg.scope_id=src.team_id))))`

const uploadSourceFromSQL = ` FROM v3_external_source_documents d
 JOIN v3_external_source_syncs s ON s.read_id=d.read_id
 JOIN v3_knowledge_source_registrations src ON src.id=s.registration_id
 JOIN v3_knowledge_source_reads rr ON rr.id=s.read_id
 JOIN v3_knowledge_source_observations o ON o.read_id=s.read_id AND o.item_hash=d.item_hash
 JOIN v3_uploads up ON up.id=d.upload_id
 JOIN users owner ON owner.id=src.created_by
 JOIN applications a ON a.id=d.application_id WHERE `

// A NUL prefix cannot collide with a stored document ID during ranking.
const uploadSourceHitPrefix = "\x00upload-source:"

// uploadSourceKeywordSearch returns at most limit scored passages from the
// synced upload sources of one application and environment.
func (s *Service) uploadSourceKeywordSearch(ctx context.Context, userID, app, env, query string, limit int) ([]publicWebHit, error) {
	if app == "" {
		return nil, nil
	}
	terms := publicWebTerms(query)
	rows, err := s.DB.Query(ctx, `SELECT src.id,d.title,d.body,d.content_version,d.content_hash,d.item_hash,d.read_id`+
		uploadSourceFromSQL+visibleUploadSourceSQL+`
 AND EXISTS (SELECT 1 FROM unnest($4::text[]) term
   WHERE aiops_search_match(d.title || ' ' || d.body,term))
 ORDER BY src.id LIMIT $5`, userID, app, env, terms, publicWebCandidateLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := make([]publicWebHit, 0)
	for rows.Next() {
		var id, title, body, contentHash, itemHash, readID string
		var version int
		if err := rows.Scan(&id, &title, &body, &version, &contentHash, &itemHash, &readID); err != nil {
			return nil, err
		}
		if score := publicWebScore(title, body, query, terms); score > 0 {
			hits = append(hits, publicWebHit{
				hit: knowledge.Hit{DocumentID: uploadSourceHitPrefix + id, ApplicationID: app,
					Title: title, Source: "upload-source://" + id, Version: version, Chunk: 0,
					Content: publicWebExcerpt(body, query, terms), Similarity: score},
				contentHash: contentHash, itemHash: itemHash, lastReadID: readID,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := 0; i < len(hits); i++ {
		for j := i + 1; j < len(hits); j++ {
			if hits[j].hit.Similarity > hits[i].hit.Similarity {
				hits[i], hits[j] = hits[j], hits[i]
			}
		}
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// visibleUploadSourceHits repeats the predicate after all retrieval reads, so
// a revocation, resync or regrant in between drops the passage.
func (s *Service) visibleUploadSourceHits(ctx context.Context, userID, app, env string, hits []publicWebHit) ([]knowledge.Hit, error) {
	if len(hits) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, strings.TrimPrefix(h.hit.DocumentID, uploadSourceHitPrefix))
	}
	rows, err := s.DB.Query(ctx, `SELECT src.id,d.content_version,d.content_hash,d.item_hash,d.read_id`+
		uploadSourceFromSQL+visibleUploadSourceSQL+` AND src.id=ANY($4::text[])`, userID, app, env, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	visible := map[string]publicWebHit{}
	for rows.Next() {
		var id string
		var current publicWebHit
		if err := rows.Scan(&id, &current.hit.Version, &current.contentHash, &current.itemHash, &current.lastReadID); err != nil {
			return nil, err
		}
		visible[id] = current
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]knowledge.Hit, 0, len(hits))
	for _, h := range hits {
		id := strings.TrimPrefix(h.hit.DocumentID, uploadSourceHitPrefix)
		if v, ok := visible[id]; ok && v.hit.Version == h.hit.Version &&
			v.contentHash == h.contentHash && v.itemHash == h.itemHash && v.lastReadID == h.lastReadID {
			out = append(out, h.hit)
		}
	}
	return out, nil
}
