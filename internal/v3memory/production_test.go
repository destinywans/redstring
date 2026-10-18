package v3memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/migrations"
)

func TestMemoryProductionValidation(t *testing.T) {
	valid := productionInput{DocumentID: "doc", ExpectedVersion: 1, ExpectedContentHash: strings.Repeat("a", 64), ExactExcerpt: "exact excerpt"}
	if !validProduction(valid, "dev") {
		t.Fatal("valid production denied")
	}
	for _, change := range []func(*productionInput){func(v *productionInput) { v.ExactExcerpt = " padded " }, func(v *productionInput) { v.ExpectedVersion = 0 }, func(v *productionInput) { v.ExpectedContentHash = "ABC" }} {
		v := valid
		change(&v)
		if validProduction(v, "dev") {
			t.Fatalf("invalid production accepted: %+v", v)
		}
	}
	if productionKey("dev", "app", "team", valid) == productionKey("stage", "app", "team", valid) {
		t.Fatal("key omitted environment")
	}
	for _, body := range []string{`{"document_id":"a","document_id":"b"}`, `{"model_extracted":true}`, `{} {}`, `[]`} {
		var in productionInput
		if err := decodeProduction(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)), &in); err == nil {
			t.Fatalf("ambiguous input accepted: %s", body)
		}
	}
	if validSourceInput(sourceInput{RequestKey: "key-long-enough", ApplicationID: "app", Title: "Title", Body: strings.Repeat("a", 16385)}) {
		t.Fatal("oversized source accepted")
	}
}
func productionFixture(t *testing.T) *actionsFixture {
	t.Helper()
	f := newActionsFixture(t)
	sql, err := migrations.Files.ReadFile("154_v3_memory_production.sql")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(string(sql))
	f.exec(`ALTER TABLE knowledge_documents ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN created_by text, ADD COLUMN created_at timestamptz DEFAULT now(), ADD COLUMN active_generation bigint;
 CREATE TABLE knowledge_revisions(document_id text,version integer,snapshot jsonb,created_by text,PRIMARY KEY(document_id,version));
 CREATE TABLE knowledge_history(document_id text,version integer,actor_id text,operation text);`)
	return f
}
func productionJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func publishTestSource(t *testing.T, f *actionsFixture, key string) sourceResult {
	t.Helper()
	in := sourceInput{RequestKey: key, ApplicationID: "app", Title: "Human-reviewed service note", Body: "Traffic restored after rollback. Traffic did not restore after rollback. Latency normalized."}
	w := f.post("/api/v1/memory/sources", productionJSON(t, in))
	f.want(w, 201, "")
	var out sourceResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func produceTestFact(t *testing.T, f *actionsFixture, src sourceResult, excerpt string) Fact {
	t.Helper()
	w := f.post("/api/v1/memory/facts", productionJSON(t, productionInput{src.DocumentID, src.Version, src.ContentHash, excerpt}))
	f.want(w, 201, "")
	var out struct {
		Fact Fact `json:"fact"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Fact
}

// PA-166: the view names fact and conflict sources by the document title.
func TestMemoryViewNamesSourcesPostgres(t *testing.T) {
	f := productionFixture(t)
	src := publishTestSource(t, f, "source-title-key")
	a := produceTestFact(t, f, src, "Traffic restored after rollback.")
	b := produceTestFact(t, f, src, "Traffic did not restore after rollback.")
	conflict := conflictInput{FactA: a.ID, FactB: b.ID, ExpectedRevisionA: 1, ExpectedRevisionB: 1, Reason: "Reviewed passages make incompatible statements about the same rollback."}
	f.want(f.post("/api/v1/memory/conflicts", productionJSON(t, conflict)), 201, "")
	w := f.call("GET", "/api/v1/memory", "", "dev", "actions-token")
	f.want(w, 200, "")
	var view struct {
		Facts []struct {
			Source      string  `json:"source"`
			SourceTitle *string `json:"source_title"`
		} `json:"facts"`
		Conflict *struct {
			A struct {
				SourceTitle *string `json:"source_title"`
			} `json:"a"`
			B struct {
				SourceTitle *string `json:"source_title"`
			} `json:"b"`
		} `json:"conflict"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Facts) != 2 || view.Conflict == nil {
		t.Fatalf("view: %s (%v)", w.Body.String(), err)
	}
	const title = "Human-reviewed service note"
	for _, fact := range view.Facts {
		if fact.Source != src.DocumentID || fact.SourceTitle == nil || *fact.SourceTitle != title {
			t.Fatalf("fact source title: %s", w.Body.String())
		}
	}
	if view.Conflict.A.SourceTitle == nil || *view.Conflict.A.SourceTitle != title || view.Conflict.B.SourceTitle == nil || *view.Conflict.B.SourceTitle != title {
		t.Fatalf("conflict source title: %s", w.Body.String())
	}
	// A blank title is unknown, not an empty name: two facts, and both sides
	// of the conflict in `conflicts` and in `conflict`.
	f.exec(`UPDATE knowledge_documents SET title='  ' WHERE id=$1`, src.DocumentID)
	w = f.call("GET", "/api/v1/memory", "", "dev", "actions-token")
	f.want(w, 200, "")
	if strings.Contains(w.Body.String(), title) || strings.Count(w.Body.String(), `"source_title":null`) != 6 {
		t.Fatalf("blank title: %s", w.Body.String())
	}
}
func TestMemoryProductionFreshHTTPChainPostgres(t *testing.T) {
	f := productionFixture(t)
	src := publishTestSource(t, f, "source-chain-key")
	if src.EnvironmentID != "dev" || src.TeamID != "ops" || src.Version != 1 || src.ContentHash == "" {
		t.Fatalf("source provenance: %+v", src)
	}
	a := produceTestFact(t, f, src, "Traffic restored after rollback.")
	b := produceTestFact(t, f, src, "Traffic did not restore after rollback.")
	in := productionInput{src.DocumentID, src.Version, src.ContentHash, a.Fact}
	w := f.post("/api/v1/memory/facts", productionJSON(t, in))
	f.want(w, 200, "")
	if !strings.Contains(w.Body.String(), a.ID) || !strings.Contains(w.Body.String(), `"created":false`) {
		t.Fatal("retry duplicated candidate", w.Body.String())
	}
	if f.auditCount("memory.fact.proposed") != 2 {
		t.Fatal("retry duplicated audit")
	}
	f.want(f.post("/api/v1/memory/facts/"+a.ID+"/confirm", ""), 200, "")
	conflict := conflictInput{FactA: a.ID, FactB: b.ID, ExpectedRevisionA: 2, ExpectedRevisionB: 1, Reason: "Reviewed passages make incompatible statements about the same rollback."}
	f.want(f.post("/api/v1/memory/conflicts", productionJSON(t, conflict)), 201, "")
	f.want(f.post("/api/v1/memory/conflicts", productionJSON(t, conflict)), 200, "")
	if f.auditCount("memory.conflict.recorded") != 1 {
		t.Fatal("retry duplicated conflict audit")
	}
	f.want(f.post("/api/v1/memory/conflicts/resolve", productionJSON(t, map[string]string{"keep": a.ID, "drop": b.ID})), 200, "")
	if state, _ := f.state(b.ID); state != "rejected" {
		t.Fatal("conflict rejection missing")
	}
	// Same reviewed source cannot resurrect an intentionally rejected candidate.
	w = f.post("/api/v1/memory/facts", productionJSON(t, productionInput{src.DocumentID, src.Version, src.ContentHash, b.Fact}))
	f.want(w, 200, "")
	if !strings.Contains(w.Body.String(), `"state":"rejected"`) {
		t.Fatal("rejected fact resurrected")
	}
	version := sourceVersionInput{src.Version, src.ContentHash}
	f.want(f.post("/api/v1/memory/sources/"+src.DocumentID+"/withdraw", productionJSON(t, version)), 200, "")
	if state, _ := f.state(a.ID); state != "expired" {
		t.Fatal("withdrawal did not expire confirmed fact")
	}
	f.want(f.post("/api/v1/memory/sources/"+src.DocumentID+"/withdraw", productionJSON(t, version)), 200, "")
	if f.auditCount("memory.source.withdrawn") != 1 {
		t.Fatal("withdrawal retry duplicated audit")
	}
	f.want(f.post("/api/v1/memory/facts", productionJSON(t, in)), 409, "conflict")
	w = f.call("GET", "/api/v1/memory", "", "dev", "actions-token")
	f.want(w, 200, "")
	if strings.Contains(w.Body.String(), a.Fact) {
		t.Fatal("withdrawn text leaked")
	}
}
func TestMemoryProductionVersionsAuthorityAndRollbackPostgres(t *testing.T) {
	f := productionFixture(t)
	src := publishTestSource(t, f, "source-auth-key")
	input := productionInput{src.DocumentID, src.Version, src.ContentHash, "Latency normalized."}
	stale := input
	stale.ExpectedVersion++
	f.want(f.post("/api/v1/memory/facts", productionJSON(t, stale)), 409, "conflict")
	invented := input
	invented.ExactExcerpt = "Invented fact"
	f.want(f.post("/api/v1/memory/facts", productionJSON(t, invented)), 422, "invalid_memory_input")
	f.want(f.call("POST", "/api/v1/memory/facts", productionJSON(t, input), "stage", "actions-token"), 404, "not_found")
	f.want(f.call("POST", "/api/v1/memory/facts", productionJSON(t, input), "all", "actions-token"), 409, "")
	f.exec(`UPDATE users SET v3_identity_kind='machine' WHERE id='editor'`)
	f.want(f.post("/api/v1/memory/facts", productionJSON(t, input)), 403, "forbidden")
	f.exec(`UPDATE users SET v3_identity_kind='human' WHERE id='editor'`)
	f.exec(`DELETE FROM v3_effective_role_grants WHERE user_id='editor' AND module='knowledge' AND action='view'`)
	f.want(f.post("/api/v1/memory/facts", productionJSON(t, input)), 403, "forbidden")
	f.exec(`INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('editor','knowledge','view','application','app'),('editor','knowledge','view','team','ops')`)
	f.exec(`CREATE FUNCTION reject_memory_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'memory.%' THEN RAISE EXCEPTION 'synthetic audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_memory_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_memory_audit()`)
	f.want(f.post("/api/v1/memory/facts", productionJSON(t, input)), 503, "memory_unavailable")
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_memory_facts WHERE source_document_id=$1`, src.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit rollback leaked fact %d %v", count, err)
	}
	f.exec(`DROP TRIGGER reject_memory_audit ON management_audit`)
	fact := produceTestFact(t, f, src, input.ExactExcerpt)
	// A legitimate newer version makes old facts non-current without deleting
	// historical decisions or expiring unrelated newer-version facts.
	f.exec(`UPDATE knowledge_documents SET version=2,body='New version: Latency normalized.',content_hash=$2 WHERE id=$1`, src.DocumentID, store.Hash("New version: Latency normalized."))
	f.want(f.post("/api/v1/memory/facts/"+fact.ID+"/confirm", ""), 409, "conflict")
	f.want(f.post("/api/v1/memory/sources/"+src.DocumentID+"/withdraw", productionJSON(t, sourceVersionInput{1, src.ContentHash})), 409, "conflict")
	w := f.call("GET", "/api/v1/memory", "", "dev", "actions-token")
	f.want(w, 200, "")
	if strings.Contains(w.Body.String(), fact.ID) {
		t.Fatal("stale version remained visible")
	}
	if state, _ := f.state(fact.ID); state != "candidate" {
		t.Fatal("source version change rewrote historical decision")
	}
}
func TestMemorySourcePublicationIdempotencyAndAuditPostgres(t *testing.T) {
	f := productionFixture(t)
	in := sourceInput{RequestKey: "source-idempotency", ApplicationID: "app", Title: "Title", Body: "Reviewed human statement."}
	body := productionJSON(t, in)
	w := f.post("/api/v1/memory/sources", body)
	f.want(w, 201, "")
	var src sourceResult
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	f.want(f.post("/api/v1/memory/sources", body), 200, "")
	in.Body = "Changed statement"
	f.want(f.post("/api/v1/memory/sources", productionJSON(t, in)), 409, "conflict")
	if f.auditCount("memory.source.published") != 1 {
		t.Fatal("publish duplicated audit")
	}
	f.exec(`CREATE FUNCTION reject_source_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='memory.source.published' THEN RAISE EXCEPTION 'synthetic source audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_source_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_source_audit()`)
	in.RequestKey = "audit-failure-new-key"
	f.want(f.post("/api/v1/memory/sources", productionJSON(t, in)), 503, "memory_unavailable")
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM knowledge_documents WHERE source LIKE 'memory://manual/%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source audit rollback: %d %v", count, err)
	}
}
func TestMemoryProductionRechecksSessionAfterLockPostgres(t *testing.T) {
	f := productionFixture(t)
	src := publishTestSource(t, f, "source-session-lock")
	blocker, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(t.Context())
	var pid int
	if err = blocker.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(t.Context(), `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,0))`, "v3-memory-source:"+src.DocumentID); err != nil {
		t.Fatal(err)
	}
	body := productionJSON(t, productionInput{src.DocumentID, src.Version, src.ContentHash, "Latency normalized."})
	r := httptest.NewRequest("POST", "/api/v1/memory/facts", strings.NewReader(body))
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "actions-token"})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { w := httptest.NewRecorder(); f.api.ServeHTTP(w, r); done <- w }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = f.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no real source lock waiter")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = blocker.Exec(t.Context(), `DELETE FROM sessions WHERE token_hash=$1`, store.Hash("actions-token")); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		f.want(w, 401, "unauthorized")
	case <-time.After(5 * time.Second):
		t.Fatal("write did not stop after session revocation")
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_memory_facts WHERE source_document_id=$1`, src.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked request wrote %d %v", count, err)
	}
}
