// Package v3canvas stores non-investigation V3 canvases and their structural
// templates. Investigation branches remain owned by the existing domain.
package v3canvas

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type Service struct{ Store *store.Store }

func New(st *store.Store) *Service { return &Service{Store: st} }

// Register requires the V3 instance/environment middleware and policy matrix.
// Node messages are deliberately absent until node-scoped durable generation
// can be connected without bypassing reply persistence and approval gates.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"GET /api/v1/canvases", "view", s.list},
		{"POST /api/v1/canvases", "change", s.create},
		{"GET /api/v1/canvases/{id}", "view", s.get},
		{"GET /api/v1/canvas-templates", "view", s.templates},
		{"POST /api/v1/canvas-templates", "change", s.saveTemplate},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3Route(route.pattern, "canvas", route.action)
	}
}

type Node struct {
	ID     string            `json:"id"`
	X      int               `json:"x"`
	Y      int               `json:"y"`
	W      int               `json:"w"`
	Icon   string            `json:"icon"`
	Tone   string            `json:"tone"`
	Title  string            `json:"title"`
	Sub    string            `json:"sub"`
	Blocks []json.RawMessage `json:"blocks"`
}
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Tone string `json:"tone,omitempty"`
	Dash bool   `json:"dash,omitempty"`
	Live bool   `json:"live,omitempty"`
}
type template struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Desc      string `json:"desc"`
	Nodes     string `json:"nodes"`
	IsDefault bool   `json:"is_default,omitempty"`
}

const maxTemplateItems = 500

func nodeCountLabel(r *http.Request, count int) string {
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		if count == 1 {
			return "1 node"
		}
		return fmt.Sprintf("%d nodes", count)
	}
	return fmt.Sprintf("%d 个节点", count)
}

type thumb struct {
	Nodes [][5]any `json:"nodes"`
	Edges []string `json:"edges"`
}
type summary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Meta         string    `json:"meta"`
	Participants []string  `json:"participants"`
	UpdatedAt    time.Time `json:"updated_at"`
	Href         string    `json:"href"`
	Services     []string  `json:"services"`
	Thumb        thumb     `json:"thumb"`
}

func validKind(k string) bool {
	return k == "plan" || k == "review" || k == "analysis" || k == "custom"
}
func allowedScope(w http.ResponseWriter, r *http.Request, write bool) ([]string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return nil, false
	}
	if write && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment before writing")
		return nil, false
	}
	return scope.EnvironmentIDs, true
}
func (s *Service) ready(w http.ResponseWriter) bool {
	if s == nil || s.Store == nil || s.Store.DB == nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas storage is unavailable")
		return false
	}
	return true
}
func parsePage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	page, size := 1, 20
	var err error
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 100000 {
			httpapi.Error(w, 422, "invalid_page", "Invalid page")
			return 0, 0, false
		}
	}
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		size, err = strconv.Atoi(raw)
		if err != nil || size < 1 || size > 100 {
			httpapi.Error(w, 422, "invalid_page_size", "Page size must be between 1 and 100")
			return 0, 0, false
		}
	}
	return page, size, true
}
func decodeNodes(raw []byte) ([]Node, error) {
	nodes := []Node{}
	if len(raw) == 0 {
		return nodes, nil
	}
	err := json.Unmarshal(raw, &nodes)
	return nodes, err
}
func decodeEdges(raw []byte) ([]Edge, error) {
	edges := []Edge{}
	if len(raw) == 0 {
		return edges, nil
	}
	err := json.Unmarshal(raw, &edges)
	return edges, err
}
func makeThumb(nodes []Node) thumb {
	out := thumb{Nodes: [][5]any{}, Edges: []string{}}
	for i, n := range nodes {
		if i >= 8 {
			break
		}
		out.Nodes = append(out.Nodes, [5]any{n.X, n.Y, n.W, 20, n.Tone})
	}
	return out
}
func publicSummary(id, title, kind, status, owner string, services []string, at time.Time, nodes []Node) summary {
	if services == nil {
		services = []string{}
	}
	people := []string{}
	if owner != "" {
		people = append(people, owner)
	}
	return summary{ID: id, Title: title, Kind: kind, Status: status, Meta: fmt.Sprintf("%d 个节点", len(nodes)), Participants: people, UpdatedAt: at, Href: "/canvas/" + id, Services: services, Thumb: makeThumb(nodes)}
}
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && kind != "investigation" && !validKind(kind) {
		httpapi.Error(w, 422, "invalid_kind", "Unknown canvas kind")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) > 200 {
		httpapi.Error(w, 422, "invalid_query", "Search is too long")
		return
	}
	page, size, ok := parsePage(w, r)
	if !ok {
		return
	}
	owner := httpapi.CurrentUser(r).ID
	counts := map[string]int{"all": 0, "investigation": 0, "plan": 0, "review": 0, "analysis": 0, "custom": 0}
	rows, err := s.Store.DB.Query(r.Context(), `SELECT kind,count(*) FROM v3_canvases WHERE owner_id=$1 AND environment_id=ANY($2::text[]) AND ($3='' OR aiops_search_match(title,$3)) GROUP BY kind`, owner, envs, q)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	for rows.Next() {
		var k string
		var count int
		if err = rows.Scan(&k, &count); err != nil {
			break
		}
		counts[k] = count
		counts["all"] += count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	items := []summary{}
	// Investigation canvases cannot be inferred from old application-bound
	// investigations without their own V3 visibility and projection adapter.
	if kind == "investigation" {
		httpapi.Error(w, 503, "investigation_canvas_unavailable", "Investigation canvas listing requires the investigation branch adapter")
		return
	}
	total := counts["all"]
	if kind != "" {
		total = counts[kind]
	}
	rows, err = s.Store.DB.Query(r.Context(), `SELECT c.id,c.title,c.kind,c.status,c.services,c.updated_at,c.nodes,COALESCE(u.name,'')
 FROM v3_canvases c JOIN users u ON u.id=c.owner_id
 WHERE c.owner_id=$1 AND c.environment_id=ANY($2::text[])
 AND ($3='' OR aiops_search_match(c.title,$3)) AND ($4='' OR c.kind=$4)
 ORDER BY c.updated_at DESC,c.id LIMIT $5 OFFSET $6`, owner, envs, q, kind, size, (page-1)*size)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	for rows.Next() {
		var id, title, k, status, name string
		var services []string
		var at time.Time
		var raw []byte
		if err = rows.Scan(&id, &title, &k, &status, &services, &at, &raw, &name); err != nil {
			break
		}
		var nodes []Node
		nodes, err = decodeNodes(raw)
		if err != nil {
			break
		}
		items = append(items, publicSummary(id, title, k, status, name, services, at, nodes))
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts, "total": total, "page": page, "page_size": size, "incomplete_kinds": []string{"investigation"}})
}

