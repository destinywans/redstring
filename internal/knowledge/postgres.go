package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository is the initial composition: source storage and generation vector
// writes share one PostgreSQL transaction. Read retrieval remains a VectorIndex.
// A future PG+Qdrant implementation must stage vectors under the reserved ID,
// confirm their availability, then CAS the active generation; it cannot copy this
// transaction assumption across databases.
type PGRepository struct {
	DB            *pgxpool.Pool
	ValidateScope func(context.Context, string, Metadata) error
}

var _ Repository = (*PGRepository)(nil)

func New(db *pgxpool.Pool, e inference.Embedder) *Service {
	return NewWithRepository(&PGRepository{DB: db}, e, PGVector{DB: db})
}
func (s *PGRepository) Migrate(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018992)"); e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector;
 CREATE TABLE IF NOT EXISTS knowledge_documents(id text PRIMARY KEY,application_id text REFERENCES applications(id),title text NOT NULL,body text NOT NULL,source text NOT NULL,version integer NOT NULL,content_hash text NOT NULL,created_by text NOT NULL REFERENCES users(id),active_generation bigint,created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS knowledge_generations(id bigserial PRIMARY KEY,document_id text NOT NULL REFERENCES knowledge_documents(id),provider text NOT NULL,model text NOT NULL,dimensions integer,status text NOT NULL CHECK(status IN('building','ready','failed')),error text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS knowledge_chunks(generation_id bigint NOT NULL REFERENCES knowledge_generations(id),ordinal integer NOT NULL,content text NOT NULL,embedding vector NOT NULL,PRIMARY KEY(generation_id,ordinal));
 ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS investigation_id text REFERENCES investigations(id);
 ALTER TABLE knowledge_documents ADD COLUMN IF NOT EXISTS evidence_ids jsonb NOT NULL DEFAULT '[]';
 CREATE TABLE IF NOT EXISTS knowledge_drafts(id text PRIMARY KEY,investigation_id text NOT NULL REFERENCES investigations(id),application_id text NOT NULL REFERENCES applications(id),title text NOT NULL,body text NOT NULL,evidence_ids jsonb NOT NULL,created_by text NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now(),confirmed_by text REFERENCES users(id),confirmed_at timestamptz,document_id text UNIQUE REFERENCES knowledge_documents(id),CHECK((document_id IS NULL AND confirmed_at IS NULL AND confirmed_by IS NULL) OR (document_id IS NOT NULL AND confirmed_at IS NOT NULL AND confirmed_by IS NOT NULL)));
 CREATE INDEX IF NOT EXISTS knowledge_drafts_investigation ON knowledge_drafts(investigation_id,created_at);`)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, governanceMigration); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

const selectDoc = `SELECT d.id,COALESCE(d.application_id,''),d.title,d.body,d.source,d.version,d.content_hash,d.created_at,COALESCE(g.id,0),COALESCE(g.provider,''),COALESCE(g.model,''),COALESCE(g.dimensions,0),COALESCE(g.status,'unindexed'),COALESCE(g.error,''),COALESCE(d.investigation_id,''),d.evidence_ids,d.lifecycle,d.metadata FROM knowledge_documents d LEFT JOIN LATERAL (SELECT * FROM knowledge_generations WHERE document_id=d.id AND document_version=d.version ORDER BY id DESC LIMIT 1) g ON true `

// V3 Runbook ACLs are enforced by their own routes. Historical Knowledge
// projections must exclude both the source namespace and linked rows whose
// source may have been changed before this guard was installed.
const legacyV3RunbookFilter = `NOT (d.source LIKE 'runbook://v3/%' OR EXISTS (SELECT 1 FROM v3_runbooks vr WHERE vr.document_id=d.id))`

func (s *PGRepository) IsV3Runbook(ctx context.Context, id string) (bool, error) {
	var found bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM v3_runbooks WHERE document_id=$1)`, id).Scan(&found)
	return found, err
}

