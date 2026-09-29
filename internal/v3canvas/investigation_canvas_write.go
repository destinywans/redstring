package v3canvas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type investigationBranchRequest struct {
	ParentID               string `json:"parent_id"`
	Parent                 string `json:"parent"`
	Title                  string `json:"title"`
	X                      *int   `json:"x"`
	Y                      *int   `json:"y"`
	ExpectedParentRevision *int64 `json:"expected_parent_revision"`
}

type investigationNodePatch struct {
	X                     *int    `json:"x"`
	Y                     *int    `json:"y"`
	Title                 *string `json:"title"`
	ExpectedTitle         *string `json:"expected_title"`
	Collapsed             *bool   `json:"collapsed"`
	Collapse              *bool   `json:"collapse"`
	ExpectedLayoutVersion *int64  `json:"expected_layout_version"`
}

func validGraphCoordinate(value int) bool { return value >= -100000 && value <= 100000 }

func canvasNodeByID(graph investigationCanvasGraph, id string) (investigationCanvasNode, bool) {
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return investigationCanvasNode{}, false
}

// Lock the concrete branch first, matching the legacy Fork/PatchBranch order.
// All object authorization then uses the investigation and current owner team
// resolved from the persisted rows in this same transaction.
func (s *Service) lockedCanvasBranch(ctx context.Context, tx pgx.Tx, investigationID, branchID string) (string, int64, *graphFailure) {
	var model string
	var revision int64
	err := tx.QueryRow(ctx, `SELECT model_id,revision FROM branches
 WHERE id=$1 AND investigation_id=$2 FOR NO KEY UPDATE`, branchID, investigationID).Scan(&model, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, graphNotFound()
	}
	if err != nil {
		return "", 0, graphUnavailable()
	}
	return model, revision, nil
}

func (s *Service) writeCanvasTarget(ctx context.Context, tx pgx.Tx, r *http.Request, env, id string) (investigationCanvasTarget, *graphFailure) {
	target, failure := s.canvasTarget(ctx, tx, id, true)
	if failure != nil {
		return target, failure
	}
	if target.environment != env {
		return target, graphNotFound()
	}
	if failure = s.canvasPermission(ctx, tx, httpapi.CurrentUser(r), target, "change"); failure != nil {
		return target, failure
	}
	if target.status != "open" {
		return target, &graphFailure{409, "investigation_closed", "Closed investigations cannot change branches"}
	}
	return target, nil
}

