package v3knowledge

import (
	"encoding/json"
	"testing"

	"github.com/wans/aiops-canvas/internal/knowledge"
)

// PA-222: a hit whose corpus has no vector index, or a search that read no
// vectors, has no vector score. The response says so with vector=null and
// names the hit's corpus; it never reports such a hit as vector 0.
func TestRetrievalHitsWithoutVectorScoreSayNull(t *testing.T) {
	keyword := []knowledge.Hit{
		{DocumentID: "doc", Version: 1, Chunk: 1, Title: "document", Content: "document passage", Similarity: 4},
		{DocumentID: publicWebHitPrefix + "web-1", Version: 1, Chunk: 0, Title: "web page", Content: "web passage", Similarity: 3},
		{DocumentID: uploadSourceHitPrefix + "upload-1", Version: 1, Chunk: 0, Title: "upload", Content: "upload passage", Similarity: 2},
	}
	vector := []knowledge.Hit{{DocumentID: "doc", Version: 1, Chunk: 1, Title: "document", Content: "document passage", Similarity: .7}}
	decode := func(t *testing.T, hits []RetrievalHit) map[string]map[string]any {
		t.Helper()
		raw, err := json.Marshal(hits)
		if err != nil {
			t.Fatal(err)
		}
		var list []map[string]any
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatal(err)
		}
		byTitle := map[string]map[string]any{}
		for _, hit := range list {
			byTitle[hit["title"].(string)] = hit
		}
		return byTitle
	}
	hasNullVector := func(hit map[string]any) bool {
		value, present := hit["vector"]
		return present && value == nil
	}

	hybrid := decode(t, rankRetrieval(keyword, vector, 5, false))
	if got := hybrid["document"]; got["vector"] != .7 || got["corpus"] != "documents" {
		t.Fatalf("document hit lost its vector score or corpus: %v", got)
	}
	for title, corpus := range map[string]string{"web page": "public_web_sources", "upload": "upload_sources"} {
		got := hybrid[title]
		if !hasNullVector(got) || got["corpus"] != corpus {
			t.Fatalf("%s hit must have vector=null and corpus=%s: %v", title, corpus, got)
		}
	}

	degraded := decode(t, rankRetrieval(keyword, nil, 5, true))
	for title, corpus := range map[string]string{"document": "documents", "web page": "public_web_sources", "upload": "upload_sources"} {
		got := degraded[title]
		if !hasNullVector(got) || got["corpus"] != corpus {
			t.Fatalf("keyword-only %s hit must have vector=null and corpus=%s: %v", title, corpus, got)
		}
	}
}
