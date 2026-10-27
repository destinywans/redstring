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

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PA-267: a read that fails with a PA-50 code the model can act on (the source
// isn't set up, the input is wrong, the source is down) leaves no evidence and
// the reply goes on, as in chat. Refused reads and uncoded failures still fail
// the reply closed. A failed attempt records why it failed, and the response
// says so in the request language. Registered route, real transactions and a
// disposable schema; the model and tools are controlled in-process.
func TestInvestigationReplyToolErrorsAndFailureCodes(t *testing.T) {
	type outcome struct {
		status                      int
		evidence, completed, failed int
		failureCode, bodyHas        string
	}
	for _, tc := range []struct {
		name     string
		changes  error // what read_service_changes returns; nil skips the tool
		profile  bool  // read the profile after the changes tool
		modelErr error
		answer   string
		want     outcome
	}{
		{name: "not configured read continues", changes: inference.NewToolError(inference.ToolNotConfigured, "no change source is registered for service service-key"), profile: true,
			want: outcome{status: 200, evidence: 1, completed: 1}},
		{name: "source unavailable alone still answers", changes: inference.NewToolError(inference.ToolSourceUnavailable, "the change source is temporarily unavailable"),
			want: outcome{status: 200, completed: 1}},
		{name: "invalid input continues", changes: inference.NewToolError(inference.ToolInvalidInput, "use a window of at most 7 days"), profile: true,
			want: outcome{status: 200, evidence: 1, completed: 1}},
		{name: "not permitted read stops", changes: inference.NewToolError(inference.ToolNotPermitted, "you may view service service-key but not its changes"), profile: true,
			want: outcome{status: 503, failed: 1, failureCode: "evidence_unavailable", bodyHas: "取证失败"}},
		{name: "unknown service read stops", changes: inference.ErrUnknownService, profile: true,
			want: outcome{status: 503, failed: 1, failureCode: "evidence_unavailable"}},
		{name: "uncoded read failure stops", changes: errors.New("connector exploded"), profile: true,
			want: outcome{status: 503, failed: 1, failureCode: "evidence_unavailable"}},
		{name: "model failure", modelErr: errors.New("provider returned 502"),
			want: outcome{status: 503, failed: 1, failureCode: "model_unavailable", bodyHas: "模型回复失败"}},
		{name: "deadline", modelErr: context.DeadlineExceeded,
			want: outcome{status: 503, failed: 1, failureCode: "reply_timeout", bodyHas: "时间上限"}},
		{name: "empty answer", answer: " ",
			want: outcome{status: 503, failed: 1, failureCode: "model_reply_invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv("TEST_DATABASE_URL")
			if dsn == "" {
				t.Skip("TEST_DATABASE_URL required; isolated disposable schema only")
			}
			ctx := t.Context()
			admin, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			schema := "inv_tool_errors_" + store.ID()
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
    ('evidence-editor','services','view','application','app')`,
				`INSERT INTO applications(id,key,name) VALUES('app','service-key','Service')`,
				`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-test','app','test','Test')`,
				`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','test')`,
				`INSERT INTO investigations(id,application_id,environment_id,title,status) VALUES('inv','app','app-test','Investigation','open')`,
				`INSERT INTO branches(id,investigation_id,title,revision) VALUES('branch','inv','Branch',0)`,
				`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES('branch',0,'[]','{}')`,
			} {
				if _, err = db.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'actor',clock_timestamp()+interval '1 hour')`, store.Hash("exact-cookie")); err != nil {
				t.Fatal(err)
			}
			profile := inference.Evidence{Kind: "service_profile", Title: "Stored service profile", Content: `{"health":"unavailable"}`, Source: "service_catalog", TargetID: base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))}
			runner := &investigationEvidenceRunner{}
			svc := NewWithRuntime(st, runner)
			svc.InvestigationToolsForTurn = func(context.Context, string, string, string, string) ([]inference.EvidenceTool, error) {
				return []inference.EvidenceTool{
					{Name: changesToolName, Description: "Controlled changes read", Read: func(context.Context, string) (inference.Evidence, error) {
						return inference.Evidence{}, tc.changes
					}},
					{Name: "read_service_profile", Description: "Controlled profile read", Read: func(context.Context, string) (inference.Evidence, error) {
						return profile, nil
					}},
				}, nil
			}
			// The runner emits what inference.Runtime emits: started, Read,
			// failed (with the ToolError code) or succeeded plus evidence, and
			// returns the collector's error the way the runtime does.
			runner.run = func(runCtx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
				if tc.modelErr != nil {
					return "", tc.modelErr
				}
				call := func(i int, step string) error {
					v := inference.ToolInvocation{Name: req.Tools[i].Name, StepID: step, Status: "started"}
					if err := emit(inference.Event{Type: "tool_invocation", StepID: step, Invocation: &v}); err != nil {
						return err
					}
					ev, readErr := req.Tools[i].Read(runCtx, "query")
					v.Status, v.TargetID = "succeeded", ev.TargetID
					if readErr != nil {
						v.Status, v.TargetID, v.ErrorCode = "failed", "", inference.ToolErrorCode(readErr)
					}
					if err := emit(inference.Event{Type: "tool_invocation", StepID: step, Invocation: &v}); err != nil {
						return err
					}
					if readErr != nil {
						return nil // handed back to the model, as the runtime does
					}
					return emit(inference.Event{Type: "evidence", StepID: step, Evidence: &ev})
				}
				if tc.changes != nil {
					if err := call(0, "tool-1"); err != nil {
						return "", err
					}
				}
				if tc.profile {
					if err := call(1, "tool-2"); err != nil {
						return "", err
					}
				}
				if tc.answer != "" {
					return tc.answer, nil
				}
				return "Answer from what could be read; the change source is not set up", nil
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
			request := httptest.NewRequest(http.MethodPost, "/api/v1/investigations/inv/nodes/branch/messages", strings.NewReader(`{"text":"Why did it stop?","model":"evidence-model"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-AIOps-Environment", "test")
			request.Header.Set("Accept-Language", "zh-CN")
			request.AddCookie(&http.Cookie{Name: "aiops_session", Value: "exact-cookie"})
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)

			var evidence, completed, failed int
			var failureCode string
			if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM evidence),
   (SELECT count(*) FROM v3_investigation_node_replies WHERE state='completed'),(SELECT count(*) FROM v3_investigation_node_replies WHERE state='failed'),
   COALESCE((SELECT failure_code FROM v3_investigation_node_replies WHERE state='failed'),'')`).Scan(&evidence, &completed, &failed, &failureCode); err != nil {
				t.Fatal(err)
			}
			body := response.Body.String()
			if response.Code != tc.want.status || evidence != tc.want.evidence || completed != tc.want.completed || failed != tc.want.failed || failureCode != tc.want.failureCode {
				t.Fatalf("status=%d evidence/completed/failed=%d/%d/%d failure_code=%q, want %+v; body=%s", response.Code, evidence, completed, failed, failureCode, tc.want, body)
			}
			if tc.want.bodyHas != "" && !strings.Contains(body, tc.want.bodyHas) {
				t.Fatalf("response doesn't say why in the request language: %s", body)
			}
			if tc.want.status != 200 {
				return
			}
			// The failed read stays in the audit; only the read that produced
			// evidence is linked to the answer.
			var failedAudit int
			if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.investigation_canvas.tool' AND target=$1 AND outcome='failed'`, changesToolName).Scan(&failedAudit); err != nil || failedAudit != 1 {
				t.Fatalf("failed read audit=%d %v", failedAudit, err)
			}
			var raw []byte
			if err = db.QueryRow(ctx, `SELECT messages FROM context_revisions WHERE branch_id='branch' AND revision=1`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var messages []core.Message
			if err = json.Unmarshal(raw, &messages); err != nil || len(messages) != 2 || len(messages[1].EvidenceIDs) != tc.want.evidence {
				t.Fatalf("answer evidence linkage: %s %v", raw, err)
			}
		})
	}
}

