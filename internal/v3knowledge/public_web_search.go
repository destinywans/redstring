package v3knowledge

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/searchtext"
)

// Source bodies live outside knowledge_documents so the legacy document and
// export routes cannot read them. This predicate is repeated for candidate
// selection and the final, current-state check after other retrieval work.
const visiblePublicWebSQL = `
 d.application_id=$2 AND d.environment_id=$3
 AND octet_length(d.body) BETWEEN 1 AND 131072
 AND d.content_hash=encode(sha256(convert_to(d.body,'UTF8')),'hex')
 AND src.source_type='web' AND src.secret_ref='' AND src.inherit_acl
 AND src.schedule='manual' AND src.spaces='[]'::jsonb
 AND src.environment_id=d.environment_id AND src.application_id=d.application_id
 AND COALESCE(src.team_id,'')=COALESCE(d.team_id,'')
 AND src.name=d.title AND src.source_url=d.source_url
 AND d.item_hash=encode(sha256(convert_to(src.source_url,'UTF8')),'hex')
 AND rr.registration_id=src.id AND rr.environment_id=src.environment_id
 AND rr.reader_type='web' AND rr.observed_count=1
 AND rr.id=(SELECT latest.id FROM v3_knowledge_source_reads latest
   WHERE latest.registration_id=src.id ORDER BY latest.observed_at DESC,latest.id DESC LIMIT 1)
 AND o.item_hash=d.item_hash AND o.content_hash=d.content_hash
 AND o.environment_id=src.environment_id AND o.application_id=src.application_id
 AND COALESCE(o.team_id,'')=COALESCE(src.team_id,'')
 AND rev.body=d.body AND rev.content_hash=d.content_hash
 AND revision_read.registration_id=src.id AND revision_read.environment_id=src.environment_id
 AND revision_read.reader_type='web' AND revision_read.observed_count=1
 AND revision_observation.item_hash=d.item_hash
 AND revision_observation.content_hash=d.content_hash
 AND revision_observation.environment_id=src.environment_id
 AND revision_observation.application_id=src.application_id
 AND COALESCE(revision_observation.team_id,'')=COALESCE(src.team_id,'')
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

const publicWebFromSQL = ` FROM v3_public_web_documents d
 JOIN v3_knowledge_source_registrations src ON src.id=d.registration_id
 JOIN v3_knowledge_source_reads rr ON rr.id=d.last_read_id
 JOIN v3_knowledge_source_observations o ON o.read_id=rr.id AND o.item_hash=d.item_hash
 JOIN v3_public_web_document_revisions rev ON rev.registration_id=d.registration_id AND rev.version=d.version
 JOIN v3_knowledge_source_reads revision_read ON revision_read.id=rev.read_id
 JOIN v3_knowledge_source_observations revision_observation ON revision_observation.read_id=revision_read.id AND revision_observation.item_hash=d.item_hash
 JOIN applications a ON a.id=d.application_id WHERE `

var errPublicWebScopeTooLarge = errors.New("too many matching public Web sources")

// PostgreSQL text IDs cannot contain NUL. The in-memory prefix therefore
// cannot collide with a historical knowledge_documents ID during ranking.
const publicWebHitPrefix = "\x00public-web:"

type publicWebHit struct {
	hit                               knowledge.Hit
	contentHash, itemHash, lastReadID string
}

// The terms mirror the ordinary knowledge search's bounded term expansion.
// SQL remains the source of truth for pinyin and literal matching.
func publicWebTerms(query string) []string {
	normalized := strings.ToLower(strings.TrimSpace(query))
	terms := []string{normalized}
	seen := map[string]bool{normalized: true}
	add := func(v string) {
		if v != "" && !seen[v] && len(terms) < 128 {
			terms = append(terms, v)
			seen[v] = true
		}
	}
	for _, word := range strings.FieldsFunc(normalized, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:()[]{}，。；：", r)
	}) {
		add(word)
		runes := []rune(word)
		for i := 0; i+1 < len(runes); i++ {
			if unicode.Is(unicode.Han, runes[i]) && unicode.Is(unicode.Han, runes[i+1]) {
				add(string(runes[i : i+2]))
			}
		}
	}
	return terms
}

const publicWebCandidateLimit = 80

func (s *Service) publicWebKeywordSearch(ctx context.Context, userID, app, env, query string, limit, remaining int) ([]publicWebHit, int, error) {
	if app == "" { // A Web page must have an exact registered application.
		return nil, 0, nil
	}
	if remaining < 0 || remaining > publicWebCandidateLimit {
		return nil, 0, errPublicWebScopeTooLarge
	}
	terms := publicWebTerms(query)
	rows, err := s.DB.Query(ctx, `SELECT d.registration_id,d.title,d.body,d.version,d.content_hash,d.item_hash,d.last_read_id`+
		publicWebFromSQL+visiblePublicWebSQL+`
 AND EXISTS (SELECT 1 FROM unnest($4::text[]) term
   WHERE aiops_search_match(d.title || ' ' || d.body,term))
 ORDER BY d.registration_id LIMIT $5`, userID, app, env, terms, remaining+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	hits := make([]publicWebHit, 0)
	matched := 0
	for rows.Next() {
		var id, title, body string
		var version int
		var contentHash, itemHash, readID string
		if err := rows.Scan(&id, &title, &body, &version, &contentHash, &itemHash, &readID); err != nil {
			return nil, 0, err
		}
		matched++
		if matched > remaining {
			return nil, 0, errPublicWebScopeTooLarge
		}
		score := publicWebScore(title, body, query, terms)
		if score > 0 {
			hits = append(hits, publicWebHit{
				hit: knowledge.Hit{DocumentID: publicWebHitPrefix + id, ApplicationID: app,
					Title: title, Source: "web://" + id, Version: version, Chunk: 0,
					Content: publicWebExcerpt(body, query, terms), Similarity: score},
				contentHash: contentHash, itemHash: itemHash, lastReadID: readID,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// The legacy keyword index scores its own candidates before its limit.
	// This source-specific path keeps the same bounded top-K behavior.
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
	return hits, matched, nil
}

func publicWebScore(title, body, query string, terms []string) float64 {
	score := 0.0
	for _, term := range terms {
		if searchtext.Match(title, term) {
			score += 6
		}
		if searchtext.Match(body, term) {
			score++
		}
	}
	if searchtext.Match(body, query) {
		score += 8
	}
	return score
}

// A source result contains a short passage around a literal hit rather than
// the page body or an unrelated leading paragraph. Pinyin-only matches use
// the first bounded passage because they have no literal byte position.
func publicWebExcerpt(body, query string, terms []string) string {
	body = strings.TrimSpace(body)
	runes := []rune(body)
	if len(runes) <= 400 {
		return string(runes)
	}
	startAt := -1
	lowerBody := strings.ToLower(body)
	for _, term := range append([]string{query}, terms...) {
		if at := strings.Index(lowerBody, strings.ToLower(term)); at >= 0 {
			// Case folding can change UTF-8 byte lengths. Count runes in the
			// folded string, then slice the original by rune position.
			startAt = utf8.RuneCountInString(lowerBody[:at])
			break
		}
	}
	start := 0
	if startAt > 120 {
		start = startAt - 120
	}
	if start+400 > len(runes) {
		start = len(runes) - 400
	}
	return string(runes[start : start+400])
}

func (s *Service) visiblePublicWebHits(ctx context.Context, userID, app, env string, hits []publicWebHit) ([]knowledge.Hit, error) {
	if len(hits) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, strings.TrimPrefix(h.hit.DocumentID, publicWebHitPrefix))
	}
	rows, err := s.DB.Query(ctx, `SELECT d.registration_id,d.version,d.content_hash,d.item_hash,d.last_read_id`+
		publicWebFromSQL+visiblePublicWebSQL+` AND d.registration_id=ANY($4::text[])`, userID, app, env, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	visible := make(map[string]publicWebHit)
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
		id := strings.TrimPrefix(h.hit.DocumentID, publicWebHitPrefix)
		if v, ok := visible[id]; ok && v.hit.Version == h.hit.Version &&
			v.contentHash == h.contentHash && v.itemHash == h.itemHash && v.lastReadID == h.lastReadID {
			out = append(out, h.hit)
		}
	}
	return out, nil
}
