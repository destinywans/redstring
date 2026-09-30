package operations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type automationFakeRuntime struct{}

func (automationFakeRuntime) Models() []inference.Model { return []inference.Model{{ID: "fake"}} }
func (automationFakeRuntime) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	return "", errors.New("unexpected model invocation")
}
func TestPatrolNeverClaimsHealthWithoutLiveData(t *testing.T) {
	valid := freshPatrolEvidence("metrics", nil)
	for _, tc := range []struct{ name, body string }{{"empty", `{"series":[],"empty":true}`}, {"empty_series", `{"series":[{}]}`}, {"failed", `{"series":[{"samples":[{"value":"0"}]}],"source":{"failure":{"kind":"timeout"}}}`}, {"nan", `{"series":[{"samples":[{"value":"NaN"}]}]}`}, {"knowledge", `{"items":[{"content":"all healthy"}]}`}} {
		t.Run(tc.name, func(t *testing.T) {
			r := parsePatrol(`{"state":"clear","summary":"healthy"}`, []inference.Evidence{{Kind: "metrics", Content: tc.body}}, false)
			if r.State != "indeterminate" {
				t.Fatalf("false health %s", r.State)
			}
		})
	}
	if parsePatrol(`{"state":"clear"}`, []inference.Evidence{valid}, true).State != "indeterminate" {
		t.Fatal("failed tool became healthy")
	}
	if parsePatrol(`{"state":"suspected","summary":"check"}`, []inference.Evidence{valid}, false).State != "suspected" {
		t.Fatal("valid suspected evidence lost")
	}
}
func isolatedAutomation(t *testing.T) *Service {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated random schema")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("database config")
	}
	schema := "test_auto_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if e != nil {
			t.Error("isolated cleanup failed")
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("config")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("connect")
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES('op','op@example.invalid','Operator','operator','unused'),('admin','admin@example.invalid','Admin','admin','unused');INSERT INTO applications(id,key,name) VALUES('a','a','App');INSERT INTO application_environments(id,application_id,key,name) VALUES('e1','a','one','One'),('e2','a','two','Two'),('e3','a','three','Three');INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES('a','user','op','operate');`)
	if err != nil {
		t.Fatal(err)
	}
	r := automationFakeRuntime{}
	// Seed actual infrastructure grants instead of relying on the retired
	// application ACL to authorize investigations and automatic admission.
	_, err = db.Exec(ctx, `INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('auto-host','host','Test host','manual');
INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES('auto-host','a','e1');
INSERT INTO rbac_roles(id,name) VALUES('auto-fixture','Automation fixture');
INSERT INTO rbac_permissions(id,name) VALUES('auto-view','Automation fixture view'),('auto-update','Automation fixture update');
INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES('auto-view','resource','auto-host','view'),('auto-update','resource','auto-host','update');
INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES('auto-fixture','auto-view'),('auto-fixture','auto-update');
INSERT INTO rbac_user_roles(user_id,role_id) VALUES('op','auto-fixture'),('admin','auto-fixture');`)
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, st, httpapi.New(st, r), r, Config{})
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}
func TestAutomationAdmissionBudgetScopeAndRecovery(t *testing.T) {
	s := isolatedAutomation(t)
	ctx := context.Background()
	admin := core.User{ID: "admin", Role: "admin"}
	for _, env := range []string{"e1", "e2", "e3"} {
		p := DefaultPolicy(env)
		p.Patrol = true
		p.ModelID = "fake"
		p.ActorID = "op"
		p.DailyBudget = 1
		if _, err := s.SavePolicy(ctx, admin, p, 0); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(id, env string) {
		p, err := s.Policy(ctx, env)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(p)
		if _, err = s.DB.Exec(ctx, "INSERT INTO ops_automation_runs(id,environment_id,kind,policy) VALUES($1,$2,'patrol',$3)", id, env, raw); err != nil {
			t.Fatal(err)
		}
	}
	insert("r1", "e1")
	insert("r1b", "e1")
	insert("r2", "e2")
	insert("r3", "e3")
	a, err := s.claimAutomation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.claimAutomation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.Environment == b.Environment {
		t.Fatal("same environment concurrency")
	}
	if _, err = s.claimAutomation(ctx); !errors.Is(err, store.ErrNoWork) {
		t.Fatal("global concurrency >2", err)
	}
	s.finishAutomation(ctx, a, "failed", "failure consumes budget", nil)
	if _, err = s.claimAutomation(ctx); !errors.Is(err, store.ErrNoWork) {
		t.Fatal("budget not enforced", err)
	}
	var status string
	if err = s.DB.QueryRow(ctx, "SELECT status FROM ops_automation_runs WHERE id='r1b'").Scan(&status); err != nil || status != "exhausted" {
		t.Fatalf("budget status %s %v", status, err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET lease_until=now()-interval '1 minute',result='{\"evidence\":[{\"title\":\"partial\"}]}' WHERE id=$1", b.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.reconcileAutomation(ctx); err != nil {
		t.Fatal(err)
	}
	var result []byte
	if err = s.DB.QueryRow(ctx, "SELECT status,result FROM ops_automation_runs WHERE id=$1", b.ID).Scan(&status, &result); err != nil || status != "indeterminate" {
		t.Fatal("interrupted task replayed", status, err)
	}
	var obj map[string]any
	if json.Unmarshal(result, &obj) != nil || obj["evidence"] == nil {
		t.Fatal("partial evidence erased")
	}
	c, err := s.claimAutomation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "DELETE FROM rbac_user_roles WHERE user_id='op' AND role_id='auto-fixture'"); err != nil {
		t.Fatal(err)
	}
	if err = s.checkAutomation(ctx, c); err == nil {
		t.Fatal("permission revocation ignored")
	}
}
func TestAutomationInheritedScheduleDoesNotReplayBacklog(t *testing.T) {
	s := isolatedAutomation(t)
	ctx := context.Background()
	p := DefaultPolicy("workspace")
	p.Patrol = true
	p.ActorID = "op"
	p.ModelID = "fake"
	if _, err := s.SavePolicy(ctx, core.User{ID: "admin", Role: "admin"}, p, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.scheduleAutomation(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM ops_automation_runs").Scan(&count); err != nil || count != 0 {
		t.Fatal("inherited enable launched immediately", count, err)
	}
	if _, err := s.DB.Exec(ctx, "UPDATE ops_policies SET next_due=now()-interval '5 days' WHERE environment_id='e1'"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.scheduleAutomation(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DB.QueryRow(ctx, "SELECT count(*) FROM ops_automation_runs WHERE environment_id='e1'").Scan(&count); err != nil || count != 1 {
		t.Fatal("missed intervals replayed", count, err)
	}
	var due bool
	if err := s.DB.QueryRow(ctx, "SELECT next_due>now() FROM ops_policies WHERE environment_id='e1'").Scan(&due); err != nil || !due {
		t.Fatal("next due not advanced", err)
	}
}

func TestAutomationInitialAdmissionIsIdempotentAndBounded(t *testing.T) {
	s := isolatedAutomation(t)
	ctx := context.Background()
	p := DefaultPolicy("e1")
	p.AutoInitial = true
	p.ModelID = "fake"
	p.ActorID = "op"
	if _, err := s.SavePolicy(ctx, core.User{ID: "admin", Role: "admin"}, p, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, "INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at) VALUES('incident','test','a','e1','fp','High errors','warning','open','firing',now())"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.scheduleAutomation(ctx); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.claimAutomation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scoped, _, err := s.scopedContext(ctx, c.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatchInitial(scoped, c); err != nil {
		t.Fatal(err)
	}
	if err = s.dispatchInitial(scoped, c); err != nil {
		t.Fatal("dispatch retry", err)
	}
	var runs, investigations int
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err = s.DB.QueryRow(ctx, "SELECT count(*) FROM investigations").Scan(&investigations); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || investigations != 1 {
		t.Fatalf("duplicates runs=%d investigations=%d", runs, investigations)
	}
	claim, err := s.Store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET run_id='',status='running' WHERE id=$1", c.ID); err != nil {
		t.Fatal(err)
	}
	req := inference.Request{ModelID: "fake"}
	if err = s.PrepareRun(scoped, claim, &req); err != nil {
		t.Fatal("enqueue link gap", err)
	}
	if req.MaxCalls != 4 || req.MaxToolCalls != 8 || req.TimeoutSeconds != 120 || req.Deadline.IsZero() {
		t.Fatalf("missing run limits %+v", req)
	}
	claim.ActorID = "admin"
	if err = s.PrepareRun(scoped, claim, &req); err == nil {
		t.Fatal("mismatched automation actor accepted")
	}
	claim.ActorID = "op"
	claim.Run.Attempt = 2
	calls := 0
	retry := inference.Request{ModelID: "fake", Tools: []inference.EvidenceTool{{Name: "query_metrics", Read: func(context.Context, string) (inference.Evidence, error) { calls++; return inference.Evidence{}, nil }}}}
	if err = s.PrepareRun(scoped, claim, &retry); !errors.Is(err, ErrAutomationInterrupted) {
		t.Fatalf("automatic retry not blocked: %v", err)
	}
	if calls != 0 || retry.MaxCalls != 0 || retry.MaxToolCalls != 0 || !retry.Deadline.IsZero() {
		t.Fatal("retry received a fresh execution budget")
	}
	var state, message string
	if err = s.DB.QueryRow(ctx, "SELECT status,error FROM ops_automation_runs WHERE id=$1", c.ID).Scan(&state, &message); err != nil || state != "indeterminate" || message != ErrAutomationInterrupted.Error() {
		t.Fatalf("interrupted state %s %q %v", state, message, err)
	}
	manual := store.Claim{Run: core.Run{ID: "not-an-automatic-run", Attempt: 2}}
	if err = s.PrepareRun(scoped, manual, &inference.Request{}); err != nil {
		t.Fatal("manual retry changed", err)
	}
}

func freshPatrolEvidence(kind string, change func(map[string]any)) inference.Evidence {
	now := time.Now().UTC().Truncate(time.Second)
	source := map[string]any{"http_status": 200, "captured_at": now, "window": map[string]any{"start": now.Add(-15 * time.Minute), "end": now, "step_seconds": 15}}
	data := map[string]any{"empty": false, "source": source}
	if kind == "metrics" {
		data["series"] = []any{map[string]any{"samples": []any{map[string]any{"timestamp": float64(now.Unix() - 15), "value": "0"}}}}
	} else {
		data["entries"] = []any{map[string]any{"timestamp": now.Add(-time.Minute), "line": "request completed"}}
	}
	if change != nil {
		change(data)
	}
	raw, _ := json.Marshal(data)
	return inference.Evidence{Kind: kind, Content: string(raw)}
}
func TestPatrolMixedMissingStaleAndIncompleteData(t *testing.T) {
	log := freshPatrolEvidence("logs", nil)
	changes := []struct {
		name   string
		change func(map[string]any)
	}{
		{"empty_metrics", func(d map[string]any) { d["series"] = []any{}; d["empty"] = true }},
		{"nan_metrics", func(d map[string]any) {
			d["series"].([]any)[0].(map[string]any)["samples"].([]any)[0].(map[string]any)["value"] = "NaN"
		}},
		{"missing_sample_time", func(d map[string]any) {
			delete(d["series"].([]any)[0].(map[string]any)["samples"].([]any)[0].(map[string]any), "timestamp")
		}},
		{"stale_sample", func(d map[string]any) {
			d["series"].([]any)[0].(map[string]any)["samples"].([]any)[0].(map[string]any)["timestamp"] = float64(time.Now().Add(-12 * time.Minute).Unix())
		}},
		{"partial_series", func(d map[string]any) { d["series"] = append(d["series"].([]any), map[string]any{"samples": []any{}}) }},
		{"missing_capture", func(d map[string]any) { delete(d["source"].(map[string]any), "captured_at") }},
		{"stale_capture", func(d map[string]any) { d["source"].(map[string]any)["captured_at"] = time.Now().Add(-time.Hour) }},
		{"stale_window", func(d map[string]any) {
			d["source"].(map[string]any)["window"].(map[string]any)["end"] = time.Now().Add(-10 * time.Minute)
		}},
		{"missing_window", func(d map[string]any) { delete(d["source"].(map[string]any), "window") }},
		{"future_capture", func(d map[string]any) { d["source"].(map[string]any)["captured_at"] = time.Now().Add(time.Hour) }},
		{"partial_response", func(d map[string]any) { d["limit_reached"] = true }},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			bad := freshPatrolEvidence("metrics", tc.change)
			for _, state := range []string{"clear", "suspected"} {
				r := parsePatrol(`{"state":"`+state+`"}`, []inference.Evidence{bad, log}, false)
				if r.State != "indeterminate" {
					t.Fatalf("mixed incomplete data became %s", r.State)
				}
			}
		})
	}
	for _, kind := range []string{"metrics", "logs"} {
		ev := freshPatrolEvidence(kind, nil)
		if !liveTelemetry(ev) {
			t.Fatalf("fresh %s rejected", kind)
		}
	}
	missingLog := freshPatrolEvidence("logs", func(d map[string]any) { delete(d["entries"].([]any)[0].(map[string]any), "timestamp") })
	if parsePatrol(`{"state":"clear"}`, []inference.Evidence{freshPatrolEvidence("metrics", nil), missingLog}, false).State != "indeterminate" {
		t.Fatal("missing log timestamp repaired by metrics")
	}
	if parsePatrol(`{"state":"clear"}`, []inference.Evidence{freshPatrolEvidence("metrics", nil), log, {Kind: "runbook", Content: `{"items":[]}`}}, false).State != "clear" {
		t.Fatal("empty knowledge incorrectly invalidated live telemetry")
	}
}
