package v3memory

import (
	"github.com/destinywans/redstring/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMemoryInteractionValidation(t *testing.T) {
	for _, p := range []memoryPolicy{{TTLDays: 0, Proposers: "human"}, {TTLDays: 90, Proposers: "ai"}, {TTLDays: 91, Proposers: "human"}} {
		if validPolicy(p) {
			t.Fatalf("accepted %+v", p)
		}
	}
	if !validPolicy(memoryPolicy{TTLDays: 180, Proposers: "human_ai"}) {
		t.Fatal("valid policy rejected")
	}
	for _, query := range []string{"source=", "confidence=fiction", "confidence=%20high"} {
		_, _, ok := memoryFacets(httptest.NewRequest("GET", "/?"+query, nil))
		if ok {
			t.Fatalf("accepted %s", query)
		}
	}
	sources, conf, ok := memoryFacets(httptest.NewRequest("GET", "/?source=a&source=b&confidence=unknown", nil))
	if !ok || len(sources) != 2 || len(conf) != 1 {
		t.Fatal("facets lost")
	}
}
func TestMemoryPolicyAndFactEditHTTP(t *testing.T) {
	f := productionFixture(t)
	// The policy route is environment governance, independent of application facts.
	f.exec(`INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('editor','knowledge','view','environment','dev'),('editor','knowledge','manage','environment','dev')`)
	proposed := f.call("POST", "/api/v1/memory/facts", `{"document_id":"source","expected_version":2,"expected_content_hash":"`+strings.Repeat("a", 64)+`","exact_excerpt":"Rollback restored traffic."}`, "dev", "actions-token")
	// The fixture uses source-hash; explicitly calibrate a real production hash.
	if proposed.Code != 409 && proposed.Code != 422 {
		t.Fatalf("unmatched hash status %d", proposed.Code)
	}
	hash := store.Hash("Rollback restored traffic. Latency normalized. Error rate decreased.")
	f.exec(`UPDATE knowledge_documents SET content_hash=$1 WHERE id='source'`, hash)
	proposed = f.call("POST", "/api/v1/memory/facts", `{"document_id":"source","expected_version":2,"expected_content_hash":"`+hash+`","exact_excerpt":"Rollback restored traffic."}`, "dev", "actions-token")
	if proposed.Code != 201 {
		t.Fatal(proposed.Code, proposed.Body.String())
	}
	var id string
	if err := f.db.QueryRow(t.Context(), `SELECT id FROM v3_memory_facts`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	w := f.call("PATCH", "/api/v1/memory/facts/"+id, `{"expected_revision":1,"fact":"Latency normalized."}`, "all", "actions-token")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.call("PATCH", "/api/v1/memory/facts/"+id, `{"expected_revision":1,"fact":"Unproven assertion."}`, "dev", "actions-token")
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.call("POST", "/api/v1/memory/facts/"+id+"/confirm", `{}`, "dev", "actions-token")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.call("PATCH", "/api/v1/memory/facts/"+id, `{"expected_revision":2,"fact":"Latency normalized."}`, "dev", "actions-token")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"candidate"`) || !strings.Contains(w.Body.String(), `"confirmed":null`) || !strings.Contains(w.Body.String(), `"source":"source"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	retry := f.call("POST", "/api/v1/memory/facts", `{"document_id":"source","expected_version":2,"expected_content_hash":"`+hash+`","exact_excerpt":"Latency normalized."}`, "dev", "actions-token")
	if retry.Code != 200 || !strings.Contains(retry.Body.String(), `"id":"`+id+`"`) {
		t.Fatal("edited fact lost semantic idempotency", retry.Code, retry.Body.String())
	}
	w = f.call("PATCH", "/api/v1/memory/facts/"+id, `{"expected_revision":2,"fact":"Latency normalized."}`, "dev", "actions-token")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.call("GET", "/api/v1/memory/policy", "", "dev", "actions-token")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":0`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.call("PUT", "/api/v1/memory/policy", `{"expected_revision":0,"ttl_days":30,"proposers":"human_ai"}`, "dev", "actions-token")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ai_production_available":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.call("PUT", "/api/v1/memory/policy", `{"expected_revision":0,"ttl_days":90,"proposers":"human"}`, "dev", "actions-token")
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.exec(`UPDATE v3_memory_facts SET created_at=now()-interval '31 days' WHERE id=$1`, id)
	w = f.call("GET", "/api/v1/memory?source=source&confidence=unknown", "", "dev", "actions-token")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"id":"`+id+`"`) {
		t.Fatal("TTL leaked expired fact", w.Code, w.Body.String())
	}
	w = f.call("POST", "/api/v1/memory/facts/"+id+"/confirm", `{}`, "dev", "actions-token")
	if w.Code != 409 {
		t.Fatal("expired fact renewed", w.Code, w.Body.String())
	}
	f.exec(`UPDATE v3_memory_facts SET created_at=now() WHERE id=$1`, id)
	w = f.call("GET", "/api/v1/memory?confidence=high", "", "dev", "actions-token")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"id":"`+id+`"`) {
		t.Fatal("unknown confidence treated as high", w.Code, w.Body.String())
	}
	var audits int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE outcome='success' AND action IN ('memory.fact.edited','memory.policy.updated')`).Scan(&audits); err != nil || audits != 2 {
		t.Fatal("success audit missing", err, audits)
	}
}
