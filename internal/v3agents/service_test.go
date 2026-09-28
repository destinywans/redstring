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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestAgentDomainPersistedAuthorizationAndUnknowns(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses one isolated disposable schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_agents_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	searchPath := schema + ",public"
	var extensionSchema string
	err = adminDB.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n
 ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&extensionSchema)
	if err == nil && extensionSchema != schema && extensionSchema != "public" {
		searchPath += "," + pgx.Identifier{extensionSchema}.Sanitize()
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-agent-password"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES
 ('agent-app','Agent App','agent-app','dev'),('other-app','Other App','other-app','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('agent-app-dev','agent-app','dev','Dev'),('other-app-dev','other-app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('agent-app','dev'),('other-app','dev')`,
		`INSERT INTO rbac_roles(id,name) VALUES('agent-viewer','Agent Viewer')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('agent-viewer','agents','view','application','agent-app')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'agent-viewer')`, viewer.ID); err != nil {
		t.Fatal(err)
	}
	machine, err := st.CreateUser(ctx, core.User{Email: "machine@example.invalid", Name: "Machine Admin", Role: "viewer"}, "isolated-machine-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, machine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'admin')`, machine.ID); err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err = db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	svc := New(db, &rbac.V3Policy{Store: st})
	if err = svc.RegisterCatalogAgent(ctx, "triage", "agent-app", "dev", "Triage", "v1", "git:1111111", adminID); err != nil {
		t.Fatal(err)
	}
	if err = svc.RegisterCatalogAgent(ctx, "triage", "agent-app", "dev", "Triage", "v2", "git:2222222", adminID); err != nil {
		t.Fatal(err)
	}
	if err = svc.RegisterCatalogAgent(ctx, "other", "other-app", "dev", "Other", "v1", "git:3333333", adminID); err != nil {
		t.Fatal(err)
	}
	if err = svc.RegisterCatalogAgent(ctx, "triage", "other-app", "dev", "Triage", "v3", "git:4444444", adminID); err == nil {
		t.Fatal("cross-app catalog conflict accepted")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_agents(id,application_id,environment_id,name)
 SELECT 'hidden-'||lpad(g::text,3,'0'),'other-app','dev',
 'A Hidden '||lpad(g::text,3,'0') FROM generate_series(1,202) g`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-agent-password")
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, machineToken, err := st.Login(ctx, "machine@example.invalid", "isolated-machine-password")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, env, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, contains string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), contains) {
			t.Fatalf("status=%d body=%s, want %d and %s", w.Code, w.Body.String(), status, contains)
		}
	}
	assert(request("GET", "/api/v1/agents/triage", viewerToken, "dev", ""), 200, `"score":null`)
	assert(request("GET", "/api/v1/agents/triage", viewerToken, "dev", ""), 200, `"status":"draft"`)
	assert(request("GET", "/api/v1/agents/other", viewerToken, "dev", ""), 403, `"code":"forbidden"`)
	list := request("GET", "/api/v1/agents/lifecycle", viewerToken, "dev", "")
	assert(list, 200, `"id":"triage"`)
	if !strings.Contains(list.Body.String(), `"truncated":false`) {
		t.Fatalf("hidden agents caused false truncation: %s", list.Body.String())
	}
	if strings.Contains(list.Body.String(), `"id":"other"`) {
		t.Fatalf("other app leaked: %s", list.Body.String())
	}
	assert(request("GET", "/api/v1/agents/lifecycle", adminToken, "dev", ""), 200, `"truncated":true`)
	assert(request("POST", "/api/v1/agents/triage/rollout", viewerToken, "dev", `{"to":"v1","percent":100}`), 403, `"code":"forbidden"`)
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('agent-viewer','agents','manage','application','agent-app')`); err != nil {
		t.Fatal(err)
	}
	assert(request("POST", "/api/v1/agents/triage/rollout", viewerToken, "dev", `{"to":"v1","percent":100,"override":true,"override_reason":"not admin"}`), 403, `"code":"admin_override_required"`)
	assert(request("POST", "/api/v1/agents/triage/rollout", machineToken, "dev", `{"to":"v1","percent":100,"override":true,"override_reason":"machine with admin role"}`), 403, `"code":"admin_override_required"`)
	var preRelease int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_release_events WHERE agent_id='triage'`).Scan(&preRelease); err != nil {
		t.Fatal(err)
	}
	if preRelease != 0 {
		t.Fatal("machine override wrote release event")
	}
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "dev", `{"to":"v1","percent":100}`), 409, `"code":"eval_gate_unverified"`)
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "all", `{"to":"v1","percent":100}`), 409, `"code":"env_view_only"`)
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "dev", `{"to":"v1","percent":100,"override":true}`), 422, `"code":"invalid_agent_request"`)
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "dev", `{"to":"v1","percent":100,"override":true,"override_reason":"Initial reviewed release"}`), 200, `"routing_connected":false`)
	detail := request("GET", "/api/v1/agents/triage", adminToken, "dev", "")
	assert(detail, 200, `"configured_traffic":"100%"`)
	if !strings.Contains(detail.Body.String(), `"traffic":"未接入"`) || !strings.Contains(detail.Body.String(), `"current":null`) {
		t.Fatalf("configured traffic presented as observed: %s", detail.Body.String())
	}
	assert(request("PATCH", "/api/v1/agents/triage/level", adminToken, "dev", `{"level":"L1"}`), 409, `"code":"agent_level_cap"`)
	if _, err = db.Exec(ctx, `INSERT INTO v3_agent_environment_limits(environment_id,max_level) VALUES('dev','L1')`); err != nil {
		t.Fatal(err)
	}
	assert(request("PATCH", "/api/v1/agents/triage/level", adminToken, "dev", `{"level":"L1"}`), 200, `"max_level":"L1"`)
	if _, err = db.Exec(ctx, `INSERT INTO v3_agent_prompts(id,agent_id,name) VALUES('p1','triage','Triage prompt')`); err != nil {
		t.Fatal(err)
	}
	assert(request("POST", "/api/v1/prompts/p1/versions", adminToken, "dev", `{"text":"Investigate {{ service }} with private details"}`), 201, `"version":"1"`)
	var auditText string
	if err = db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE action='v3.agent.prompt.version' ORDER BY id DESC LIMIT 1`).Scan(&auditText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditText, "private details") {
		t.Fatal("prompt text leaked to audit")
	}
	assert(request("POST", "/api/v1/prompts/p1/trial", adminToken, "dev", `{"case":"c1","a":"v1","b":"v2"}`), 503, `"code":"prompt_trial_unavailable"`)
	assert(request("GET", "/api/v1/prompts", adminToken, "dev", ""), 200, `"id":"p1"`)
	if err = svc.RegisterTool(ctx, ToolRegistration{ID: "read-pods", AgentID: "triage", Name: "Read Pods", Kind: "read", Risk: "low"}); err != nil {
		t.Fatal(err)
	}
	assert(request("GET", "/api/v1/agent-tools", adminToken, "dev", ""), 200, `"id":"read-pods"`)
	if err = svc.RegisterTool(ctx, ToolRegistration{ID: "write-pods", AgentID: "triage", Name: "Write Pods", Kind: "write", Risk: "high", ApprovalRequired: false}); err == nil {
		t.Fatal("write tool without approval accepted")
	}
	assert(request("POST", "/api/v1/evals", adminToken, "dev", `{"agent_id":"triage","title":"V2 gate","baseline_version":"v1","candidate_version":"v2"}`), 201, `"status":"pending"`)
	if err = svc.RegisterEvalCase(ctx, "case1", "triage", "Known alert", "git:case1"); err != nil {
		t.Fatal(err)
	}
	assert(request("GET", "/api/v1/eval-cases/case1", viewerToken, "dev", ""), 200, `"partial":true`)
	assert(request("POST", "/api/v1/eval-cases/case1/labels", viewerToken, "dev", `{"next":"correct"}`), 403, `"code":"forbidden"`)
	assert(request("POST", "/api/v1/eval-cases/case1/labels", adminToken, "dev", `{"next":"partial","note":"needs investigation"}`), 200, `"next":"partial"`)
	var evalID string
	if err = db.QueryRow(ctx, `SELECT id FROM v3_agent_eval_experiments WHERE agent_id='triage'`).Scan(&evalID); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordEvaluationOutcome(ctx, EvaluationOutcome{ID: evalID, Status: "passed", ResultRef: "git:eval-proof", Metrics: map[string]float64{"accuracy": 0.9}}); err != nil {
		t.Fatal(err)
	}
	var leaked int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.agent.eval.outcome'
 AND changes::text LIKE '%git:eval-proof%'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("evaluation result reference leaked to audit")
	}
	assert(request("GET", "/api/v1/evals/"+evalID, adminToken, "dev", ""), 200, `"accuracy":0.9`)
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "dev", `{"to":"v2","percent":50}`), 200, `"gate_experiment_id":"`+evalID+`"`)
	assert(request("POST", "/api/v1/agents/triage/rollback", adminToken, "dev", `{}`), 200, `"to":"v1"`)
	if err = svc.StartRun(ctx, "run1", "triage", "v1", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordSpan(ctx, SpanEvent{ID: "span1", RunID: "run1", Kind: "tool", Name: "kubectl.read", Status: "ok", Started: time.Now(), ToolID: "kube.read"}); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordSpan(ctx, SpanEvent{ID: "bad-span", RunID: "run1", Kind: "tool", Name: "kubectl.read", Status: "ok", Started: time.Now(), ToolID: "vault:kv/secret"}); err == nil {
		t.Fatal("secret-shaped tool id accepted")
	}
	if err = svc.RegisterGuardPolicy(ctx, "mandatory", "dev", "Mandatory", "Write approval", true); err != nil {
		t.Fatal(err)
	}
	mandatoryDecision, err := svc.PolicySnapshot(ctx, "mandatory", "dev")
	if err != nil || !mandatoryDecision.Enabled || mandatoryDecision.Revision != 1 {
		t.Fatalf("mandatory policy snapshot=%+v err=%v", mandatoryDecision, err)
	}
	if err = svc.CompleteRun(ctx, RunCompletion{ID: "run1", Status: "failed", ErrorCode: "vault:kv/secret"}); err == nil {
		t.Fatal("secret-shaped error code accepted")
	}
	if err = svc.CompleteRun(ctx, RunCompletion{ID: "run1", Status: "blocked"}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_agent_runs
 (id,agent_id,version,application_id,environment_id,trigger_kind,status,started_at,finished_at)
 SELECT 'hidden-run-'||lpad(g::text,3,'0'),'other','v1','other-app','dev','manual',
 'success',now()+interval '1 minute',now()+interval '61 seconds' FROM generate_series(1,202) g`); err != nil {
		t.Fatal(err)
	}
	assert(request("GET", "/api/v1/agent-runs", adminToken, "dev", ""), 200, `"truncated":true`)
	viewerRuns := request("GET", "/api/v1/agent-runs", viewerToken, "dev", "")
	assert(viewerRuns, 200, `"id":"run1"`)
	if strings.Contains(viewerRuns.Body.String(), `"id":"hidden-run-`) {
		t.Fatalf("hidden run leaked: %s", viewerRuns.Body.String())
	}
	assert(request("GET", "/api/v1/agent-runs/run1/trace", adminToken, "dev", ""), 200, `"partial":true`)
	assert(request("POST", "/api/v1/agent-runs/run1/feedback", adminToken, "dev", `{"vote":"down"}`), 200, `"vote":"down"`)
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "guard1", RunID: "run1", PolicyID: "mandatory", Action: "blocked", TargetKind: "tool", ReasonCode: "write_approval_required",
		PolicyRevision: mandatoryDecision.Revision, ObservedAt: mandatoryDecision.ObservedAt}); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "bad-guard", RunID: "run1", PolicyID: "mandatory", Action: "blocked", TargetKind: "tool", ReasonCode: "vault:kv/secret",
		PolicyRevision: mandatoryDecision.Revision, ObservedAt: mandatoryDecision.ObservedAt}); err == nil {
		t.Fatal("secret-shaped guard reason accepted")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_agent_guard_events
 (id,run_id,policy_id,action,target_kind,reason_code,created_at,observed_at,policy_revision)
 SELECT 'hidden-guard-'||lpad(g::text,3,'0'),'hidden-run-'||lpad(g::text,3,'0'),
 'mandatory','blocked','tool','blocked_by_policy',now()+interval '1 minute',
 now()+interval '1 minute',1
 FROM generate_series(1,202) g`); err != nil {
		t.Fatal(err)
	}
	assert(request("GET", "/api/v1/guardrails", adminToken, "dev", ""), 200, `"truncated":true`)
	viewerGuards := request("GET", "/api/v1/guardrails", viewerToken, "dev", "")
	assert(viewerGuards, 200, `"id":"guard1"`)
	if strings.Contains(viewerGuards.Body.String(), `"id":"hidden-guard-`) {
		t.Fatalf("hidden guard event leaked: %s", viewerGuards.Body.String())
	}
	assert(request("PATCH", "/api/v1/guardrails/mandatory", adminToken, "dev", `{"enabled":false}`), 409, `"code":"forced_guardrail"`)
	if err = svc.StartRun(ctx, "run2", "triage", "v1", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = svc.RegisterGuardPolicy(ctx, "optional", "dev", "Optional", "Review output", false); err != nil {
		t.Fatal(err)
	}
	beforeDisable, err := svc.PolicySnapshot(ctx, "optional", "dev")
	if err != nil || !beforeDisable.Enabled || beforeDisable.Revision != 1 {
		t.Fatalf("pre-disable snapshot=%+v err=%v", beforeDisable, err)
	}
	assert(request("PATCH", "/api/v1/guardrails/optional", adminToken, "dev", `{"enabled":false}`), 200, `"revision":2`)
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "late-guard", RunID: "run2", PolicyID: "optional",
		Action: "masked", TargetKind: "output", ReasonCode: "sensitive_output",
		PolicyRevision: beforeDisable.Revision, ObservedAt: beforeDisable.ObservedAt}); err != nil {
		t.Fatalf("late event lost after disable: %v", err)
	}
	afterDisable, err := svc.PolicySnapshot(ctx, "optional", "dev")
	if err != nil || afterDisable.Enabled || afterDisable.Revision != 2 {
		t.Fatalf("post-disable snapshot=%+v err=%v", afterDisable, err)
	}
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "disabled-guard", RunID: "run2", PolicyID: "optional",
		Action: "masked", TargetKind: "output", ReasonCode: "sensitive_output",
		PolicyRevision: afterDisable.Revision, ObservedAt: afterDisable.ObservedAt}); err == nil {
		t.Fatal("disabled policy decision accepted")
	}
	var disabledAt time.Time
	if err = db.QueryRow(ctx, `SELECT valid_to FROM v3_agent_guard_policy_revisions WHERE policy_id='optional' AND revision=1`).Scan(&disabledAt); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "boundary-guard", RunID: "run2", PolicyID: "optional",
		Action: "masked", TargetKind: "output", ReasonCode: "sensitive_output",
		PolicyRevision: beforeDisable.Revision, ObservedAt: disabledAt}); err == nil {
		t.Fatal("old revision accepted at disable boundary")
	}
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "unknown-revision-guard", RunID: "run2", PolicyID: "optional",
		Action: "masked", TargetKind: "output", ReasonCode: "sensitive_output",
		PolicyRevision: 99, ObservedAt: beforeDisable.ObservedAt}); err == nil {
		t.Fatal("unknown policy revision accepted")
	}
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "future-guard", RunID: "run2", PolicyID: "optional",
		Action: "masked", TargetKind: "output", ReasonCode: "sensitive_output",
		PolicyRevision: beforeDisable.Revision, ObservedAt: time.Now().Add(2 * time.Second)}); err == nil {
		t.Fatal("future decision time accepted")
	}
	if err = svc.CompleteRun(ctx, RunCompletion{ID: "run2", Status: "blocked"}); err != nil {
		t.Fatal(err)
	}
	var lateAudit int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.agent.guard.event' AND target='late-guard'`).Scan(&lateAudit); err != nil {
		t.Fatal(err)
	}
	if lateAudit != 1 {
		t.Fatalf("late guard audit rows=%d", lateAudit)
	}
	// Model an already persisted decision interval and run from three days ago.
	// Reporting after the policy was disabled must not have an arbitrary 24h cutoff.
	if _, err = db.Exec(ctx, `INSERT INTO v3_agent_runs
 (id,agent_id,version,application_id,environment_id,trigger_kind,status,started_at,finished_at)
 VALUES('old-run','triage','v1','agent-app','dev','manual','blocked',
 clock_timestamp()-interval '72 hours',clock_timestamp()-interval '70 hours')`); err != nil {
		t.Fatal(err)
	}
	if err = svc.RegisterGuardPolicy(ctx, "historical", "dev", "Historical", "Late reporting", false); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE v3_agent_guard_policy_revisions
 SET valid_from=clock_timestamp()-interval '73 hours'
 WHERE policy_id='historical' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	var oldDecisionAt time.Time
	if err = db.QueryRow(ctx, `SELECT started_at+interval '1 hour' FROM v3_agent_runs WHERE id='old-run'`).Scan(&oldDecisionAt); err != nil {
		t.Fatal(err)
	}
	assert(request("PATCH", "/api/v1/guardrails/historical", adminToken, "dev", `{"enabled":false}`), 200, `"revision":2`)
	if err = svc.RecordGuardEvent(ctx, GuardEvent{ID: "old-guard", RunID: "old-run", PolicyID: "historical",
		Action: "blocked", TargetKind: "tool", ReasonCode: "historical_block",
		PolicyRevision: 1, ObservedAt: oldDecisionAt}); err != nil {
		t.Fatalf("historical event lost after disable: %v", err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit
 WHERE action='v3.agent.guard.event' AND target='old-guard'`).Scan(&lateAudit); err != nil {
		t.Fatal(err)
	}
	if lateAudit != 1 {
		t.Fatalf("historical guard audit rows=%d", lateAudit)
	}
	if err = svc.RegisterGuardPolicy(ctx, "racing", "dev", "Racing", "Concurrent disable", false); err != nil {
		t.Fatal(err)
	}
	raceCfg := cfg.Copy()
	raceCfg.MaxConns = 2
	raceDB, err := pgxpool.NewWithConfig(ctx, raceCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer raceDB.Close()
	lockTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(ctx)
	if err = lockTx.QueryRow(ctx, `SELECT revision FROM v3_agent_guard_policies
 WHERE id='racing' FOR UPDATE`).Scan(new(int64)); err != nil {
		t.Fatal(err)
	}
	type snapshotResult struct {
		snapshot GuardPolicySnapshot
		err      error
	}
	snapshotCh := make(chan snapshotResult, 1)
	go func() {
		snapshot, snapshotErr := New(raceDB, nil).PolicySnapshot(ctx, "racing", "dev")
		snapshotCh <- snapshotResult{snapshot, snapshotErr}
	}()
	select {
	case result := <-snapshotCh:
		t.Fatalf("snapshot bypassed an uncommitted disable: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	var changedAt time.Time
	if err = lockTx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&changedAt); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE v3_agent_guard_policy_revisions SET valid_to=$1 WHERE policy_id='racing' AND revision=1`,
		`INSERT INTO v3_agent_guard_policy_revisions(policy_id,revision,enabled,valid_from)
 VALUES('racing',2,false,$1)`,
		`UPDATE v3_agent_guard_policies SET revision=2,enabled=false,updated_at=$1 WHERE id='racing'`,
	} {
		if _, err = lockTx.Exec(ctx, sql, changedAt); err != nil {
			t.Fatal(err)
		}
	}
	if err = lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-snapshotCh:
		if result.err != nil || result.snapshot.Enabled || result.snapshot.Revision != 2 || result.snapshot.ObservedAt.Before(changedAt) {
			t.Fatalf("snapshot crossed disable boundary: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot remained blocked after disable commit")
	}
	var toolAudit int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.agent.tool.called'`).Scan(&toolAudit); err != nil {
		t.Fatal(err)
	}
	if toolAudit != 1 {
		t.Fatalf("tool call audit rows=%d", toolAudit)
	}
	var scope map[string]any
	if err = json.Unmarshal(request("GET", "/api/v1/agent-runs/run1", adminToken, "dev", "").Body.Bytes(), &scope); err != nil {
		t.Fatal(err)
	}
	if scope["context"] != nil || scope["memory"] != nil {
		t.Fatalf("invented run context: %v", scope)
	}
	if err = svc.RegisterCatalogAgent(ctx, "triage", "agent-app", "dev", "Triage", "v3", "git:3333333", adminID); err != nil {
		t.Fatal(err)
	}
	assert(request("POST", "/api/v1/evals", adminToken, "dev", `{"agent_id":"triage","title":"stale gate","baseline_version":"v1","candidate_version":"v3"}`), 201, `"status":"pending"`)
	var staleEvalID string
	if err = db.QueryRow(ctx, `SELECT id FROM v3_agent_eval_experiments WHERE title='stale gate'`).Scan(&staleEvalID); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordEvaluationOutcome(ctx, EvaluationOutcome{ID: staleEvalID, Status: "passed", ResultRef: "vault:kv/secret", Metrics: map[string]float64{"accuracy": 0.9}}); err == nil {
		t.Fatal("Vault reference accepted as evaluation result")
	}
	if err = svc.RecordEvaluationOutcome(ctx, EvaluationOutcome{ID: staleEvalID, Status: "passed", ResultRef: "git:stale-proof", Metrics: map[string]float64{"accuracy": 0.9}}); err != nil {
		t.Fatal(err)
	}
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "dev", `{"to":"v2","percent":100}`), 200, `"configured_percent":100`)
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "dev", `{"to":"v3","percent":50}`), 409, `"code":"eval_gate_unverified"`)
	var staleRollouts int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_release_events WHERE to_version='v3'`).Scan(&staleRollouts); err != nil {
		t.Fatal(err)
	}
	if staleRollouts != 0 {
		t.Fatalf("stale baseline release rows=%d", staleRollouts)
	}
	for _, outcome := range []struct{ title, status string }{
		{"current pass", "passed"}, {"current fail", "failed"},
	} {
		assert(request("POST", "/api/v1/evals", adminToken, "dev",
			`{"agent_id":"triage","title":"`+outcome.title+`","baseline_version":"v2","candidate_version":"v3"}`),
			201, `"status":"pending"`)
		var id string
		if err = db.QueryRow(ctx, `SELECT id FROM v3_agent_eval_experiments WHERE title=$1`, outcome.title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if err = svc.RecordEvaluationOutcome(ctx, EvaluationOutcome{ID: id, Status: outcome.status,
			ResultRef: "git:" + strings.ReplaceAll(outcome.title, " ", "-"), Metrics: map[string]float64{"accuracy": 0.9}}); err != nil {
			t.Fatal(err)
		}
	}
	assert(request("POST", "/api/v1/agents/triage/rollout", adminToken, "dev", `{"to":"v3","percent":50}`), 409, `"code":"eval_gate_unverified"`)
}
