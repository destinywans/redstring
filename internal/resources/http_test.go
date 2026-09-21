package resources

import (
	"net/url"
	"testing"
)

func TestGraphHopBoundaryAndEdgeKind(t *testing.T) {
	rows := []Resource{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	edges := []Relation{{ID: "ab", SourceResourceID: "a", TargetResourceID: "b", Kind: "depends_on"}, {ID: "bc", SourceResourceID: "b", TargetResourceID: "c", Kind: "depends_on"}, {ID: "cd", SourceResourceID: "c", TargetResourceID: "d", Kind: "belongs_to"}}
	q := url.Values{"root_id": {"a"}, "kind": {"depends_on"}}
	one := buildGraph(rows, edges, q, 1)
	if len(one.Nodes) != 2 || len(one.Edges) != 1 {
		t.Fatalf("one hop: %#v", one)
	}
	two := buildGraph(rows, edges, q, 2)
	if len(two.Nodes) != 3 || len(two.Edges) != 2 {
		t.Fatalf("two hops: %#v", two)
	}
	three := buildGraph(rows, edges, q, 3)
	if len(three.Nodes) != 3 {
		t.Fatal("relationship type allowed unrelated edge")
	}
}
func TestResourceMultiSelectionAndSearch(t *testing.T) {
	r := Resource{Name: "checkout", Kind: "redis", OwnerUserID: "u1", Applications: []ApplicationLink{{ID: "a1"}}, Attributes: map[string]any{"ip": "10.0.0.7"}}
	q := url.Values{"kind": {"mysql", "redis"}, "owner_id": {"u1", "u2"}, "applications": {"a1"}, "q": {"10.0.0.7"}}
	if !matches(r, q, "kind") {
		t.Fatal("OR within fields should match")
	}
	q.Set("team_id", "other")
	if matches(r, q, "kind") {
		t.Fatal("AND across fields must narrow")
	}
}
func TestFacetsDoNotDoubleCountApplicationAcrossEnvironments(t *testing.T) {
	rows := []Resource{{Kind: "redis", Applications: []ApplicationLink{{ID: "a", Key: "app", EnvironmentID: "e1"}, {ID: "a", Key: "app", EnvironmentID: "e2"}}}}
	f := facets(rows)
	if len(f.Applications) != 1 || f.Applications[0].Count != 1 {
		t.Fatalf("unexpected facets %#v", f)
	}
}
func TestGraphCapNeverReturnsDanglingEdges(t *testing.T) {
	rows := make([]Resource, 301)
	for i := range rows {
		rows[i].ID = string(rune(1000 + i))
	}
	g := buildGraph(rows, []Relation{{ID: "outside", SourceResourceID: rows[0].ID, TargetResourceID: rows[300].ID}}, url.Values{}, 1)
	if !g.Truncated || len(g.Nodes) != 300 || len(g.Edges) != 0 {
		t.Fatal("cap produced dangling edge or hid truncation")
	}
}
