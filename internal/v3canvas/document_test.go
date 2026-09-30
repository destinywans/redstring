package v3canvas

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func canvasTestNode(id, blocks string) string {
	return fmt.Sprintf(`{"id":%q,"x":1,"y":2,"w":230,"icon":"layers","tone":"info","title":"Title","sub":"","blocks":%s}`, id, blocks)
}

func TestDecodeCanvasDocumentEmptyAndAllBlocks(t *testing.T) {
	nodes, edges, artifacts, err := decodeCanvasDocument([]byte(`[]`), []byte(`[]`), []byte(`[]`))
	if err != nil || nodes == nil || edges == nil || artifacts == nil || len(nodes)+len(edges)+len(artifacts) != 0 {
		t.Fatalf("empty document: nodes=%v edges=%v artifacts=%v err=%v", nodes, edges, artifacts, err)
	}
	blocks := `[
		{"type":"kv","items":[["key","value"]],"extra":"secret"},
		{"type":"tags","items":["tag"]},
		{"type":"text","text":"hello"},
		{"type":"bars","values":[0,1.25,-2],"highlight":0,"color":"var(--c5)"},
		{"type":"rows","items":[{"badge":"A","badge_tone":"accent","dot":"ok","text":"row","value":"5","state":"ready","state_tone":"warn","extra":"secret"}]},
		{"type":"steps","items":[{"text":"step","sub":"detail","state":"gate","extra":"secret"}]},
		{"type":"callout","tone":"danger","icon":"shield","text":"warning","extra":"secret"}
	]`
	node := strings.TrimSuffix(canvasTestNode("n1", blocks), "}") + `,"badge":{"text":"Active","tone":"ok","extra":"secret"},"extra":"secret"}`
	nodes, edges, artifacts, err = decodeCanvasDocument([]byte("["+node+"]"), []byte(`[]`), []byte(`[{"id":"a1","x":3,"y":4,"icon":"file","brand":"grafana","ext":"MD","tone":"ok","title":"Artifact","sub":"details","href":"/safe","extra":"secret"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(edges) != 0 || len(artifacts) != 1 || nodes[0].Badge == nil || nodes[0].Badge.Text != "Active" || len(nodes[0].Blocks) != 7 || artifacts[0].Href != "/safe" {
		t.Fatalf("decoded content: nodes=%+v edges=%v artifacts=%+v", nodes, edges, artifacts)
	}
	for _, block := range nodes[0].Blocks {
		if strings.Contains(string(block), "secret") {
			t.Fatalf("unknown nested field leaked: %s", block)
		}
	}
	encoded, err := json.Marshal(nodes[0])
	if err != nil || strings.Contains(string(encoded), "secret") {
		t.Fatalf("unknown node/badge field leaked: %s, %v", encoded, err)
	}
	encoded, err = json.Marshal(artifacts[0])
	if err != nil || strings.Contains(string(encoded), "secret") {
		t.Fatalf("unknown artifact field leaked: %s, %v", encoded, err)
	}
}

func TestDecodeCanvasBlockRejectsInvalidShapes(t *testing.T) {
	cases := []struct{ name, block string }{
		{"missing type", `{"text":"x"}`},
		{"unknown type", `{"type":"html","html":"<script>"}`},
		{"null required", `{"type":"text","text":null}`},
		{"missing required", `{"type":"text"}`},
		{"kv tuple short", `{"type":"kv","items":[["key"]]}`},
		{"kv null item", `{"type":"kv","items":[null]}`},
		{"kv null value", `{"type":"kv","items":[["key",null]]}`},
		{"tags object", `{"type":"tags","items":{}}`},
		{"tags null item", `{"type":"tags","items":[null]}`},
		{"bars bad value", `{"type":"bars","values":[1e309]}`},
		{"bars null value", `{"type":"bars","values":[null]}`},
		{"bars huge value", `{"type":"bars","values":[1e100]}`},
		{"bars bad highlight", `{"type":"bars","values":[1],"highlight":1}`},
		{"rows missing text", `{"type":"rows","items":[{"badge":"A"}]}`},
		{"rows wrong dot", `{"type":"rows","items":[{"text":"A","dot":"evil"}]}`},
		{"rows wrong state tone", `{"type":"rows","items":[{"text":"A","state_tone":"evil"}]}`},
		{"steps null sub", `{"type":"steps","items":[{"text":"x","sub":null,"state":"done"}]}`},
		{"steps wrong state", `{"type":"steps","items":[{"text":"x","sub":"","state":"running"}]}`},
		{"callout wrong tone", `{"type":"callout","tone":"t1","text":"x"}`},
		{"null blocks", `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := decodeCanvasDocument([]byte("["+canvasTestNode("n1", "["+tc.block+"]")+"]"), []byte(`[]`), []byte(`[]`))
			if err == nil {
				t.Fatal("expected malformed block to be rejected")
			}
		})
	}
}

