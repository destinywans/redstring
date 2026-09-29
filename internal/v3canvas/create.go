package v3canvas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type creationFailure struct {
	status int
	code   string
	text   string
}

func (f *creationFailure) write(w http.ResponseWriter) {
	httpapi.Error(w, f.status, f.code, f.text)
}

func creationUnavailable() *creationFailure {
	return &creationFailure{503, "canvas_unavailable", "Canvas creation is unavailable"}
}

type mappedService struct {
	environmentRow string
	team           string
}

// The same checks run before a model call and again inside the write
// transaction. Model latency never holds session or catalog row locks.
func (s *Service) prepareCreation(ctx context.Context, tx pgx.Tx, token string, user core.User, env string, in createRequest) (mappedService, *creationFailure) {
	var sessionUser string
	err := tx.QueryRow(ctx, `SELECT s.user_id FROM sessions s JOIN users u ON u.id=s.user_id
 WHERE s.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL FOR SHARE OF s,u`, store.Hash(token), user.ID).Scan(&sessionUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return mappedService{}, &creationFailure{401, "unauthorized", "Login required"}
	}
	if err != nil {
		return mappedService{}, creationUnavailable()
	}
	if err = lockCanvasGrantSources(ctx, tx, sessionUser); err != nil {
		return mappedService{}, creationUnavailable()
	}
	policy := &rbac.V3Policy{Store: s.Store}
	decision := rbac.V3Decision{Module: "canvas", Action: "change", EnvironmentID: env}
	if in.Kind != "investigation" {
		if err = policy.AuthorizeWithTx(ctx, tx, user, decision); errors.Is(err, store.ErrForbidden) {
			return mappedService{}, &creationFailure{403, "forbidden", "Canvas change permission is required"}
		} else if err != nil {
			return mappedService{}, creationUnavailable()
		}
	}
	service := mappedService{}
	missingService := false
	for _, app := range in.Services {
		var row, team string
		err = tx.QueryRow(ctx, `SELECT ae.id,COALESCE(a.owner_team_id,'')
 FROM applications a JOIN application_environments ae ON ae.application_id=a.id
 JOIN v3_application_environments vae ON vae.application_id=a.id AND vae.environment_id=ae.key
 WHERE a.id=$1 AND ae.key=$2 AND NOT a.archived AND NOT ae.archived
 FOR SHARE OF a,ae,vae`, app, env).Scan(&row, &team)
		if errors.Is(err, pgx.ErrNoRows) {
			missingService = true
			continue
		}
		if err != nil {
			return mappedService{}, creationUnavailable()
		}
		if in.Kind == "investigation" {
			service = mappedService{environmentRow: row, team: team}
		}
	}
	if in.Kind == "investigation" {
		decision.ApplicationID = in.Services[0]
		err = policy.AuthorizeWithTx(ctx, tx, user, decision)
		if errors.Is(err, store.ErrForbidden) && service.team != "" {
			decision.ApplicationID = ""
			decision.TeamID = service.team
			err = policy.AuthorizeWithTx(ctx, tx, user, decision)
		}
		if errors.Is(err, store.ErrForbidden) {
			return mappedService{}, &creationFailure{403, "forbidden", "Canvas change permission is required"}
		}
		if err != nil {
			return mappedService{}, creationUnavailable()
		}
	}
	if missingService {
		return mappedService{}, &creationFailure{422, "invalid_service", "Service is not active in the selected environment"}
	}
	return service, nil
}

type draftNode struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

