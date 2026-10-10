package v3canvas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// suggestionWorkloadJSON is read_service_workload evidence: current revision
// 8 and the given other revisions, with an optional blocked reason.
func suggestionWorkloadJSON(service string, others []string, blocked string) string {
	revisions := []map[string]any{{"revision": "8", "current": true, "created_at": "2026-10-10T10:42:00Z", "change_cause": "lab drill bad-release"}}
	for _, r := range others {
		revisions = append(revisions, map[string]any{"revision": r, "current": false, "created_at": "2026-10-10T09:15:00Z", "change_cause": "release 1.4.2"})
	}
	var reason any
	if blocked != "" {
		reason = blocked
	}
	raw, _ := json.Marshal(map[string]any{"service_id": service, "environment_id": "test", "namespace": "aiops-lab",
		"deployment": "checkout-api", "current_revision": "8", "revisions": revisions, "rollback": map[string]any{"blocked_reason": reason}})
	return string(raw)
}

// PA-72 on registered routes with real transactions: the suggestion step only
// runs when the reply read a workload with another revision that may be rolled
// back to, and keeps only what the evidence supports.
func TestInvestigationRollbackSuggestion(t *testing.T) {
	service := base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))
	valid := `{"suggest":true,"target_revision":"7","reasons":[{"text":"Errors rose when revision 8 went out","cites":[1,9,1]},{"text":"` +
		strings.Repeat("x", 201) + `","cites":[2]},{"text":"Revision 7 only lacks LAB_FAULTS","cites":[2]}]}`
	for _, c := range []struct {
		name        string
		workload    string // "" reads no workload
		output      string
		modelFails  bool
		wantCall    bool
		wantOutcome string
		wantCard    bool
	}{
		{name: "suggested", workload: suggestionWorkloadJSON(service, []string{"7"}, ""), output: "```json\n" + valid + "\n```", wantCall: true, wantOutcome: "suggested", wantCard: true},
		{name: "no workload read", output: valid},
		{name: "only the current revision", workload: suggestionWorkloadJSON(service, nil, ""), output: valid},
		{name: "rollback not allowed", workload: suggestionWorkloadJSON(service, []string{"7"}, "rollback_not_allowed"), output: valid},
		{name: "target out of range", workload: suggestionWorkloadJSON(service, []string{"7"}, ""), wantCall: true, wantOutcome: "rejected",
			output: `{"suggest":true,"target_revision":"3","reasons":[{"text":"Roll back further","cites":[1]}]}`},
		{name: "declined", workload: suggestionWorkloadJSON(service, []string{"7"}, "write_not_approved"), wantCall: true, wantOutcome: "declined",
			output: `{"suggest":false}`},
		{name: "model fails", workload: suggestionWorkloadJSON(service, []string{"7"}, ""), modelFails: true, wantCall: true, wantOutcome: "unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, st := suggestionDB(t)
			ctx := t.Context()
			metrics := inference.Evidence{Kind: "metrics", Title: "Prometheus samples: service-key / error_rate", Content: `{"status":"observed"}`,
				Source: "prometheus", TargetID: service}
			workload := inference.Evidence{Kind: "workload", Title: "Kubernetes workload: service-key", Content: c.workload,
				Source: "kubernetes", TargetID: service}
			runner := &investigationEvidenceRunner{}
			svc := NewWithRuntime(st, runner)
			svc.InvestigationToolsForTurn = func(context.Context, string, string, string, string) ([]inference.EvidenceTool, error) {
				return []inference.EvidenceTool{
					{Name: "read_service_metrics", Description: "metrics", Read: func(context.Context, string) (inference.Evidence, error) { return metrics, nil }},
					{Name: workloadToolName, Description: "workload", Read: func(context.Context, string) (inference.Evidence, error) { return workload, nil }},
				}, nil
			}
			var mu sync.Mutex
			var suggestionCalls []inference.Request
			runner.run = func(runCtx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
				if strings.HasSuffix(req.AttemptID, "-suggestion") {
					mu.Lock()
					suggestionCalls = append(suggestionCalls, req)
					mu.Unlock()
					if len(req.Tools) != 0 || req.MaxCalls != 1 {
						return "", errors.New("the suggestion step must not get tools")
					}
					if c.modelFails {
						return "", errors.New("model unavailable")
					}
					return c.output, nil
				}
				if req.MaxToolCalls != 6 || req.MaxCalls != 7 || len(req.Tools) != 2 {
					return "", fmt.Errorf("budget %d/%d tools %d", req.MaxToolCalls, req.MaxCalls, len(req.Tools))
				}
				reads := []inference.EvidenceTool{req.Tools[0]}
				if c.workload != "" {
					reads = append(reads, req.Tools[1])
				}
				for i, tool := range reads {
					v := inference.ToolInvocation{Name: tool.Name, StepID: fmt.Sprintf("tool-%d", i+1), Status: "started"}
					if err := emit(inference.Event{Type: "tool_invocation", StepID: v.StepID, Invocation: &v}); err != nil {
						return "", err
					}
					ev, err := tool.Read(runCtx, `{"service":"service-key"}`)
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
				}
				return "Revision 8 caused the 5xx; revision 7 was healthy.", nil
			}
			api := suggestionAPI(t, st, svc)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/investigations/inv/nodes/branch/messages", strings.NewReader(`{"text":"Is it the release?","model":"evidence-model"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-AIOps-Environment", "test")
			request.Header.Set("Accept-Language", "en")
			request.AddCookie(&http.Cookie{Name: "aiops_session", Value: "exact-cookie"})
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != 200 {
				t.Fatalf("reply: %d %s", response.Code, response.Body.String())
			}
			var reply struct {
				Summary    string              `json:"summary"`
				Suggestion *rollbackSuggestion `json:"rollback_suggestion"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if (len(suggestionCalls) == 1) != c.wantCall || len(suggestionCalls) > 1 || (reply.Suggestion != nil) != c.wantCard {
				t.Fatalf("calls=%d suggestion=%+v body=%s", len(suggestionCalls), reply.Suggestion, response.Body.String())
			}
			if c.wantCall {
				prompt := suggestionCalls[0].Messages[1].Content
				if !strings.Contains(prompt, "[1] Prometheus samples") || !strings.Contains(prompt, "[2] Kubernetes workload") ||
					!strings.Contains(prompt, "Revision 8 caused the 5xx") || !strings.Contains(prompt, "in English") {
					t.Fatalf("suggestion prompt: %s", prompt)
				}
			}
			var outcome, reason string
			err := db.QueryRow(ctx, `SELECT outcome,changes->>'reason' FROM management_audit WHERE action='v3.ai.rollback_suggested'`).Scan(&outcome, &reason)
			if c.wantOutcome == "" {
				if !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("audit without a suggestion step: %s %v", outcome, err)
				}
			} else if err != nil || outcome != c.wantOutcome || (c.name == "target out of range" && reason != "target_not_candidate") {
				t.Fatalf("audit: %s %s %v", outcome, reason, err)
			}
			if !c.wantCard {
				return
			}
			s := reply.Suggestion
			if s.ServiceID != service || s.Workload != "aiops-lab/checkout-api" || s.FromRevision != "8" || s.TargetRevision != "7" ||
				s.TargetChangeCause == nil || *s.TargetChangeCause != "release 1.4.2" || len(s.Reasons) != 2 ||
				fmt.Sprint(s.Reasons[0].Cites) != "[1]" || fmt.Sprint(s.Reasons[1].Cites) != "[2]" || len(s.EvidenceIDs) != 2 ||
				!strings.HasPrefix(s.ReplyID, "INR-") {
				t.Fatalf("suggestion: %+v", s)
			}
			// Persisted with the reply, projected on its node, immutable.
			canvas := httptest.NewRequest(http.MethodGet, "/api/v1/investigations/inv/canvas", nil)
			canvas.Header.Set("X-AIOps-Environment", "test")
			canvas.AddCookie(&http.Cookie{Name: "aiops_session", Value: "exact-cookie"})
			graphResponse := httptest.NewRecorder()
			api.ServeHTTP(graphResponse, canvas)
			var graph struct {
				Nodes []struct {
					ID         string              `json:"id"`
					Suggestion *rollbackSuggestion `json:"rollback_suggestion"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(graphResponse.Body.Bytes(), &graph); err != nil || graphResponse.Code != 200 {
				t.Fatalf("canvas: %d %s", graphResponse.Code, graphResponse.Body.String())
			}
			for _, node := range graph.Nodes {
				if (node.ID == "branch") != (node.Suggestion != nil) ||
					(node.Suggestion != nil && (node.Suggestion.TargetRevision != "7" || node.Suggestion.ReplyID != s.ReplyID)) {
					t.Fatalf("projected suggestion on %s: %+v", node.ID, node.Suggestion)
				}
			}
			if _, err := db.Exec(ctx, `UPDATE v3_investigation_node_replies SET rollback_suggestion='{}'`); err == nil {
				t.Fatal("a stored suggestion changed")
			}
		})
	}
}

