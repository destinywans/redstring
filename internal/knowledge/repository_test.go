package knowledge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

// This fake deliberately has no SQL handle. It validates that orchestration can
// work with an alternate source repository and vector index independently.
type memoryRepository struct {
	doc      Document
	snapshot GenerationSnapshot
	failed   bool
}

func (m *memoryRepository) Migrate(context.Context) error                           { return nil }
func (m *memoryRepository) ApplicationExists(context.Context, string) (bool, error) { return true, nil }
func (m *memoryRepository) SaveSource(_ context.Context, d Document, _ string) error {
	m.doc = d
	return nil
}
func (m *memoryRepository) Get(context.Context, string) (Document, error) { return m.doc, nil }
func (m *memoryRepository) List(context.Context, string) ([]Document, error) {
	return []Document{m.doc}, nil
}
func (m *memoryRepository) ReserveGeneration(_ context.Context, id string, d inference.EmbeddingDescriptor) (int64, error) {
	m.doc.GenerationID = 1
	m.doc.Status = "building"
	return 1, nil
}
func (m *memoryRepository) FailGeneration(context.Context, int64, string) error {
	m.failed = true
	m.doc.Status = "failed"
	return nil
}
func (m *memoryRepository) PublishGeneration(_ context.Context, s GenerationSnapshot) error {
	m.snapshot = s
	m.doc.Status = "ready"
	return nil
}

type recordingIndex struct {
	app        string
	desc       inference.EmbeddingDescriptor
	dimensions int
}

func (i *recordingIndex) Search(_ context.Context, app string, d inference.EmbeddingDescriptor, v []float32, _ int) ([]Hit, error) {
	i.app = app
	i.desc = d
	i.dimensions = len(v)
	return []Hit{{DocumentID: "external-vector-record"}}, nil
}
func TestKnowledgeOrchestrationUsesPortableRepositoryAndIndex(t *testing.T) {
	repo := &memoryRepository{}
	index := &recordingIndex{}
	embed := &fakeEmbedder{desc: inference.EmbeddingDescriptor{Provider: "portable", Model: "model"}, dimensions: 3}
	s := NewWithRepository(repo, embed, index)
	d, e := s.Import(context.Background(), Document{ApplicationID: "app", Title: "Runbook", Body: "调查节点继承已确认上下文"}, "user")
	if e != nil || d.Status != "ready" {
		t.Fatalf("portable import %+v %v", d, e)
	}
	if repo.snapshot.DocumentID != d.ID || repo.snapshot.Descriptor != embed.desc || repo.snapshot.Dimensions != 3 || len(repo.snapshot.Chunks) != 1 || repo.snapshot.Chunks[0].Content != d.Body {
		t.Fatalf("invalid publication snapshot %+v", repo.snapshot)
	}
	hits, e := s.Search(context.Background(), "app", "investigate", 5)
	if e != nil || len(hits) != 1 || index.app != "app" || index.desc != embed.desc || index.dimensions != 3 {
		t.Fatalf("portable search %+v %v", hits, e)
	}
	embed.fail = true
	if e = s.Index(context.Background(), d.ID); e == nil || !repo.failed || repo.doc.Body != d.Body {
		t.Fatal("failed rebuild did not preserve source and record failure")
	}
}
func TestPostgresPublicationCannotReplaceNewerGeneration(t *testing.T) {
	s, embed := isolatedKnowledge(t)
	ctx := context.Background()
	d, e := s.Import(ctx, Document{ApplicationID: "a", Title: "Runbook", Body: "original"}, "u")
	if e != nil {
		t.Fatal(e)
	}
	repo := s.Repository
	old, e := repo.ReserveGeneration(ctx, d.ID, embed.desc)
	if e != nil {
		t.Fatal(e)
	}
	newer, e := repo.ReserveGeneration(ctx, d.ID, embed.desc)
	if e != nil {
		t.Fatal(e)
	}
	snapshot := func(id int64) GenerationSnapshot {
		return GenerationSnapshot{ID: id, DocumentID: d.ID, Descriptor: embed.desc, Dimensions: 3, Chunks: []IndexedChunk{{Content: "record", Vector: []float32{1, 0, 0}}}}
	}
	if e = repo.PublishGeneration(ctx, snapshot(newer)); e != nil {
		t.Fatal(e)
	}
	if e = repo.PublishGeneration(ctx, snapshot(old)); e != nil {
		t.Fatal(e)
	}
	var active int64
	if e = repo.(*PGRepository).DB.QueryRow(ctx, "SELECT active_generation FROM knowledge_documents WHERE id=$1", d.ID).Scan(&active); e != nil || active != newer {
		t.Fatalf("older build replaced new index: %d %v", active, e)
	}
	reserved, e := repo.ReserveGeneration(ctx, d.ID, embed.desc)
	if e != nil {
		t.Fatal(e)
	}
	wrong := snapshot(reserved)
	wrong.Descriptor.Model = "wrong-model"
	if e = repo.PublishGeneration(ctx, wrong); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("mismatched generation identity accepted: %v", e)
	}
}
func TestPostgresKnowledgeHTTPInvalidInputIs422(t *testing.T) {
	s, _ := isolatedKnowledge(t)
	db := s.Repository.(*PGRepository).DB
	ctx := context.Background()
	token := "test-token"
	if _, e := db.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'u',now()+interval '1 hour')", store.Hash(token)); e != nil {
		t.Fatal(e)
	}
	api := httpapi.New(&store.Store{DB: db}, nil)
	s.Register(api)
	for _, tc := range []struct{ path, body string }{{"/api/v1/knowledge", `{"title":"","body":"runbook"}`}, {"/api/v1/knowledge/search", `{"application_id":"a","query":" "}`}, {"/api/v1/knowledge", `{"unknown":true}`}} {
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "invalid_input") {
			t.Fatalf("%s returned %d: %s", tc.path, w.Code, w.Body.String())
		}
	}
}

func (m *memoryRepository) CreateDraft(context.Context, string, DraftInput, string) (Draft, error) {
	return Draft{}, errors.New("not used")
}
func (m *memoryRepository) ListDrafts(context.Context, string) ([]Draft, error) {
	return nil, errors.New("not used")
}
func (m *memoryRepository) ConfirmDraft(context.Context, string, string) (string, bool, error) {
	return "", false, errors.New("not used")
}