func (s *Service) createInvestigationBranch(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, true)
	if !ok {
		return
	}
	var in investigationBranchRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	parent := in.ParentID
	if parent == "" {
		parent = in.Parent
	}
	if in.ParentID != "" && in.Parent != "" && in.ParentID != in.Parent || parent == "" || len(parent) > 200 ||
		in.Title == "" || len([]rune(in.Title)) > 200 ||
		in.ExpectedParentRevision != nil && *in.ExpectedParentRevision < 0 ||
		in.X != nil && !validGraphCoordinate(*in.X) || in.Y != nil && !validGraphCoordinate(*in.Y) {
		(&graphFailure{422, "invalid_branch", "Check parent, title, coordinates and parent revision"}).write(w)
		return
	}
	ctx, investigationID := r.Context(), r.PathValue("id")
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	defer tx.Rollback(ctx)
	if failure := s.canvasPrincipal(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	modelID, parentRevision, failure := s.lockedCanvasBranch(ctx, tx, investigationID, parent)
	if failure != nil {
		failure.write(w)
		return
	}
	target, failure := s.writeCanvasTarget(ctx, tx, r, envs[0], investigationID)
	if failure != nil {
		failure.write(w)
		return
	}
	if in.ExpectedParentRevision != nil && *in.ExpectedParentRevision != parentRevision {
		(&graphFailure{409, "parent_revision_conflict", "Parent context changed; refresh before forking"}).write(w)
		return
	}
	graph, failure := s.readCanvasGraph(ctx, tx, target, r.Header.Get("Accept-Language"))
	if failure != nil {
		failure.write(w)
		return
	}
	if len(graph.Nodes) >= maxInvestigationNodes {
		(&graphFailure{413, "canvas_graph_too_large", "Investigation graph exceeds node limit"}).write(w)
		return
	}
	parentNode, found := canvasNodeByID(graph, parent)
	if !found {
		graphUnavailable().write(w)
		return
	}
	x, y := parentNode.X+parentNode.W+70, parentNode.Y+40
	for _, node := range graph.Nodes {
		if node.Parent == parent {
			y += 30
		}
	}
	if in.X != nil {
		x = *in.X
	}
	if in.Y != nil {
		y = *in.Y
	}
	if !validGraphCoordinate(x) || !validGraphCoordinate(y) {
		(&graphFailure{422, "invalid_branch", "Branch coordinates exceed limits"}).write(w)
		return
	}
	var raw []byte
	var contextBytes int
	err = tx.QueryRow(ctx, `SELECT octet_length(messages::text),
 CASE WHEN octet_length(messages::text)<=$3 THEN messages END
 FROM context_revisions WHERE branch_id=$1 AND revision=$2`,
		parent, parentRevision, maxNodeContextBytes).Scan(&contextBytes, &raw)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	if contextBytes > maxNodeContextBytes {
		(&graphFailure{413, "node_context_too_large", "Parent context exceeds the fork limit"}).write(w)
		return
	}
	var messages []json.RawMessage
	if err = json.Unmarshal(raw, &messages); err != nil || messages == nil {
		graphUnavailable().write(w)
		return
	}
	branchID := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO branches(id,investigation_id,parent_id,title,model_id,inherited_parent_revision,synced_parent_revision)
 VALUES($1,$2,$3,$4,$5,$6,$6)`, branchID, investigationID, parent, in.Title, modelID, parentRevision)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	provenance := mustJSON(map[string]any{"kind": "fork", "source_branch_id": parent, "source_revision": parentRevision})
	_, err = tx.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 VALUES($1,0,$2::jsonb,$3::jsonb)`, branchID, raw, provenance)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_investigation_node_layouts(branch_id,x,y,w,updated_by)
 VALUES($1,$2,$3,260,$4)`, branchID, x, y, httpapi.CurrentUser(r).ID)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(investigation_id,branch_id,type,payload)
 VALUES($1,$2,'branch.forked',$3::jsonb)`, investigationID, branchID,
		mustJSON(map[string]any{"parent_id": parent, "parent_revision": parentRevision}))
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	_, err = tx.Exec(ctx, `UPDATE investigations SET updated_at=clock_timestamp() WHERE id=$1`, investigationID)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.investigation_canvas.branch_created',$3,'success',$4::jsonb)`,
		httpapi.CurrentUser(r).ID, target.application, branchID,
		mustJSON(map[string]any{"investigation_id": investigationID, "environment_id": target.environment,
			"parent_id": parent, "parent_revision": parentRevision}))
	if err != nil {
		(&graphFailure{503, "audit_unavailable", "Branch audit unavailable"}).write(w)
		return
	}
	graph, failure = s.readCanvasGraph(ctx, tx, target, r.Header.Get("Accept-Language"))
	if failure != nil {
		failure.write(w)
		return
	}
	child, found := canvasNodeByID(graph, branchID)
	if !found {
		graphUnavailable().write(w)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		graphUnavailable().write(w)
		return
	}
	httpapi.WriteJSON(w, 201, child)
}