func TestDecodeCanvasDocumentRejectsMalformedGraph(t *testing.T) {
	n1 := canvasTestNode("n1", `[]`)
	n2 := canvasTestNode("n2", `[]`)
	cases := []struct{ name, nodes, edges, artifacts string }{
		{"null nodes", `null`, `[]`, `[]`},
		{"null edges", `[` + n1 + `]`, `null`, `[]`},
		{"null artifacts", `[` + n1 + `]`, `[]`, `null`},
		{"duplicate nodes", `[` + n1 + `,` + n1 + `]`, `[]`, `[]`},
		{"duplicate artifacts", `[` + n1 + `]`, `[]`, `[ {"id":"a","x":0,"y":0,"title":"A","sub":""},{"id":"a","x":1,"y":1,"title":"B","sub":""}]`},
		{"node artifact ID collision", `[` + n1 + `]`, `[]`, `[{"id":"n1","x":0,"y":0,"title":"A","sub":""}]`},
		{"missing edge reference", `[` + n1 + `]`, `[{"from":"n1","to":"missing"}]`, `[]`},
		{"self edge", `[` + n1 + `]`, `[{"from":"n1","to":"n1"}]`, `[]`},
		{"duplicate edge", `[` + n1 + `,` + n2 + `]`, `[{"from":"n1","to":"n2"},{"from":"n1","to":"n2"}]`, `[]`},
		{"cycle", `[` + n1 + `,` + n2 + `]`, `[{"from":"n1","to":"n2"},{"from":"n2","to":"n1"}]`, `[]`},
		{"bad edge tone", `[` + n1 + `,` + n2 + `]`, `[{"from":"n1","to":"n2","tone":"t1"}]`, `[]`},
		{"null edge from", `[` + n1 + `]`, `[{"from":null,"to":"n1"}]`, `[]`},
		{"missing artifact title", `[` + n1 + `]`, `[]`, `[{"id":"a","x":0,"y":0,"sub":""}]`},
		{"bad artifact tone", `[` + n1 + `]`, `[]`, `[{"id":"a","x":0,"y":0,"title":"A","sub":"","tone":"dev"}]`},
		{"unsafe artifact href", `[` + n1 + `]`, `[]`, `[{"id":"a","x":0,"y":0,"title":"A","sub":"","href":"javascript:alert(1)"}]`},
		{"oversize artifact coordinate", `[` + n1 + `]`, `[]`, `[{"id":"a","x":100001,"y":0,"title":"A","sub":""}]`},
		{"missing node blocks", `[ {"id":"n1","x":1,"y":2,"w":230,"icon":"layers","tone":"info","title":"Title","sub":""}]`, `[]`, `[]`},
		{"null node title", `[ {"id":"n1","x":1,"y":2,"w":230,"icon":"layers","tone":"info","title":null,"sub":"","blocks":[]}]`, `[]`, `[]`},
		{"invalid badge tone", `[ {"id":"n1","x":1,"y":2,"w":230,"icon":"layers","tone":"info","title":"Title","sub":"","blocks":[],"badge":{"text":"Hi","tone":"dev"}}]`, `[]`, `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := decodeCanvasDocument([]byte(tc.nodes), []byte(tc.edges), []byte(tc.artifacts)); err == nil {
				t.Fatal("expected malformed graph to be rejected")
			}
		})
	}
}

func TestDecodeCanvasDocumentAcceptsDirectedAcyclicGraph(t *testing.T) {
	n1 := canvasTestNode("root", `[]`)
	n2 := canvasTestNode("branch", `[]`)
	n3 := canvasTestNode("leaf", `[]`)
	nodes, edges, artifacts, err := decodeCanvasDocument(
		[]byte("["+strings.Join([]string{n1, n2, n3}, ",")+"]"),
		[]byte(`[{"from":"root","to":"branch","tone":"accent","dash":true,"live":true},{"from":"branch","to":"leaf"}]`),
		[]byte(`[]`),
	)
	if err != nil || len(nodes) != 3 || len(edges) != 2 || len(artifacts) != 0 || !edges[0].Dash || !edges[0].Live || edges[0].Tone != "accent" {
		t.Fatalf("valid DAG rejected or distorted: nodes=%v edges=%v artifacts=%v err=%v", nodes, edges, artifacts, err)
	}
}

func TestDecodeCanvasDocumentLimits(t *testing.T) {
	var nodes []string
	for i := 0; i <= maxCanvasNodes; i++ {
		nodes = append(nodes, canvasTestNode(fmt.Sprintf("n%d", i), `[]`))
	}
	if _, _, _, err := decodeCanvasDocument([]byte("["+strings.Join(nodes, ",")+"]"), []byte(`[]`), []byte(`[]`)); err == nil {
		t.Fatal("expected node count limit")
	}
	big := strings.Repeat("a", maxCanvasJSONBytes)
	if _, _, _, err := decodeCanvasDocument([]byte(big), []byte(`[]`), []byte(`[]`)); err == nil {
		t.Fatal("expected document byte limit")
	}
}
