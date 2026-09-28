package v3canvas

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTemplateSanitizationDropsCanvasContent(t *testing.T) {
	original := []Node{{ID: "branch", X: 20, Y: 30, W: 240, Icon: "alert", Tone: "warn", Title: "Secret incident title", Sub: "Private note", Blocks: []json.RawMessage{json.RawMessage(`{"type":"text","text":"private evidence"}`)}}}
	saved := sanitizeNodes(original)
	if len(saved) != 1 || saved[0].ID != "branch" || saved[0].X != 20 || saved[0].W != 240 || len(saved[0].Blocks) != 0 {
		t.Fatalf("structure was not retained safely: %#v", saved)
	}
	encoded := string(mustJSON(saved))
	for _, s := range []string{"Secret incident title", "Private note", "private evidence"} {
		if strings.Contains(encoded, s) {
			t.Fatalf("template leaked canvas content %q", s)
		}
	}
	if original[0].Title != "Secret incident title" {
		t.Fatal("sanitization changed the source canvas")
	}
}

func TestSummaryHasRealCountsAndNoInventedParticipants(t *testing.T) {
	at := time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC)
	s := publicSummary("CV-1", "Review", "review", "draft", "", nil, at, []Node{{ID: "root"}})
	if s.Meta != "1 个节点" || len(s.Participants) != 0 || len(s.Services) != 0 || s.Href != "/canvas/CV-1" || len(s.Thumb.Nodes) != 1 {
		t.Fatalf("incorrect summary: %#v", s)
	}
}

func TestServiceIDsRejectDuplicatesAndInvalidValues(t *testing.T) {
	for _, ids := range [][]string{{"a", "a"}, {""}, {" a"}, {strings.Repeat("x", 201)}} {
		if validStrings(ids) {
			t.Fatalf("accepted invalid services: %#v", ids)
		}
	}
	if !validStrings(nil) || !validStrings([]string{"checkout", "payment"}) {
		t.Fatal("rejected valid service IDs")
	}
}
