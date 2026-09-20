// Package knowledge owns source documents and rebuildable vector generations.
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

var ErrInvalid = errors.New("invalid knowledge input")

type Document struct {
	Lifecycle         string    `json:"lifecycle"`
	Metadata          Metadata  `json:"metadata"`
	InvestigationID   string    `json:"investigation_id,omitempty"`
	EvidenceIDs       []string  `json:"evidence_ids,omitempty"`
	ID                string    `json:"id"`
	ApplicationID     string    `json:"application_id"`
	Title             string    `json:"title"`
	Body              string    `json:"body"`
	Source            string    `json:"source"`
	Version           int       `json:"version"`
	ContentHash       string    `json:"content_hash"`
	Status            string    `json:"status"`
	Error             string    `json:"error,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	GenerationID      int64     `json:"generation_id"`
	EmbeddingProvider string    `json:"embedding_provider,omitempty"`
	EmbeddingModel    string    `json:"embedding_model,omitempty"`
	Dimensions        int       `json:"dimensions,omitempty"`
}
type Hit struct {
	DocumentID    string  `json:"document_id"`
	ApplicationID string  `json:"application_id"`
	Title         string  `json:"title"`
	Source        string  `json:"source"`
	Version       int     `json:"version"`
	Chunk         int     `json:"chunk"`
	Content       string  `json:"content"`
	Similarity    float64 `json:"similarity"`
	GenerationID  int64   `json:"generation_id"`
}

// VectorIndex is independent of document storage and the embedding provider.
// A replacement adapter must enforce scope and full generation identity itself.
type VectorIndex interface {
	Search(context.Context, string, inference.EmbeddingDescriptor, []float32, int) ([]Hit, error)
}

// Repository owns durable source documents and index publication. A generation
// is reserved before embedding, and published only when all vector data is ready.
// Methods use product types rather than database transactions or query handles.
type Repository interface {
	DraftRepository
	Migrate(context.Context) error
	ApplicationExists(context.Context, string) (bool, error)
	SaveSource(context.Context, Document, string) error
	Get(context.Context, string) (Document, error)
	List(context.Context, string) ([]Document, error)
	ReserveGeneration(context.Context, string, inference.EmbeddingDescriptor) (int64, error)
	FailGeneration(context.Context, int64, string) error
	PublishGeneration(context.Context, GenerationSnapshot) error
}

// GenerationSnapshot is the immutable, complete build result. PublishGeneration
// must make its vectors searchable before switching the source's active pointer,
// preserve the last successful generation on failure, and prevent an older build
// replacing a newer successful one. External vector adapters must implement that
// protocol explicitly; this contract does not assume a cross-database transaction.
type GenerationSnapshot struct {
	ID         int64
	DocumentID string
	Descriptor inference.EmbeddingDescriptor
	Dimensions int
	Chunks     []IndexedChunk
}
type IndexedChunk struct {
	Content string
	Vector  []float32
}
type Service struct {
	VisibleApplications   func(context.Context, string) ([]string, error)
	ExperienceGenerator   ExperienceGenerator
	AutoExperienceEnabled func(context.Context, string) bool
	Authorize             func(context.Context, string, string, string) error
	ValidateEnvironment   func(context.Context, string, string) error
	Repository            Repository
	Embedding             inference.Embedder
	Vectors               VectorIndex
}

func NewWithRepository(repo Repository, e inference.Embedder, index VectorIndex) *Service {
	return &Service{Repository: repo, Embedding: e, Vectors: index}
}
func (s *Service) Migrate(ctx context.Context) error { return s.Repository.Migrate(ctx) }
func (s *Service) Get(ctx context.Context, id string) (Document, error) {
	d, err := s.Repository.Get(ctx, id)
	if err == nil {
		err = s.access(ctx, d, "view")
	}
	return d, err
}
func (s *Service) List(ctx context.Context, app string) ([]Document, error) {
	if app != "" {
		if err := s.authorize(ctx, app, "view"); err != nil {
			return nil, err
		}
	}
	var ds []Document
	var err error
	if app == "" && s.VisibleApplications != nil {
		ids, e := s.VisibleApplications(ctx, PrincipalFromContext(ctx).ID)
		if e != nil {
			return nil, e
		}
		if repo, ok := s.Repository.(interface {
			ListVisible(context.Context, []string) ([]Document, error)
		}); ok {
			ds, err = repo.ListVisible(ctx, ids)
		} else {
			ds, err = s.Repository.List(ctx, app)
		}
	} else {
		ds, err = s.Repository.List(ctx, app)
	}
	if err != nil {
		return nil, err
	}
	out := []Document{}
	for _, d := range ds {
		if s.access(ctx, d, "view") == nil {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *Service) Import(ctx context.Context, d Document, user string) (Document, error) {
	if err := s.authorize(ctx, d.ApplicationID, "operate"); err != nil {
		return d, err
	}
	d.Metadata.Origin = "human"
	d.Metadata.ActorID = user
	d.Metadata.GenerationRunID = ""
	d.Metadata.GenerationModel = ""
	d.Metadata.EvidenceIDs = nil
	if d.ApplicationID == "" && d.Metadata.Visibility == "" {
		d.Metadata.Visibility = "workspace"
	}
	var err error
	d.Metadata, err = s.normalizeMetadata(ctx, d.ApplicationID, user, d.Metadata)
	if err != nil {
		return d, err
	}
	d.Lifecycle = "draft"
	d.Title = strings.TrimSpace(d.Title)
	d.Body = strings.TrimSpace(d.Body)
	if d.Title == "" || d.Body == "" || len(d.Title) > 200 || len(d.Body) > 128*1024 || len(d.Source) > 2048 {
		return d, fmt.Errorf("%w: title and body are required; body limit 128 KiB", ErrInvalid)
	}
	d.Version = 1
	d.ID = store.ID()
	d.ContentHash = store.Hash(d.Body)
	if d.ApplicationID != "" {
		exists, e := s.Repository.ApplicationExists(ctx, d.ApplicationID)
		if e != nil {
			return d, e
		}
		if !exists {
			return d, store.ErrNotFound
		}
	}
	e := s.Repository.SaveSource(ctx, d, user)
	if e != nil {
		return d, e
	}
	// The original survives an unavailable embedding service; retry indexes this ID.
	if e = s.Index(ctx, d.ID); e != nil {
		d, e2 := s.Get(ctx, d.ID)
		if e2 != nil {
			return d, e2
		}
		return d, nil
	}
	return s.Get(ctx, d.ID)
}

func (s *Service) Index(ctx context.Context, id string) (retErr error) {
	d, e := s.Get(ctx, id)
	if e != nil {
		return e
	}
	if err := s.access(ctx, d, "operate"); err != nil {
		return err
	}
	if s.Embedding == nil {
		return errors.New("embedding unavailable")
	}
	desc := s.Embedding.Descriptor()
	var gen int64
	if r, ok := s.Repository.(interface {
		ReserveRevisionGeneration(context.Context, string, int, inference.EmbeddingDescriptor) (int64, error)
	}); ok {
		gen, e = r.ReserveRevisionGeneration(ctx, id, d.Version, desc)
	} else {
		gen, e = s.Repository.ReserveGeneration(ctx, id, desc)
	}
	if e != nil {
		return e
	}
	defer func() {
		if retErr != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.Repository.FailGeneration(cleanup, gen, "embedding or index build failed; retry reindex")
		}
	}()
	chunks := chunkText(d.Body)
	vectors := make([][]float32, 0, len(chunks))
	for start := 0; start < len(chunks); start += 64 {
		end := min(start+64, len(chunks))
		vs, e := s.Embedding.Embed(ctx, chunks[start:end])
		if e != nil {
			return e
		}
		vectors = append(vectors, vs...)
	}
	if len(vectors) != len(chunks) || len(vectors) == 0 {
		return errors.New("embedding count mismatch")
	}
	dim := len(vectors[0])
	if dim == 0 || dim > 16000 {
		return errors.New("unsupported embedding dimensions")
	}
	snapshot := GenerationSnapshot{ID: gen, DocumentID: id, Descriptor: desc, Dimensions: dim, Chunks: make([]IndexedChunk, len(chunks))}
	for i, v := range vectors {
		if len(v) != dim {
			return errors.New("inconsistent embedding dimensions")
		}
		if _, e := vector(v); e != nil {
			return e
		}
		snapshot.Chunks[i] = IndexedChunk{Content: chunks[i], Vector: v}
	}
	return s.Repository.PublishGeneration(ctx, snapshot)
}

func (s *Service) vectorSearch(ctx context.Context, app, query string, limit int) ([]Hit, error) {
	if app == "" || strings.TrimSpace(query) == "" || len(query) > 8000 {
		return nil, fmt.Errorf("%w: application_id and a query up to 8000 bytes are required", ErrInvalid)
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}
	exists, e := s.Repository.ApplicationExists(ctx, app)
	if e != nil {
		return nil, e
	}
	if !exists {
		return nil, store.ErrNotFound
	}
	vs, e := s.Embedding.Embed(ctx, []string{query})
	if e != nil {
		return nil, e
	}
	if len(vs) != 1 {
		return nil, errors.New("embedding count mismatch")
	}
	return s.Vectors.Search(ctx, app, s.Embedding.Descriptor(), vs[0], limit)
}

// Keep Markdown sections intact when possible; split oversized sections on rune
// boundaries with overlap. A heading remains attached to its first paragraph.
func chunkText(s string) []string {
	sections := []string{}
	var current strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "#") && current.Len() > 0 {
			sections = append(sections, strings.TrimSpace(current.String()))
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		sections = append(sections, strings.TrimSpace(current.String()))
	}
	out := []string{}
	for _, section := range sections {
		r := []rune(section)
		for start := 0; start < len(r); start += 900 {
			end := min(start+1100, len(r))
			out = append(out, string(r[start:end]))
			if end == len(r) {
				break
			}
		}
	}
	return out
}
func vector(v []float32) (string, error) {
	if len(v) == 0 {
		return "", errors.New("empty vector")
	}
	parts := make([]string, len(v))
	norm := float64(0)
	for i, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return "", errors.New("nonfinite vector")
		}
		norm += float64(f) * float64(f)
		parts[i] = strconv.FormatFloat(float64(f), 'g', -1, 32)
	}
	if norm == 0 {
		return "", errors.New("zero vector")
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}
func (s *Service) EvidenceTool(app string) inference.EvidenceTool {
	return inference.EvidenceTool{Name: "search_runbooks", Description: "Search confirmed runbooks for this registered application. Returned passages are references, not current live evidence.", Read: func(ctx context.Context, q string) (inference.Evidence, error) {
		result, e := s.SearchScoped(ctx, app, store.EnvironmentID(ctx), q, 5)
		if e != nil {
			return inference.Evidence{}, e
		}
		b, e := json.Marshal(result)
		return inference.Evidence{Kind: "runbook", Title: fmt.Sprintf("Runbook search (%d passages)", len(result.Items)), Content: string(b), Source: "knowledge://search"}, e
	}}
}