type createRequest struct {
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	TemplateID string   `json:"template_id"`
	Goal       string   `json:"goal"`
	Services   []string `json:"services"`
	AIDraft    bool     `json:"ai_draft"`
}

func validStrings(input []string) bool {
	if len(input) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range input {
		if v == "" || len(v) > 200 || strings.TrimSpace(v) != v || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, true)
	if !ok {
		return
	}
	var in createRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Goal = strings.TrimSpace(in.Goal)
	if in.Title == "" || len([]rune(in.Title)) > 200 || len(in.Goal) > 10000 || !validStrings(in.Services) {
		httpapi.Error(w, 422, "invalid_canvas", "Check title, goal and distinct service IDs")
		return
	}
	if in.Kind == "investigation" {
		httpapi.Error(w, 503, "investigation_canvas_unavailable", "Investigation canvases require the investigation branch adapter")
		return
	}
	if !validKind(in.Kind) {
		httpapi.Error(w, 422, "invalid_kind", "Unknown canvas kind")
		return
	}
	if in.AIDraft {
		httpapi.Error(w, 503, "canvas_draft_unavailable", "AI drafting is not configured for generic canvases")
		return
	}
	env, owner := envs[0], httpapi.CurrentUser(r).ID
	for _, app := range in.Services {
		var exists bool
		if err := s.Store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_application_environments WHERE application_id=$1 AND environment_id=$2)`, app, env).Scan(&exists); err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Service scope unavailable")
			return
		}
		if !exists {
			httpapi.Error(w, 422, "invalid_service", "Service is not registered in the selected environment")
			return
		}
	}
	nodes := []Node{{ID: "root", X: 16, Y: 140, W: 260, Icon: "layers", Tone: "info", Title: in.Title, Sub: "主线", Blocks: []json.RawMessage{}}}
	edges := []Edge{}
	if in.TemplateID != "" {
		var rawNodes, rawEdges []byte
		var k string
		err := s.Store.DB.QueryRow(r.Context(), `SELECT kind,nodes,edges FROM v3_canvas_templates
   WHERE id=$1 AND kind=$2 AND (owner_id IS NULL OR (owner_id=$3 AND environment_id=$4))`, in.TemplateID, in.Kind, owner, env).Scan(&k, &rawNodes, &rawEdges)
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 404, "template_not_found", "Template not found")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "template_unavailable", "Template unavailable")
			return
		}
		nodes, err = decodeNodes(rawNodes)
		if err == nil {
			edges, err = decodeEdges(rawEdges)
		}
		if err != nil {
			httpapi.Error(w, 503, "template_unavailable", "Template structure is invalid")
			return
		}
		if len(nodes) == 0 {
			nodes = []Node{{ID: "root", X: 16, Y: 140, W: 260, Icon: "layers", Tone: "info", Title: in.Title, Sub: "主线", Blocks: []json.RawMessage{}}}
		}
		nodes[0].Title = in.Title
	}
	id := "CV-" + store.ID()
	_, err := s.Store.DB.Exec(r.Context(), `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,goal,services,nodes,edges)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb)`, id, owner, env, in.Title, in.Kind, in.Goal, in.Services, mustJSON(nodes), mustJSON(edges))
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas creation failed")
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"id": id, "href": "/canvas/" + id})
}
func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	var title, kind, status, goal, env, owner string
	var services []string
	var nodeRaw, edgeRaw, artifactRaw []byte
	err := s.Store.DB.QueryRow(r.Context(), `SELECT c.title,c.kind,c.status,c.goal,c.environment_id,c.services,c.nodes,c.edges,c.artifacts,COALESCE(u.name,'')
 FROM v3_canvases c JOIN users u ON u.id=c.owner_id
 WHERE c.id=$1 AND c.owner_id=$2 AND c.environment_id=ANY($3::text[])`, r.PathValue("id"), httpapi.CurrentUser(r).ID, envs).Scan(&title, &kind, &status, &goal, &env, &services, &nodeRaw, &edgeRaw, &artifactRaw, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Canvas not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas unavailable")
		return
	}
	nodes, err := decodeNodes(nodeRaw)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas structure is invalid")
		return
	}
	edges, err := decodeEdges(edgeRaw)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas structure is invalid")
		return
	}
	artifacts := []json.RawMessage{}
	if err = json.Unmarshal(artifactRaw, &artifacts); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas artifacts are invalid")
		return
	}
	people := []string{}
	if owner != "" {
		people = append(people, owner)
	}
	doc := map[string]any{"id": r.PathValue("id"), "title": title, "kind": kind, "status": status, "participants": people, "note": goal, "nodes": nodes, "edges": edges, "artifacts": artifacts, "threads": map[string]any{}}
	// EnvName in the current frontend cannot represent every configured V3
	// environment. Omit unsupported names instead of labeling them incorrectly.
	if env == "prod" || env == "staging" || env == "test" {
		doc["env"] = env
	}
	httpapi.WriteJSON(w, 200, doc)
}

func (s *Service) templates(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	rows, err := s.Store.DB.Query(r.Context(), `SELECT id,kind,name,description,jsonb_array_length(nodes),is_default
 FROM v3_canvas_templates WHERE owner_id IS NULL OR (owner_id=$1 AND environment_id=ANY($2::text[]))
 ORDER BY kind,is_default DESC,created_at,id LIMIT $3`, httpapi.CurrentUser(r).ID, envs, maxTemplateItems+1)
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Templates unavailable")
		return
	}
	items := []template{}
	for rows.Next() {
		var t template
		var count int
		if err = rows.Scan(&t.ID, &t.Kind, &t.Name, &t.Desc, &count, &t.IsDefault); err != nil {
			break
		}
		t.Nodes = nodeCountLabel(r, count)
		items = append(items, t)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Templates unavailable")
		return
	}
	if len(items) > maxTemplateItems {
		httpapi.Error(w, 422, "template_scope_too_broad", "Template list exceeds the bounded result")
		return
	}
	// Only persisted generic templates are returned. Investigation templates
	// have a separate domain and no V3 template projection here yet.
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

type saveTemplateRequest struct {
	FromCanvas string `json:"from_canvas"`
	Name       string `json:"name"`
	Keep       *struct {
		Nodes    bool `json:"nodes"`
		Steps    bool `json:"steps"`
		Runbooks bool `json:"runbooks"`
		Content  bool `json:"content"`
	} `json:"keep"`
}

func (s *Service) saveTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, true)
	if !ok {
		return
	}
	var in saveTemplateRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 200 || in.FromCanvas == "" || in.Keep == nil {
		httpapi.Error(w, 422, "invalid_template", "Select a canvas and a template name")
		return
	}
	if in.Keep.Steps || in.Keep.Runbooks || in.Keep.Content {
		httpapi.Error(w, 422, "unsafe_template_content", "Only node structure can currently be saved; evidence, steps and content are excluded")
		return
	}
	var kind string
	var rawNodes, rawEdges []byte
	err := s.Store.DB.QueryRow(r.Context(), `SELECT kind,nodes,edges FROM v3_canvases WHERE id=$1 AND owner_id=$2 AND environment_id=$3`, in.FromCanvas, httpapi.CurrentUser(r).ID, envs[0]).Scan(&kind, &rawNodes, &rawEdges)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Source canvas not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Source canvas unavailable")
		return
	}
	if len(rawNodes) > 1<<20 || len(rawEdges) > 1<<20 {
		httpapi.Error(w, 422, "unsafe_template_structure", "Canvas structure exceeds template limits")
		return
	}
	nodes, err := decodeNodes(rawNodes)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Invalid source structure")
		return
	}
	edges, err := decodeEdges(rawEdges)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Invalid source structure")
		return
	}
	if !in.Keep.Nodes {
		nodes = []Node{}
		edges = []Edge{}
	} else {
		nodes, edges, err = sanitizeTemplateStructure(nodes, edges)
		if err != nil {
			httpapi.Error(w, 422, "unsafe_template_structure", "Canvas graph cannot be saved as a structural template")
			return
		}
	}
	id := "CT-" + store.ID()
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Template save failed")
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name,description,nodes,edges)
		VALUES($1,$2,$3,$4,$5,'', $6::jsonb,$7::jsonb)`, id, httpapi.CurrentUser(r).ID, envs[0], kind, in.Name, mustJSON(nodes), mustJSON(edges))
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Template save failed")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
	 VALUES($1,'v3.canvas_template.saved',$2,'success',jsonb_build_object('environment_id',$3,'source_canvas_id',$4))`,
		httpapi.CurrentUser(r).ID, id, envs[0], in.FromCanvas)
	if err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Template audit unavailable")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Template save failed")
		return
	}
	// Persisted owner and environment make this a private template; the V3 DTO
	// has no scope field, so the response contains only contracted fields.
	httpapi.WriteJSON(w, 201, template{ID: id, Kind: kind, Name: in.Name, Desc: "",
		Nodes: nodeCountLabel(r, len(nodes))})
}

// sanitizeNodes copies only layout and harmless presentation tokens. Titles,
// annotations, evidence, content blocks and turns never enter a template.
func sanitizeNodes(src []Node) []Node {
	dst := make([]Node, len(src))
	for i, n := range src {
		icon, tone := templateIcon(n.Icon), n.Tone
		if !validTemplateTone(tone) {
			tone = "info"
		}
		dst[i] = Node{ID: fmt.Sprintf("node-%d", i+1), X: n.X, Y: n.Y, W: n.W, Icon: icon, Tone: tone, Title: fmt.Sprintf("节点 %d", i+1), Sub: "", Blocks: []json.RawMessage{}}
	}
	return dst
}

var safeNodeID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func templateIcon(icon string) string {
	switch icon {
	case "layers", "alert", "activity", "server", "database", "shield", "file-text", "code", "sparkles":
		return icon
	default:
		return "layers"
	}
}

func validTemplateTone(t string) bool {
	switch t {
	case "neutral", "accent", "ok", "warn", "danger", "info", "t1", "dev":
		return true
	}
	return false
}

func validEdgeTone(t string) bool {
	switch t {
	case "neutral", "accent", "ok", "warn", "danger", "info":
		return true
	}
	return false
}

func sanitizeTemplateStructure(srcNodes []Node, srcEdges []Edge) ([]Node, []Edge, error) {
	if len(srcNodes) > 100 || len(srcEdges) > 200 {
		return nil, nil, errors.New("template graph exceeds limits")
	}
	seen := map[string]bool{}
	for _, n := range srcNodes {
		if !safeNodeID.MatchString(n.ID) || seen[n.ID] || n.W <= 0 || n.W > 2000 ||
			n.X < -100000 || n.X > 100000 || n.Y < -100000 || n.Y > 100000 {
			return nil, nil, errors.New("invalid node structure")
		}
		seen[n.ID] = true
	}
	nodes := sanitizeNodes(srcNodes)
	ids := make(map[string]string, len(srcNodes))
	for i, n := range srcNodes {
		ids[n.ID] = nodes[i].ID
	}
	edges := make([]Edge, 0, len(srcEdges))
	links := map[string]bool{}
	for _, edge := range srcEdges {
		key := edge.From + "\x00" + edge.To
		if !seen[edge.From] || !seen[edge.To] || edge.From == edge.To || links[key] {
			return nil, nil, errors.New("invalid edge structure")
		}
		links[key] = true
		if !validEdgeTone(edge.Tone) {
			edge.Tone = ""
		}
		edges = append(edges, Edge{From: ids[edge.From], To: ids[edge.To], Tone: edge.Tone, Dash: edge.Dash})
	}
	return nodes, edges, nil
}