func scanDoc(row pgx.Row) (Document, error) {
	var d Document
	e := row.Scan(&d.ID, &d.ApplicationID, &d.Title, &d.Body, &d.Source, &d.Version, &d.ContentHash, &d.CreatedAt, &d.GenerationID, &d.EmbeddingProvider, &d.EmbeddingModel, &d.Dimensions, &d.Status, &d.Error, &d.InvestigationID, &d.EvidenceIDs, &d.Lifecycle, &d.Metadata)
	if errors.Is(e, pgx.ErrNoRows) {
		e = store.ErrNotFound
	}
	return d, e
}
func (s *PGRepository) Get(ctx context.Context, id string) (Document, error) {
	return scanDoc(s.DB.QueryRow(ctx, selectDoc+" WHERE d.id=$1", id))
}
func (s *PGRepository) List(ctx context.Context, app string) ([]Document, error) {
	rows, e := s.DB.Query(ctx, selectDoc+" WHERE "+legacyV3RunbookFilter+" AND ($1='' OR d.application_id=$1 OR d.metadata->>'visibility'='workspace' OR COALESCE(d.metadata->'shared_application_ids','[]'::jsonb) ? $1) ORDER BY d.created_at DESC LIMIT 200", app)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, e := scanDoc(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PGRepository) ApplicationExists(ctx context.Context, app string) (bool, error) {
	var exists bool
	e := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1)", app).Scan(&exists)
	return exists, e
}
func (s *PGRepository) SaveSource(ctx context.Context, d Document, user string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	meta, e := json.Marshal(d.Metadata)
	if e != nil {
		return e
	}
	e = tx.QueryRow(ctx, "INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata) VALUES($1,NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,$10) RETURNING created_at", d.ID, d.ApplicationID, d.Title, d.Body, d.Source, d.Version, d.ContentHash, user, d.Lifecycle, meta).Scan(&d.CreatedAt)
	if e != nil {
		return e
	}
	if e = saveRevision(ctx, tx, d, user); e != nil {
		return e
	}
	if e = knowledgeAudit(ctx, tx, d, user, "knowledge.created"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *PGRepository) ReserveGeneration(ctx context.Context, id string, desc inference.EmbeddingDescriptor) (int64, error) {
	var gen int64
	e := s.DB.QueryRow(ctx, "INSERT INTO knowledge_generations(document_id,provider,model,status,document_version) SELECT id,$2,$3,'building',version FROM knowledge_documents WHERE id=$1 RETURNING id", id, desc.Provider, desc.Model).Scan(&gen)
	return gen, e
}
func (s *PGRepository) FailGeneration(ctx context.Context, gen int64, message string) error {
	_, e := s.DB.Exec(ctx, "UPDATE knowledge_generations SET status='failed',error=$2 WHERE id=$1 AND status='building'", gen, message)
	return e
}
func (s *PGRepository) PublishGeneration(ctx context.Context, snapshot GenerationSnapshot) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var document, provider, model, status string
	if e = tx.QueryRow(ctx, "SELECT document_id,provider,model,status FROM knowledge_generations WHERE id=$1 FOR UPDATE", snapshot.ID).Scan(&document, &provider, &model, &status); e != nil {
		return e
	}
	if document != snapshot.DocumentID || provider != snapshot.Descriptor.Provider || model != snapshot.Descriptor.Model || status != "building" {
		return store.ErrConflict
	}
	if snapshot.Dimensions < 1 || snapshot.Dimensions > 16000 || len(snapshot.Chunks) == 0 {
		return errors.New("invalid generation snapshot")
	}

	for i, chunk := range snapshot.Chunks {
		v := chunk.Vector
		if len(v) != snapshot.Dimensions {
			return errors.New("inconsistent embedding dimensions")
		}
		encoded, e := vector(v)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "INSERT INTO knowledge_chunks(generation_id,ordinal,content,embedding) VALUES($1,$2,$3,$4::vector)", snapshot.ID, i, chunk.Content, encoded); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE knowledge_generations SET status='ready',dimensions=$2 WHERE id=$1", snapshot.ID, snapshot.Dimensions); e != nil {
		return e
	}
	// A slow older rebuild cannot replace the result of a newer successful one.
	if _, e = tx.Exec(ctx, "UPDATE knowledge_documents SET active_generation=$2 WHERE id=$1 AND version=(SELECT document_version FROM knowledge_generations WHERE id=$2) AND (active_generation IS NULL OR active_generation<$2)", snapshot.DocumentID, snapshot.ID); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type PGVector struct{ DB *pgxpool.Pool }

func (p PGVector) Search(ctx context.Context, app string, desc inference.EmbeddingDescriptor, v []float32, limit int) ([]Hit, error) {
	return p.SearchScoped(ctx, app, "", desc, v, limit)
}
func (p PGVector) SearchScoped(ctx context.Context, app, environment string, desc inference.EmbeddingDescriptor, v []float32, limit int) ([]Hit, error) {
	encoded, e := vector(v)
	if e != nil {
		return nil, e
	}
	// Exact cosine search is intentional for MVP corpus sizes. Do not claim a
	// million-vector latency budget until a workload-specific ANN benchmark exists.
	// Legacy callers have no V3 team principal. Keep team-scoped passages out
	// even when an old Worker searches without an environment selection.
	rows, e := p.DB.Query(ctx, `WITH eligible AS MATERIALIZED (SELECT d.id,COALESCE(d.application_id,'') application_id,d.title,d.source,d.version,c.ordinal,c.content,c.embedding,g.id generation_id FROM knowledge_documents d JOIN knowledge_generations g ON g.id=d.active_generation JOIN knowledge_chunks c ON c.generation_id=g.id WHERE `+legacyV3RunbookFilter+` AND (d.application_id=$1 OR d.metadata->>'visibility'='workspace' OR COALESCE(d.metadata->'shared_application_ids','[]'::jsonb) ? $1) AND d.lifecycle='published' AND NOT (d.metadata ? 'source_registration_id') AND NOT (d.metadata ? 'team_id') AND (NOT (d.metadata ? 'environment_ids') OR CASE WHEN jsonb_typeof(d.metadata->'environment_ids')='array' THEN NOT EXISTS (SELECT 1 FROM jsonb_array_elements(d.metadata->'environment_ids') env_item WHERE jsonb_typeof(env_item)<>'string' OR btrim(env_item #>> '{}')='') ELSE false END) AND g.document_version=d.version AND g.status='ready' AND g.provider=$2 AND g.model=$3 AND g.dimensions=$4 AND ($7='' OR COALESCE(d.metadata->'environment_ids','[]'::jsonb)='[]'::jsonb OR d.metadata->'environment_ids' ? $7)) SELECT id,application_id,title,source,version,ordinal,content,1-(embedding <=> $5::vector),generation_id FROM eligible ORDER BY embedding <=> $5::vector,id,ordinal LIMIT $6`, app, desc.Provider, desc.Model, len(v), encoded, limit, environment)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if e = rows.Scan(&h.DocumentID, &h.ApplicationID, &h.Title, &h.Source, &h.Version, &h.Chunk, &h.Content, &h.Similarity, &h.GenerationID); e != nil {
			return nil, e
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
