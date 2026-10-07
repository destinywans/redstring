package v3agents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func executionTestConfig() ExecutionConfig {
	return ExecutionConfig{ModelID: "test", PromptID: "prompt", PromptVersion: 1, ToolNames: []string{}, MaxModelCalls: 2, MaxToolCalls: 0, MaxInputBytes: 100, MaxOutputBytes: 1000, MaxOutputTokens: 100, TimeoutSeconds: 10}
}
func TestExecutionConfigurationAndRouting(t *testing.T) {
	c := executionTestConfig()
	if !c.valid() {
		t.Fatal("valid config rejected")
	}
	h := executionConfigHash(c)
	c.Prompt = "different"
	if h == executionConfigHash(c) {
		t.Fatal("prompt absent from hash")
	}
	c = executionTestConfig()
	c.ToolNames = []string{"execute_shell"}
	c.MaxToolCalls = 1
	if c.valid() {
		t.Fatal("write tool admitted")
	}
	a := agent{ID: "agent", Stable: "v1", Canary: "v2", Percent: 35}
	count := 0
	for i := 0; i < 1000; i++ {
		id := fmt.Sprint(i)
		v := executionVersion(a, id)
		if v != executionVersion(a, id) {
			t.Fatal("unstable router")
		}
		if v == "v2" {
			count++
		}
	}
	if count < 270 || count > 430 {
		t.Fatalf("canary sample %d", count)
	}
	g := executionGovernance{Level: "L0", Cap: "L1"}
	c = executionTestConfig()
	c.ToolNames = []string{"read_service_profile"}
	c.MaxToolCalls = 1
	if code, _ := g.blocked(c); code != "agent_level_tools" {
		t.Fatal(code)
	}
	g = executionGovernance{Level: "L0", Cap: "L0", Policies: []executionPolicy{{ID: "unknown", Name: "arbitrary", Enabled: true}}}
	if code, _ := g.blocked(executionTestConfig()); code != "unsupported_guard_policy" {
		t.Fatal(code)
	}
	if validExecutionEvidence("read_service_profile", "app", "dev", inference.Evidence{Kind: "service_profile", Source: "service_catalog", TargetID: base64.RawURLEncoding.EncodeToString([]byte("other\x00dev")), Content: "private"}) {
		t.Fatal("cross-app evidence admitted")
	}
}

type governedTestRunner struct {
	calls int
	fn    func(context.Context, inference.Request, func(inference.Event) error) (string, error)
}

func (r *governedTestRunner) Models() []inference.Model {
	return []inference.Model{{ID: "test", Tools: true}}
}
func (r *governedTestRunner) Run(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
	r.calls++
	if r.fn != nil {
		return r.fn(ctx, req, emit)
	}
	started := time.Now()
	step := inference.Step{ID: "m1", Kind: "model", Status: "running", StartedAt: started}
	if err := emit(inference.Event{Type: "step", Step: &step}); err != nil {
		return "", err
	}
	step.Status = "succeeded"
	finished := time.Now()
	step.FinishedAt = &finished
	if err := emit(inference.Event{Type: "step", Step: &step}); err != nil {
		return "", err
	}
	return "safe answer", nil
}

func TestExecutionGovernanceHTTPPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated disposable schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_execution_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
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
	user, token, err := st.Login(ctx, "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(db, policy)
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, marker string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), marker) {
			t.Fatalf("status=%d body=%s want %d %s", w.Code, w.Body.String(), status, marker)
		}
	}
	assert(request("POST", "/agents", `{"id":"agent","application_id":"app","name":"Agent"}`), 201, `"id":"agent"`)
	for _, v := range []string{"v1", "v2"} {
		assert(request("POST", "/agents/agent/versions", fmt.Sprintf(`{"version":%q,"artifact_ref":%q}`, v, "git:"+v)), 201, v)
	}
	pw := request("POST", "/prompts", `{"agent_id":"agent","name":"Prompt","text":"Only answer from scoped evidence"}`)
	assert(pw, 201, `"version":"v1"`)
	var prompt struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(pw.Body.Bytes(), &prompt); err != nil {
		t.Fatal(err)
	}
	config := executionTestConfig()
	config.PromptID = prompt.ID
	raw, _ := json.Marshal(config)
	assert(request("PUT", "/agents/agent/versions/v1/execution", string(raw)), 200, `"sha256"`)
	assert(request("PUT", "/agents/agent/versions/v1/execution", string(raw)), 200, `"sha256"`)
	config.MaxOutputTokens++
	raw2, _ := json.Marshal(config)
	assert(request("PUT", "/agents/agent/versions/v1/execution", string(raw2)), 409, "execution_config_immutable")
	assert(request("POST", "/agents/agent/rollout", `{"to":"v1","percent":100,"expected_revision":1,"override":true,"override_reason":"isolated fixture first release"}`), 200, "v1")
	runner := &governedTestRunner{}
	svc.SetExecutionRuntime(runner, nil)
	t.Cleanup(func() { svc.SetExecutionRuntime(nil, nil) })
	t.Run("formal guardrail registration authority idempotency and audit", func(t *testing.T) {
		assert(request("POST", "/guardrails", `{"name":"arbitrary_code"}`), 422, "invalid_agent_request")
		w := request("POST", "/guardrails", `{"name":"read_only"}`)
		assert(w, 201, `"forced":true`)
		var first struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &first); err != nil {
			t.Fatal(err)
		}
		w = request("POST", "/guardrails", `{"name":"read_only"}`)
		assert(w, 200, `"created":false`)
		var again struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &again); err != nil || again.ID != first.ID {
			t.Fatal("registration not idempotent")
		}
		var audits int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.agent.guardrail.registered' AND target=$1`, first.ID).Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("duplicate audit %d %v", audits, err)
		}
		manager, e := st.CreateUser(ctx, core.User{Email: "manager@example.invalid", Name: "Manager", Role: "viewer"}, "isolated-manager-password")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, `INSERT INTO rbac_roles(id,name) VALUES('governance-manager','Governance manager'); INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('governance-manager','agents','manage','global','*'),('governance-manager','agents','view','global','*'),('governance-manager','environment','view','global','*')`); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'governance-manager')`, manager.ID); e != nil {
			t.Fatal(e)
		}
		_, managerToken, e := st.Login(ctx, "manager@example.invalid", "isolated-manager-password")
		if e != nil {
			t.Fatal(e)
		}
		previous := token
		token = managerToken
		denied := request("POST", "/guardrails", `{"name":"bounded_execution"}`)
		token = previous
		assert(denied, 403, "forbidden")
		if _, e = db.Exec(ctx, `CREATE FUNCTION reject_guard_registration() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='v3.agent.guardrail.registered' THEN RAISE EXCEPTION 'isolated registration fault'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_guard_registration BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_guard_registration()`); e != nil {
			t.Fatal(e)
		}
		assert(request("POST", "/guardrails", `{"name":"bounded_execution"}`), 503, "unavailable")
		var rows int
		if e = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_guard_policies WHERE name='bounded_execution'`).Scan(&rows); e != nil || rows != 0 {
			t.Fatalf("registration survived failed audit %d %v", rows, e)
		}
		if _, e = db.Exec(ctx, `DROP TRIGGER reject_guard_registration ON management_audit; DROP FUNCTION reject_guard_registration()`); e != nil {
			t.Fatal(e)
		}
		assert(request("POST", "/guardrails", `{"name":"bounded_execution"}`), 201, `"forced":true`)
		assert(request("GET", "/guardrails", ""), 200, `"manual_execution_connected":true`)
	})
	t.Run("governance authorization does not invert role creation locks", func(t *testing.T) {
		// Hold the first write of CreateRole's transaction. Governance must finish
		// before this transaction reaches its second write to v3_role_grants;
		// recursively locking the platform-admin view would wait on rbac_roles
		// while retaining SHARE on v3_role_grants, creating the opposite order.
		creator, e := db.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer creator.Rollback(context.Background())
		if _, e = creator.Exec(ctx, `INSERT INTO rbac_roles(id,name,description,v3_kind,builtin) VALUES('custom-concurrent-role','Concurrent role','','business',false)`); e != nil {
			t.Fatal(e)
		}
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agents/environment-limit", nil).WithContext(readCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		assert(w, 200, `"max_level":"L0"`)
		if _, e = creator.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES('custom-concurrent-role','environment','view','environment','dev','standard')`); e != nil {
			t.Fatal(e)
		}
		if e = creator.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("legacy passed report cannot release new executable config", func(t *testing.T) {
		w := request("POST", "/evals", `{"agent_id":"agent","title":"Old metadata report","baseline_version":"v1","candidate_version":"v2"}`)
		assert(w, 201, `"status":"pending"`)
		var eval struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &eval); err != nil {
			t.Fatal(err)
		}
		if err = svc.RecordEvaluationOutcome(ctx, EvaluationOutcome{ID: eval.ID, Status: "passed", ResultRef: "git:legacy-result", Metrics: map[string]float64{"score": 1}}); err != nil {
			t.Fatal(err)
		}
		assert(request("PUT", "/agents/agent/versions/v2/execution", string(raw)), 200, `"sha256"`)
		assert(request("POST", "/agents/agent/rollout", `{"to":"v2","percent":100,"expected_revision":2}`), 409, "eval_gate_unverified")
	})
	t.Run("success and provenance", func(t *testing.T) {
		assert(request("POST", "/agents/agent/runs", `{"input":"`+strings.Repeat("x", 90)+`"}`), 422, "invalid_agent_request")
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, `"status":"success"`)
		var runID string
		if err = db.QueryRow(ctx, `SELECT id FROM v3_agent_runs WHERE status='success'`).Scan(&runID); err != nil {
			t.Fatal(err)
		}
		assert(request("GET", "/agent-runs/"+runID+"/result", ""), 200, `"output":"safe answer"`)
		var runs, spans, snapshots int
		err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM v3_agent_runs WHERE status='success'),(SELECT count(*) FROM v3_agent_run_spans),(SELECT count(*) FROM v3_agent_execution_snapshots WHERE model_id='test')`).Scan(&runs, &spans, &snapshots)
		if err != nil || runs != 1 || spans != 1 || snapshots != 1 {
			t.Fatalf("provenance %d %d %d %v", runs, spans, snapshots, err)
		}
	})
	t.Run("rollout changes selected config", func(t *testing.T) {
		assert(request("POST", "/agents/agent/rollout", `{"to":"v2","percent":100,"expected_revision":2,"override":true,"override_reason":"isolated fixture release"}`), 200, "governed_requests_consume_configuration")
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, `"version":"v2"`)
		assert(request("POST", "/agents/agent/rollback", `{"expected_revision":3}`), 200, `"to":"v1"`)
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, `"version":"v1"`)
	})
	t.Run("policy blocks before model", func(t *testing.T) {
		if err = svc.RegisterGuardPolicy(ctx, "unknown-policy", "dev", "unknown_policy", "Unsupported test rule", false); err != nil {
			t.Fatal(err)
		}
		before := runner.calls
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, `"error_code":"unsupported_guard_policy"`)
		if runner.calls != before {
			t.Fatal("blocked policy called model")
		}
		assert(request("PATCH", "/guardrails/unknown-policy", `{"enabled":false}`), 200, `"enabled":false`)
	})

	t.Run("level cap and scoped read tools", func(t *testing.T) {
		assert(request("GET", "/agents/environment-limit", ""), 200, `"max_level":"L0"`)
		assert(request("PUT", "/agents/environment-limit", `{"max_level":"L1","expected_max_level":"L0"}`), 200, `"max_level":"L1"`)
		assert(request("PUT", "/agents/environment-limit", `{"max_level":"L2","expected_max_level":"L0"}`), 409, "version_conflict")
		assert(request("POST", "/agents/agent/versions", `{"version":"v3","artifact_ref":"git:v3"}`), 201, "v3")
		tc := executionTestConfig()
		tc.PromptID = prompt.ID
		tc.ToolNames = []string{"read_service_profile"}
		tc.MaxToolCalls = 1
		raw, _ := json.Marshal(tc)
		assert(request("PUT", "/agents/agent/versions/v3/execution", string(raw)), 200, `"sha256"`)
		var revision int64
		if err = db.QueryRow(ctx, `SELECT lifecycle_revision FROM v3_agents WHERE id='agent'`).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		assert(request("POST", "/agents/agent/rollout", fmt.Sprintf(`{"to":"v3","percent":100,"expected_revision":%d,"override":true,"override_reason":"isolated tools"}`, revision)), 200, "v3")
		before := runner.calls
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, "agent_level_tools")
		if runner.calls != before {
			t.Fatal("L0 called model")
		}
		assert(request("PATCH", "/agents/agent/level", fmt.Sprintf(`{"level":"L1","expected_revision":%d}`, revision+1)), 200, `"level":"L1"`)
		reads := 0
		svc.SetExecutionRuntime(runner, func(ctx context.Context, actor, app, env, model string) ([]inference.EvidenceTool, error) {
			if actor != user.ID || app != "app" || env != "dev" || model != "test" {
				t.Fatal("unbound factory")
			}
			return []inference.EvidenceTool{{Name: "read_service_profile", Read: func(context.Context, string) (inference.Evidence, error) {
				reads++
				return inference.Evidence{Kind: "service_profile", Source: "service_catalog", TargetID: base64.RawURLEncoding.EncodeToString([]byte("app\x00dev")), Content: "healthy"}, nil
			}}}, nil
		})
		runner.fn = func(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
			if len(req.Tools) != 1 || req.MaxToolCalls != 1 || req.MaxOutputTokens != 100 {
				t.Fatal("budget not consumed")
			}
			_, err := req.Tools[0].Read(ctx, "status")
			return "scoped", err
		}
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, `"status":"success"`)
		if reads != 1 {
			t.Fatal("read not called")
		}
		runner.fn = nil
	})
	t.Run("sensitive guard blocks input and output", func(t *testing.T) {
		assert(request("POST", "/guardrails", `{"name":"deny_sensitive_content"}`), 201, `"forced":false`)
		before := runner.calls
		assert(request("POST", "/agents/agent/runs", `{"input":"password=private"}`), 200, "sensitive_input")
		if runner.calls != before {
			t.Fatal("blocked input reached model")
		}
		runner.fn = func(context.Context, inference.Request, func(inference.Event) error) (string, error) {
			return "password=must-not-escape", nil
		}
		w := request("POST", "/agents/agent/runs", `{"input":"status"}`)
		assert(w, 200, "sensitive_output")
		if strings.Contains(w.Body.String(), "must-not-escape") {
			t.Fatal("secret escaped")
		}
		runner.fn = nil
	})
	t.Run("final lock wait respects persisted deadline", func(t *testing.T) {
		released := make(chan error, 1)
		runner.fn = func(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
			blocker, e := db.Begin(ctx)
			if e != nil {
				return "", e
			}
			if _, e = blocker.Exec(ctx, `SELECT id FROM v3_agent_runs WHERE id=$1 FOR UPDATE`, req.RunID); e != nil {
				blocker.Rollback(ctx)
				return "", e
			}
			if _, e = blocker.Exec(ctx, `UPDATE v3_agent_execution_snapshots SET deadline=clock_timestamp()+interval '100 milliseconds' WHERE run_id=$1`, req.RunID); e != nil {
				blocker.Rollback(ctx)
				return "", e
			}
			go func() { time.Sleep(350 * time.Millisecond); released <- blocker.Commit(context.Background()) }()
			return "must expire", nil
		}
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, "execution_expired")
		if e := <-released; e != nil {
			t.Fatal(e)
		}
		runner.fn = nil
	})
	t.Run("configuration drift suppresses output", func(t *testing.T) {
		runner.fn = func(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
			_, err := db.Exec(ctx, `UPDATE v3_agents SET lifecycle_revision=lifecycle_revision+1 WHERE id='agent'`)
			return "must not escape", err
		}
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, `"error_code":"execution_authority_changed"`)
		runner.fn = nil
	})
	t.Run("provider failure terminal", func(t *testing.T) {
		runner.fn = func(context.Context, inference.Request, func(inference.Event) error) (string, error) {
			return "private", fmt.Errorf("upstream private payload")
		}
		w := request("POST", "/agents/agent/runs", `{"input":"status"}`)
		assert(w, 200, `"status":"failed"`)
		if strings.Contains(w.Body.String(), "private") {
			t.Fatal("failure leaked content")
		}
		runner.fn = nil
	})
	t.Run("cannot backfill executable config after release", func(t *testing.T) {
		assert(request("POST", "/agents/agent/versions", `{"version":"legacy-active","artifact_ref":"git:legacy-active"}`), 201, "legacy-active")
		var revision int64
		if err = db.QueryRow(ctx, `SELECT lifecycle_revision FROM v3_agents WHERE id='agent'`).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		assert(request("POST", "/agents/agent/rollout", fmt.Sprintf(`{"to":"legacy-active","percent":100,"expected_revision":%d,"override":true,"override_reason":"isolated legacy release"}`, revision)), 200, "legacy-active")
		assert(request("PUT", "/agents/agent/versions/legacy-active/execution", string(raw)), 409, "active_version_unconfigured")
		assert(request("POST", "/agents/agent/rollback", fmt.Sprintf(`{"expected_revision":%d}`, revision+1)), 200, `"to":"v3"`)
		assert(request("PUT", "/agents/agent/versions/legacy-active/execution", string(raw)), 409, "active_version_unconfigured")
		var remains int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_execution_configs WHERE agent_id='agent' AND version='legacy-active'`).Scan(&remains); err != nil || remains != 0 {
			t.Fatalf("rollback target config persisted %d %v", remains, err)
		}
	})
	t.Run("audit failure prevents success result", func(t *testing.T) {
		if _, err = db.Exec(ctx, `CREATE FUNCTION reject_execution_success() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='v3.agent.run.completed' AND NEW.changes->>'status'='success' THEN RAISE EXCEPTION 'isolated audit fault'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_execution_success BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_execution_success()`); err != nil {
			t.Fatal(err)
		}
		var before int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_execution_results`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 503, "unavailable")
		var after int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_execution_results`).Scan(&after); err != nil || after != before {
			t.Fatalf("result committed without audit %d %d %v", before, after, err)
		}
		if _, err = db.Exec(ctx, `DROP TRIGGER reject_execution_success ON management_audit; DROP FUNCTION reject_execution_success(); UPDATE v3_agent_execution_snapshots SET deadline=now()-interval '1 minute'`); err != nil {
			t.Fatal(err)
		}
		if err = svc.RecoverExpiredExecutions(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("expiry recovery", func(t *testing.T) {
		if _, err = db.Exec(ctx, `UPDATE v3_agent_runs SET status='running',finished_at=NULL WHERE id=(SELECT run_id FROM v3_agent_execution_snapshots LIMIT 1); UPDATE v3_agent_execution_snapshots SET deadline=now()-interval '1 minute'`); err != nil {
			t.Fatal(err)
		}
		if err = svc.RecoverExpiredExecutions(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_runs WHERE status='running'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("active %d %v", count, err)
		}
	})
	t.Run("logout during model suppresses output", func(t *testing.T) {
		runner.fn = func(ctx context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
			return "private answer", st.Logout(ctx, token)
		}
		assert(request("POST", "/agents/agent/runs", `{"input":"status"}`), 200, "execution_authority_changed")
	})

}
