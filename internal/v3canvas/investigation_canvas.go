package v3canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

const (
	maxInvestigationNodes    = 100
	maxInvestigationVersions = 1000
	maxInvestigationEvidence = 100
	maxNodeSummaryRunes      = 280
	maxInvestigationText     = 16 << 10
)

type graphFailure struct {
	status int
	code   string
	text   string
}

func (f *graphFailure) write(w http.ResponseWriter) { httpapi.Error(w, f.status, f.code, f.text) }

func graphUnavailable() *graphFailure {
	return &graphFailure{503, "investigation_canvas_unavailable", "Investigation canvas is unavailable"}
}

func graphNotFound() *graphFailure {
	return &graphFailure{404, "not_found", "Investigation canvas not found"}
}

type investigationCanvasTarget struct {
	investigationReadTarget
	id, title, status, creator string
}

// A current session and role-source locks bind the transaction's decision to
// the principal that entered through the registered V3 middleware.
func (s *Service) canvasPrincipal(ctx context.Context, tx pgx.Tx, r *http.Request) *graphFailure {
	if failure := s.canvasSession(ctx, tx, r); failure != nil {
		return failure
	}
	if err := lockCanvasGrantSources(ctx, tx, httpapi.CurrentUser(r).ID); err != nil {
		return graphUnavailable()
	}
	return nil
}

// Use the shared user-then-session lock order, including at the final check:
// row locks pin logout and account changes, but time can expire the Cookie
// while an investigation or branch lock blocks the request.
func (s *Service) canvasSession(ctx context.Context, tx pgx.Tx, r *http.Request) *graphFailure {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return &graphFailure{401, "unauthorized", "Login required"}
	}
	err = store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID)
	if errors.Is(err, store.ErrSessionRevoked) {
		return &graphFailure{401, "unauthorized", "Login required"}
	}
	if err != nil {
		return graphUnavailable()
	}
	return nil
}

// The application owner team and environment always come from the database.
// Write callers lock a branch before this investigation row, matching the
// legacy branch mutation lock order.
func (s *Service) canvasTarget(ctx context.Context, tx pgx.Tx, id string, write bool) (investigationCanvasTarget, *graphFailure) {
	lock := " FOR SHARE OF i,a,ae,vae"
	if write {
		lock = " FOR NO KEY UPDATE OF i FOR SHARE OF a,ae,vae"
	}
	var target investigationCanvasTarget
	err := tx.QueryRow(ctx, `SELECT i.id,i.title,i.status,COALESCE(creator.name,''),
 i.application_id,ae.key,COALESCE(a.owner_team_id,'')
 FROM investigations i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments ae ON ae.id=i.environment_id AND ae.application_id=i.application_id AND NOT ae.archived
 JOIN v3_application_environments vae ON vae.application_id=i.application_id AND vae.environment_id=ae.key
 LEFT JOIN users creator ON creator.id=i.created_by
 WHERE i.id=$1`+lock, id).Scan(&target.id, &target.title, &target.status, &target.creator,
		&target.application, &target.environment, &target.team)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, graphNotFound()
	}
	if err != nil {
		return target, graphUnavailable()
	}
	return target, nil
}

func (s *Service) canvasPermission(ctx context.Context, tx pgx.Tx, user core.User, target investigationCanvasTarget, action string) *graphFailure {
	policy := &rbac.V3Policy{Store: s.Store}
	err := policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
		Module: "canvas", Action: action, EnvironmentID: target.environment, ApplicationID: target.application,
	})
	if errors.Is(err, store.ErrForbidden) && target.team != "" {
		err = policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
			Module: "canvas", Action: action, EnvironmentID: target.environment, TeamID: target.team,
		})
	}
	if errors.Is(err, store.ErrForbidden) {
		return graphNotFound()
	}
	if err != nil {
		return graphUnavailable()
	}
	return nil
}

func canvasEnvironment(env string) bool { return env == "prod" || env == "staging" || env == "test" }

type investigationCanvasContext struct {
	Version  string   `json:"version"`
	Inherits string   `json:"inherits,omitempty"`
	Tokens   *int64   `json:"tokens"`
	Messages int      `json:"messages"`
	Excludes []string `json:"excludes"`
}

