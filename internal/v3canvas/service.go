// Package v3canvas stores non-investigation V3 canvases and their structural
// templates. Investigation branches remain owned by the existing domain.
package v3canvas

import (
	"context"
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
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// Investigation visibility depends on mutable application ownership and both
// environment mappings. Pin every currently visible source row before counts
// and page selection. Under REPEATABLE READ an update committed since the
// snapshot fails serialization; one started later waits until this read ends.
func lockCanvasInvestigationScopes(ctx context.Context, tx pgx.Tx, userID string, envs []string) error {
	rows, err := tx.Query(ctx, `SELECT a.id,ae.id,vae.environment_id
 FROM applications a
 JOIN application_environments ae ON ae.application_id=a.id AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=ae.key
 WHERE NOT a.archived AND ae.key=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM investigations i WHERE i.application_id=a.id AND i.environment_id=ae.id)
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='canvas' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=ae.key)
     OR (g.scope_kind='application' AND g.scope_id=a.id)
     OR (a.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=a.owner_team_id)))
 ORDER BY a.id,ae.id FOR SHARE OF a,ae,vae`, userID, envs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var applicationID, applicationEnvironmentID, environmentID string
		if err = rows.Scan(&applicationID, &applicationEnvironmentID, &environmentID); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return err
}

type Service struct {
	Store   *store.Store
	Runtime inference.Runner
}

func New(st *store.Store) *Service { return &Service{Store: st} }

// NewWithRuntime enables durable node replies. The V3 composition root must
// pass its configured runner; New remains useful for storage-only use.
func NewWithRuntime(st *store.Store, runtime inference.Runner) *Service {
	return &Service{Store: st, Runtime: runtime}
}

// Register requires the V3 instance/environment middleware and policy matrix.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"GET /api/v1/canvases", "view", s.list},
		{"POST /api/v1/canvases", "change", s.createCanvas},
		{"GET /api/v1/canvases/{id}", "view", s.get},
		{"POST /api/v1/canvases/{id}/nodes/{node}/messages", "change", s.sendNodeMessage},
		{"GET /api/v1/investigations/{id}/nodes/{node}/messages", "view", s.readInvestigationNodeMessages},
		{"GET /api/v1/canvas-templates", "view", s.templates},
		{"POST /api/v1/canvas-templates", "change", s.saveTemplate},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		if route.pattern == "GET /api/v1/canvases" || route.pattern == "POST /api/v1/canvases" || route.pattern == "GET /api/v1/investigations/{id}/nodes/{node}/messages" {
			// The handlers resolve application/team scope from persisted
			// investigations or the active service catalog before checking grants.
			api.MarkV3ObjectRoute(route.pattern, "canvas", route.action)
		} else {
			api.MarkV3Route(route.pattern, "canvas", route.action)
		}
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
	Badge  *NodeBadge        `json:"badge,omitempty"`
	Blocks []json.RawMessage `json:"blocks"`
}
type NodeBadge struct {
	Text string `json:"text"`
	Tone string `json:"tone"`
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
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	Kind            string    `json:"kind"`
	Status          string    `json:"status"`
	Meta            string    `json:"meta"`
	Participants    *[]string `json:"participants"`
	UpdatedAt       time.Time `json:"updated_at"`
	Href            string    `json:"href"`
	InvestigationID string    `json:"investigation_id,omitempty"`
	Services        []string  `json:"services"`
	Thumb           thumb     `json:"thumb"`
}

type branchPreview struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
}

// Investigation thumbnails are a deterministic drawing of persisted branch
// topology. The coordinates are presentation only; no branch content is
// inferred from the layout.
func investigationThumb(branches []branchPreview) thumb {
	out := thumb{Nodes: [][5]any{}, Edges: []string{}}
	position := map[string][2]int{}
	for i, b := range branches {
		if i >= 8 {
			break
		}
		x, y := (i%4)*52+8, (i/4)*42+8
		position[b.ID] = [2]int{x, y}
		out.Nodes = append(out.Nodes, [5]any{x, y, 36, 18, "info"})
	}
	for _, b := range branches {
		from, parentVisible := position[b.Parent]
		to, childVisible := position[b.ID]
		if parentVisible && childVisible {
			out.Edges = append(out.Edges, fmt.Sprintf("M%d %d L%d %d", from[0]+18, from[1]+9, to[0]+18, to[1]+9))
		}
	}
	return out
}