func decodeDraft(answer string) ([]draftNode, error) {
	if answer == "" || len(answer) > 16<<10 {
		return nil, errors.New("empty or oversized draft")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(answer))
	decoder.DisallowUnknownFields()
	var document struct {
		Nodes []draftNode `json:"nodes"`
	}
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("draft has trailing content")
	}
	if len(document.Nodes) < 1 || len(document.Nodes) > 3 {
		return nil, errors.New("draft must have one to three nodes")
	}
	for _, node := range document.Nodes {
		if strings.TrimSpace(node.Title) != node.Title || node.Title == "" || len([]rune(node.Title)) > 100 ||
			strings.TrimSpace(node.Summary) != node.Summary || node.Summary == "" || len([]rune(node.Summary)) > 1000 {
			return nil, errors.New("draft node is invalid")
		}
	}
	return document.Nodes, nil
}

func (s *Service) generateDraft(ctx context.Context, env string, in createRequest) ([]draftNode, string, *creationFailure) {
	model, ok := s.selectedModelForEnvironment(env, "")
	if !ok {
		return nil, "", &creationFailure{503, "canvas_draft_unavailable", "No drafting model is configured"}
	}
	modelCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Service IDs have already been verified against the selected environment.
	// They are identifiers, not observations about live service health.
	input := mustJSON(map[string]any{"title": in.Title, "goal": in.Goal, "service_ids": in.Services})
	answer, err := s.Runtime.Run(modelCtx, inference.Request{
		RunID: store.ID(), AttemptID: store.ID(), BranchID: "canvas-draft", EnvironmentID: env, ModelID: model.ID,
		Messages: []inference.Message{
			{Role: "system", Content: "Create an unverified canvas outline from the user's title, goal and service IDs only. Do not claim to have inspected systems, logs, metrics or runbooks. Return only a JSON object with nodes: 1 to 3 objects, each containing a short title and summary. No markdown."},
			{Role: "user", Content: string(input)},
		},
		MaxCalls: 1, MaxToolCalls: 1, TimeoutSeconds: 45,
	}, func(event inference.Event) error {
		if event.Type == "evidence" || event.Evidence != nil || event.Invocation != nil {
			return errors.New("draft cannot use tools or evidence")
		}
		return modelCtx.Err()
	})
	if err != nil || modelCtx.Err() != nil {
		return nil, "", &creationFailure{503, "canvas_draft_unavailable", "AI draft could not be generated"}
	}
	nodes, err := decodeDraft(answer)
	if err != nil {
		return nil, "", &creationFailure{503, "canvas_draft_unavailable", "AI draft structure was invalid"}
	}
	return nodes, model.ID, nil
}

func (s *Service) createCanvas(w http.ResponseWriter, r *http.Request) {
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
	in.Title, in.Goal = strings.TrimSpace(in.Title), strings.TrimSpace(in.Goal)
	if in.Title == "" || len([]rune(in.Title)) > 200 || len(in.Goal) > 10000 || !validStrings(in.Services) || len(in.TemplateID) > 200 {
		(&creationFailure{422, "invalid_canvas", "Check title, goal, template and distinct service IDs"}).write(w)
		return
	}
	if in.Kind != "investigation" && !validKind(in.Kind) {
		(&creationFailure{422, "invalid_kind", "Unknown canvas kind"}).write(w)
		return
	}
	if in.Kind == "investigation" && len(in.Services) != 1 {
		(&creationFailure{422, "invalid_service", "Select exactly one investigation service"}).write(w)
		return
	}
	if in.Kind == "investigation" && in.TemplateID != "" {
		(&creationFailure{422, "invalid_template", "Investigation templates are not available"}).write(w)
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		(&creationFailure{401, "unauthorized", "Login required"}).write(w)
		return
	}
	user, env := httpapi.CurrentUser(r), envs[0]
	var draft []draftNode
	var modelID string
	if in.AIDraft {
		preflight, err := s.Store.DB.Begin(r.Context())
		if err != nil {
			creationUnavailable().write(w)
			return
		}
		_, failure := s.prepareCreation(r.Context(), preflight, cookie.Value, user, env, in)
		if failure == nil && in.Kind != "investigation" && in.TemplateID != "" {
			var nodes []Node
			var edges []Edge
			nodes, edges, failure = loadCreationTemplate(r.Context(), preflight, user.ID, env, in)
			if failure == nil && (len(nodes) > maxCanvasNodes-3 || len(edges) > maxCanvasEdges-3) {
				failure = &creationFailure{422, "invalid_canvas", "Template has no room for an AI draft"}
			}
		}
		_ = preflight.Rollback(r.Context())
		if failure != nil {
			failure.write(w)
			return
		}
		draft, modelID, failure = s.generateDraft(r.Context(), env, in)
		if failure != nil {
			failure.write(w)
			return
		}
	}
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		creationUnavailable().write(w)
		return
	}
	defer tx.Rollback(r.Context())
	service, failure := s.prepareCreation(r.Context(), tx, cookie.Value, user, env, in)
	if failure != nil {
		failure.write(w)
		return
	}
	var id string
	if in.Kind == "investigation" {
		id, failure = s.createInvestigationCanvas(r.Context(), tx, user, env, in, service, draft, modelID)
	} else {
		id, failure = s.createGenericCanvas(r.Context(), tx, user, env, in, draft, modelID)
	}
	if failure != nil {
		failure.write(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		creationUnavailable().write(w)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"id": id, "href": "/canvas/" + id})
}

