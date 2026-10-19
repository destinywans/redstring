package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/destinywans/redstring/internal/store"
)

const graphNodeLimit = 300
const graphEdgeLimit = 600

func parseGraphOptions(q url.Values) (graphOptions, error) {
	o := graphOptions{mode: q.Get("mode"), root: q.Get("root_id"), group: q.Get("group_id"), direction: q.Get("direction")}
	if o.mode == "" {
		o.mode = "graph"
	}
	if o.group != "" && (o.mode == "graph" || o.mode == "neighbors") && o.root == "" {
		o.mode = "group"
	}
	if o.direction == "" {
		o.direction = "both"
	}
	if o.direction != "both" && o.direction != "out" && o.direction != "in" {
		return o, store.ErrInvalidReference
	}
	if q.Get("depth") == "" && q.Get("hops") != "" {
		q = cloneGraphQuery(q)
		q.Set("depth", q.Get("hops"))
	}
	var valid bool
	if o.depth, valid = integer(q, "depth", 2, 1, 3); !valid {
		return o, store.ErrInvalidReference
	}
	if o.offset, valid = integer(q, "offset", 0, 0, 1000000); !valid {
		return o, store.ErrInvalidReference
	}
	if o.limit, valid = integer(q, "limit", 30, 1, 100); !valid {
		return o, store.ErrInvalidReference
	}
	switch q.Get("include_runtime") {
	case "", "false":
	case "true":
		o.includeRuntime = true
	default:
		return o, store.ErrInvalidReference
	}
	switch o.mode {
	case "overview":
		if o.root != "" || o.group != "" {
			return o, store.ErrInvalidReference
		}
	case "neighbors":
		if o.root == "" || o.group != "" {
			return o, store.ErrInvalidReference
		}
	case "group":
		if o.group == "" || o.root != "" {
			return o, store.ErrInvalidReference
		}
	case "graph":
		if o.group != "" {
			return o, store.ErrInvalidReference
		}
	default:
		return o, store.ErrInvalidReference
	}
	seen := map[string]bool{}
	for _, id := range values(q, "visible_id") {
		if !seen[id] {
			o.visible = append(o.visible, id)
			seen[id] = true
		}
	}
	if len(o.visible) > graphNodeLimit || len(o.visible) > 0 && (o.mode == "overview" || o.mode == "graph") {
		return o, &graphBudgetError{message: "visible_id 仅用于局部探索，最多 300 个资源"}
	}
	return o, nil
}

func cloneGraphQuery(q url.Values) url.Values {
	out := url.Values{}
	for key, values := range q {
		out[key] = append([]string(nil), values...)
	}
	return out
}

// Keep multi-select options available within the authorized/source scope even
// after selecting one resource or relationship kind.
func graphFacetQuery(q url.Values) url.Values {
	out := cloneGraphQuery(q)
	out.Del("resource_kind")
	out.Del("kind")
	return out
}

func graphRuntimeAllowed(r Resource, include bool) bool {
	if r.Kind == "event" || textAttribute(r, "inventory_presence") == "missing" {
		return false
	}
	isContainer := r.Kind == "container" || r.Kind == "host" && strings.EqualFold(textAttribute(r, "resource_type"), "Container")
	return include || r.Kind != "pod" && !isContainer
}

// Optional catalog search eligibility shares exactly the graph's runtime and
// missing-resource rules, before catalog pagination is applied.
func graphSearchOptions(q url.Values) (enabled, includeRuntime, valid bool) {
	for _, key := range []string{"graph_eligible", "include_runtime"} {
		if value := q.Get(key); value != "" && value != "true" && value != "false" {
			return false, false, false
		}
	}
	return q.Get("graph_eligible") == "true", q.Get("include_runtime") == "true", true
}

func graphEligibleRows(rows []Resource, includeRuntime bool) []Resource {
	out := make([]Resource, 0, len(rows))
	for _, row := range rows {
		if graphRuntimeAllowed(row, includeRuntime) {
			out = append(out, row)
		}
	}
	return out
}