type investigationCanvasVersion struct {
	V    string    `json:"v"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type investigationCanvasNode struct {
	ID            string                        `json:"id"`
	Kind          string                        `json:"kind"`
	Parent        string                        `json:"parent,omitempty"`
	Title         string                        `json:"title"`
	Subtitle      string                        `json:"subtitle"`
	Status        string                        `json:"status"`
	X             int                           `json:"x"`
	Y             int                           `json:"y"`
	W             int                           `json:"w"`
	Collapsed     bool                          `json:"collapsed"`
	LayoutVersion int64                         `json:"layout_version"`
	EvidenceRefs  []string                      `json:"evidence_refs,omitempty"`
	Context       investigationCanvasContext    `json:"context"`
	Versions      []investigationCanvasVersion  `json:"versions"`
	Writebacks    investigationCanvasWritebacks `json:"writebacks"`
	// Summary is the start of the branch's latest completed AI reply (at most
	// 280 characters, "…" when cut), or null; the full replies are on
	// /investigations/{id}/nodes/{node}/replies (PA-75).
	Summary *string `json:"summary"`
	// RollbackSuggestion is the latest AI rollback suggestion of this branch
	// (PA-72), or null. The card re-reads the candidates to tell if it is
	// out of date.
	RollbackSuggestion *rollbackSuggestion `json:"rollback_suggestion"`
}

// investigationCanvasWritebacks counts the writebacks this node received, as
// persisted writeback context revisions, and the nodes they came from (PA-62).
type investigationCanvasWritebacks struct {
	Received int      `json:"received"`
	Sources  []string `json:"sources"`
}

type investigationCanvasEvidence struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Source string `json:"source"`
	Body   struct {
		Type string     `json:"type"`
		Rows [][]string `json:"rows"`
	} `json:"body"`
}

type investigationCanvasGraph struct {
	Investigation struct {
		ID           string   `json:"id"`
		Title        string   `json:"title"`
		Status       string   `json:"status"`
		Env          string   `json:"env"`
		Participants []string `json:"participants"`
	} `json:"investigation"`
	Loop  []any                         `json:"loop"`
	Nodes []investigationCanvasNode     `json:"nodes"`
	Tray  []investigationCanvasEvidence `json:"tray"`
}

type graphBranch struct {
	id, parent, title string
	revision          int64
	inherited         *int64
	x, y, w           int
	collapsed         bool
	layoutVersion     int64
	hasLayout         bool
	running           bool
}

func canvasVersion(n int64) string { return fmt.Sprintf("v%d", n) }

// The database owns topology and saved layout. For an older branch with no
// layout row, only its drawing coordinates are deterministically projected;
// no evidence, conclusion, or model result is invented.
func orderedGraphBranches(input []graphBranch) ([]graphBranch, error) {
	byID := make(map[string]graphBranch, len(input))
	children := map[string][]string{}
	for _, branch := range input {
		if _, duplicate := byID[branch.id]; duplicate {
			return nil, errors.New("duplicate branch")
		}
		byID[branch.id] = branch
		children[branch.parent] = append(children[branch.parent], branch.id)
	}
	for parent, ids := range children {
		if parent != "" {
			if _, exists := byID[parent]; !exists {
				return nil, errors.New("cross-investigation parent")
			}
		}
		sort.Strings(ids)
	}
	if len(children[""]) == 0 {
		return nil, errors.New("investigation has no root branch")
	}
	type queued struct {
		id, parent string
		depth      int
	}
	queue := []queued{}
	for _, id := range children[""] {
		queue = append(queue, queued{id: id})
	}
	ordered := make([]graphBranch, 0, len(input))
	visited := map[string]bool{}
	for len(queue) > 0 {
		entry := queue[0]
		queue = queue[1:]
		if visited[entry.id] {
			return nil, errors.New("branch cycle")
		}
		visited[entry.id] = true
		branch := byID[entry.id]
		if !branch.hasLayout {
			branch.x, branch.y, branch.w = 16+entry.depth*320, 40+len(ordered)*150, 260
		}
		ordered = append(ordered, branch)
		for _, id := range children[entry.id] {
			queue = append(queue, queued{id: id, parent: entry.id, depth: entry.depth + 1})
		}
	}
	if len(ordered) != len(input) {
		return nil, errors.New("unreachable branch")
	}
	return ordered, nil
}

// branchTitle shows the fixed title a root branch is created with — "主调查"
// on the canvas, "Main investigation" when an alert starts the investigation —
// in the request language. Stored data is unchanged, and any other title is a
// person's and shown as stored (PA-166).
func branchTitle(title, parent, language string) string {
	if parent != "" || title != "主调查" && title != "Main investigation" {
		return title
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en") {
		return "Main investigation"
	}
	return "主调查"
}

func (s *Service) readCanvasGraph(ctx context.Context, tx pgx.Tx, target investigationCanvasTarget, language string) (investigationCanvasGraph, *graphFailure) {
	graph := investigationCanvasGraph{Loop: []any{}, Nodes: []investigationCanvasNode{}, Tray: []investigationCanvasEvidence{}}
	if !canvasEnvironment(target.environment) {
		return graph, &graphFailure{422, "canvas_environment_unsupported", "Investigation environment has no CanvasGraph representation"}
	}
	graph.Investigation.ID, graph.Investigation.Title, graph.Investigation.Env = target.id, target.title, target.environment
	switch target.status {
	case "open":
		graph.Investigation.Status = "active"
	case "closed":
		graph.Investigation.Status = "resolved"
	default:
		return graph, graphUnavailable()
	}
	graph.Investigation.Participants = []string{}
	if target.creator != "" {
		graph.Investigation.Participants = append(graph.Investigation.Participants, target.creator)
	}

	rows, err := tx.Query(ctx, `SELECT b.id,COALESCE(b.parent_id,''),b.title,b.revision,b.inherited_parent_revision,
 COALESCE(l.x,0),COALESCE(l.y,0),COALESCE(l.w,260),COALESCE(l.collapsed,false),COALESCE(l.version,0),
 l.branch_id IS NOT NULL,
 (EXISTS(SELECT 1 FROM runs r WHERE r.branch_id=b.id AND r.status IN ('queued','running'))
  OR EXISTS(SELECT 1 FROM v3_investigation_node_replies nr WHERE nr.branch_id=b.id
    AND nr.state='running' AND nr.lease_expires_at>clock_timestamp()))
 FROM branches b LEFT JOIN v3_investigation_node_layouts l ON l.branch_id=b.id
 WHERE b.investigation_id=$1 ORDER BY b.id LIMIT $2`, target.id, maxInvestigationNodes+1)
	if err != nil {
		return graph, graphUnavailable()
	}
	branches := []graphBranch{}
	for rows.Next() {
		var branch graphBranch
		if err = rows.Scan(&branch.id, &branch.parent, &branch.title, &branch.revision, &branch.inherited,
			&branch.x, &branch.y, &branch.w, &branch.collapsed, &branch.layoutVersion, &branch.hasLayout, &branch.running); err != nil {
			break
		}
		branches = append(branches, branch)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(branches) == 0 {
		return graph, graphUnavailable()
	}
	if len(branches) > maxInvestigationNodes {
		return graph, &graphFailure{413, "canvas_graph_too_large", "Investigation graph exceeds node limit"}
	}
	branches, err = orderedGraphBranches(branches)
	if err != nil {
		return graph, graphUnavailable()
	}
	ids := make([]string, 0, len(branches))
	index := make(map[string]int, len(branches))
	for _, branch := range branches {
		ids = append(ids, branch.id)
		index[branch.id] = len(graph.Nodes)
		kind, subtitle := "branch", "Branch"
		if branch.parent == "" {
			kind, subtitle = "root", "Main investigation"
		}
		if !strings.HasPrefix(strings.ToLower(language), "en") {
			if branch.parent == "" {
				subtitle = "主调查"
			} else {
				subtitle = "调查分支"
			}
		}
		status := "active"
		if branch.running {
			status = "running"
		}
		node := investigationCanvasNode{
			ID: branch.id, Kind: kind, Parent: branch.parent, Title: branchTitle(branch.title, branch.parent, language), Subtitle: subtitle,
			Status: status, X: branch.x, Y: branch.y, W: branch.w,
			Collapsed: branch.collapsed, LayoutVersion: branch.layoutVersion,
			Context:    investigationCanvasContext{Version: canvasVersion(branch.revision), Excludes: []string{}},
			Versions:   []investigationCanvasVersion{},
			Writebacks: investigationCanvasWritebacks{Sources: []string{}},
		}
		if branch.inherited != nil {
			node.Context.Inherits = canvasVersion(*branch.inherited)
		}
		graph.Nodes = append(graph.Nodes, node)
	}

	rows, err = tx.Query(ctx, `SELECT branch_id,revision,created_at,
 CASE WHEN jsonb_typeof(messages)='array' THEN jsonb_array_length(messages) ELSE -1 END
 FROM context_revisions WHERE branch_id=ANY($1::text[])
 ORDER BY branch_id,revision LIMIT $2`, ids, maxInvestigationVersions+1)
	if err != nil {
		return graph, graphUnavailable()
	}
	versions := 0
	currentFound := make(map[string]bool, len(branches))
	for rows.Next() {
		var branchID string
		var revision int64
		var at time.Time
		var count int
		if err = rows.Scan(&branchID, &revision, &at, &count); err != nil {
			break
		}
		versions++
		if versions > maxInvestigationVersions {
			break
		}
		if count < 0 {
			err = errors.New("context revision is not an array")
			break
		}
		atIndex, found := index[branchID]
		if !found {
			err = errors.New("unbound context revision")
			break
		}
		text := fmt.Sprintf("Context revision %d", revision)
		if !strings.HasPrefix(strings.ToLower(language), "en") {
			text = fmt.Sprintf("上下文版本 %d", revision)
		}
		graph.Nodes[atIndex].Versions = append(graph.Nodes[atIndex].Versions,
			investigationCanvasVersion{V: canvasVersion(revision), Text: text, At: at})
		if graph.Nodes[atIndex].Context.Version == canvasVersion(revision) {
			graph.Nodes[atIndex].Context.Messages = count
			currentFound[branchID] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if versions > maxInvestigationVersions {
		return graph, &graphFailure{413, "canvas_graph_too_large", "Investigation graph exceeds revision limit"}
	}
	if err != nil || len(currentFound) != len(branches) {
		return graph, graphUnavailable()
	}

	// No provider-neutral token count is recorded for the current context
	// revision. Historical run-step usage is not the current context budget.

	// Compute the display bound in PostgreSQL. CASE keeps oversized text out of
	// the result set before pgx materializes a row in this process.
	rows, err = tx.Query(ctx, `SELECT e.id,e.branch_id,
 CASE WHEN b.display_bytes<=$3 THEN e.kind ELSE '' END,
 CASE WHEN b.display_bytes<=$3 THEN e.title ELSE '' END,
 CASE WHEN b.display_bytes<=$3 THEN e.content ELSE '' END,
 CASE WHEN b.display_bytes<=$3 THEN e.source ELSE '' END,b.display_bytes
 FROM evidence e CROSS JOIN LATERAL (VALUES (
   octet_length(e.kind)::bigint+octet_length(e.title)+octet_length(e.content)+octet_length(e.source)
 )) b(display_bytes)
 WHERE e.branch_id=ANY($1::text[]) ORDER BY e.created_at,e.id LIMIT $2`,
		ids, maxInvestigationEvidence+1, maxInvestigationText)
	if err != nil {
		return graph, graphUnavailable()
	}
	evidenceLimit := false
	for rows.Next() {
		var id, branchID, kind, title, content, source string
		var displayBytes int64
		if err = rows.Scan(&id, &branchID, &kind, &title, &content, &source, &displayBytes); err != nil {
			break
		}
		if len(graph.Tray) >= maxInvestigationEvidence || displayBytes > maxInvestigationText {
			evidenceLimit = true
			break
		}
		item := investigationCanvasEvidence{ID: id, Kind: "table", Title: title, Source: source}
		item.Body.Type = "table"
		item.Body.Rows = [][]string{{"kind", kind}, {"content", content}}
		graph.Tray = append(graph.Tray, item)
		atIndex, found := index[branchID]
		if !found {
			err = errors.New("unbound evidence")
			break
		}
		graph.Nodes[atIndex].EvidenceRefs = append(graph.Nodes[atIndex].EvidenceRefs, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if evidenceLimit {
		return graph, &graphFailure{413, "canvas_graph_too_large", "Investigation evidence exceeds display limit"}
	}
	if err != nil {
		return graph, graphUnavailable()
	}
	rows, err = tx.Query(ctx, `SELECT branch_id,count(*)::int,
 COALESCE(array_agg(DISTINCT provenance->>'source_branch_id') FILTER (WHERE provenance->>'source_branch_id' IS NOT NULL),'{}')
 FROM context_revisions WHERE branch_id=ANY($1::text[]) AND provenance->>'kind'='writeback'
 GROUP BY branch_id`, ids)
	if err != nil {
		return graph, graphUnavailable()
	}
	for rows.Next() {
		var branchID string
		var received int
		var sources []string
		if err = rows.Scan(&branchID, &received, &sources); err != nil {
			break
		}
		atIndex, found := index[branchID]
		if !found {
			continue
		}
		graph.Nodes[atIndex].Writebacks.Received = received
		for _, source := range sources {
			if _, inGraph := index[source]; inGraph {
				graph.Nodes[atIndex].Writebacks.Sources = append(graph.Nodes[atIndex].Writebacks.Sources, source)
			}
		}
		sort.Strings(graph.Nodes[atIndex].Writebacks.Sources)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return graph, graphUnavailable()
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT ON (branch_id) branch_id,left(assistant_text,$2+1) FROM v3_investigation_node_replies
 WHERE branch_id=ANY($1::text[]) AND state='completed'
 ORDER BY branch_id,completed_at DESC,id DESC`, ids, maxNodeSummaryRunes)
	if err != nil {
		return graph, graphUnavailable()
	}
	for rows.Next() {
		var branchID, text string
		if err = rows.Scan(&branchID, &text); err != nil {
			break
		}
		if atIndex, found := index[branchID]; found {
			if runes := []rune(strings.TrimSpace(text)); len(runes) > maxNodeSummaryRunes {
				text = string(runes[:maxNodeSummaryRunes]) + "…"
			} else {
				text = string(runes)
			}
			graph.Nodes[atIndex].Summary = &text
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return graph, graphUnavailable()
	}
	rows, err = tx.Query(ctx, `SELECT DISTINCT ON (branch_id) branch_id,rollback_suggestion FROM v3_investigation_node_replies
 WHERE branch_id=ANY($1::text[]) AND state='completed' AND rollback_suggestion IS NOT NULL
 ORDER BY branch_id,completed_at DESC,id DESC`, ids)
	if err != nil {
		return graph, graphUnavailable()
	}
	for rows.Next() {
		var branchID string
		var raw []byte
		if err = rows.Scan(&branchID, &raw); err != nil {
			break
		}
		var suggestion rollbackSuggestion
		if err = json.Unmarshal(raw, &suggestion); err != nil {
			break
		}
		if atIndex, found := index[branchID]; found {
			graph.Nodes[atIndex].RollbackSuggestion = &suggestion
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return graph, graphUnavailable()
	}
	return graph, nil
}

func (s *Service) investigationCanvas(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, false)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.Store.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	defer tx.Rollback(ctx)
	if failure := s.canvasPrincipal(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	target, failure := s.canvasTarget(ctx, tx, r.PathValue("id"), false)
	if failure != nil {
		failure.write(w)
		return
	}
	if !slicesContains(envs, target.environment) {
		graphNotFound().write(w)
		return
	}
	if failure = s.canvasPermission(ctx, tx, httpapi.CurrentUser(r), target, "view"); failure != nil {
		failure.write(w)
		return
	}
	graph, failure := s.readCanvasGraph(ctx, tx, target, r.Header.Get("Accept-Language"))
	if failure != nil {
		failure.write(w)
		return
	}
	if failure = s.canvasSession(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		graphUnavailable().write(w)
		return
	}
	httpapi.WriteJSON(w, 200, graph)
}
