package resources

import (
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/destinywans/redstring/internal/store"
)

func exploreMemory(t *testing.T, rows []Resource, edges []Relation, q url.Values) GraphExploration {
	t.Helper()
	options, err := parseGraphOptions(q)
	if err != nil {
		t.Fatal(err)
	}
	result, err := exploreGraph(rows, edges, q, options)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestGraphExploreDirectionPagingAndRoot(t *testing.T) {
	rows := []Resource{{ID: "root", Name: "z root"}, {ID: "a", Name: "a"}, {ID: "b", Name: "b"}, {ID: "c", Name: "c"}}
	edges := []Relation{{ID: "ra", SourceResourceID: "root", TargetResourceID: "a"}, {ID: "br", SourceResourceID: "b", TargetResourceID: "root"}, {ID: "rc", SourceResourceID: "root", TargetResourceID: "c"}, {ID: "ac", SourceResourceID: "a", TargetResourceID: "c"}}
	q := url.Values{"mode": {"neighbors"}, "root_id": {"root"}, "direction": {"out"}, "limit": {"1"}}
	first := exploreMemory(t, rows, edges, q)
	if first.Total != 2 || len(first.Nodes) != 2 || first.Nodes[0].ID != "root" || first.Nodes[1].ID != "a" || first.Remaining != 1 || first.NextOffset == nil || *first.NextOffset != 1 {
		t.Fatalf("bad first neighborhood page: %+v", first)
	}
	q.Set("offset", "1")
	second := exploreMemory(t, rows, edges, q)
	if second.Nodes[0].ID != "root" || second.Nodes[1].ID != "c" || second.Remaining != 0 || second.NextOffset != nil || len(second.Edges) != 1 {
		t.Fatalf("bad second page: %+v", second)
	}
	q.Set("offset", "0")
	q.Set("direction", "in")
	incoming := exploreMemory(t, rows, edges, q)
	if incoming.Total != 1 || incoming.Nodes[1].ID != "b" {
		t.Fatal("incoming direction included outgoing neighbor")
	}
	q.Set("direction", "both")
	q.Set("limit", "30")
	both := exploreMemory(t, rows, edges, q)
	if both.Total != 3 || len(both.Edges) != 4 {
		t.Fatal("all real edges among the returned nodes must remain visible")
	}
	q.Set("offset", "100")
	empty := exploreMemory(t, rows, edges, q)
	if len(empty.Nodes) != 1 || empty.Nodes[0].ID != "root" || empty.Remaining != 0 {
		t.Fatal("out of range page must retain root without negative remaining")
	}
}

func TestGraphExploreStrictFiltersAndSearch(t *testing.T) {
	rows := []Resource{
		{ID: "root", Name: "root", Kind: "node", SourceKind: "kubernetes"},
		{ID: "match", Name: "needle", Kind: "node", SourceKind: "kubernetes"},
		{ID: "wrong-kind", Name: "needle", Kind: "redis", SourceKind: "kubernetes"},
		{ID: "wrong-source", Name: "needle", Kind: "node", SourceKind: "docker"},
		{ID: "far", Name: "needle", Kind: "node", SourceKind: "kubernetes"},
	}
	edges := []Relation{{ID: "rm", SourceResourceID: "root", TargetResourceID: "match", Kind: "belongs_to"}, {ID: "rk", SourceResourceID: "root", TargetResourceID: "wrong-kind", Kind: "belongs_to"}, {ID: "rs", SourceResourceID: "root", TargetResourceID: "wrong-source", Kind: "belongs_to"}, {ID: "mf", SourceResourceID: "match", TargetResourceID: "far", Kind: "belongs_to"}}
	q := url.Values{"mode": {"neighbors"}, "root_id": {"root"}, "q": {"needle"}, "source_kind": {"kubernetes"}, "resource_kind": {"node"}}
	got := exploreMemory(t, rows, edges, q)
	if got.Total != 1 || len(got.Nodes) != 2 || got.Nodes[1].ID != "match" || len(got.Edges) != 1 {
		t.Fatalf("filters leaked or search expanded: %+v", got)
	}
	q.Set("resource_kind", "redis")
	options, _ := parseGraphOptions(q)
	if _, err := exploreGraph(rows, edges, q, options); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("filtered root must not bypass strict resource selection")
	}
	q = url.Values{"q": {"root"}}
	legacy := buildGraph(rows, edges, q, 3)
	if len(legacy.Nodes) != 1 || len(legacy.Edges) != 0 {
		t.Fatal("legacy search brought unrelated neighbors into results")
	}
}

