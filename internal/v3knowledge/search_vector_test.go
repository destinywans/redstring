package v3knowledge

import (
	"encoding/json"
	"testing"

	"github.com/destinywans/redstring/internal/knowledge"
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

// PA-222: a document passage found only by keyword never entered the vector
// candidates, so its vector similarity was not computed; it is below the
// candidate line, not 0. Its vector is null like any hit without a score.
func TestDocumentHitOutsideVectorCandidatesSaysNull(t *testing.T) {
	keyword := []knowledge.Hit{
		{DocumentID: "doc-a", Version: 1, Chunk: 1, Title: "in candidates", Content: "a", Similarity: 4},
		{DocumentID: "doc-b", Version: 1, Chunk: 1, Title: "keyword only", Content: "b", Similarity: 3},
	}
	vector := []knowledge.Hit{{DocumentID: "doc-a", Version: 1, Chunk: 1, Title: "in candidates", Content: "a", Similarity: .6}}
	raw, err := json.Marshal(rankRetrieval(keyword, vector, 5, false))
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
	if got := byTitle["in candidates"]["vector"]; got != .6 {
		t.Fatalf("a document in the vector candidates keeps its score, got %v", got)
	}
	if value, present := byTitle["keyword only"]["vector"]; !present || value != nil {
		t.Fatalf("a document outside the vector candidates must have vector=null, got %v", byTitle["keyword only"])
	}
}
