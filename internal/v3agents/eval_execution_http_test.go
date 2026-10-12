package v3agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// This is registered HTTP + isolated PG, with a controlled in-process model.
// It never calls an external provider. Catalog, versions, prompts, configs,
// human input, experiments and labels are produced through their normal APIs.
func TestEvalExecutionRegisteredWorkflow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random disposable schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_eval_execution_" + store.ID()
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
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev')`, `INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`, `INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(db, policy)
	runner := &evalControlledRunner{}
	svc.SetExecutionRuntime(runner, nil)
	defer svc.SetExecutionRuntime(nil, nil)
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	must := func(method, path string, body any, status int) map[string]any {
		t.Helper()
		w := request(method, path, body)
		if w.Code != status {
			t.Fatalf("%s %s status=%d body=%s", method, path, w.Code, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	must("POST", "/agents", map[string]any{"id": "agent", "application_id": "app", "name": "Eval Agent", "summary": "Controlled testing"}, 201)
	for _, v := range []string{"v1", "v2", "v3"} {
		must("POST", "/agents/agent/versions", map[string]any{"version": v, "artifact_ref": "git:" + v}, 201)
	}
	p := must("POST", "/prompts", map[string]any{"agent_id": "agent", "name": "Evaluation prompt", "text": "baseline prompt"}, 201)
	promptID := p["id"].(string)
	must("POST", "/prompts/"+promptID+"/versions", map[string]any{"text": "candidate prompt"}, 201)
	for i, v := range []string{"v1", "v2", "v3"} {
		config := ExecutionConfig{ModelID: "eval-model", PromptID: promptID, PromptVersion: 1 + i%2, ToolNames: []string{}, MaxModelCalls: 1, MaxToolCalls: 0, MaxInputBytes: 8192, MaxOutputBytes: 1024, MaxOutputTokens: 64, TimeoutSeconds: 5}
		if v == "v3" {
			config.ToolNames = []string{"read_service_profile"}
			config.MaxToolCalls = 1
		}
		must("PUT", "/agents/agent/versions/"+v+"/execution", config, 200)
	}
	input := evalInputFixture()
	caseResult := must("POST", "/eval-cases", input, 201)
	caseID := caseResult["id"].(string)
	detail := must("GET", "/eval-cases/"+caseID, nil, 200)
	if detail["input_available"] != true || detail["complete_v3_dto"] != false {
		t.Fatalf("input wrongly projected %#v", detail)
	}
	var inputs int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_prompt_trial_case_inputs WHERE case_id=$1`, caseID).Scan(&inputs); err != nil || inputs != 1 {
		t.Fatalf("trial input missing %d %v", inputs, err)
	}
	for _, scenario := range []string{"success", "literal failure", "provider failure", "cancelled", "logout", "audit failure", "late deadline", "tool event", "expired", "tool config"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := "v2"
			if scenario == "tool config" {
				candidate = "v3"
			}
			experiment := must("POST", "/evals", map[string]any{"agent_id": "agent", "title": scenario, "baseline_version": "v1", "candidate_version": candidate}, 201)
			id := experiment["id"].(string)
			before := runner.calls
			side := 0
			runner.run = func(callCtx context.Context, in inference.Request, emit func(inference.Event) error) (string, error) {
				side++
				if len(in.Tools) != 0 || in.MaxCalls != 1 || in.MaxOutputTokens != 64 || in.EnvironmentID != "dev" || in.ModelID != "eval-model" || in.Messages[1].Content != input.Input {
					t.Fatalf("unbound request %#v", in)
				}
				if side == 1 && in.Messages[0].Content != "baseline prompt" || side == 2 && in.Messages[0].Content != "candidate prompt" {
					t.Fatalf("wrong immutable prompt %#v", in.Messages)
				}
				if scenario == "cancelled" && side == 1 {
					must("POST", "/evals/"+id+"/cancel", nil, 200)
				}
				if scenario == "logout" && side == 1 {
					if err := st.Logout(ctx, token); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "expired" && side == 1 {
					if _, err := db.Exec(ctx, `UPDATE v3_agent_eval_executions SET deadline=clock_timestamp()-interval '1 second' WHERE experiment_id=$1`, id); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "tool event" {
					_ = emit(inference.Event{Type: "tool_invocation", Invocation: &inference.ToolInvocation{Name: "execute_shell"}})
				}
				if scenario == "provider failure" && side == 2 {
					return "", errors.New("controlled provider failure")
				}
				if side == 1 || scenario == "literal failure" {
					return "healthy", nil
				}
				return "\nhealthy restart\n", nil
			}
			if scenario == "audit failure" {
				if _, err := db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT eval_audit_fail CHECK(action<>'v3.agent.eval.execution.completed') NOT VALID`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "late deadline" {
				// Expire only after the completion transaction has passed its
				// initial deadline check and inserted its snapshot/metrics. This
				// deterministically models a wait crossing the deadline; no sleep
				// or manipulation before the early check can mask the regression.
				if _, err := db.Exec(ctx, `CREATE FUNCTION eval_expire_on_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.action='v3.agent.eval.execution.completed' THEN
 UPDATE v3_agent_eval_executions SET deadline=clock_timestamp()-interval '1 second' WHERE experiment_id=NEW.target;
 END IF;
 RETURN NEW;
 END $$;
 CREATE TRIGGER eval_expire_on_audit AFTER INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION eval_expire_on_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			body := map[string]any{"case_id": caseID, "request_key": "explicit-key-" + strings.ReplaceAll(scenario, " ", "-")}
			w := request("POST", "/evals/"+id+"/execute", body)
			if scenario == "logout" {
				_, token, err = st.Login(ctx, "admin@example.invalid", "isolated-password")
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "audit failure" {
				if _, err := db.Exec(ctx, `ALTER TABLE management_audit DROP CONSTRAINT eval_audit_fail`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "late deadline" {
				if _, err := db.Exec(ctx, `DROP TRIGGER eval_expire_on_audit ON management_audit; DROP FUNCTION eval_expire_on_audit()`); err != nil {
					t.Fatal(err)
				}
				if side != 2 {
					t.Fatalf("late deadline did not reach both successful model calls: %d", side)
				}
				var successfulAudits int
				if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.agent.eval.execution.completed' AND target=$1`, id).Scan(&successfulAudits); err != nil || successfulAudits != 0 {
					t.Fatalf("late success audit survived rollback: %d %v", successfulAudits, err)
				}
			}
			var status string
			var snapshots int
			if err = db.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM v3_agent_eval_case_snapshots WHERE producer_ref=$2) FROM v3_agent_eval_experiments WHERE id=$1`, id, "eval-execution:"+id).Scan(&status, &snapshots); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "success", "literal failure":
				want := "passed"
				if scenario == "literal failure" {
					want = "failed"
				}
				if w.Code != 200 || status != want || snapshots != 1 || side != 2 {
					t.Fatalf("wrong result %d %s %d %d %s", w.Code, status, snapshots, side, w.Body.String())
				}
				replay := request("POST", "/evals/"+id+"/execute", body)
				if replay.Code != 200 || runner.calls != before+2 {
					t.Fatal("idempotent replay called provider")
				}
			case "tool config":
				if w.Code != 422 || status != "pending" || snapshots != 0 || side != 0 {
					t.Fatalf("tool config admitted %d %s %d", w.Code, status, side)
				}
			default:
				if w.Code == 200 || status != "invalid" || snapshots != 0 {
					t.Fatalf("failure became success %d %s %d %s", w.Code, status, snapshots, w.Body.String())
				}
			}
			if scenario != "tool config" {
				saved := must("GET", "/evals/"+id+"/execution", nil, 200)
				if scenario != "success" && scenario != "literal failure" && saved["results"] != nil {
					t.Fatal("failed execution retained report")
				}
			}
		})
	}
	var labels int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_eval_labels WHERE case_id=$1`, caseID).Scan(&labels); err != nil || labels != 0 {
		t.Fatal("automatic execution wrote human labels")
	}
	must("POST", "/eval-cases/"+caseID+"/labels", map[string]any{"next": "partial", "note": "Human review differs from literal checks", "expected_revision": 0}, 200)
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_eval_labels WHERE case_id=$1 AND snapshot_id IS NOT NULL`, caseID).Scan(&labels); err != nil || labels != 1 {
		t.Fatal("human label snapshot missing")
	}
	// PA-93: every agent audit row names an environment (the agent's when the
	// caller gave none, as eval.call.started does), so GET /audit lists it.
	var calls, withoutEnv int
	if err = db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE action='v3.agent.eval.call.started'),
 count(*) FILTER (WHERE NULLIF(changes->>'environment_id','') IS NULL)
 FROM management_audit WHERE action LIKE 'v3.agent.%' AND application_id<>''`).Scan(&calls, &withoutEnv); err != nil || calls == 0 || withoutEnv != 0 {
		t.Fatalf("agent audits: eval calls=%d without environment=%d err=%v", calls, withoutEnv, err)
	}
}