func TestGraphOverviewGroupsUseSourceAndNamespaceIdentity(t *testing.T) {
	row := func(id, source, namespace string) Resource {
		return Resource{ID: id, Name: id, Kind: "deployment", SourceKind: "kubernetes", SourceID: source, Attributes: map[string]any{"cluster": source, "namespace": namespace}}
	}
	rows := []Resource{row("one", "cluster-a", "payments"), row("two", "cluster-b", "payments"), row("three", "cluster-a", "payments"), row("four", "cluster-a", "data")}
	edges := []Relation{{ID: "within", SourceResourceID: "one", TargetResourceID: "three", Kind: "depends_on"}, {ID: "cross1", SourceResourceID: "one", TargetResourceID: "four", Kind: "depends_on"}, {ID: "cross2", SourceResourceID: "three", TargetResourceID: "four", Kind: "depends_on"}, {ID: "invisible", SourceResourceID: "four", TargetResourceID: "not-authorized", Kind: "depends_on"}}
	q := url.Values{"mode": {"overview"}}
	got := exploreMemory(t, rows, edges, q)
	if len(got.Nodes) != 0 || len(got.Edges) != 0 || got.Total != 4 || len(got.Groups) != 3 || len(got.GroupEdges) != 1 || got.GroupEdges[0].Count != 2 || !got.GroupEdges[0].Virtual {
		t.Fatalf("incorrect group projection: %+v", got)
	}
	var group ResourceGroup
	for _, candidate := range got.Groups {
		if candidate.SourceID == "cluster-a" && candidate.Namespace == "payments" {
			group = candidate
		}
	}
	if group.Count != 2 || !group.Virtual {
		t.Fatal("same namespace from separate clusters was merged")
	}
	q = url.Values{"group_id": {group.ID}, "limit": {"1"}}
	page := exploreMemory(t, rows, edges, q)
	if page.Mode != "neighbors" || page.Total != 2 || page.Remaining != 1 || len(page.Nodes) != 1 || page.RootID != "" {
		t.Fatalf("bad group page: %+v", page)
	}
	q.Set("q", "no-match")
	page = exploreMemory(t, rows, edges, q)
	if page.Total != 0 || len(page.Nodes) != 0 {
		t.Fatal("group search must not fetch arbitrary neighbors")
	}
	q.Set("source_kind", "docker")
	options, _ := parseGraphOptions(q)
	if _, err := exploreGraph(rows, edges, q, options); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("opaque group ID bypassed filters")
	}
}

func TestGraphRuntimeOptInExcludesEventsAndMissing(t *testing.T) {
	rows := []Resource{{ID: "node", Kind: "node"}, {ID: "pod", Kind: "pod"}, {ID: "container", Kind: "container"}, {ID: "host-container", Kind: "host", Attributes: map[string]any{"resource_type": "Container"}}, {ID: "mysql", Kind: "mysql", Attributes: map[string]any{"resource_type": "Container"}}, {ID: "event", Kind: "event"}, {ID: "missing", Kind: "node", Attributes: map[string]any{"inventory_presence": "missing"}}}
	q := url.Values{"mode": {"overview"}}
	if got := exploreMemory(t, rows, nil, q); got.Total != 2 {
		t.Fatalf("default runtime filtering failed: %+v", got)
	}
	q.Set("include_runtime", "true")
	if got := exploreMemory(t, rows, nil, q); got.Total != 5 {
		t.Fatalf("runtime opt-in included event or missing resource: %+v", got)
	}
}