func (s *Service) patchInvestigationNode(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	envs, ok := allowedScope(w, r, true)
	if !ok {
		return
	}
	var in investigationNodePatch
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.ExpectedLayoutVersion == nil {
		(&graphFailure{428, "precondition_required", "Supply expected_layout_version from the canvas graph"}).write(w)
		return
	}
	if in.Title == nil && in.X == nil && in.Y == nil && in.Collapsed == nil && in.Collapse == nil ||
		*in.ExpectedLayoutVersion < 0 ||
		in.Collapsed != nil && in.Collapse != nil && *in.Collapsed != *in.Collapse ||
		in.X != nil && !validGraphCoordinate(*in.X) || in.Y != nil && !validGraphCoordinate(*in.Y) {
		(&graphFailure{422, "invalid_node_patch", "Check position, title, collapse and layout version"}).write(w)
		return
	}
	if in.Title != nil {
		trimmed := strings.TrimSpace(*in.Title)
		if trimmed == "" || len([]rune(trimmed)) > 200 {
			(&graphFailure{422, "invalid_node_patch", "Node title must be between 1 and 200 characters"}).write(w)
			return
		}
		in.Title = &trimmed
		if in.ExpectedTitle == nil {
			(&graphFailure{428, "precondition_required", "Supply expected_title when editing a node title"}).write(w)
			return
		}
	}
	ctx, investigationID, nodeID := r.Context(), r.PathValue("id"), r.PathValue("node")
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	defer tx.Rollback(ctx)
	if failure := s.canvasPrincipal(ctx, tx, r); failure != nil {
		failure.write(w)
		return
	}
	_, _, failure := s.lockedCanvasBranch(ctx, tx, investigationID, nodeID)
	if failure != nil {
		failure.write(w)
		return
	}
	target, failure := s.writeCanvasTarget(ctx, tx, r, envs[0], investigationID)
	if failure != nil {
		failure.write(w)
		return
	}
	graph, failure := s.readCanvasGraph(ctx, tx, target, r.Header.Get("Accept-Language"))
	if failure != nil {
		failure.write(w)
		return
	}
	node, found := canvasNodeByID(graph, nodeID)
	if !found {
		graphUnavailable().write(w)
		return
	}
	if node.LayoutVersion != *in.ExpectedLayoutVersion {
		(&graphFailure{409, "layout_version_conflict", "Node layout changed; refresh before editing"}).write(w)
		return
	}
	if in.Title != nil && node.Title != *in.ExpectedTitle {
		(&graphFailure{409, "title_conflict", "Node title changed; refresh before editing"}).write(w)
		return
	}
	x, y, collapsed := node.X, node.Y, node.Collapsed
	if in.X != nil {
		x = *in.X
	}
	if in.Y != nil {
		y = *in.Y
	}
	if in.Collapsed != nil {
		collapsed = *in.Collapsed
	}
	if in.Collapse != nil {
		collapsed = *in.Collapse
	}
	if in.Title != nil {
		_, err = tx.Exec(ctx, `UPDATE branches SET title=$2 WHERE id=$1`, nodeID, *in.Title)
		if err != nil {
			graphUnavailable().write(w)
			return
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO v3_investigation_node_layouts(branch_id,x,y,w,collapsed,version,updated_at,updated_by)
 VALUES($1,$2,$3,$4,$5,$6,clock_timestamp(),$7)
 ON CONFLICT(branch_id) DO UPDATE SET x=excluded.x,y=excluded.y,w=excluded.w,
 collapsed=excluded.collapsed,version=excluded.version,updated_at=excluded.updated_at,updated_by=excluded.updated_by`,
		nodeID, x, y, node.W, collapsed, node.LayoutVersion+1, httpapi.CurrentUser(r).ID); err != nil {
		graphUnavailable().write(w)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO events(investigation_id,branch_id,type,payload)
 VALUES($1,$2,'branch.updated',$3::jsonb)`, investigationID, nodeID,
		mustJSON(map[string]any{"id": nodeID, "layout_version": node.LayoutVersion + 1}))
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	_, err = tx.Exec(ctx, `UPDATE investigations SET updated_at=clock_timestamp() WHERE id=$1`, investigationID)
	if err != nil {
		graphUnavailable().write(w)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.investigation_canvas.node_updated',$3,'success',$4::jsonb)`,
		httpapi.CurrentUser(r).ID, target.application, nodeID,
		mustJSON(map[string]any{"investigation_id": investigationID, "environment_id": target.environment,
			"old_layout_version": node.LayoutVersion, "new_layout_version": node.LayoutVersion + 1,
			"title_changed": in.Title != nil, "position_changed": in.X != nil || in.Y != nil,
			"collapse_changed": in.Collapsed != nil || in.Collapse != nil}))
	if err != nil {
		(&graphFailure{503, "audit_unavailable", "Node audit unavailable"}).write(w)
		return
	}
	graph, failure = s.readCanvasGraph(ctx, tx, target, r.Header.Get("Accept-Language"))
	if failure != nil {
		failure.write(w)
		return
	}
	updated, found := canvasNodeByID(graph, nodeID)
	if !found {
		graphUnavailable().write(w)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		graphUnavailable().write(w)
		return
	}
	httpapi.WriteJSON(w, 200, updated)
}