// PA-267, offline: which failed reads let the reply go on.
func TestInvestigationEvidenceFailedReadCodes(t *testing.T) {
	for code, continues := range map[string]bool{
		inference.ToolNotConfigured: true, inference.ToolInvalidInput: true, inference.ToolSourceUnavailable: true,
		inference.ToolNotPermitted: false, inference.ToolUnknownService: false, inference.ToolEvidenceLimit: false, "": false, "made_up": false,
	} {
		c, e := evidenceCollectorFixture()
		c.names["read_service_metrics"] = true
		if err := emitInvestigationInvocation(c, "started", ""); err != nil {
			t.Fatal(err)
		}
		failed := inference.ToolInvocation{Name: "read_service_profile", StepID: "step-1", Status: "failed", ErrorCode: code}
		err := c.handle(inference.Event{Type: "tool_invocation", StepID: "step-1", Invocation: &failed})
		if (err == nil) != continues {
			t.Fatalf("code %q: continue=%v err=%v", code, continues, err)
		}
		if !continues {
			if _, err = c.finish(); err == nil || c.failure() == nil {
				t.Fatalf("code %q: a stopped collection finished", code)
			}
			continue
		}
		// A later read still needs its evidence; the failed one needs none.
		start := inference.ToolInvocation{Name: "read_service_profile", StepID: "step-2", Status: "started"}
		done := inference.ToolInvocation{Name: "read_service_profile", StepID: "step-2", Status: "succeeded", TargetID: e.TargetID}
		for _, v := range []inference.ToolInvocation{start, done} {
			if err = c.handle(inference.Event{Type: "tool_invocation", StepID: "step-2", Invocation: &v}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = c.finish(); err == nil {
			t.Fatalf("code %q: a successful read without evidence finished", code)
		}
		if err = c.handle(inference.Event{Type: "evidence", StepID: "step-2", Evidence: &e}); err != nil {
			t.Fatal(err)
		}
		if got, err := c.finish(); err != nil || len(got) != 1 || got[0].Step != "step-2" {
			t.Fatalf("code %q: receipts %+v %v", code, got, err)
		}
		// Evidence for the failed read is never accepted.
		if c.handle(inference.Event{Type: "evidence", StepID: "step-1", Evidence: &e}) == nil {
			t.Fatalf("code %q: evidence for a failed read", code)
		}
	}
	// A code on anything but a failed read is a forged event.
	c, _ := evidenceCollectorFixture()
	if err := emitInvestigationInvocation(c, "started", ""); err != nil {
		t.Fatal(err)
	}
	forged := inference.ToolInvocation{Name: "read_service_profile", StepID: "step-1", Status: "succeeded", ErrorCode: inference.ToolNotConfigured}
	if c.handle(inference.Event{Type: "tool_invocation", StepID: "step-1", Invocation: &forged}) == nil {
		t.Fatal("code on a succeeded read accepted")
	}
}

func TestInvestigationReplyFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		run, collector error
		want           string
	}{
		{errors.New("provider returned 502"), nil, "model_unavailable"},
		{errInvestigationEvidence, errInvestigationEvidence, "evidence_unavailable"},
		{errors.New("session_revoked"), errors.New("session_revoked"), "evidence_unavailable"},
		{errInvestigationEvidence, nil, "evidence_unavailable"},
		{context.Canceled, context.Canceled, "reply_interrupted"},
		{errors.Join(errors.New("stream"), context.DeadlineExceeded), nil, "reply_timeout"},
		{errNodeReplyInvalid, nil, "model_reply_invalid"},
	} {
		if got := investigationReplyFailure(tc.run, tc.collector); got != tc.want {
			t.Fatalf("%v / %v: %q, want %q", tc.run, tc.collector, got, tc.want)
		}
		if zh, en := investigationReplyFailureText(tc.want, false), investigationReplyFailureText(tc.want, true); zh == en || zh == "这条回复没有完成。" || strings.ContainsAny(en, "，。") {
			t.Fatalf("%s: text %q / %q", tc.want, zh, en)
		}
	}
}