// Query text selects matches within an already authorized and strictly filtered
// candidate set. It is not permission to traverse outside the selected filters.
func graphCandidates(rows []Resource, q url.Values, includeRuntime bool) []Resource {
	strict := cloneGraphQuery(q)
	strict.Del("q")
	out := []Resource{}
	for _, r := range rows {
		if graphRuntimeAllowed(r, includeRuntime) && matches(r, strict, "resource_kind") {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func graphEdges(rows []Resource, edges []Relation, q url.Values) []Relation {
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.ID] = true
	}
	out := []Relation{}
	for _, e := range edges {
		if ids[e.SourceResourceID] && ids[e.TargetResourceID] && accepts(values(q, "kind"), e.Kind) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func newGraphExploration(mode string, rows []Resource, edges []Relation) GraphExploration {
	out := GraphExploration{
		Graph: Graph{Nodes: []Resource{}, Edges: []Relation{}}, Mode: mode,
		Groups: []ResourceGroup{}, GroupEdges: []GroupRelation{},
		Facets:   GraphFacets{ResourceKinds: []Option{}, RelationKinds: []Option{}},
		Limits:   GraphLimits{Resources: 10000, Relationships: 20000, Nodes: graphNodeLimit, Edges: graphEdgeLimit},
		Warnings: []string{}, PageNodeIDs: []string{},
	}
	kindsCount, relationCount := map[string]int{}, map[string]int{}
	for _, r := range rows {
		kindsCount[r.Kind]++
	}
	for _, e := range edges {
		relationCount[e.Kind]++
	}
	out.Facets.ResourceKinds = graphKindOptions(kindsCount)
	for kind, count := range relationCount {
		out.Facets.RelationKinds = append(out.Facets.RelationKinds, Option{Value: kind, Label: kind, Count: count})
	}
	sort.Slice(out.Facets.RelationKinds, func(i, j int) bool { return out.Facets.RelationKinds[i].Value < out.Facets.RelationKinds[j].Value })
	if len(rows) >= out.Limits.Resources {
		out.Warnings = append(out.Warnings, "当前读取已达到 10000 个资源上限；超过此范围的请求会被拒绝，请缩小全局环境。")
	}
	if len(edges) >= out.Limits.Relationships {
		out.Warnings = append(out.Warnings, "当前读取已达到 20000 条关系上限；超过此范围的请求会被拒绝，请缩小资源范围。")
	}
	return out
}

func graphKindOptions(counts map[string]int) []Option {
	out := []Option{}
	for kind, count := range counts {
		label := kinds[kind]
		if label == "" {
			label = kind
		}
		out = append(out, Option{Value: kind, Label: label, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

func resourceGraphGroup(r Resource) ResourceGroup {
	namespace, cluster := "", ""
	if r.SourceKind == "kubernetes" {
		namespace, cluster = textAttribute(r, "namespace"), textAttribute(r, "cluster")
		if cluster == "" {
			cluster = r.SourceID
		}
	}
	key, _ := json.Marshal([]string{r.SourceKind, r.SourceID, namespace})
	digest := sha256.Sum256(key)
	label := textAttribute(r, "sync_source_name")
	if label == "" {
		label = r.SourceID
	}
	if label == "" {
		label = r.SourceKind
	}
	if r.SourceKind == "kubernetes" {
		if namespace != "" {
			label += " · " + namespace
		} else {
			label += " · 集群资源"
		}
	}
	return ResourceGroup{ID: "group-" + hex.EncodeToString(digest[:]), Label: label, Virtual: true, SourceID: r.SourceID, SourceKind: r.SourceKind, Cluster: cluster, Namespace: namespace, ResourceKinds: []Option{}}
}

func groupGraphResources(rows []Resource, edges []Relation) ([]ResourceGroup, []GroupRelation) {
	groups := map[string]ResourceGroup{}
	counts := map[string]map[string]int{}
	membership := map[string]string{}
	for _, r := range rows {
		group := resourceGraphGroup(r)
		if prior, exists := groups[group.ID]; exists {
			group = prior
		} else {
			counts[group.ID] = map[string]int{}
		}
		group.Count++
		counts[group.ID][r.Kind]++
		groups[group.ID], membership[r.ID] = group, group.ID
	}
	out := []ResourceGroup{}
	for _, group := range groups {
		group.ResourceKinds = graphKindOptions(counts[group.ID])
		out = append(out, group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceKind != out[j].SourceKind {
			return out[i].SourceKind < out[j].SourceKind
		}
		if out[i].SourceID != out[j].SourceID {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].Namespace < out[j].Namespace
	})
	type edgeKey struct{ source, target, kind string }
	aggregates := map[edgeKey]int{}
	for _, edge := range edges {
		source, target := membership[edge.SourceResourceID], membership[edge.TargetResourceID]
		if source != "" && target != "" && source != target {
			aggregates[edgeKey{source, target, edge.Kind}]++
		}
	}
	links := []GroupRelation{}
	for key, count := range aggregates {
		links = append(links, GroupRelation{SourceGroupID: key.source, TargetGroupID: key.target, Kind: key.kind, Count: count, Virtual: true})
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].SourceGroupID != links[j].SourceGroupID {
			return links[i].SourceGroupID < links[j].SourceGroupID
		}
		if links[i].TargetGroupID != links[j].TargetGroupID {
			return links[i].TargetGroupID < links[j].TargetGroupID
		}
		return links[i].Kind < links[j].Kind
	})
	return out, links
}

func exploreGraph(rows []Resource, edges []Relation, q url.Values, options graphOptions) (GraphExploration, error) {
	facetQuery := graphFacetQuery(q)
	facetRows := graphCandidates(rows, facetQuery, options.includeRuntime)
	out := newGraphExploration(options.mode, facetRows, graphEdges(facetRows, edges, facetQuery))
	rows = graphCandidates(rows, q, options.includeRuntime)
	edges = graphEdges(rows, edges, q)
	if options.mode == "group" {
		out.Mode = "neighbors"
	}
	out.RootID, out.Offset, out.Limit = options.root, options.offset, options.limit
	var root *Resource
	if options.root != "" {
		for i := range rows {
			if rows[i].ID == options.root {
				root = &rows[i]
				break
			}
		}
		if root == nil {
			return out, store.ErrNotFound
		}
	}
	if options.mode == "graph" {
		out.Graph = buildGraph(rows, edges, q, options.depth)
		out.Total = len(out.Nodes)
		out.Limit = graphNodeLimit
		if out.Truncated {
			out.Warnings = append(out.Warnings, "当前关系图达到显示上限；请使用分组或局部邻居继续探索。")
		}
		return out, nil
	}
	if options.mode == "overview" {
		selected := []Resource{}
		for _, row := range rows {
			if matches(row, q, "resource_kind") {
				selected = append(selected, row)
			}
		}
		out.Groups, out.GroupEdges = groupGraphResources(selected, graphEdges(selected, edges, q))
		out.Total = len(selected)
		// Group summaries also have a display budget. Their count is not confused
		// with the actual resource total; the caller can narrow its resource scope.
		if len(out.Groups) > graphNodeLimit {
			out.Groups, out.Truncated = out.Groups[:graphNodeLimit], true
			kept := map[string]bool{}
			for _, group := range out.Groups {
				kept[group.ID] = true
			}
			links := []GroupRelation{}
			for _, edge := range out.GroupEdges {
				if kept[edge.SourceGroupID] && kept[edge.TargetGroupID] {
					links = append(links, edge)
				}
			}
			out.GroupEdges = links
			out.Warnings = append(out.Warnings, "资源域超过 300 个，当前只显示部分资源域；请缩小来源或资源类型范围。")
		}
		if len(out.GroupEdges) > graphEdgeLimit {
			out.GroupEdges, out.Truncated = out.GroupEdges[:graphEdgeLimit], true
			out.Warnings = append(out.Warnings, "跨资源域关系汇总超过 600 条，当前只显示部分关系。")
		}
		return out, nil
	}
	candidates := []Resource{}
	if options.mode == "group" {
		found := false
		for _, row := range rows {
			if resourceGraphGroup(row).ID == options.group {
				found = true
				if matches(row, q, "resource_kind") {
					candidates = append(candidates, row)
				}
			}
		}
		if !found {
			return out, store.ErrNotFound
		}
	} else {
		adjacent := map[string]bool{}
		for _, edge := range edges {
			if edge.SourceResourceID == options.root && options.direction != "in" {
				adjacent[edge.TargetResourceID] = true
			}
			if edge.TargetResourceID == options.root && options.direction != "out" {
				adjacent[edge.SourceResourceID] = true
			}
		}
		for _, row := range rows {
			if row.ID != options.root && adjacent[row.ID] && matches(row, q, "resource_kind") {
				candidates = append(candidates, row)
			}
		}
		out.Nodes = append(out.Nodes, *root)
	}
	out.Total = len(candidates)
	start := min(options.offset, out.Total)
	end := min(start+options.limit, out.Total)
	for _, row := range candidates[start:end] {
		out.PageNodeIDs = append(out.PageNodeIDs, row.ID)
	}
	byID := map[string]Resource{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	retained := map[string]bool{}
	for _, row := range out.Nodes {
		retained[row.ID] = true
	}
	for _, id := range options.visible {
		row, ok := byID[id]
		if !ok {
			return out, store.ErrNotFound
		}
		if !retained[id] {
			out.Nodes = append(out.Nodes, row)
			retained[id] = true
		}
	}
	for _, row := range candidates[start:end] {
		if !retained[row.ID] {
			out.Nodes = append(out.Nodes, row)
			retained[row.ID] = true
		}
	}
	if len(out.Nodes) > graphNodeLimit {
		return out, &graphBudgetError{message: "已加载与本页资源合计超过 300 个，请先收起部分资源"}
	}
	out.Remaining = out.Total - end
	if out.Remaining > 0 {
		next := end
		out.NextOffset = &next
	}
	out.Truncated = start > 0 || out.Remaining > 0
	out.Edges = graphEdges(out.Nodes, edges, q)
	if len(out.Edges) > graphEdgeLimit {
		out.Edges, out.Truncated = out.Edges[:graphEdgeLimit], true
		out.Warnings = append(out.Warnings, "本页资源之间的关系超过 600 条，当前只显示部分关系。")
	}
	return out, nil
}

// Legacy bounded graph mode preserves its nodes/edges shape while keeping the
// chosen root first, strict filters intact, and nearer resources ahead of limits.
func buildGraph(rows []Resource, edges []Relation, q url.Values, depth int) Graph {
	out := Graph{Nodes: []Resource{}, Edges: []Relation{}}
	rows = graphCandidates(rows, q, q.Get("include_runtime") == "true")
	allowed := graphEdges(rows, edges, q)
	byID := map[string]Resource{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	selected, ordered := map[string]bool{}, []string{}
	root := q.Get("root_id")
	for _, row := range rows {
		if root != "" && row.ID == root || root == "" && matches(row, q, "resource_kind") {
			selected[row.ID], ordered = true, append(ordered, row.ID)
		}
	}
	// A search result without a center stays a search result, not a whole graph.
	if root != "" {
		for hop := 0; hop < depth; hop++ {
			next := map[string]bool{}
			for _, edge := range allowed {
				if selected[edge.SourceResourceID] || selected[edge.TargetResourceID] {
					next[edge.SourceResourceID], next[edge.TargetResourceID] = true, true
				}
			}
			for _, row := range rows {
				if next[row.ID] && !selected[row.ID] && matches(row, q, "resource_kind") {
					selected[row.ID], ordered = true, append(ordered, row.ID)
				}
			}
		}
	}
	if len(ordered) > graphNodeLimit {
		ordered, out.Truncated = ordered[:graphNodeLimit], true
	}
	for _, id := range ordered {
		out.Nodes = append(out.Nodes, byID[id])
	}
	out.Edges = graphEdges(out.Nodes, allowed, q)
	if len(out.Edges) > graphEdgeLimit {
		out.Edges, out.Truncated = out.Edges[:graphEdgeLimit], true
	}
	return out
}
