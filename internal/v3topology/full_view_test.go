package v3topology

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func projectionFixture() (map[string]node, []edge, topologyObservation) {
	nodes := map[string]node{
		"caller": {ID: "caller", Name: "checkout", Kind: "deployment", Cluster: "prod", Namespace: "sales", Zone: "a", SourceKind: "kubernetes", EnvironmentIDs: []string{"test"}},
		"db":     {ID: "db", Name: "orders-db", Kind: "database", Cluster: "prod", Namespace: "sales", Zone: "b", SourceKind: "kubernetes", SourceID: "collector", Owner: "ops", EnvironmentIDs: []string{"test"}},
		"host":   {ID: "host", Name: "node-1", Kind: "node", Cluster: "prod", Namespace: "", Zone: "b", SourceKind: "kubernetes", EnvironmentIDs: []string{"test"}},
	}
	edges := []edge{
		{ID: "depends", From: "caller", To: "db", Kind: "depends_on", EnvironmentIDs: []string{"test"}},
		{ID: "host-call", From: "db", To: "host", Kind: "calls", EnvironmentIDs: []string{"test"}},
	}
	ready, desired := 2, 3
	cpu, mem := 71.0, 43.0
	obs := topologyObservation{
		CapturedAt: time.Now(),
		Nodes: map[string]nodeObservation{
			"caller": {Status: "warn", Impacted: true, Ready: &ready, Desired: &desired, CPU: &cpu},
			"db":     {Status: "danger", Impacted: true, Mem: &mem},
			"host":   {Status: "ok"},
		},
		Edges: map[string]edgeObservation{
			"depends":   {RPS: 420, ErrorPct: 8.4},
			"host-call": {RPS: 2, ErrorPct: 0},
		},
		Center: centerObservation{Used: 312, Max: 800, FromUsed: 50, FromMax: 100, FromID: "caller", Wait: []float64{12, 18, 21}},
	}
	return nodes, edges, obs
}

func TestProjectFullTopologySourceBoundAndDeterministic(t *testing.T) {
	nodes, edges, obs := projectionFixture()
	first, err := projectFullTopology(nodes, edges, obs, "db", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Groups) != 1 || first.Groups[0].Label != "prod" || first.Groups[0].Zone != " · a / b" {
		t.Fatalf("groups: %#v", first.Groups)
	}
	if first.Blast != "2 observed impacted resources / 3 visible resources" {
		t.Fatalf("blast: %q", first.Blast)
	}
	if first.Detail.ID != "db" || first.Detail.Conns.Used != 312 || first.Detail.FromService.Service != "checkout" || first.Detail.Dependents != "1 visible explicit dependents" || !reflect.DeepEqual(first.Detail.Wait, []float64{12, 18, 21}) {
		t.Fatalf("detail: %#v", first.Detail)
	}
	if len(first.Detail.Health) != 2 || first.Detail.Health[0].V != "danger" || first.Detail.Health[1].V != "43%" {
		t.Fatalf("health: %#v", first.Detail.Health)
	}
	if len(first.Sankey.Cols) != 2 || len(first.Sankey.Links) != 2 || first.Sankey.Links[0].From != "src:caller" || first.Sankey.Links[0].To != "dst:db" || first.Sankey.Links[0].Value != 420 {
		t.Fatalf("sankey: %#v", first.Sankey)
	}
	if len(first.Tree) != 4 || first.Tree[0].Status != "danger" || first.Tree[1].Name != "checkout" || first.Tree[1].Replicas != "2/3" || first.Tree[2].Name != "orders-db" || first.Tree[2].Replicas != "N/A" || first.Tree[3].Name != "node-1" || first.Tree[3].Depth != 1 {
		t.Fatalf("tree: %#v", first.Tree)
	}
	if first.Edges[0].Label != "420/s · 8.4%" || first.Edges[0].Tone != "danger" || first.Edges[0].Path == "" || first.Edges[0].LX <= 0 {
		t.Fatalf("edge: %#v", first.Edges[0])
	}
	for _, n := range first.Nodes {
		if n.X < 0 || n.Y < 0 || n.X+172 > 880 || n.Y+48 > 760 {
			t.Fatalf("out of bounds: %#v", n)
		}
	}
	for i := 0; i < 10; i++ {
		again, err := projectFullTopology(nodes, edges, obs, "db", "")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("nondeterministic projection at %d", i)
		}
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"groups"`, `"impacted":true`, `"from_service"`, `"lx":`, `"ly":`, `"value":420`} {
		if !strings.Contains(string(raw), fragment) {
			t.Fatalf("missing %s in %s", fragment, raw)
		}
	}
}

func TestProjectFullTopologyRejectsIncompleteOrInconsistentEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]node, *[]edge, *topologyObservation)
		center string
	}{
		{"missing node observation", func(_ map[string]node, _ *[]edge, o *topologyObservation) { delete(o.Nodes, "caller") }, "db"},
		{"unknown edge observation", func(_ map[string]node, _ *[]edge, o *topologyObservation) {
			o.Edges["hidden"] = edgeObservation{RPS: 1}
		}, "db"},
		{"cross environment edge", func(n map[string]node, _ *[]edge, _ *topologyObservation) {
			v := n["caller"]
			v.EnvironmentIDs = []string{"other"}
			n["caller"] = v
		}, "db"},
		{"nonfinite traffic", func(_ map[string]node, _ *[]edge, o *topologyObservation) {
			o.Edges["depends"] = edgeObservation{RPS: math.NaN(), ErrorPct: 1}
		}, "db"},
		{"zero traffic edge", func(_ map[string]node, _ *[]edge, o *topologyObservation) { o.Edges["depends"] = edgeObservation{} }, "db"},
		{"structural placement edge", func(_ map[string]node, e *[]edge, _ *topologyObservation) { (*e)[1].Kind = "runs_on" }, "db"},
		{"unobserved center", func(_ map[string]node, _ *[]edge, _ *topologyObservation) {}, ""},
		{"unknown caller", func(_ map[string]node, _ *[]edge, o *topologyObservation) { o.Center.FromID = "hidden" }, "db"},
		{"duplicate flow pair", func(_ map[string]node, e *[]edge, o *topologyObservation) {
			*e = append(*e, edge{ID: "dup", From: "caller", To: "db", Kind: "calls", EnvironmentIDs: []string{"test"}})
			o.Edges["dup"] = edgeObservation{RPS: 1}
		}, "db"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, e, o := projectionFixture()
			tc.mutate(n, &e, &o)
			_, err := projectFullTopology(n, e, o, tc.center, "")
			if !errors.Is(err, errTopologyIncomplete) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestProjectFullTopologyGroupsUseOnlyCMDBDimensions(t *testing.T) {
	n, e, o := projectionFixture()
	got, err := projectFullTopology(n, e, o, "db", "zone")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 2 || got.Groups[0].Label != "a" || got.Groups[1].Label != "b" {
		t.Fatalf("zones: %#v", got.Groups)
	}
	v := n["caller"]
	v.Zone = ""
	n["caller"] = v
	got, err = projectFullTopology(n, e, o, "db", "zone")
	if err != nil {
		t.Fatal(err)
	}
	if got.Groups[0].Label != "Unspecified" {
		t.Fatalf("missing zone: %#v", got.Groups)
	}
}
