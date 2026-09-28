package v3knowledge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/knowledge"
)

func TestRunbookReadUnavailableDoesNotClaimMeasuredValues(t *testing.T) {
	w := httptest.NewRecorder()
	writeRunbookReadUnavailable(w)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got, ok := body["error"].(map[string]any)
	if !ok || got["code"] != "runbook_read_unavailable" {
		t.Fatalf("unexpected error body: %#v", body)
	}
	if len(body) != 1 || len(got) != 2 {
		t.Fatalf("response includes unmeasured runbook fields: %#v", body)
	}
}

func TestValidateCreateRejectsUnsafeRunbookInputs(t *testing.T) {
	valid := createInput{Title: "Inspect pool", Source: "blank", Steps: []Step{{Kind: "query", Title: "Read metrics", Readonly: true, Connectors: []string{"prometheus"}}}}
	if err := validateCreate(valid); err != nil {
		t.Fatalf("valid draft: %v", err)
	}
	cases := []createInput{
		{Title: "", Source: "blank"},
		{Title: "Inspect", Source: "unknown"},
		{Title: "Inspect", Source: "postmortem"},
		{Title: "Inspect", Source: "template"},
		{Title: "Inspect", Source: "import"},
		{Title: "Inspect", Source: "blank", From: "fabricated-source"},
		{Title: "Inspect", Source: "blank", Status: "published"},
		{Title: "Inspect", Source: "blank", Steps: []Step{{Kind: "execute", Title: "restart"}}},
	}
	for _, in := range cases {
		if err := validateCreate(in); err == nil {
			t.Errorf("accepted invalid input: %+v", in)
		}
	}
}

func TestDryRunPreviewNeverExecutesAction(t *testing.T) {
	checks, ok := previewSteps([]Step{
		{N: 1, Kind: "query", Title: "Read metrics", Readonly: true, Connectors: []string{"prometheus"}},
		{N: 2, Kind: "action", Title: "Change replica count", Approval: true},
	})
	if ok {
		t.Fatal("action step must prevent a passing read-only preview")
	}
	if checks[0]["validated"] != true || checks[1]["validated"] != false || checks[1]["requires_approval"] != true {
		t.Fatalf("unexpected checks: %#v", checks)
	}
	for _, check := range checks {
		if check["executed"] != false {
			t.Fatalf("preview executed a step: %#v", check)
		}
	}
}

func TestRunbookBodyPreservesSteps(t *testing.T) {
	body := renderBody(createInput{Title: "Recovery", Steps: []Step{{Title: "Inspect", Body: "Compare the current values", Code: "up"}}})
	for _, want := range []string{"# Recovery", "## 1. Inspect", "Compare the current values", "```\nup\n```"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}

func TestRetrievalRankingKeepsPassageIdentityAndTruthfulScores(t *testing.T) {
	keyword := []knowledge.Hit{{DocumentID: "doc", Version: 1, Chunk: 1, Title: "one", Content: "matching phrase", Similarity: 8}, {DocumentID: "doc", Version: 1, Chunk: 2, Title: "two", Content: "other passage", Similarity: 4}}
	vector := []knowledge.Hit{{DocumentID: "doc", Version: 1, Chunk: 1, Title: "one", Content: "matching phrase", Similarity: .8}}
	hits := rankRetrieval(keyword, vector, 2, false)
	if len(hits) != 2 || hits[0].Title != "one" || hits[0].Vector != .8 || hits[1].Vector != 0 {
		t.Fatalf("unexpected hybrid ranking: %#v", hits)
	}
	only := rankRetrieval(keyword, nil, 2, true)
	if only[0].Score != 1 || only[1].Score != .5 {
		t.Fatalf("keyword fallback should use actual normalized keyword score: %#v", only)
	}
}
