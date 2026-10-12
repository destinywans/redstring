package v3canvas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type investigationEvidenceRunner struct {
	run func(context.Context, inference.Request, func(inference.Event) error) (string, error)
}

func (*investigationEvidenceRunner) Models() []inference.Model {
	return []inference.Model{{ID: "evidence-model", Provider: "controlled", Name: "Controlled", Tools: true}}
}
func (r *investigationEvidenceRunner) Run(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
	return r.run(ctx, req, emit)
}

// Registered routes, real transactions and a fresh disposable schema. The
// model/tool are controlled in-process; this is not real upstream acceptance.
func TestInvestigationEvidenceRegisteredRoute(t *testing.T) {
	for _, scenario := range []string{"success", "source revoked before commit", "logout during read", "audit rollback", "cross app evidence", "knowledge unpublished"} {
		t.Run(scenario, func(t *testing.T) {
			dsn := os.Getenv("TEST_DATABASE_URL")
			if dsn == "" {
				t.Skip("TEST_DATABASE_URL required; isolated disposable schema only")
			}
			ctx := t.Context()
			admin, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			schema := "inv_evidence_" + store.ID()
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
			db, err = pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			st := &store.Store{DB: db}
			if err = st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			statements := []string{
				`INSERT INTO users(id,email,name,role,password_hash) VALUES('actor','actor@example.invalid','Actor','viewer','unused')`,
				`DELETE FROM rbac_user_roles WHERE user_id='actor'`,
				`INSERT INTO rbac_roles(id,name) VALUES('evidence-editor','Evidence editor')`,
				`INSERT INTO rbac_user_roles(user_id,role_id) VALUES('actor','evidence-editor')`,
				`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
    ('evidence-editor','environment','view','environment','test'),
    ('evidence-editor','canvas','view','application','app'),
    ('evidence-editor','canvas','change','application','app'),
    ('evidence-editor','services','view','application','app'),
    ('evidence-editor','knowledge','view','application','app')`,
				`INSERT INTO applications(id,key,name) VALUES('app','service-key','Service')`,
				`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-test','app','test','Test')`,
				`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','test')`,
				`INSERT INTO investigations(id,application_id,environment_id,title,status) VALUES('inv','app','app-test','Investigation','open')`,
				`INSERT INTO branches(id,investigation_id,title,revision) VALUES('branch','inv','Branch',0),('sibling','inv','Sibling',0)`,
				`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES('branch',0,'[]','{}'),('sibling',0,'[{"id":"secret","role":"user","content":"sibling-only-secret"}]','{}')`,
			}
			for _, statement := range statements {
				if _, err = db.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'actor',clock_timestamp()+interval '1 hour')`, store.Hash("exact-cookie")); err != nil {
				t.Fatal(err)
			}
			if scenario == "audit rollback" {
				if _, err = db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT fail_evidence CHECK(action<>'v3.investigation_canvas.evidence') NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			evidence := inference.Evidence{Kind: "service_profile", Title: "Stored service profile", Content: `{"health":"unavailable"}`, Source: "service_catalog", TargetID: base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))}
			toolName := "read_service_profile"
			if scenario == "knowledge unpublished" {
				toolName = "search_knowledge"
				if err = knowledge.New(db, nil).Migrate(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,source,body,content_hash,created_by,version,lifecycle,metadata) VALUES('doc','app','Published','manual','source body','hash','actor',1,'published','{"environment_ids":["test"]}')`); err != nil {
					t.Fatal(err)
				}
				evidence = inference.Evidence{Kind: "knowledge", Title: "Published knowledge", Content: `{"application_id":"app","environment_id":"test","passages":[{"document_id":"doc","version":1}]}`, Source: "knowledge://published-documents", TargetID: "app"}
			}
			reads := 0
			runner := &investigationEvidenceRunner{}
			svc := NewWithRuntime(st, runner)
			svc.InvestigationToolsForTurn = func(_ context.Context, owner, app, env, model string) ([]inference.EvidenceTool, error) {
				if owner != "actor" || app != "app" || env != "test" || model != "evidence-model" {
					t.Errorf("unbound factory args: %s/%s/%s/%s", owner, app, env, model)
				}
				return []inference.EvidenceTool{{Name: toolName, Description: "Controlled fixed-scope read", Read: func(context.Context, string) (inference.Evidence, error) {
					reads++
					if scenario == "logout during read" {
						if err := st.Logout(ctx, "exact-cookie"); err != nil {
							t.Error(err)
						}
					}
					if scenario == "cross app evidence" {
						bad := evidence
						bad.TargetID = "other-app"
						return bad, nil
					}
					return evidence, nil
				}}}, nil
			}
			runner.run = func(runCtx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
				if len(req.Tools) != 1 || req.MaxToolCalls != 8 || req.MaxCalls != 9 || req.BranchID != "branch" || req.EnvironmentID != "test" {
					return "", errors.New("wrong scoped tools or budget")
				}
				for _, m := range req.Messages {
					if strings.Contains(m.Content, "sibling-only-secret") {
						return "", errors.New("sibling context leaked")
					}
				}
				v := inference.ToolInvocation{Name: toolName, StepID: "tool-1", Status: "started"}
				if err := emit(inference.Event{Type: "tool_invocation", StepID: v.StepID, Invocation: &v}); err != nil {
					return "", err
				}
				ev, readErr := req.Tools[0].Read(runCtx, "private-query-must-not-be-audited")
				v.Status = "succeeded"
				v.TargetID = ev.TargetID
				if readErr != nil {
					v.Status = "failed"
					v.TargetID = ""
				}
				if err := emit(inference.Event{Type: "tool_invocation", StepID: v.StepID, Invocation: &v}); err != nil {
					return "", err
				}
				if readErr != nil {
					return "", readErr
				}
				if err := emit(inference.Event{Type: "evidence", StepID: v.StepID, Evidence: &ev}); err != nil {
					return "", err
				}
				if scenario == "source revoked before commit" {
					if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='evidence-editor' AND module='services'`); err != nil {
						return "", err
					}
				}
				if scenario == "knowledge unpublished" {
					if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET lifecycle='draft' WHERE id='doc'`); err != nil {
						return "", err
					}
				}
				return "Answer grounded in the captured evidence", nil
			}
			policy := &rbac.V3Policy{Store: st}
			api := httpapi.New(st, nil, true)
			svc.Register(api)
			if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}, policy, st.Session,
				func(ctx context.Context, user core.User, module, action, env string) error {
					return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
				}); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/investigations/inv/nodes/branch/messages", strings.NewReader(`{"text":"Investigate this service","model":"evidence-model"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-AIOps-Environment", "test")
			request.AddCookie(&http.Cookie{Name: "aiops_session", Value: "exact-cookie"})
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			var evCount, revision, completed, failed, receipts int
			if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM evidence),(SELECT revision FROM branches WHERE id='branch'),
   (SELECT count(*) FROM v3_investigation_node_replies WHERE state='completed'),(SELECT count(*) FROM v3_investigation_node_replies WHERE state='failed'),
   (SELECT count(*) FROM management_audit WHERE action='v3.investigation_canvas.evidence')`).Scan(&evCount, &revision, &completed, &failed, &receipts); err != nil {
				t.Fatal(err)
			}
			if reads != 1 {
				t.Fatalf("reads=%d status=%d body=%s", reads, response.Code, response.Body.String())
			}
			if scenario == "success" {
				if response.Code != 200 || evCount != 1 || revision != 1 || completed != 1 || failed != 0 || receipts != 1 {
					t.Fatalf("success mismatch: status=%d evidence/revision/completed/failed/receipts=%d/%d/%d/%d/%d body=%s", response.Code, evCount, revision, completed, failed, receipts, response.Body.String())
				}
				var raw []byte
				if err = db.QueryRow(ctx, `SELECT messages FROM context_revisions WHERE branch_id='branch' AND revision=1`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var messages []core.Message
				if err = json.Unmarshal(raw, &messages); err != nil || len(messages) != 2 || len(messages[1].EvidenceIDs) != 1 {
					t.Fatalf("context receipt linkage missing: %s %v", raw, err)
				}
				var orphan int
				if err = db.QueryRow(ctx, `SELECT count(*) FROM evidence WHERE id=$1 AND branch_id='branch' AND run_id IS NULL`, messages[1].EvidenceIDs[0]).Scan(&orphan); err != nil || orphan != 1 {
					t.Fatalf("evidence wrongly assigned: %d %v", orphan, err)
				}
			} else if response.Code == 200 || evCount != 0 || revision != 0 || completed != 0 || failed != 1 || receipts != 0 {
				t.Fatalf("rejection leaked success: status=%d evidence/revision/completed/failed/receipts=%d/%d/%d/%d/%d body=%s", response.Code, evCount, revision, completed, failed, receipts, response.Body.String())
			}
			var leaked int
			if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE changes::text LIKE '%private-query-must-not-be-audited%' OR changes::text LIKE '%health%'`).Scan(&leaked); err != nil || leaked != 0 {
				t.Fatalf("query/content audit leak: %d %v", leaked, err)
			}
		})
	}
}
