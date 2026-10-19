package knowledge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLegacyKnowledgeExcludesV3Runbooks(t *testing.T) {
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		// The isolated schema helper requires vector in public. Set it up in the
		// test database so this security check also runs on a fresh PG instance.
		admin, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		_, err = admin.Exec(context.Background(), `CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public`)
		admin.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	s, _, st := isolatedGovernance(t)
	ctx := context.Background()
	db := s.Repository.(*PGRepository).DB
	grantLegacyKnowledgeResource(t, db, "v", "a", "a-default", false)
	_, err := db.Exec(ctx, `
INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata) VALUES
('legacy-visible','a','legacy needle','public legacy needle','manual://legacy',1,'a','u','published','{"visibility":"application"}'),
('v3-prefix','a','prefix needle','secret prefix needle','runbook://v3/v3-prefix',1,'b','u','published','{"visibility":"application"}'),
('v3-linked','a','linked needle','secret linked needle','historical://rewritten',1,'c','u','published','{"visibility":"application"}');
INSERT INTO v3_runbooks(document_id,source_kind,source_ref) VALUES('v3-linked','blank','');
INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by)
 SELECT id,1,jsonb_build_object('id',id,'title',title,'body',body,'source',source,'version',1,'metadata',metadata),'u'
 FROM knowledge_documents;
INSERT INTO knowledge_generations(document_id,provider,model,dimensions,status,document_version)
 SELECT id,'test','fake',3,'ready',1 FROM knowledge_documents;
UPDATE knowledge_documents d SET active_generation=g.id FROM knowledge_generations g WHERE g.document_id=d.id;
INSERT INTO knowledge_chunks(generation_id,ordinal,content,embedding)
 SELECT id,0,'needle','[1,0,0]'::vector FROM knowledge_generations;`)
	if err != nil {
		t.Fatal(err)
	}
	viewer := WithPrincipal(ctx, "v", "operator")
	noAccess := WithPrincipal(ctx, "no-app-grant", "viewer")
	for _, id := range []string{"v3-prefix", "v3-linked"} {
		if _, err := s.legacyGet(viewer, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("legacy detail %q: %v", id, err)
		}
		if _, err := s.legacyGet(noAccess, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("legacy detail leaked V3 identity without app access %q: %v", id, err)
		}
		if _, err := s.Revisions(viewer, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("legacy revisions %q: %v", id, err)
		}
		if _, err := s.Revision(viewer, id, 1); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("legacy revision %q: %v", id, err)
		}
		if _, err := s.Export(viewer, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("legacy export %q: %v", id, err)
		}
	}
	for _, app := range []string{"a", ""} {
		docs, err := s.List(viewer, app)
		if err != nil || len(docs) != 1 || docs[0].ID != "legacy-visible" {
			t.Fatalf("legacy list app=%q: %+v %v", app, docs, err)
		}
	}
	keywords, err := s.Repository.(*PGRepository).KeywordSearch(viewer, "a", "", "needle", 20)
	if err != nil || len(keywords) != 1 || keywords[0].DocumentID != "legacy-visible" {
		t.Fatalf("legacy keyword hits: %+v %v", keywords, err)
	}
	vectors, err := (PGVector{DB: db}).Search(viewer, "a", inference.EmbeddingDescriptor{Provider: "test", Model: "fake"}, []float32{1, 0, 0}, 20)
	if err != nil || len(vectors) != 1 || vectors[0].DocumentID != "legacy-visible" {
		t.Fatalf("legacy vector hits: %+v %v", vectors, err)
	}
	writer := WithPrincipal(ctx, "u", "operator")
	for _, id := range []string{"v3-prefix", "v3-linked"} {
		if _, err := s.Update(writer, id, UpdateInput{ExpectedVersion: 1, Title: "rewrite", Body: "leak", Source: "manual://rewritten"}, "u"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("legacy update %q: %v", id, err)
		}
		if _, err := s.Transition(writer, id, TransitionInput{ExpectedVersion: 1, State: "archived"}, "u"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("legacy transition %q: %v", id, err)
		}
		if _, err := s.Repository.(*PGRepository).UpdateDocument(writer, id, UpdateInput{ExpectedVersion: 1, Title: "rewrite", Body: "leak", Source: "manual://rewritten"}, "u"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("locked legacy update %q: %v", id, err)
		}
		if _, err := s.Repository.(*PGRepository).TransitionDocument(writer, id, 1, "archived", "u"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("locked legacy transition %q: %v", id, err)
		}
	}
	if _, err := s.Import(writer, Document{ApplicationID: "a", Title: "forged", Body: "forged", Source: "runbook://v3/forged"}, "u"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserved source accepted: %v", err)
	}
	var altered int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents WHERE id IN ('v3-prefix','v3-linked') AND (version<>1 OR lifecycle<>'published')`).Scan(&altered); err != nil || altered != 0 {
		t.Fatalf("legacy mutation changed V3 rows: %d %v", altered, err)
	}
	viewerToken, writerToken := "legacy-viewer-token", "legacy-writer-token"
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'v',now()+interval '1 hour'),($2,'u',now()+interval '1 hour')`, store.Hash(viewerToken), store.Hash(writerToken)); err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil)
	s.Register(api)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, id := range []string{"v3-prefix", "v3-linked"} {
		for _, suffix := range []string{"", "/revisions", "/revisions/1", "/export"} {
			w := request(http.MethodGet, "/api/v1/knowledge/"+id+suffix, viewerToken, "")
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("legacy GET %s: %d %s", id+suffix, w.Code, w.Body.String())
			}
		}
		w := request(http.MethodPost, "/api/v1/knowledge/"+id+"/reindex", writerToken, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("legacy reindex %s: %d %s", id, w.Code, w.Body.String())
		}
		w = request(http.MethodPut, "/api/v1/knowledge/"+id, writerToken, `{"expected_version":1,"title":"rewrite","body":"leak","source":"manual://rewritten"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("legacy update %s: %d %s", id, w.Code, w.Body.String())
		}
		w = request(http.MethodPost, "/api/v1/knowledge/"+id+"/transition", writerToken, `{"expected_version":1,"state":"archived"}`)
		if w.Code != http.StatusNotFound {
			t.Fatalf("legacy transition %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/knowledge", "/api/v1/knowledge?application_id=a"} {
		w := request(http.MethodGet, path, viewerToken, "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "legacy-visible") || strings.Contains(w.Body.String(), "v3-prefix") || strings.Contains(w.Body.String(), "v3-linked") {
			t.Fatalf("legacy GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w := request(http.MethodPost, "/api/v1/knowledge/search", viewerToken, `{"application_id":"a","query":"needle"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "legacy-visible") || strings.Contains(w.Body.String(), "v3-prefix") || strings.Contains(w.Body.String(), "v3-linked") {
		t.Fatalf("legacy search: %d %s", w.Code, w.Body.String())
	}
}
