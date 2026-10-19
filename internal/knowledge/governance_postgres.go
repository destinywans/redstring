package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

const governanceMigration = `
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS lifecycle text NOT NULL DEFAULT 'published';
ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS metadata jsonb NOT NULL DEFAULT '{}';
ALTER TABLE knowledge_generations ADD COLUMN IF NOT EXISTS document_version integer;
UPDATE knowledge_generations g SET document_version=d.version FROM knowledge_documents d WHERE g.document_id=d.id AND g.document_version IS NULL;
CREATE TABLE IF NOT EXISTS knowledge_revisions(document_id text NOT NULL REFERENCES knowledge_documents(id),version integer NOT NULL,snapshot jsonb NOT NULL,created_by text NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(document_id,version));
CREATE TABLE IF NOT EXISTS knowledge_history(id bigserial PRIMARY KEY,document_id text NOT NULL REFERENCES knowledge_documents(id),version integer NOT NULL,actor_id text NOT NULL REFERENCES users(id),operation text NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
UPDATE knowledge_documents SET metadata=jsonb_build_object('maintainer_id',created_by,'actor_id',created_by,'origin','historical_unknown','visibility',CASE WHEN application_id IS NULL THEN 'workspace' ELSE 'application' END) WHERE metadata='{}'::jsonb;
INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by) SELECT id,version,jsonb_build_object('id',id,'application_id',coalesce(application_id,''),'title',title,'body',body,'source',source,'version',version,'content_hash',content_hash,'created_at',created_at,'lifecycle',lifecycle,'metadata',metadata,'investigation_id',coalesce(investigation_id,''),'evidence_ids',evidence_ids),created_by FROM knowledge_documents ON CONFLICT DO NOTHING;
ALTER TABLE knowledge_documents ALTER COLUMN lifecycle SET DEFAULT 'draft';
ALTER TABLE knowledge_drafts ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'human';
ALTER TABLE knowledge_drafts ADD COLUMN IF NOT EXISTS generation_run_id text NOT NULL DEFAULT '';
ALTER TABLE knowledge_drafts ADD COLUMN IF NOT EXISTS generation_model text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS knowledge_experience_generations(investigation_id text PRIMARY KEY REFERENCES investigations(id),status text NOT NULL,lease_until timestamptz NOT NULL,token text NOT NULL,draft_id text REFERENCES knowledge_drafts(id),last_error text NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now());
`

func saveRevision(ctx context.Context, tx pgx.Tx, d Document, user string) error {
	snapshot, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by) VALUES($1,$2,$3,$4)", d.ID, d.Version, snapshot, user)
	return err
}
func (s *PGRepository) ValidateMetadata(ctx context.Context, app string, m Metadata) error {
	var exists bool
	if err := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND disabled=false)", m.MaintainerID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: maintainer must be an active user", ErrInvalid)
	}
	for _, shared := range m.SharedApplicationIDs {
		ok, err := s.ApplicationExists(ctx, shared)
		if err != nil {
			return err
		}
		if !ok {
			return store.ErrNotFound
		}
	}
	// Environment/team checks are supplied by the application foundation schema.
	if s.ValidateScope != nil {
		return s.ValidateScope(ctx, app, m)
	}
	if len(m.EnvironmentIDs) > 0 || m.TeamID != "" {
		return fmt.Errorf("%w: environment/team validation unavailable", ErrInvalid)
	}
	return nil
}
func (s *PGRepository) UpdateDocument(ctx context.Context, id string, in UpdateInput, user string) (Document, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(ctx)
	d, err := scanDoc(tx.QueryRow(ctx, selectDoc+" WHERE d.id=$1 FOR UPDATE OF d", id))
	if err != nil {
		return d, err
	}
	if strings.HasPrefix(d.Source, "runbook://v3/") {
		return Document{}, store.ErrNotFound
	}
	var v3Runbook bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_runbooks WHERE document_id=$1)`, id).Scan(&v3Runbook); err != nil {
		return Document{}, err
	}
	if v3Runbook {
		return Document{}, store.ErrNotFound
	}
	if d.Version != in.ExpectedVersion {
		return d, store.ErrConflict
	}
	d.Version++
	d.Title = strings.TrimSpace(in.Title)
	d.Body = strings.TrimSpace(in.Body)
	d.Source = in.Source
	d.Metadata = in.Metadata
	d.Lifecycle = "draft"
	d.ContentHash = store.Hash(d.Body)
	d.GenerationID = 0
	d.Status = "unindexed"
	d.Error = ""
	meta, _ := json.Marshal(d.Metadata)
	_, err = tx.Exec(ctx, "UPDATE knowledge_documents SET title=$2,body=$3,source=$4,version=$5,content_hash=$6,metadata=$7,lifecycle='draft',active_generation=NULL WHERE id=$1", id, d.Title, d.Body, d.Source, d.Version, d.ContentHash, meta)
	if err != nil {
		return d, err
	}
	if err = saveRevision(ctx, tx, d, user); err != nil {
		return d, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,$2,$3,'revision.created')", id, d.Version, user); err != nil {
		return d, err
	}
	if err = knowledgeAudit(ctx, tx, d, user, "knowledge.revised"); err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}
func (s *PGRepository) TransitionDocument(ctx context.Context, id string, version int, state, user string) (Document, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Document{}, err
	}
	defer tx.Rollback(ctx)
	d, err := scanDoc(tx.QueryRow(ctx, selectDoc+" WHERE d.id=$1 FOR UPDATE OF d", id))
	if err != nil {
		return d, err
	}
	if strings.HasPrefix(d.Source, "runbook://v3/") {
		return Document{}, store.ErrNotFound
	}
	var v3Runbook bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_runbooks WHERE document_id=$1)`, id).Scan(&v3Runbook); err != nil {
		return Document{}, err
	}
	if v3Runbook {
		return Document{}, store.ErrNotFound
	}
	if d.Version != version {
		return d, store.ErrConflict
	}
	allowed := map[string]map[string]bool{"draft": {"review": true, "archived": true}, "review": {"draft": true, "published": true, "archived": true}, "published": {"archived": true}, "archived": {"draft": true}}
	if !allowed[d.Lifecycle][state] {
		return d, store.ErrConflict
	}
	if state == "published" && d.Metadata.MaintainerID == "" {
		return d, fmt.Errorf("%w: publication requires a maintainer", ErrInvalid)
	}
	_, err = tx.Exec(ctx, "UPDATE knowledge_documents SET lifecycle=$2 WHERE id=$1", id, state)
	if err != nil {
		return d, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,$2,$3,$4)", id, version, user, "lifecycle."+state)
	if err != nil {
		return d, err
	}
	d.Lifecycle = state
	if err = knowledgeAudit(ctx, tx, d, user, "knowledge."+state); err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}