const canvasListSource = `WITH visible AS (
 SELECT c.id,c.title,c.kind,c.status,c.services,c.updated_at,c.nodes,c.edges,
        COALESCE(u.name,'') AS owner_name,NULL::text AS investigation_id,
        '[]'::jsonb AS branches,0::bigint AS branch_count
 FROM v3_canvases c JOIN users u ON u.id=c.owner_id
 WHERE c.owner_id=$1 AND c.environment_id=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='canvas' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=c.environment_id)))
 UNION ALL
 SELECT i.id,i.title,'investigation',i.status,ARRAY[i.application_id],i.updated_at,
        '[]'::jsonb,'[]'::jsonb,COALESCE(creator.name,''),i.id,
        COALESCE((SELECT jsonb_agg(jsonb_build_object('id',b.id,'parent',COALESCE(b.parent_id,'')) ORDER BY b.depth,b.id)
                  FROM (WITH RECURSIVE tree AS (
                    SELECT id,parent_id,0 AS depth FROM branches
                     WHERE investigation_id=i.id AND parent_id IS NULL
                    UNION ALL
                    SELECT child.id,child.parent_id,tree.depth+1 FROM branches child
                     JOIN tree ON child.parent_id=tree.id
                     WHERE child.investigation_id=i.id AND tree.depth<7
                  ) SELECT id,parent_id,depth FROM tree ORDER BY depth,id LIMIT 8) b),'[]'::jsonb),
        (SELECT count(*) FROM branches b WHERE b.investigation_id=i.id)
 FROM investigations i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 LEFT JOIN users creator ON creator.id=i.created_by
 JOIN application_environments ae ON ae.id=i.environment_id AND ae.application_id=i.application_id AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=i.application_id AND vae.environment_id=ae.key
 WHERE ae.key=ANY($2::text[])
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='canvas' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
     OR (g.scope_kind='environment' AND g.scope_id=ae.key)
     OR (g.scope_kind='application' AND g.scope_id=i.application_id)
     OR (a.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=a.owner_team_id)))
), searched AS (
 SELECT * FROM visible WHERE ($3='' OR aiops_search_match(title,$3))
) `

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
func makeThumb(nodes []Node, edges []Edge) thumb {
	out := thumb{Nodes: [][5]any{}, Edges: []string{}}
	position := map[string][3]int{}
	for i, n := range nodes {
		if i >= 8 {
			break
		}
		out.Nodes = append(out.Nodes, [5]any{n.X, n.Y, n.W, 20, n.Tone})
		position[n.ID] = [3]int{n.X, n.Y, n.W}
	}
	for _, edge := range edges {
		from, fromVisible := position[edge.From]
		to, toVisible := position[edge.To]
		if fromVisible && toVisible {
			out.Edges = append(out.Edges, fmt.Sprintf("M%d %d L%d %d", from[0]+from[2]/2, from[1]+10, to[0]+to[2]/2, to[1]+10))
		}
	}
	return out
}
func publicSummary(id, title, kind, status, owner string, services []string, at time.Time, nodes []Node, edges ...Edge) summary {
	if services == nil {
		services = []string{}
	}
	people := []string{}
	if owner != "" {
		people = append(people, owner)
	}
	return summary{ID: id, Title: title, Kind: kind, Status: status, Meta: fmt.Sprintf("%d 个节点", len(nodes)), Participants: &people, UpdatedAt: at, Href: "/canvas/" + id, Services: services, Thumb: makeThumb(nodes, edges)}
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
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	// Counts and the page must see the same owner, grant, team and canvas
	// snapshot. Grant-source locks serialize a concurrent role revocation with
	// this read; a failed serialization is reported as unavailable.
	tx, err := s.Store.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	var sessionUser string
	err = tx.QueryRow(r.Context(), `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(cookie.Value), owner).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		return
	}
	if err = lockCanvasGrantSources(r.Context(), tx, sessionUser); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		return
	}
	currentEnvs := make([]string, 0, len(envs))
	for _, environmentID := range envs {
		var permitted bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$1 AND g.module='environment' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR
      (g.scope_kind='environment' AND g.scope_id=$2)))`, sessionUser, environmentID).Scan(&permitted)
		if err != nil {
			httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
			return
		}
		if permitted {
			currentEnvs = append(currentEnvs, environmentID)
		}
	}
	if len(currentEnvs) == 0 {
		httpapi.Error(w, 403, "forbidden", "Environment access required")
		return
	}
	envs = currentEnvs
	if err = lockCanvasInvestigationScopes(r.Context(), tx, sessionUser, envs); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		return
	}
	counts := map[string]int{"all": 0, "investigation": 0, "plan": 0, "review": 0, "analysis": 0, "custom": 0}
	rows, err := tx.Query(r.Context(), canvasListSource+`SELECT kind,count(*),bool_and(
   kind<>'investigation' OR status IN ('open','closed')) FROM searched GROUP BY kind`, owner, envs, q)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	for rows.Next() {
		var k string
		var count int
		var validStatus bool
		if err = rows.Scan(&k, &count, &validStatus); err != nil {
			break
		}
		if !validStatus {
			err = errors.New("unknown investigation status")
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
	total := counts["all"]
	if kind != "" {
		total = counts[kind]
	}
	rows, err = tx.Query(r.Context(), canvasListSource+`SELECT id,title,kind,status,services,updated_at,nodes,edges,owner_name,investigation_id,branches,branch_count
 FROM searched WHERE ($4='' OR kind=$4)
 ORDER BY updated_at DESC,id LIMIT $5 OFFSET $6`, owner, envs, q, kind, size, (page-1)*size)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	for rows.Next() {
		var id, title, k, status, name string
		var services []string
		var at time.Time
		var raw, edgeRaw, branchRaw []byte
		var investigationID *string
		var branchCount int64
		if err = rows.Scan(&id, &title, &k, &status, &services, &at, &raw, &edgeRaw, &name, &investigationID, &branchRaw, &branchCount); err != nil {
			break
		}
		if investigationID != nil {
			branches := []branchPreview{}
			if err = json.Unmarshal(branchRaw, &branches); err != nil {
				break
			}
			mapped, valid := investigationStatus(status)
			if !valid {
				err = errors.New("unknown investigation status")
				break
			}
			status = mapped
			item := publicSummary(id, title, k, status, name, services, at, nil)
			item.Meta = nodeCountLabel(r, int(branchCount))
			item.Thumb = investigationThumb(branches)
			// Historical investigations have no recorded creator. An empty
			// array means no known participant, never a fabricated name.
			item.InvestigationID = *investigationID
			item.Href = "/canvas/" + *investigationID
			items = append(items, item)
			continue
		}
		var nodes []Node
		nodes, err = decodeNodes(raw)
		if err != nil {
			break
		}
		var edges []Edge
		edges, err = decodeEdges(edgeRaw)
		if err != nil {
			break
		}
		item := publicSummary(id, title, k, status, name, services, at, nodes, edges...)
		item.Meta = nodeCountLabel(r, len(nodes))
		items = append(items, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas list unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "counts": counts, "total": total, "page": page, "page_size": size})
}

func investigationStatus(status string) (string, bool) {
	switch status {
	case "open":
		return "active", true
	case "closed":
		return "resolved", true
	default:
		return "", false
	}
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
func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	// Every accepted node start/completion takes a conflicting canvas row
	// lock. READ COMMITTED lets a detail request wait for such a completion
	// and then read its final row and exchanges without a stale-snapshot 40001.
	tx, err := s.Store.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	var sessionUser string
	err = tx.QueryRow(r.Context(), `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
	 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
	 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		return
	}
	if err = lockCanvasGrantSources(r.Context(), tx, sessionUser); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		return
	}
	var title, kind, status, goal, env, owner string
	var services []string
	var nodeRaw, edgeRaw, artifactRaw []byte
	err = tx.QueryRow(r.Context(), `SELECT c.title,c.kind,c.status,c.goal,c.environment_id,c.services,c.nodes,c.edges,c.artifacts,COALESCE(u.name,'')
 FROM v3_canvases c JOIN users u ON u.id=c.owner_id
	WHERE c.id=$1 AND c.owner_id=$2 AND c.environment_id=ANY($3::text[])
	FOR SHARE OF c`, r.PathValue("id"), httpapi.CurrentUser(r).ID, envs).Scan(&title, &kind, &status, &goal, &env, &services, &nodeRaw, &edgeRaw, &artifactRaw, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Canvas not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas unavailable")
		return
	}
	if err = (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{Module: "canvas", Action: "view", EnvironmentID: env}); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 404, "not_found", "Canvas not found")
		} else {
			httpapi.Error(w, 503, "canvas_unavailable", "Canvas authorization unavailable")
		}
		return
	}
	nodes, edges, artifacts, err := decodeCanvasDocument(nodeRaw, edgeRaw, artifactRaw)
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas structure is invalid")
		return
	}
	people := []string{}
	if owner != "" {
		people = append(people, owner)
	}
	threads, err := s.nodeThreads(r.Context(), tx, r.PathValue("id"), nodes)
	if err != nil {
		if errors.Is(err, errCanvasHistoryTooLarge) {
			httpapi.Error(w, 413, "canvas_history_too_large", "Canvas conversation history exceeds the detail limit")
		} else {
			httpapi.Error(w, 503, "canvas_unavailable", "Canvas conversations unavailable")
		}
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Canvas unavailable")
		return
	}
	doc := map[string]any{"id": r.PathValue("id"), "title": title, "kind": kind, "status": status, "participants": people, "note": goal, "nodes": nodes, "edges": edges, "artifacts": artifacts, "threads": threads}
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
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Template save failed")
		return
	}
	defer tx.Rollback(r.Context())
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	owner := httpapi.CurrentUser(r)
	var sessionUser string
	err = tx.QueryRow(r.Context(), `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(cookie.Value), owner.ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Template authorization unavailable")
		return
	}
	if err = lockCanvasGrantSources(r.Context(), tx, owner.ID); err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Template authorization unavailable")
		return
	}
	var kind string
	var rawNodes, rawEdges []byte
	err = tx.QueryRow(r.Context(), `SELECT kind,nodes,edges FROM v3_canvases
 WHERE id=$1 AND owner_id=$2 AND environment_id=$3 FOR SHARE`, in.FromCanvas, owner.ID, envs[0]).Scan(&kind, &rawNodes, &rawEdges)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Source canvas not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "canvas_unavailable", "Source canvas unavailable")
		return
	}
	if err = (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(r.Context(), tx, owner, rbac.V3Decision{Module: "canvas", Action: "change", EnvironmentID: envs[0]}); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Canvas change permission is required")
		} else {
			httpapi.Error(w, 503, "template_unavailable", "Template authorization unavailable")
		}
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
	if _, _, _, err = decodeCanvasDocument(mustJSON(nodes), mustJSON(edges), []byte("[]")); err != nil {
		httpapi.Error(w, 422, "unsafe_template_structure", "Canvas graph cannot be saved as a structural template")
		return
	}
	id := "CT-" + store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name,description,nodes,edges)
		VALUES($1,$2,$3,$4,$5,'', $6::jsonb,$7::jsonb)`, id, owner.ID, envs[0], kind, in.Name, mustJSON(nodes), mustJSON(edges))
	if err != nil {
		httpapi.Error(w, 503, "template_unavailable", "Template save failed")
		return
	}
	auditChanges := mustJSON(map[string]string{"environment_id": envs[0], "source_canvas_id": in.FromCanvas})
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
	 VALUES($1,'v3.canvas_template.saved',$2,'success',$3::jsonb)`,
		owner.ID, id, auditChanges)
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