func suggestionDB(t *testing.T) (*pgxpool.Pool, *store.Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated disposable schema only")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "inv_suggestion_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,name,role,password_hash) VALUES('actor','actor@example.invalid','Actor','viewer','unused')`,
		`DELETE FROM rbac_user_roles WHERE user_id='actor'`,
		`INSERT INTO rbac_roles(id,name) VALUES('evidence-editor','Evidence editor')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES('actor','evidence-editor')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
    ('evidence-editor','environment','view','environment','test'),
    ('evidence-editor','canvas','view','application','app'),
    ('evidence-editor','canvas','change','application','app'),
    ('evidence-editor','services','view','application','app'),
    ('evidence-editor','resources','view','application','app')`,
		`INSERT INTO applications(id,key,name) VALUES('app','service-key','Service')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-test','app','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','test')`,
		`INSERT INTO investigations(id,application_id,environment_id,title,status) VALUES('inv','app','app-test','Investigation','open')`,
		`INSERT INTO branches(id,investigation_id,title,revision) VALUES('branch','inv','Branch',0),('sibling','inv','Sibling',0)`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES('branch',0,'[]','{}'),('sibling',0,'[]','{}')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'actor',clock_timestamp()+interval '1 hour')`, store.Hash("exact-cookie")); err != nil {
		t.Fatal(err)
	}
	return db, st
}

func suggestionAPI(t *testing.T, st *store.Store, svc *Service) *httpapi.Server {
	t.Helper()
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}, policy, st.Session,
		func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	return api
}