func (s *Service) createGenericCanvas(ctx context.Context, tx pgx.Tx, user core.User, env string, in createRequest, draft []draftNode, modelID string) (string, *creationFailure) {
	nodes := []Node{{ID: "root", X: 16, Y: 140, W: 260, Icon: "layers", Tone: "info", Title: in.Title, Sub: "主线", Blocks: []json.RawMessage{}}}
	edges := []Edge{}
	if in.TemplateID != "" {
		var failure *creationFailure
		nodes, edges, failure = loadCreationTemplate(ctx, tx, user.ID, env, in)
		if failure != nil {
			return "", failure
		}
		nodes[0].Title = in.Title
	}
	root := nodes[0].ID
	used := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		used[node.ID] = true
	}
	for i, item := range draft {
		id := "draft-" + store.ID()
		for used[id] {
			id = "draft-" + store.ID()
		}
		used[id] = true
		block := mustJSON(map[string]string{"type": "text", "text": "AI 草稿（未经验证）：" + item.Summary})
		nodes = append(nodes, Node{ID: id, X: 320, Y: 60 + i*160, W: 240, Icon: "sparkles", Tone: "accent", Title: item.Title, Sub: "AI 草稿", Blocks: []json.RawMessage{block}})
		edges = append(edges, Edge{From: root, To: id})
	}
	if _, _, _, err := decodeCanvasDocument(mustJSON(nodes), mustJSON(edges), []byte(`[]`)); err != nil {
		return "", &creationFailure{422, "invalid_canvas", "Canvas structure exceeds limits"}
	}
	id := "CV-" + store.ID()
	_, err := tx.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,goal,services,nodes,edges)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb)`, id, user.ID, env, in.Title, in.Kind, in.Goal, in.Services, mustJSON(nodes), mustJSON(edges))
	if err != nil {
		return "", creationUnavailable()
	}
	changes := mustJSON(map[string]any{"environment_id": env, "kind": in.Kind, "services": in.Services, "ai_draft": in.AIDraft, "model_id": modelID})
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.canvas.created',$2,'success',$3::jsonb)`, user.ID, id, changes)
	if err != nil {
		return "", &creationFailure{503, "audit_unavailable", "Canvas audit unavailable"}
	}
	return id, nil
}

