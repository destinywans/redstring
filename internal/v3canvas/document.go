package v3canvas

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// CanvasArtifact is the persisted, public shape of a generic canvas artifact.
type CanvasArtifact struct {
	ID    string `json:"id"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Icon  string `json:"icon,omitempty"`
	Brand string `json:"brand,omitempty"`
	Ext   string `json:"ext,omitempty"`
	Tone  string `json:"tone,omitempty"`
	Title string `json:"title"`
	Sub   string `json:"sub"`
	Href  string `json:"href,omitempty"`
}

const (
	maxCanvasJSONBytes = 1 << 20
	maxCanvasNodes     = 100
	maxCanvasEdges     = 200
	maxCanvasArtifacts = 100
	maxCanvasBlocks    = 50
	maxCanvasItems     = 200
	maxCanvasText      = 8192
)

// decodeCanvasDocument validates persisted JSON before it reaches the V3 view.
// Decoding into typed values and re-encoding each block discards unknown fields.
func decodeCanvasDocument(nodeRaw, edgeRaw, artifactRaw []byte) ([]Node, []Edge, []CanvasArtifact, error) {
	if len(nodeRaw)+len(edgeRaw)+len(artifactRaw) > maxCanvasJSONBytes {
		return nil, nil, nil, errors.New("canvas document exceeds size limit")
	}
	var rawNodes, rawEdges, rawArtifacts []json.RawMessage
	if err := decodeCanvasArray(nodeRaw, maxCanvasNodes, &rawNodes); err != nil {
		return nil, nil, nil, fmt.Errorf("nodes: %w", err)
	}
	if err := decodeCanvasArray(edgeRaw, maxCanvasEdges, &rawEdges); err != nil {
		return nil, nil, nil, fmt.Errorf("edges: %w", err)
	}
	if err := decodeCanvasArray(artifactRaw, maxCanvasArtifacts, &rawArtifacts); err != nil {
		return nil, nil, nil, fmt.Errorf("artifacts: %w", err)
	}

	nodes := make([]Node, 0, len(rawNodes))
	ids := make(map[string]bool, len(rawNodes))
	for i, raw := range rawNodes {
		obj, err := canvasObject(raw, "id", "x", "y", "w", "icon", "tone", "title", "sub", "blocks")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("node %d: %w", i, err)
		}
		var n Node
		if err := json.Unmarshal(raw, &n); err != nil {
			return nil, nil, nil, fmt.Errorf("node %d: %w", i, err)
		}
		if !safeNodeID.MatchString(n.ID) || ids[n.ID] || !canvasCoordinate(n.X) || !canvasCoordinate(n.Y) || n.W <= 0 || n.W > 2000 || !validTemplateTone(n.Tone) || !canvasText(n.Icon, 100) || !canvasText(n.Title, maxCanvasText) || !canvasText(n.Sub, maxCanvasText) || strings.TrimSpace(n.Title) == "" || strings.TrimSpace(n.Icon) == "" {
			return nil, nil, nil, fmt.Errorf("node %d: invalid structure", i)
		}
		if badgeRaw, ok := obj["badge"]; ok && !canvasNull(badgeRaw) {
			if _, err := canvasObject(badgeRaw, "text", "tone"); err != nil {
				return nil, nil, nil, fmt.Errorf("node %d badge: %w", i, err)
			}
			if n.Badge == nil || !canvasText(n.Badge.Text, maxCanvasText) || !validEdgeTone(n.Badge.Tone) {
				return nil, nil, nil, fmt.Errorf("node %d: invalid badge", i)
			}
		}
		var blocks []json.RawMessage
		if err := decodeCanvasArray(obj["blocks"], maxCanvasBlocks, &blocks); err != nil {
			return nil, nil, nil, fmt.Errorf("node %d blocks: %w", i, err)
		}
		n.Blocks = make([]json.RawMessage, 0, len(blocks))
		for j, block := range blocks {
			clean, err := decodeCanvasBlock(block)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("node %d block %d: %w", i, j, err)
			}
			n.Blocks = append(n.Blocks, clean)
		}
		ids[n.ID] = true
		nodes = append(nodes, n)
	}

	edges := make([]Edge, 0, len(rawEdges))
	links := make(map[string]bool, len(rawEdges))
	for i, raw := range rawEdges {
		obj, err := canvasObject(raw, "from", "to")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("edge %d: %w", i, err)
		}
		var edge Edge
		if err := json.Unmarshal(raw, &edge); err != nil {
			return nil, nil, nil, fmt.Errorf("edge %d: %w", i, err)
		}
		key := edge.From + "\x00" + edge.To
		if !ids[edge.From] || !ids[edge.To] || edge.From == edge.To || links[key] || (canvasPresent(obj, "tone") && !validEdgeTone(edge.Tone)) {
			return nil, nil, nil, fmt.Errorf("edge %d: invalid structure", i)
		}
		links[key] = true
		edges = append(edges, edge)
	}
	if !canvasAcyclic(ids, edges) {
		return nil, nil, nil, errors.New("canvas edges contain a cycle")
	}

	artifacts := make([]CanvasArtifact, 0, len(rawArtifacts))
	artifactIDs := make(map[string]bool, len(rawArtifacts))
	for i, raw := range rawArtifacts {
		obj, err := canvasObject(raw, "id", "x", "y", "title", "sub")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("artifact %d: %w", i, err)
		}
		var a CanvasArtifact
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, nil, nil, fmt.Errorf("artifact %d: %w", i, err)
		}
		if !safeNodeID.MatchString(a.ID) || artifactIDs[a.ID] || ids[a.ID] || !canvasCoordinate(a.X) || !canvasCoordinate(a.Y) || strings.TrimSpace(a.Title) == "" || !canvasText(a.Title, maxCanvasText) || !canvasText(a.Sub, maxCanvasText) || !canvasText(a.Icon, 100) || !canvasText(a.Brand, 100) || !canvasText(a.Ext, 100) || !canvasText(a.Href, 2048) || (canvasPresent(obj, "tone") && !validEdgeTone(a.Tone)) || !canvasSafeHref(a.Href) {
			return nil, nil, nil, fmt.Errorf("artifact %d: invalid structure", i)
		}
		artifactIDs[a.ID] = true
		artifacts = append(artifacts, a)
	}
	return nodes, edges, artifacts, nil
}

func canvasCoordinate(n int) bool         { return n >= -100000 && n <= 100000 }
func canvasText(s string, limit int) bool { return len(s) <= limit }
func canvasNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }
func canvasPresent(obj map[string]json.RawMessage, key string) bool {
	raw, ok := obj[key]
	return ok && !canvasNull(raw)
}
func canvasSafeHref(href string) bool {
	return href == "" || (strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//")) || strings.HasPrefix(href, "https://")
}

func canvasAcyclic(ids map[string]bool, edges []Edge) bool {
	in := make(map[string]int, len(ids))
	out := make(map[string][]string, len(ids))
	for id := range ids {
		in[id] = 0
	}
	for _, edge := range edges {
		in[edge.To]++
		out[edge.From] = append(out[edge.From], edge.To)
	}
	queue := make([]string, 0, len(ids))
	for id, degree := range in {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		visited++
		for _, next := range out[id] {
			in[next]--
			if in[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return visited == len(ids)
}

func canvasObject(raw json.RawMessage, required ...string) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, errors.New("expected object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	for _, key := range required {
		if !canvasPresent(obj, key) {
			return nil, fmt.Errorf("missing %s", key)
		}
	}
	return obj, nil
}

func decodeCanvasArray(raw json.RawMessage, limit int, dst *[]json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return errors.New("expected array")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return err
	}
	if len(*dst) > limit {
		return errors.New("array exceeds limit")
	}
	return nil
}

type canvasRow struct {
	Badge     string `json:"badge,omitempty"`
	BadgeTone string `json:"badge_tone,omitempty"`
	Dot       string `json:"dot,omitempty"`
	Text      string `json:"text"`
	Value     string `json:"value,omitempty"`
	State     string `json:"state,omitempty"`
	StateTone string `json:"state_tone,omitempty"`
}
type canvasStep struct {
	Text  string `json:"text"`
	Sub   string `json:"sub"`
	State string `json:"state"`
}

func decodeCanvasBlock(raw json.RawMessage) (json.RawMessage, error) {
	obj, err := canvasObject(raw, "type")
	if err != nil {
		return nil, err
	}
	var kind string
	if err := json.Unmarshal(obj["type"], &kind); err != nil {
		return nil, err
	}
	var clean any
	switch kind {
	case "kv":
		if _, err = canvasObject(raw, "type", "items"); err != nil {
			return nil, err
		}
		var pairs []json.RawMessage
		if err = decodeCanvasArray(obj["items"], maxCanvasItems, &pairs); err != nil {
			return nil, errors.New("invalid kv items")
		}
		items := make([][]string, 0, len(pairs))
		for _, pairRaw := range pairs {
			var parts []json.RawMessage
			if err = decodeCanvasArray(pairRaw, 2, &parts); err != nil || len(parts) != 2 || canvasNull(parts[0]) || canvasNull(parts[1]) {
				return nil, errors.New("invalid kv pair")
			}
			var pair [2]string
			if err = json.Unmarshal(parts[0], &pair[0]); err != nil {
				return nil, errors.New("invalid kv key")
			}
			if err = json.Unmarshal(parts[1], &pair[1]); err != nil {
				return nil, errors.New("invalid kv value")
			}
			if !canvasText(pair[0], maxCanvasText) || !canvasText(pair[1], maxCanvasText) {
				return nil, errors.New("kv pair exceeds limit")
			}
			items = append(items, []string{pair[0], pair[1]})
		}
		clean = struct {
			Type  string     `json:"type"`
			Items [][]string `json:"items"`
		}{kind, items}
	case "tags":
		if _, err = canvasObject(raw, "type", "items"); err != nil {
			return nil, err
		}
		var rawItems []json.RawMessage
		if err = decodeCanvasArray(obj["items"], maxCanvasItems, &rawItems); err != nil {
			return nil, errors.New("invalid tags items")
		}
		items := make([]string, 0, len(rawItems))
		for _, itemRaw := range rawItems {
			if canvasNull(itemRaw) {
				return nil, errors.New("invalid tag")
			}
			var item string
			if err = json.Unmarshal(itemRaw, &item); err != nil || !canvasText(item, maxCanvasText) {
				return nil, errors.New("tag exceeds limit")
			}
			items = append(items, item)
		}
		clean = struct {
			Type  string   `json:"type"`
			Items []string `json:"items"`
		}{kind, items}
	case "text":
		if _, err = canvasObject(raw, "type", "text"); err != nil {
			return nil, err
		}
		var value string
		if err = json.Unmarshal(obj["text"], &value); err != nil || !canvasText(value, maxCanvasText) {
			return nil, errors.New("invalid text")
		}
		clean = struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{kind, value}
	case "bars":
		if _, err = canvasObject(raw, "type", "values"); err != nil {
			return nil, err
		}
		var b struct {
			Values    []float64 `json:"values"`
			Highlight *int      `json:"highlight,omitempty"`
			Color     string    `json:"color,omitempty"`
		}
		if err = json.Unmarshal(raw, &b); err != nil || b.Values == nil || len(b.Values) > maxCanvasItems || !canvasText(b.Color, 100) {
			return nil, errors.New("invalid bars")
		}
		var rawValues []json.RawMessage
		if err = decodeCanvasArray(obj["values"], maxCanvasItems, &rawValues); err != nil {
			return nil, errors.New("invalid bars values")
		}
		for _, value := range rawValues {
			if canvasNull(value) {
				return nil, errors.New("null bar value")
			}
		}
		for _, v := range b.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e9 {
				return nil, errors.New("invalid bar value")
			}
		}
		if b.Highlight != nil && (*b.Highlight < 0 || *b.Highlight >= len(b.Values)) {
			return nil, errors.New("invalid bar highlight")
		}
		clean = struct {
			Type      string    `json:"type"`
			Values    []float64 `json:"values"`
			Highlight *int      `json:"highlight,omitempty"`
			Color     string    `json:"color,omitempty"`
		}{kind, b.Values, b.Highlight, b.Color}
	case "rows":
		if _, err = canvasObject(raw, "type", "items"); err != nil {
			return nil, err
		}
		var rows []json.RawMessage
		if err = decodeCanvasArray(obj["items"], maxCanvasItems, &rows); err != nil {
			return nil, err
		}
		items := make([]canvasRow, 0, len(rows))
		for _, item := range rows {
			rowObj, e := canvasObject(item, "text")
			if e != nil {
				return nil, e
			}
			var row canvasRow
			if e = json.Unmarshal(item, &row); e != nil || !canvasText(row.Text, maxCanvasText) || !canvasText(row.Badge, maxCanvasText) || !canvasText(row.Value, maxCanvasText) || !canvasText(row.State, maxCanvasText) {
				return nil, errors.New("invalid row")
			}
			for _, key := range []string{"badge_tone", "dot", "state_tone"} {
				if canvasPresent(rowObj, key) && !validEdgeTone(map[string]string{"badge_tone": row.BadgeTone, "dot": row.Dot, "state_tone": row.StateTone}[key]) {
					return nil, errors.New("invalid row tone")
				}
			}
			items = append(items, row)
		}
		clean = struct {
			Type  string      `json:"type"`
			Items []canvasRow `json:"items"`
		}{kind, items}
	case "steps":
		if _, err = canvasObject(raw, "type", "items"); err != nil {
			return nil, err
		}
		var steps []json.RawMessage
		if err = decodeCanvasArray(obj["items"], maxCanvasItems, &steps); err != nil {
			return nil, err
		}
		items := make([]canvasStep, 0, len(steps))
		for _, item := range steps {
			if _, e := canvasObject(item, "text", "sub", "state"); e != nil {
				return nil, e
			}
			var step canvasStep
			if e := json.Unmarshal(item, &step); e != nil || !canvasText(step.Text, maxCanvasText) || !canvasText(step.Sub, maxCanvasText) || (step.State != "done" && step.State != "gate" && step.State != "todo") {
				return nil, errors.New("invalid step")
			}
			items = append(items, step)
		}
		clean = struct {
			Type  string       `json:"type"`
			Items []canvasStep `json:"items"`
		}{kind, items}
	case "callout":
		if _, err = canvasObject(raw, "type", "tone", "text"); err != nil {
			return nil, err
		}
		var c struct {
			Tone string `json:"tone"`
			Icon string `json:"icon,omitempty"`
			Text string `json:"text"`
		}
		if err = json.Unmarshal(raw, &c); err != nil || !validEdgeTone(c.Tone) || !canvasText(c.Icon, 100) || !canvasText(c.Text, maxCanvasText) {
			return nil, errors.New("invalid callout")
		}
		clean = struct {
			Type string `json:"type"`
			Tone string `json:"tone"`
			Icon string `json:"icon,omitempty"`
			Text string `json:"text"`
		}{kind, c.Tone, c.Icon, c.Text}
	default:
		return nil, errors.New("unknown block type")
	}
	return json.Marshal(clean)
}