func TestGraphLegacyRootRetainedBeforeCap(t *testing.T) {
	rows, edges := []Resource{{ID: "root", Name: "zzz root"}}, []Relation{}
	for i := 0; i < 310; i++ {
		id := fmt.Sprintf("%03d", i)
		rows = append(rows, Resource{ID: id, Name: id})
		edges = append(edges, Relation{ID: "edge-" + id, SourceResourceID: "root", TargetResourceID: id})
	}
	got := buildGraph(rows, edges, url.Values{"root_id": {"root"}}, 1)
	if !got.Truncated || len(got.Nodes) != 300 || got.Nodes[0].ID != "root" || len(got.Edges) != 299 {
		t.Fatal("center was dropped by alphabetical truncation")
	}
}

func TestGraphOptionsRejectAmbiguityAndUnboundedReads(t *testing.T) {
	for _, query := range []url.Values{{"mode": {"neighbors"}}, {"mode": {"invalid"}}, {"mode": {"group"}}, {"mode": {"overview"}, "root_id": {"root"}}, {"root_id": {"root"}, "group_id": {"group"}}, {"include_runtime": {"yes"}}, {"limit": {"101"}}, {"offset": {"-1"}}, {"direction": {"sideways"}}} {
		if _, err := parseGraphOptions(query); err == nil {
			t.Fatalf("accepted invalid graph query %v", query)
		}
	}
}

func TestGraphLoadedContextRechecksScopeAndIncludesCrossPageEdges(t *testing.T) {
	rows := []Resource{{ID: "root", Name: "root", SourceKind: "kubernetes"}, {ID: "a", Name: "a", SourceKind: "kubernetes"}, {ID: "b", Name: "b", SourceKind: "kubernetes"}, {ID: "other", Name: "other", SourceKind: "docker"}}
	edges := []Relation{{ID: "ra", SourceResourceID: "root", TargetResourceID: "a"}, {ID: "rb", SourceResourceID: "root", TargetResourceID: "b"}, {ID: "ab", SourceResourceID: "a", TargetResourceID: "b"}}
	q := url.Values{"mode": {"neighbors"}, "root_id": {"root"}, "offset": {"1"}, "limit": {"1"}, "visible_id": {"a", "root", "a"}, "source_kind": {"kubernetes"}}
	got := exploreMemory(t, rows, edges, q)
	if len(got.Nodes) != 3 || len(got.Edges) != 3 || got.Total != 2 || got.Nodes[0].ID != "root" || len(got.PageNodeIDs) != 1 || got.PageNodeIDs[0] != "b" {
		t.Fatalf("missing loaded context edge or incorrect page identities: %+v", got)
	}
	q.Add("visible_id", "other")
	options, _ := parseGraphOptions(q)
	if _, err := exploreGraph(rows, edges, q, options); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("loaded IDs bypassed strict source scope")
	}
	q.Set("visible_id", "not-authorized")
	options, _ = parseGraphOptions(q)
	if _, err := exploreGraph(rows, edges, q, options); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("loaded IDs bypassed authorization")
	}
}

func TestGraphFacetsRemainAvailableAcrossTypeSelections(t *testing.T) {
	rows := []Resource{
		{ID: "a", Name: "a", Kind: "node", SourceKind: "kubernetes"},
		{ID: "b", Name: "b", Kind: "node", SourceKind: "kubernetes"},
		{ID: "c", Name: "c", Kind: "host", SourceKind: "kubernetes"},
		{ID: "hidden-source", Kind: "redis", SourceKind: "docker"},
		{ID: "runtime", Kind: "pod", SourceKind: "kubernetes"},
	}
	edges := []Relation{
		{ID: "ab", SourceResourceID: "a", TargetResourceID: "b", Kind: "contains"},
		{ID: "bc", SourceResourceID: "b", TargetResourceID: "c", Kind: "runs_on"},
		{ID: "hidden", SourceResourceID: "a", TargetResourceID: "hidden-source", Kind: "hidden"},
	}
	q := url.Values{"mode": {"neighbors"}, "root_id": {"a"}, "source_kind": {"kubernetes"}, "resource_kind": {"node"}, "kind": {"contains"}}
	result := exploreMemory(t, rows, edges, q)
	if len(result.Nodes) != 2 || len(result.Edges) != 1 || result.Edges[0].ID != "ab" {
		t.Fatalf("graph no longer respects strict selections: %+v", result.Graph)
	}
	if len(result.Facets.ResourceKinds) != 2 || result.Facets.ResourceKinds[0].Value != "host" || result.Facets.ResourceKinds[1].Value != "node" {
		t.Fatalf("resource facets self-filtered or leaked scope/runtime: %+v", result.Facets.ResourceKinds)
	}
	if len(result.Facets.RelationKinds) != 2 || result.Facets.RelationKinds[0].Value != "contains" || result.Facets.RelationKinds[1].Value != "runs_on" {
		t.Fatalf("relation facets self-filtered or leaked scope: %+v", result.Facets.RelationKinds)
	}
}