func loadCreationTemplate(ctx context.Context, tx pgx.Tx, owner, env string, in createRequest) ([]Node, []Edge, *creationFailure) {
	var rawNodes, rawEdges []byte
	err := tx.QueryRow(ctx, `SELECT nodes,edges FROM v3_canvas_templates
 WHERE id=$1 AND kind=$2 AND (owner_id IS NULL OR (owner_id=$3 AND environment_id=$4))
 FOR SHARE`, in.TemplateID, in.Kind, owner, env).Scan(&rawNodes, &rawEdges)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, &creationFailure{404, "template_not_found", "Template not found"}
	}
	if err != nil {
		return nil, nil, creationUnavailable()
	}
	nodes, edges, _, err := decodeCanvasDocument(rawNodes, rawEdges, []byte(`[]`))
	if err != nil {
		return nil, nil, &creationFailure{503, "template_unavailable", "Template structure is invalid"}
	}
	if len(nodes) == 0 {
		nodes = []Node{{ID: "root", X: 16, Y: 140, W: 260, Icon: "layers", Tone: "info", Title: in.Title, Sub: "主线", Blocks: []json.RawMessage{}}}
	}
	return nodes, edges, nil
}

func (s *Service) createInvestigationCanvas(ctx context.Context, tx pgx.Tx, user core.User, env string, in createRequest, service mappedService, draft []draftNode, modelID string) (string, *creationFailure) {
	id, root := "INV-"+store.ID(), store.ID()
	_, err := tx.Exec(ctx, `INSERT INTO investigations(id,application_id,environment_id,title,goal,created_by)
 VALUES($1,$2,$3,$4,$5,$6)`, id, in.Services[0], service.environmentRow, in.Title, in.Goal, user.ID)
	if err != nil {
		return "", creationUnavailable()
	}
	if _, err = tx.Exec(ctx, `INSERT INTO branches(id,investigation_id,title) VALUES($1,$2,'主调查')`, root, id); err != nil {
		return "", creationUnavailable()
	}
	if _, err = tx.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 VALUES($1,0,'[]'::jsonb,$2::jsonb)`, root, mustJSON(map[string]string{"kind": "initial"})); err != nil {
		return "", creationUnavailable()
	}
	if _, err = tx.Exec(ctx, `INSERT INTO events(investigation_id,branch_id,type,payload)
 VALUES($1,$2,'investigation.created',$3::jsonb)`, id, root, mustJSON(map[string]string{"id": id})); err != nil {
		return "", creationUnavailable()
	}
	for _, item := range draft {
		branchID := store.ID()
		_, err = tx.Exec(ctx, `INSERT INTO branches(id,investigation_id,parent_id,title,model_id,inherited_parent_revision,synced_parent_revision)
 VALUES($1,$2,$3,$4,$5,0,0)`, branchID, id, root, item.Title, modelID)
		if err != nil {
			return "", creationUnavailable()
		}
		messages := []core.Message{{ID: store.ID(), Role: "assistant", Content: "AI 草稿（未经验证）：" + item.Summary, CreatedAt: time.Now().UTC(), ModelID: modelID}}
		provenance := mustJSON(map[string]any{"kind": "ai_draft", "source_branch_id": root, "source_revision": 0, "model_id": modelID})
		_, err = tx.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 VALUES($1,0,$2::jsonb,$3::jsonb)`, branchID, mustJSON(messages), provenance)
		if err != nil {
			return "", creationUnavailable()
		}
		_, err = tx.Exec(ctx, `INSERT INTO events(investigation_id,branch_id,type,payload)
 VALUES($1,$2,'branch.forked',$3::jsonb)`, id, branchID, mustJSON(map[string]any{"parent_id": root, "parent_revision": 0, "source": "ai_draft"}))
		if err != nil {
			return "", creationUnavailable()
		}
	}
	changes := mustJSON(map[string]any{"environment_id": env, "kind": in.Kind, "services": in.Services, "ai_draft": in.AIDraft, "model_id": modelID})
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.investigation_canvas.created',$3,'success',$4::jsonb)`, user.ID, in.Services[0], id, changes)
	if err != nil {
		return "", &creationFailure{503, "audit_unavailable", "Investigation audit unavailable"}
	}
	return id, nil
}