func (s *PGRepository) Revisions(ctx context.Context, id string) ([]Document, error) {
	rows, err := s.DB.Query(ctx, "SELECT snapshot FROM knowledge_revisions WHERE document_id=$1 ORDER BY version DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		var d Document
		if err = rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *PGRepository) Revision(ctx context.Context, id string, version int) (Document, error) {
	var d Document
	err := s.DB.QueryRow(ctx, "SELECT snapshot FROM knowledge_revisions WHERE document_id=$1 AND version=$2", id, version).Scan(&d)
	if errors.Is(err, pgx.ErrNoRows) {
		err = store.ErrNotFound
	}
	return d, err
}
func (s *PGRepository) ReserveRevisionGeneration(ctx context.Context, id string, version int, desc inference.EmbeddingDescriptor) (int64, error) {
	var gen int64
	err := s.DB.QueryRow(ctx, "INSERT INTO knowledge_generations(document_id,provider,model,status,document_version) SELECT id,$2,$3,'building',version FROM knowledge_documents WHERE id=$1 AND version=$4 RETURNING id", id, desc.Provider, desc.Model, version).Scan(&gen)
	if errors.Is(err, pgx.ErrNoRows) {
		err = store.ErrConflict
	}
	return gen, err
}

// Keyword candidates are scoped before scoring. Matching substrings and Unicode
// bigrams works without requiring an unavailable Chinese text-search extension.
func (s *PGRepository) KeywordSearch(ctx context.Context, app, environment, query string, limit int) ([]Hit, error) {
	terms := queryTerms(query)
	// Legacy callers have no V3 team principal. Never expose team-scoped or
	// unverified source documents through this application-only path.
	rows, err := s.DB.Query(ctx, `SELECT d.id,coalesce(d.application_id,''),d.title,d.source,d.version,d.body,d.metadata FROM knowledge_documents d WHERE `+legacyV3RunbookFilter+` AND d.lifecycle='published' AND NOT (d.metadata ? 'source_registration_id') AND NOT (d.metadata ? 'team_id') AND (NOT (d.metadata ? 'environment_ids') OR CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array' THEN NOT EXISTS (SELECT 1 FROM jsonb_array_elements(d.metadata->'environment_ids') env_item WHERE jsonb_typeof(env_item)<>'string' OR btrim(env_item #>> '{}')='') ELSE false END) AND (d.application_id=$1 OR d.metadata->>'visibility'='workspace' OR COALESCE(d.metadata->'shared_application_ids','[]'::jsonb) ? $1) AND ($2='' OR COALESCE(d.metadata->'environment_ids','[]'::jsonb)='[]'::jsonb OR d.metadata->'environment_ids' ? $2) AND EXISTS(SELECT 1 FROM unnest($3::text[]) term WHERE aiops_search_match(d.title || ' ' || d.body || ' ' || coalesce(d.metadata->'tags','[]')::text,term)) ORDER BY d.id`, app, environment, terms)
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

// Filter before LIMIT, so documents outside the actor's applications cannot
// displace visible records from the first result page.
func (s *PGRepository) ListVisible(ctx context.Context, apps []string) ([]Document, error) {
	rows, err := s.DB.Query(ctx, selectDoc+" WHERE "+legacyV3RunbookFilter+" AND (d.application_id=ANY($1::text[]) OR d.metadata->>'visibility'='workspace' OR COALESCE(d.metadata->'shared_application_ids','[]'::jsonb) ?| $1::text[]) ORDER BY d.created_at DESC LIMIT 200", apps)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func knowledgeAudit(ctx context.Context, tx pgx.Tx, d Document, user, action string) error {
	changes, _ := json.Marshal(map[string]any{"version": d.Version, "lifecycle": d.Lifecycle, "visibility": d.Metadata.Visibility})
	_, err := tx.Exec(ctx, "INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user',$2,$3,$4,'succeeded',$5)", user, d.ApplicationID, action, d.ID, changes)
	return err
}
