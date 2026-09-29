package v3topology

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// fullTopology mirrors the frozen V3 Topology DTO. Geometry and labels are
// presentation calculations; health, impact and traffic come from observations.
type fullTopology struct {
	Groups []topologyGroup   `json:"groups"`
	Nodes  []topologyNode    `json:"nodes"`
	Edges  []topologyEdge    `json:"edges"`
	Blast  string            `json:"blast"`
	Tree   []topologyTreeRow `json:"tree"`
	Sankey topologySankey    `json:"sankey"`
	Detail topologyDetail    `json:"detail"`
}

type topologyGroup struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Zone  string `json:"zone"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
}

type topologyNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Brand    string `json:"brand,omitempty"`
	Sub      string `json:"sub"`
	Status   string `json:"status"`
	Group    string `json:"group"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Impacted bool   `json:"impacted"`
}

type topologyEdge struct {
	ID    string  `json:"id"`
	From  string  `json:"from"`
	To    string  `json:"to"`
	Label string  `json:"label"`
	Tone  string  `json:"tone"`
	Fast  bool    `json:"fast"`
	Width float64 `json:"width"`
	Path  string  `json:"path"`
	LX    float64 `json:"lx"`
	LY    float64 `json:"ly"`
}

type topologyTreeRow struct {
	Depth    int      `json:"depth"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Brand    string   `json:"brand,omitempty"`
	Status   string   `json:"status"`
	Replicas string   `json:"replicas"`
	CPU      *float64 `json:"cpu,omitempty"`
	Mem      *float64 `json:"mem,omitempty"`
	Open     *bool    `json:"open,omitempty"`
	Hit      bool     `json:"hit,omitempty"`
}

type topologySankey struct {
	Cols  [][]topologySankeyNode `json:"cols"`
	Links []topologySankeyLink   `json:"links"`
}

type topologySankeyNode struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Color string  `json:"color"`
	Value float64 `json:"value"`
}

type topologySankeyLink struct {
	From  string  `json:"from"`
	To    string  `json:"to"`
	Color string  `json:"color"`
	Value float64 `json:"value"`
}

type topologyDetail struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Meta        string             `json:"meta"`
	Brand       string             `json:"brand"`
	Conns       topologyConnection `json:"conns"`
	FromService topologyCaller     `json:"from_service"`
	Wait        []float64          `json:"wait"`
	Health      []topologyHealth   `json:"health"`
	Owner       string             `json:"owner"`
	Dependents  string             `json:"dependents"`
	Source      string             `json:"source"`
}

type topologyConnection struct {
	Used int `json:"used"`
	Max  int `json:"max"`
}
type topologyCaller struct {
	Used    int    `json:"used"`
	Max     int    `json:"max"`
	Service string `json:"service"`
}
type topologyHealth struct {
	K string `json:"k"`
	V string `json:"v"`
}

func projectFullTopology(nodes map[string]node, edges []edge, obs topologyObservation, center, groupBy string) (fullTopology, error) {
	var out fullTopology
	if len(nodes) == 0 || center == "" || obs.CapturedAt.IsZero() || len(nodes) > 300 || len(edges) > 600 {
		return out, errTopologyIncomplete
	}
	if groupBy == "" {
		groupBy = "cluster"
	}
	if groupBy != "cluster" && groupBy != "namespace" && groupBy != "zone" {
		return out, errTopologyIncomplete
	}
	centerNode, ok := nodes[center]
	if !ok {
		return out, errTopologyIncomplete
	}
	if len(obs.Nodes) != len(nodes) || len(obs.Edges) != len(edges) {
		return out, errTopologyIncomplete
	}
	for id := range obs.Nodes {
		if _, ok := nodes[id]; !ok {
			return out, errTopologyIncomplete
		}
	}
	groups := map[string][]string{}
	for id, n := range nodes {
		if id == "" || id != n.ID || len(n.EnvironmentIDs) == 0 || n.Name == "" || n.Kind == "" {
			return out, errTopologyIncomplete
		}
		o, ok := obs.Nodes[id]
		if !ok || !validTopologyStatus(o.Status) || !validPercent(o.CPU) || !validPercent(o.Mem) || !validReplicas(o.Ready, o.Desired) {
			return out, errTopologyIncomplete
		}
		groups[topologyGroupValue(n, groupBy)] = append(groups[topologyGroupValue(n, groupBy)], id)
	}
	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		sort.Strings(groups[name])
	}
	out.Groups = make([]topologyGroup, 0, len(groupNames))
	out.Nodes = make([]topologyNode, 0, len(nodes))
	byID := map[string]topologyNode{}
	// Keep each group inside the current 880px SVG width. Vertical overflow is
	// rejected; the frontend's SVG does not grow with the computed bounds.
	x, y, rowH := 20, 20, 0
	for _, name := range groupNames {
		if x+200 > 860 {
			x, y, rowH = 20, y+rowH+20, 0
		}
		h := 44 + 68*len(groups[name])
		if h > rowH {
			rowH = h
		}
		if y+h > 740 {
			return fullTopology{}, errTopologyIncomplete
		}
		id := "g:" + groupBy + ":" + name
		zones := map[string]bool{}
		for _, nid := range groups[name] {
			zones[explicitLabel(nodes[nid].Zone)] = true
		}
		zoneNames := make([]string, 0, len(zones))
		for z := range zones {
			zoneNames = append(zoneNames, z)
		}
		sort.Strings(zoneNames)
		g := topologyGroup{ID: id, Label: name, Zone: " · " + strings.Join(zoneNames, " / "), X: x, Y: y, W: 200, H: h}
		out.Groups = append(out.Groups, g)
		for i, nid := range groups[name] {
			n, o := nodes[nid], obs.Nodes[nid]
			sub := n.Kind
			if n.Namespace != "" {
				sub += " · " + n.Namespace
			}
			item := topologyNode{ID: nid, Name: n.Name, Brand: n.SourceKind, Sub: sub, Status: o.Status, Group: id, X: x + 14, Y: y + 32 + 68*i, Impacted: o.Impacted}
			out.Nodes = append(out.Nodes, item)
			byID[nid] = item
		}
		x += 270
	}
	out.Edges = make([]topologyEdge, 0, len(edges))
	seenEdges := map[string]bool{}
	for _, e := range edges {
		from, aok := byID[e.From]
		to, bok := byID[e.To]
		o, ook := obs.Edges[e.ID]
		// The frozen edge shape always renders request rate and error tone.
		// Placement/containment relationships, and zero-request intervals with
		// undefined error percentage, cannot be represented truthfully here.
		if e.ID == "" || seenEdges[e.ID] || !aok || !bok || !ook || e.From == e.To ||
			(e.Kind != "depends_on" && e.Kind != "calls") || o.RPS <= 0 ||
			!sharedEnvironment(nodes[e.From], nodes[e.To]) || !validEdgeObservation(o) {
			return fullTopology{}, errTopologyIncomplete
		}
		seenEdges[e.ID] = true
		path, lx, ly := topologyRoute(from, to)
		tone := topologyErrorTone(o.ErrorPct)
		out.Edges = append(out.Edges, topologyEdge{ID: e.ID, From: e.From, To: e.To, Label: fmt.Sprintf("%.3g/s · %.1f%%", o.RPS, o.ErrorPct), Tone: tone, Fast: o.RPS >= 1000, Width: math.Min(5, 1.5+math.Log10(1+o.RPS)), Path: path, LX: lx, LY: ly})
	}
	sort.Slice(out.Edges, func(i, j int) bool { return out.Edges[i].ID < out.Edges[j].ID })
	for id := range obs.Edges {
		if !seenEdges[id] {
			return fullTopology{}, errTopologyIncomplete
		}
	}
	impacted := 0
	for _, n := range out.Nodes {
		if n.Impacted {
			impacted++
		}
	}
	out.Blast = fmt.Sprintf("%d observed impacted resources / %d visible resources", impacted, len(out.Nodes))
	tree, err := projectTopologyTree(nodes, edges, obs, groups, groupNames, groupBy)
	if err != nil {
		return fullTopology{}, err
	}
	out.Tree = tree
	sankey, err := projectTopologySankey(nodes, edges, obs)
	if err != nil {
		return fullTopology{}, err
	}
	out.Sankey = sankey
	detail, err := projectTopologyDetail(centerNode, nodes, edges, obs)
	if err != nil {
		return fullTopology{}, err
	}
	out.Detail = detail
	return out, nil
}

func topologyGroupValue(n node, by string) string {
	switch by {
	case "namespace":
		return explicitLabel(n.Namespace)
	case "zone":
		return explicitLabel(n.Zone)
	default:
		return explicitLabel(n.Cluster)
	}
}

func explicitLabel(v string) string {
	if strings.TrimSpace(v) == "" {
		return "Unspecified"
	}
	return v
}
func validTopologyStatus(v string) bool { return v == "ok" || v == "warn" || v == "danger" }
func validPercent(p *float64) bool      { return p == nil || finite(*p) && *p >= 0 && *p <= 100 }
func validReplicas(ready, desired *int) bool {
	return (ready == nil && desired == nil) || (ready != nil && desired != nil && *ready >= 0 && *desired >= 0 && *ready <= *desired)
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func validEdgeObservation(v edgeObservation) bool {
	return finite(v.RPS) && finite(v.ErrorPct) && v.RPS >= 0 && v.ErrorPct >= 0 && v.ErrorPct <= 100
}
func sharedEnvironment(a, b node) bool {
	for _, x := range a.EnvironmentIDs {
		for _, y := range b.EnvironmentIDs {
			if x == y {
				return true
			}
		}
	}
	return false
}

// 1% and 5% are presentation thresholds, not incident or causal assertions.
func topologyErrorTone(pct float64) string {
	if pct >= 5 {
		return "danger"
	}
	if pct >= 1 {
		return "warn"
	}
	return "ok"
}
func topologyColor(tone string) string {
	switch tone {
	case "danger":
		return "var(--danger)"
	case "warn":
		return "var(--warn)"
	default:
		return "var(--c2)"
	}
}

func topologyRoute(a, b topologyNode) (string, float64, float64) {
	if a.X == b.X {
		up, down := a, b
		if a.Y > b.Y {
			up, down = b, a
		}
		x := float64(up.X + 86)
		y1 := float64(up.Y + 48)
		y2 := float64(down.Y)
		return fmt.Sprintf("M%g %g L %g %g", x, y1, x, y2), x + 22, (y1 + y2) / 2
	}
	left, right := a, b
	if a.X > b.X {
		left, right = b, a
	}
	x1 := float64(left.X + 172)
	y1 := float64(left.Y + 24)
	x2 := float64(right.X)
	y2 := float64(right.Y + 24)
	mx := (x1 + x2) / 2
	return fmt.Sprintf("M%g %g C %g %g, %g %g, %g %g", x1, y1, mx, y1, mx, y2, x2, y2), mx, (y1 + y2) / 2
}

func projectTopologyTree(nodes map[string]node, edges []edge, obs topologyObservation, groups map[string][]string, groupNames []string, groupBy string) ([]topologyTreeRow, error) {
	parent := map[string]string{}
	parentKind := map[string]string{}
	for _, e := range edges {
		if e.Kind != "belongs_to" && e.Kind != "runs_on" {
			continue
		}
		if _, ok := nodes[e.From]; !ok {
			return nil, errTopologyIncomplete
		}
		if _, ok := nodes[e.To]; !ok {
			return nil, errTopologyIncomplete
		}
		if e.From == e.To {
			return nil, errTopologyIncomplete
		}
		old, oldKind := parent[e.From], parentKind[e.From]
		if oldKind == e.Kind && old != "" && old != e.To {
			return nil, errTopologyIncomplete
		}
		// Kubernetes objects can both belong to a workload and run on a node.
		// Containment is the tree parent; placement is still shown as an edge.
		if old == "" || e.Kind == "belongs_to" || oldKind != "belongs_to" {
			parent[e.From], parentKind[e.From] = e.To, e.Kind
		}
	}
	for id := range nodes {
		seen := map[string]bool{}
		for p := id; p != ""; p = parent[p] {
			if seen[p] {
				return nil, errTopologyIncomplete
			}
			seen[p] = true
		}
	}
	children := map[string][]string{}
	for child, p := range parent {
		if topologyGroupValue(nodes[child], groupBy) == topologyGroupValue(nodes[p], groupBy) {
			children[p] = append(children[p], child)
		}
	}
	for p := range children {
		sort.Strings(children[p])
	}
	out := make([]topologyTreeRow, 0, len(nodes)+len(groups))
	visited := map[string]bool{}
	active := map[string]bool{}
	var walk func(string, int) error
	walk = func(id string, depth int) error {
		if active[id] || visited[id] {
			return errTopologyIncomplete
		}
		active[id] = true
		n, o := nodes[id], obs.Nodes[id]
		replicas := "N/A"
		if o.Ready != nil {
			replicas = strconv.Itoa(*o.Ready) + "/" + strconv.Itoa(*o.Desired)
		}
		var open *bool
		if len(children[id]) > 0 {
			v := true
			open = &v
		}
		out = append(out, topologyTreeRow{Depth: depth, Kind: n.Kind, Name: n.Name, Brand: n.SourceKind, Status: o.Status, Replicas: replicas, CPU: o.CPU, Mem: o.Mem, Open: open, Hit: o.Impacted})
		visited[id] = true
		for _, child := range children[id] {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		delete(active, id)
		return nil
	}
	for _, g := range groupNames {
		status := "ok"
		hit := false
		for _, id := range groups[g] {
			o := obs.Nodes[id]
			if o.Status == "danger" || o.Status == "warn" && status == "ok" {
				status = o.Status
			}
			hit = hit || o.Impacted
		}
		open := true
		out = append(out, topologyTreeRow{Depth: 0, Kind: "Group", Name: g, Status: status, Replicas: "N/A", Open: &open, Hit: hit})
		for _, id := range groups[g] {
			if p := parent[id]; p != "" && topologyGroupValue(nodes[p], groupBy) == g {
				continue
			}
			if err := walk(id, 1); err != nil {
				return nil, err
			}
		}
	}
	if len(visited) != len(nodes) {
		return nil, errTopologyIncomplete
	}
	return out, nil
}

func projectTopologySankey(nodes map[string]node, edges []edge, obs topologyObservation) (topologySankey, error) {
	var out topologySankey
	sources := map[string]float64{}
	targets := map[string]float64{}
	pairs := map[string]bool{}
	for _, e := range edges {
		rps := obs.Edges[e.ID].RPS
		if rps <= 0 {
			continue
		}
		pair := e.From + "\x00" + e.To
		if pairs[pair] {
			return topologySankey{}, errTopologyIncomplete
		}
		pairs[pair] = true
		sources[e.From] += rps
		targets[e.To] += rps
		if !finite(sources[e.From]) || !finite(targets[e.To]) {
			return topologySankey{}, errTopologyIncomplete
		}
		out.Links = append(out.Links, topologySankeyLink{From: "src:" + e.From, To: "dst:" + e.To, Value: rps, Color: topologyColor(topologyErrorTone(obs.Edges[e.ID].ErrorPct))})
	}
	if len(out.Links) == 0 {
		return topologySankey{}, errTopologyIncomplete
	}
	sort.Slice(out.Links, func(i, j int) bool {
		if out.Links[i].From != out.Links[j].From {
			return out.Links[i].From < out.Links[j].From
		}
		return out.Links[i].To < out.Links[j].To
	})
	out.Cols = make([][]topologySankeyNode, 2)
	for _, col := range []struct {
		values map[string]float64
		prefix string
	}{{sources, "src:"}, {targets, "dst:"}} {
		ids := make([]string, 0, len(col.values))
		for id := range col.values {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		idx := 0
		if col.prefix == "dst:" {
			idx = 1
		}
		for _, id := range ids {
			o := obs.Nodes[id]
			out.Cols[idx] = append(out.Cols[idx], topologySankeyNode{ID: col.prefix + id, Label: nodes[id].Name, Value: col.values[id], Color: topologyColor(o.Status)})
		}
	}
	return out, nil
}

func projectTopologyDetail(center node, nodes map[string]node, edges []edge, obs topologyObservation) (topologyDetail, error) {
	var out topologyDetail
	c := obs.Center
	if c.Max <= 0 || c.Used < 0 || c.Used > c.Max || c.FromMax <= 0 || c.FromUsed < 0 || c.FromUsed > c.FromMax || len(c.Wait) == 0 {
		return out, errTopologyIncomplete
	}
	caller, ok := nodes[c.FromID]
	if !ok {
		return out, errTopologyIncomplete
	}
	for _, v := range c.Wait {
		if !finite(v) || v < 0 {
			return out, errTopologyIncomplete
		}
	}
	dependent := map[string]bool{}
	for _, e := range edges {
		if e.Kind == "depends_on" && e.To == center.ID {
			dependent[e.From] = true
		}
	}
	n := obs.Nodes[center.ID]
	meta := center.Kind
	if center.Namespace != "" {
		meta += " · " + center.Namespace
	}
	if center.Cluster != "" {
		meta += " · " + center.Cluster
	}
	health := []topologyHealth{{K: "Status", V: n.Status}}
	if n.CPU != nil {
		health = append(health, topologyHealth{K: "CPU", V: fmt.Sprintf("%g%%", *n.CPU)})
	}
	if n.Mem != nil {
		health = append(health, topologyHealth{K: "Memory", V: fmt.Sprintf("%g%%", *n.Mem)})
	}
	source := explicitLabel(center.SourceKind)
	if center.SourceID != "" {
		source += " · " + center.SourceID
	}
	return topologyDetail{ID: center.ID, Name: center.Name, Meta: meta, Brand: center.SourceKind, Conns: topologyConnection{c.Used, c.Max}, FromService: topologyCaller{c.FromUsed, c.FromMax, caller.Name}, Wait: append([]float64(nil), c.Wait...), Health: health, Owner: explicitLabel(center.Owner), Dependents: fmt.Sprintf("%d visible explicit dependents", len(dependent)), Source: source}, nil
}
