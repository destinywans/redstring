package knowledge

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type fakeEmbedder struct {
	desc       inference.EmbeddingDescriptor
	dimensions int
	fail       bool
}

func (f *fakeEmbedder) Descriptor() inference.EmbeddingDescriptor { return f.desc }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.fail {
		return nil, errors.New("embedding test unavailable")
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, f.dimensions)
		out[i][0] = 1
	}
	return out, nil
}

func isolatedKnowledge(t *testing.T) (*Service, *fakeEmbedder) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated random schema")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("cannot configure test database")
	}
	schema := "test_knowledge_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		_, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error("test schema cleanup failed")
		}
	})
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal("cannot configure test pool")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e = pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = (&store.Store{DB: pool}).Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	pool.Close()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, e = pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	var extension bool
	if e = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector' AND n.nspname='public')").Scan(&extension); e != nil || !extension {
		t.Fatal("requires preinstalled public.vector extension")
	}
	if _, e = pool.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES('u','test@example.invalid','Test','operator','unused'); INSERT INTO applications(id,name) VALUES('a','A'),('b','B')"); e != nil {
		t.Fatal(e)
	}
	f := &fakeEmbedder{desc: inference.EmbeddingDescriptor{Provider: "test-provider", Model: "test-model"}, dimensions: 3}
	s := New(pool, f)
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	return s, f
}
func TestPostgresKnowledgePreservesSourceAndRetriesGeneration(t *testing.T) {
	s, f := isolatedKnowledge(t)
	ctx := context.Background()
	f.fail = true
	d, e := s.Import(ctx, Document{ApplicationID: "a", Title: "发布恢复", Body: "原始 Runbook 保留。", Source: "runbook://release", Version: 2}, "u")
	if e != nil {
		t.Fatal(e)
	}
	if d.Status != "failed" || d.Body != "原始 Runbook 保留。" || d.Source != "runbook://release" || d.ContentHash != store.Hash(d.Body) {
		t.Fatalf("source or failed state lost: %+v", d)
	}
	failedGeneration := d.GenerationID
	var active *int64
	if e = s.Repository.(*PGRepository).DB.QueryRow(ctx, "SELECT active_generation FROM knowledge_documents WHERE id=$1", d.ID).Scan(&active); e != nil || active != nil {
		t.Fatalf("failed generation activated: %v %v", active, e)
	}
	f.fail = false
	if e = s.Index(ctx, d.ID); e != nil {
		t.Fatal(e)
	}
	ready, e := s.Get(ctx, d.ID)
	if e != nil {
		t.Fatal(e)
	}
	if ready.Status != "ready" || ready.GenerationID <= failedGeneration || ready.Dimensions != 3 || ready.Body != d.Body {
		t.Fatalf("retry not activated: %+v", ready)
	}
	if e = s.Repository.(*PGRepository).DB.QueryRow(ctx, "SELECT active_generation FROM knowledge_documents WHERE id=$1", d.ID).Scan(&active); e != nil || active == nil || *active != ready.GenerationID {
		t.Fatalf("wrong active generation: %v %v", active, e)
	}
	f.fail = true
	if e = s.Index(ctx, d.ID); e == nil {
		t.Fatal("retry unexpectedly succeeded")
	}
	if e = s.Repository.(*PGRepository).DB.QueryRow(ctx, "SELECT active_generation FROM knowledge_documents WHERE id=$1", d.ID).Scan(&active); e != nil || active == nil || *active != ready.GenerationID {
		t.Fatal("failed rebuild replaced last successful generation")
	}
}
func TestPostgresKnowledgeScopeAndEmbeddingIdentity(t *testing.T) {
	s, f := isolatedKnowledge(t)
	ctx := context.Background()
	ids := map[string]string{}
	for _, app := range []string{"a", "b", ""} {
		d, e := s.Import(ctx, Document{ApplicationID: app, Title: "runbook " + app, Body: "Use approved deployment rollback", Source: "runbook://" + app}, "u")
		if e != nil || d.Status != "ready" {
			t.Fatalf("import: %v %+v", e, d)
		}
		ids[app] = d.ID
	}
	for _, app := range []string{"a", "b"} {
		hits, e := s.Search(ctx, app, "rollback", 10)
		if e != nil {
			t.Fatal(e)
		}
		if len(hits) != 2 {
			t.Fatalf("scope %s: got %d hits", app, len(hits))
		}
		seen := map[string]bool{}
		for _, h := range hits {
			seen[h.DocumentID] = true
			if h.ApplicationID != app && h.ApplicationID != "" {
				t.Fatalf("cross-app leak %+v", h)
			}
			if h.Source == "" || h.Version != 1 || h.GenerationID == 0 {
				t.Fatal("missing provenance")
			}
		}
		if !seen[ids[app]] || !seen[ids[""]] {
			t.Fatal("app/global scope not both searched")
		}
	}
	original := f.desc
	for _, change := range []func(){func() { f.desc.Provider = "other-provider" }, func() { f.desc = original; f.desc.Model = "other-model" }, func() { f.desc = original; f.dimensions = 4 }} {
		change()
		hits, e := s.Search(ctx, "a", "rollback", 10)
		if e != nil {
			t.Fatal(e)
		}
		if len(hits) != 0 {
			t.Fatal("cross-generation embedding records returned")
		}
	}
}
func TestChunkTextUnicodeOverlap(t *testing.T) {
	body := strings.Repeat("故障🙂恢复", 550)
	runes := []rune(body)
	chunks := chunkText(body)
	if len(chunks) < 2 {
		t.Fatal("did not chunk")
	}
	var rebuilt []rune
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatal("split UTF-8 encoding")
		}
		r := []rune(c)
		start := i * 900
		if string(runes[start:min(start+1100, len(runes))]) != c {
			t.Fatal("rune range changed")
		}
		if i == 0 {
			rebuilt = append(rebuilt, r...)
		} else {
			if len(r) < 200 {
				t.Fatal("unexpected overlap")
			}
			rebuilt = append(rebuilt, r[200:]...)
		}
	}
	if string(rebuilt) != body {
		t.Fatal("chunk overlap loses original text")
	}
}
