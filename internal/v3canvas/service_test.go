package v3canvas

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTemplateSanitizationDropsCanvasContent(t *testing.T) {
	original := []Node{{ID: "branch", X: 20, Y: 30, W: 240, Icon: "alert", Tone: "warn", Title: "Secret incident title", Sub: "Private note", Blocks: []json.RawMessage{json.RawMessage(`{"type":"text","text":"private evidence"}`)}}}
	saved := sanitizeNodes(original)
	if len(saved) != 1 || saved[0].ID != "node-1" || saved[0].X != 20 || saved[0].W != 240 || len(saved[0].Blocks) != 0 {
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

func TestTemplateGraphRetainsOnlyBoundedStructure(t *testing.T) {
	nodes := []Node{
		{ID: "secret-incident-id", X: 10, Y: 20, W: 260, Icon: "bad secret", Tone: "private", Title: "incident secret", Blocks: []json.RawMessage{json.RawMessage(`{"text":"evidence secret"}`)}},
		{ID: "branch-1", X: 300, Y: 20, W: 260, Icon: "alert", Tone: "warn", Title: "customer detail"},
	}
	edges := []Edge{{From: "secret-incident-id", To: "branch-1", Tone: "secret", Dash: true, Live: true}}
	gotNodes, gotEdges, err := sanitizeTemplateStructure(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	if gotNodes[0].ID != "node-1" || gotNodes[1].ID != "node-2" ||
		gotNodes[0].Icon != "layers" || gotNodes[0].Tone != "info" ||
		gotNodes[1].Icon != "alert" || gotNodes[1].Tone != "warn" ||
		gotEdges[0].From != "node-1" || gotEdges[0].To != "node-2" ||
		gotEdges[0].Tone != "" || gotEdges[0].Live || !gotEdges[0].Dash {
		t.Fatalf("unsafe or lost structure: nodes=%+v edges=%+v", gotNodes, gotEdges)
	}
	encoded := string(mustJSON(struct {
		Nodes []Node
		Edges []Edge
	}{gotNodes, gotEdges}))
	for _, secret := range []string{"incident secret", "evidence secret", "customer detail", "bad secret", "private", "secret-incident-id"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("template retained %q", secret)
		}
	}
}

func TestTemplateGraphRejectsInvalidReferencesAndIdentifiers(t *testing.T) {
	base := []Node{{ID: "root", W: 260}}
	for _, tc := range []struct {
		nodes []Node
		edges []Edge
	}{
		{base, []Edge{{From: "root", To: "missing"}}},
		{[]Node{{ID: "root", W: 260}, {ID: "root", W: 260}}, nil},
		{[]Node{{ID: "private data", W: 260}}, nil},
		{[]Node{{ID: "root", W: 0}}, nil},
	} {
		if _, _, err := sanitizeTemplateStructure(tc.nodes, tc.edges); err == nil {
			t.Fatalf("accepted unsafe graph: %+v", tc)
		}
	}
}

func TestTemplateCountFollowsRequestedLocale(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/canvas-templates", nil)
	if got := nodeCountLabel(r, 2); got != "2 个节点" {
		t.Fatal(got)
	}
	r.Header.Set("Accept-Language", "en-US")
	if got := nodeCountLabel(r, 1); got != "1 node" {
		t.Fatal(got)
	}
	if got := nodeCountLabel(r, 2); got != "2 nodes" {
		t.Fatal(got)
	}
}
