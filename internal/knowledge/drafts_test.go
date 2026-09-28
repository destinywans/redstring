package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func investigationEvidence(t *testing.T, s *Service, app string) (string, string) {
	t.Helper()
	db := s.Repository.(*PGRepository).DB
	st := &store.Store{DB: db}
	ctx := context.Background()
	inv, e := st.CreateInvestigation(ctx, app, "Investigated incident")
	if e != nil {
		t.Fatal(e)
	}
	run, e := st.Enqueue(ctx, inv.Branches[0].ID, "Investigate", "test", "request")
	if e != nil {
		t.Fatal(e)
	}
	claim, e := st.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = st.SaveEvidence(ctx, claim, core.Evidence{Kind: "health", Title: "Recovered", Content: "25 successful requests", Source: "app://health"}); e != nil {
		t.Fatal(e)
	}
	if e = st.Finish(ctx, claim, "Observed recovery", nil); e != nil {
		t.Fatal(e)
	}
	var evidence string
	if e = db.QueryRow(ctx, "SELECT id FROM evidence WHERE run_id=$1", run.ID).Scan(&evidence); e != nil {
		t.Fatal(e)
	}
	return inv.ID, evidence
}
func closeFixture(t *testing.T, s *Service, inv string) {
	t.Helper()
	if _, e := s.Repository.(*PGRepository).DB.Exec(context.Background(), "UPDATE investigations SET status='closed' WHERE id=$1", inv); e != nil {
		t.Fatal(e)
	}
}
func TestPostgresExperienceDraftRequiresConfirmationAndPreservesProvenance(t *testing.T) {
	s, _ := isolatedKnowledge(t)
	ctx := context.Background()
	inv, evidence := investigationEvidence(t, s, "a")
	_, otherEvidence := investigationEvidence(t, s, "b")
	in := DraftInput{Title: "Rollback experience", Body: "Confirm target identity before approved rollback", EvidenceIDs: []string{evidence}}
	wrong := in
	wrong.EvidenceIDs = []string{otherEvidence}
	if _, e := s.CreateDraft(ctx, inv, wrong, "u"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("cross-investigation evidence accepted: %v", e)
	}
	draft, e := s.CreateDraft(ctx, inv, in, "u")
	if e != nil {
		t.Fatal(e)
	}
	if draft.DocumentID != "" || draft.ConfirmedAt != nil || draft.ApplicationID != "a" {
		t.Fatalf("draft prematurely published %+v", draft)
	}
	hits, e := s.Search(ctx, "a", "rollback", 5)
	if e != nil || len(hits) != 0 {
		t.Fatalf("unconfirmed draft searchable %+v %v", hits, e)
	}
	docs, e := s.List(ctx, "a")
	if e != nil || len(docs) != 0 {
		t.Fatal("draft saved as source before confirmation")
	}
	if _, e = s.ConfirmDraft(ctx, draft.ID, "u"); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("open investigation confirmed: %v", e)
	}
	closeFixture(t, s, inv)
	d, e := s.ConfirmDraft(ctx, draft.ID, "u")
	if e != nil {
		t.Fatal(e)
	}
	if d.Status != "ready" || d.InvestigationID != inv || d.ApplicationID != "a" || d.Version != 1 || len(d.EvidenceIDs) != 1 || d.EvidenceIDs[0] != evidence || d.Body != in.Body || d.ContentHash != store.Hash(in.Body) || !strings.Contains(d.Source, draft.ID) {
		t.Fatalf("lost immutable provenance %+v", d)
	}
	hits, e = s.Search(ctx, "a", "rollback", 5)
	if e != nil || len(hits) != 1 || hits[0].DocumentID != d.ID {
		t.Fatalf("confirmed source not indexed %+v %v", hits, e)
	}
	hits, e = s.Search(ctx, "b", "rollback", 5)
	if e != nil || len(hits) != 0 {
		t.Fatal("confirmed experience leaked application scope")
	}
	drafts, e := s.Drafts(ctx, inv)
	if e != nil || len(drafts) != 1 || drafts[0].DocumentID != d.ID || drafts[0].ConfirmedBy != "u" || drafts[0].ConfirmedAt == nil {
		t.Fatalf("confirmation trace missing %+v %v", drafts, e)
	}
}
func TestPostgresConcurrentDraftConfirmationCreatesOneDocumentAndGeneration(t *testing.T) {
	s, _ := isolatedKnowledge(t)
	ctx := context.Background()
	inv, evidence := investigationEvidence(t, s, "a")
	draft, e := s.CreateDraft(ctx, inv, DraftInput{Title: "Experience", Body: "Recovery evidence", EvidenceIDs: []string{evidence}}, "u")
	if e != nil {
		t.Fatal(e)
	}
	closeFixture(t, s, inv)
	const parallel = 8
	ids := make(chan string, parallel)
	errs := make(chan error, parallel)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, e := s.ConfirmDraft(ctx, draft.ID, "u")
			if e != nil {
				errs <- e
				return
			}
			ids <- d.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate source document")
		}
	}
	db := s.Repository.(*PGRepository).DB
	for _, q := range []string{"SELECT count(*) FROM knowledge_documents", "SELECT count(*) FROM knowledge_generations", "SELECT count(*) FROM events WHERE type='knowledge.confirmed'"} {
		var count int
		if e = db.QueryRow(ctx, q).Scan(&count); e != nil || count != 1 {
			t.Fatalf("confirmation duplicated: count=%d err=%v", count, e)
		}
	}
}
func TestPostgresConfirmedExperienceSurvivesEmbeddingFailureAndRetries(t *testing.T) {
	s, f := isolatedKnowledge(t)
	ctx := context.Background()
	inv, evidence := investigationEvidence(t, s, "a")
	draft, e := s.CreateDraft(ctx, inv, DraftInput{Title: "Experience", Body: "Original confirmed body", EvidenceIDs: []string{evidence}}, "u")
	if e != nil {
		t.Fatal(e)
	}
	closeFixture(t, s, inv)
	f.fail = true
	d, e := s.ConfirmDraft(ctx, draft.ID, "u")
	if e != nil || d.Status != "failed" || d.Body != "Original confirmed body" || d.InvestigationID != inv {
		t.Fatalf("confirmed original lost %+v %v", d, e)
	}
	again, e := s.ConfirmDraft(ctx, draft.ID, "u")
	if e != nil || again.ID != d.ID || again.GenerationID != d.GenerationID {
		t.Fatal("confirmation retry duplicated document or index")
	}
	f.fail = false
	if e = s.Index(ctx, d.ID); e != nil {
		t.Fatal(e)
	}
	ready, e := s.Get(ctx, d.ID)
	if e != nil || ready.Status != "ready" || ready.Version != d.Version || ready.Body != d.Body || ready.EvidenceIDs[0] != evidence {
		t.Fatalf("retry corrupted original %+v %v", ready, e)
	}
}
func TestPostgresExperienceDraftHTTPRolesAndValidation(t *testing.T) {
	s, _ := isolatedKnowledge(t)
	ctx := context.Background()
	db := s.Repository.(*PGRepository).DB
	inv, evidence := investigationEvidence(t, s, "a")
	if _, e := db.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES('viewer','viewer@example.invalid','Viewer','viewer','unused')"); e != nil {
		t.Fatal(e)
	}
	grantLegacyKnowledgeResource(t, db, "u", "a", "a-default", true)
	grantLegacyKnowledgeResource(t, db, "viewer", "a", "a-default", false)
	if _, e := db.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'u',now()+interval '1 hour'),($2,'viewer',now()+interval '1 hour')", store.Hash("operator-token"), store.Hash("viewer-token")); e != nil {
		t.Fatal(e)
	}
	api := httpapi.New(&store.Store{DB: db}, nil)
	s.Register(api)
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/investigations/" + inv + "/knowledge-drafts"
	body, _ := json.Marshal(DraftInput{Title: "Experience", Body: "Human reviewed recovery", EvidenceIDs: []string{evidence}})
	if w := request("POST", path, string(body), ""); w.Code != 401 {
		t.Fatalf("anonymous %d", w.Code)
	}
	if w := request("POST", path, string(body), "viewer-token"); w.Code != 403 {
		t.Fatalf("viewer wrote draft %d", w.Code)
	}
	if w := request("POST", path, `{"title":"x","body":"y","evidence_ids":[]}`, "operator-token"); w.Code != 422 {
		t.Fatalf("empty evidence %d %s", w.Code, w.Body.String())
	}
	w := request("POST", path, string(body), "operator-token")
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var draft Draft
	if e := json.Unmarshal(w.Body.Bytes(), &draft); e != nil {
		t.Fatal(e)
	}
	confirm := "/api/v1/knowledge-drafts/" + draft.ID + "/confirm"
	if w = request("POST", confirm, "", "viewer-token"); w.Code != 403 {
		t.Fatalf("viewer confirmed %d", w.Code)
	}
	if w = request("POST", confirm, "", "operator-token"); w.Code != 409 {
		t.Fatalf("open investigation confirmed %d", w.Code)
	}
	if w = request("GET", path, "", "viewer-token"); w.Code != 200 {
		t.Fatalf("viewer list %d", w.Code)
	}
	closeFixture(t, s, inv)
	if w = request("POST", confirm, "", "operator-token"); w.Code != 200 {
		t.Fatalf("confirmation %d %s", w.Code, w.Body.String())
	}
}
