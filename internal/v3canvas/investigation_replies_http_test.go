package v3canvas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/inference"
)

// PA-75: an investigation branch's replies can be read back after a reload,
// newest first and paged, with the question, answer, model, state and
// evidence; the node carries the start of its latest answer.
func TestInvestigationRepliesReadBack(t *testing.T) {
	db, st := suggestionDB(t)
	ctx := t.Context()
	service := base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))
	metrics := inference.Evidence{Kind: "metrics", Title: "Prometheus samples: service-key / error_rate", Content: `{"status":"observed"}`,
		Source: "prometheus", TargetID: service}
	runner := &investigationEvidenceRunner{}
	svc := NewWithRuntime(st, runner)
	svc.InvestigationToolsForTurn = func(context.Context, string, string, string, string) ([]inference.EvidenceTool, error) {
		return []inference.EvidenceTool{{Name: "read_service_metrics", Description: "metrics",
			Read: func(context.Context, string) (inference.Evidence, error) { return metrics, nil }}}, nil
	}
	long := strings.Repeat("长", 300)
	calls := 0
	runner.run = func(runCtx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
		calls++
		switch calls {
		case 1:
			v := inference.ToolInvocation{Name: "read_service_metrics", StepID: "tool-1", Status: "started"}
			if err := emit(inference.Event{Type: "tool_invocation", StepID: v.StepID, Invocation: &v}); err != nil {
				return "", err
			}
			ev, err := req.Tools[0].Read(runCtx, "service-key")
			if err != nil {
				return "", err
			}
			v.Status, v.TargetID = "succeeded", ev.TargetID
			if err := emit(inference.Event{Type: "tool_invocation", StepID: v.StepID, Invocation: &v}); err != nil {
				return "", err
			}
			if err := emit(inference.Event{Type: "evidence", StepID: v.StepID, Evidence: &ev}); err != nil {
				return "", err
			}
			return "First answer from the error rate.", nil
		case 2:
			return "", errors.New("model unavailable")
		default:
			return long, nil
		}
	}
	api := suggestionAPI(t, st, svc)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "test")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "exact-cookie"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for i, want := range []int{200, 503, 200} {
		if w := call("POST", "/api/v1/investigations/inv/nodes/branch/messages", `{"text":"Question `+string(rune('A'+i))+`","model":"evidence-model"}`); w.Code != want {
			t.Fatalf("reply %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	// An attempt whose lease expired on another branch.
	if _, err := db.Exec(ctx, `INSERT INTO v3_investigation_node_replies
 (id,investigation_id,branch_id,user_id,user_text,model_id,model_name,model_provider,state,base_revision,lease_expires_at)
 VALUES('INR-expired','inv','sibling','actor','Stuck question','evidence-model','Controlled','controlled','running',0,clock_timestamp()-interval '1 minute')`); err != nil {
		t.Fatal(err)
	}

	type page struct {
		Replies    []investigationReply `json:"replies"`
		HasMore    bool                 `json:"has_more"`
		NextCursor *string              `json:"next_cursor"`
	}
	read := func(path string) page {
		t.Helper()
		w := call("GET", path, "")
		var p page
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return p
	}
	first := read("/api/v1/investigations/inv/nodes/branch/replies?page_size=2")
	if len(first.Replies) != 2 || !first.HasMore || first.NextCursor == nil ||
		first.Replies[0].Text != "Question C" || first.Replies[0].Answer == nil || *first.Replies[0].Answer != long ||
		first.Replies[1].Text != "Question B" || first.Replies[1].State != "failed" || first.Replies[1].Answer != nil ||
		first.Replies[1].FailureCode == nil || *first.Replies[1].FailureCode != "model_unavailable" {
		t.Fatalf("first page: %+v", first)
	}
	second := read("/api/v1/investigations/inv/nodes/branch/replies?page_size=2&cursor=" + *first.NextCursor)
	if len(second.Replies) != 1 || second.HasMore || second.NextCursor != nil {
		t.Fatalf("second page: %+v", second)
	}
	r := second.Replies[0]
	if r.Text != "Question A" || r.State != "completed" || r.Answer == nil || *r.Answer != "First answer from the error rate." ||
		r.Author == nil || *r.Author != "Actor" || r.Model.ID != "evidence-model" || r.Model.Name != "Controlled" ||
		r.Model.Provider != "controlled" || r.CompletedAt == nil || len(r.EvidenceIDs) != 1 || r.RollbackSuggestion != nil {
		t.Fatalf("completed reply: %+v", r)
	}
	var evidenceBranch string
	if err := db.QueryRow(ctx, `SELECT branch_id FROM evidence WHERE id=$1`, r.EvidenceIDs[0]).Scan(&evidenceBranch); err != nil || evidenceBranch != "branch" {
		t.Fatalf("evidence id: %s %v", evidenceBranch, err)
	}
	sibling := read("/api/v1/investigations/inv/nodes/sibling/replies")
	if len(sibling.Replies) != 1 || sibling.Replies[0].State != "failed" || sibling.Replies[0].FailureCode == nil ||
		*sibling.Replies[0].FailureCode != "lease_expired" {
		t.Fatalf("sibling replies: %+v", sibling)
	}

	// The node shows the start of its latest answer.
	var graph struct {
		Nodes []struct {
			ID      string  `json:"id"`
			Summary *string `json:"summary"`
		} `json:"nodes"`
	}
	w := call("GET", "/api/v1/investigations/inv/canvas", "")
	if err := json.Unmarshal(w.Body.Bytes(), &graph); err != nil || w.Code != 200 {
		t.Fatalf("canvas: %d %s", w.Code, w.Body.String())
	}
	for _, n := range graph.Nodes {
		switch n.ID {
		case "branch":
			if n.Summary == nil || *n.Summary != strings.Repeat("长", 280)+"…" {
				t.Fatalf("branch summary: %v", n.Summary)
			}
		case "sibling":
			if n.Summary != nil {
				t.Fatalf("sibling has no completed reply: %v", *n.Summary)
			}
		}
	}

	for path, status := range map[string]int{
		"/api/v1/investigations/inv/nodes/branch/replies?cursor=garbage":               422,
		"/api/v1/investigations/inv/nodes/sibling/replies?cursor=" + *first.NextCursor: 422,
		"/api/v1/investigations/inv/nodes/branch/replies?page_size=0":                  422,
		"/api/v1/investigations/inv/nodes/branch/replies?limit=5":                      422,
		"/api/v1/investigations/inv/nodes/missing/replies":                             404,
	} {
		if w := call("GET", path, ""); w.Code != status {
			t.Fatalf("%s: %d, want %d: %s", path, w.Code, status, w.Body.String())
		}
	}
	// Without canvas.view the investigation is hidden, as for the canvas.
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='evidence-editor' AND module='canvas'`); err != nil {
		t.Fatal(err)
	}
	canvasStatus := call("GET", "/api/v1/investigations/inv/canvas", "").Code
	if w := call("GET", "/api/v1/investigations/inv/nodes/branch/replies", ""); w.Code != canvasStatus || w.Code == 200 {
		t.Fatalf("without canvas.view: %d (canvas %d)", w.Code, canvasStatus)
	}
}