func TestGraphGroupContextMayCrossGroupsWithinAuthorizedScope(t *testing.T) {
	rows := []Resource{
		{ID: "a", Name: "a", Kind: "node", SourceKind: "kubernetes", SourceID: "one"},
		{ID: "b", Name: "b", Kind: "node", SourceKind: "kubernetes", SourceID: "one"},
		{ID: "outside", Name: "outside", Kind: "node", SourceKind: "kubernetes", SourceID: "two"},
		{ID: "wrong-source", Name: "wrong-source", Kind: "host", SourceKind: "docker", SourceID: "three"},
	}
	edges := []Relation{{ID: "cross", SourceResourceID: "b", TargetResourceID: "outside", Kind: "depends_on"}}
	q := url.Values{"mode": {"neighbors"}, "group_id": {resourceGraphGroup(rows[0]).ID}, "offset": {"1"}, "limit": {"1"}, "visible_id": {"a", "outside"}, "source_kind": {"kubernetes"}}
	result := exploreMemory(t, rows, edges, q)
	if result.Total != 2 || len(result.PageNodeIDs) != 1 || result.PageNodeIDs[0] != "b" || len(result.Nodes) != 3 || len(result.Edges) != 1 || result.Edges[0].ID != "cross" {
		t.Fatalf("group context lost cross-page edges or changed page membership: %+v", result)
	}
	for _, id := range []string{"wrong-source", "not-authorized"} {
		q.Set("visible_id", id)
		options, _ := parseGraphOptions(q)
		if _, err := exploreGraph(rows, edges, q, options); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("group context bypassed authorized strict scope: %s", id)
		}
	}
}

func TestGraphSearchEligibilityBeforePagination(t *testing.T) {
	rows := []Resource{
		{ID: "node", Kind: "node"},
		{ID: "pod", Kind: "pod"},
		{ID: "container", Kind: "host", Attributes: map[string]any{"resource_type": "Container"}},
		{ID: "event", Kind: "event"},
		{ID: "missing", Kind: "node", Attributes: map[string]any{"inventory_presence": "missing"}},
	}
	if got := graphEligibleRows(rows, false); len(got) != 1 || got[0].ID != "node" {
		t.Fatalf("default search eligibility differs from graph: %+v", got)
	}
	if got := graphEligibleRows(rows, true); len(got) != 3 {
		t.Fatalf("runtime search must include pods/containers but exclude events/missing: %+v", got)
	}
	if enabled, runtime, valid := graphSearchOptions(url.Values{}); enabled || runtime || !valid {
		t.Fatal("catalog default must remain opt-out")
	}
	if enabled, runtime, valid := graphSearchOptions(url.Values{"graph_eligible": {"true"}, "include_runtime": {"true"}}); !enabled || !runtime || !valid {
		t.Fatal("explicit graph search options rejected")
	}
	for _, key := range []string{"graph_eligible", "include_runtime"} {
		if _, _, valid := graphSearchOptions(url.Values{key: {"invalid"}}); valid {
			t.Fatalf("invalid boolean accepted: %s", key)
		}
	}
}
