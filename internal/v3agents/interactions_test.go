package v3agents

import (
	"context"
	"encoding/json"
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

func TestInteractionFilters(t *testing.T) {
	for _, q := range []string{"range=bogus", "range=1h&range=7d", "agent=", "trigger=automatic", "version=%20v1"} {
		_, _, ok := runQuery(httptest.NewRequest("GET", "/?"+q, nil))
		if ok {
			t.Fatalf("accepted %s", q)
		}
	}
	f, d, ok := runQuery(httptest.NewRequest("GET", "/?agent=a&agent=b&version=v1&trigger=manual&range=7d", nil))
	if !ok || len(f["agent"]) != 2 || d != 7*24*time.Hour {
		t.Fatal("multi-valued query lost")
	}
	f, _, ok = runQuery(httptest.NewRequest("GET", "/", nil))
	if !ok || f["agent"] == nil {
		t.Fatal("absent facets must bind an empty PG array, not NULL")
	}
}
func TestAgentInteractionsHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated disposable schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_interactions_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 2
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
	if err = st.Bootstrap(ctx, "admin@example.invalid", "test-interaction-password"); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err = db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, store.Hash("interaction-token"), userID)
	for _, sql := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev'),('hidden','Hidden','hidden','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev'),('hidden-dev','hidden','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('hidden','dev')`,
		`INSERT INTO v3_agents(id,application_id,environment_id,name) VALUES('agent','app','dev','Agent'),('hidden-agent','hidden','dev','Hidden')`,
		`INSERT INTO v3_agent_versions(agent_id,version,artifact_ref,created_by) VALUES('agent','v1','git:one','system'),('agent','v2','git:two','system'),('hidden-agent','v1','git:three','system')`,
		`INSERT INTO v3_agent_runs(id,agent_id,version,application_id,environment_id,trigger_kind,status,started_at,finished_at) VALUES('run','agent','v1','app','dev','manual','success',now()-interval '1 minute',now()),('old-run','agent','v2','app','dev','api','success',now()-interval '2 day',now()-interval '2 day'),('hidden-run','hidden-agent','v1','hidden','dev','manual','success',now()-interval '1 minute',now())`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method) VALUES('connector','dev','prometheus','Prometheus','metrics','http://example.invalid','none')`,
	} {
		exec(sql)
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(db, policy)
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "stage"}}, policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, env string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "interaction-token"})
		r.Header.Set("X-AIOps-Environment", env)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	want := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d body=%s expected=%d", w.Code, w.Body.String(), status)
		}
	}
	want(call("POST", "/api/v1/agent-tools", `{"name":"Read metrics","kind":"read","connector_id":"connector"}`, "all"), 409)
	w := call("POST", "/api/v1/agent-tools", `{"name":"Read metrics","kind":"read","connector_id":"connector"}`, "dev")
	want(w, 201)
	var grants int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_tools`).Scan(&grants); err != nil || grants != 0 {
		t.Fatal("catalog created Agent grant", err, grants)
	}
	w = call("GET", "/api/v1/agent-tools?connector=connector", "", "dev")
	want(w, 200)
	if !strings.Contains(w.Body.String(), `"catalog_only":true`) {
		t.Fatal(w.Body.String())
	}
	w = call("GET", "/api/v1/agent-tools?agent=agent", "", "dev")
	want(w, 200)
	if strings.Contains(w.Body.String(), `"catalog_only":true`) {
		t.Fatal("ungranted catalog survived Agent facet")
	}
	w = call("POST", "/api/v1/eval-datasets", `{"name":"Cases","source":"runs"}`, "dev")
	want(w, 201)
	var created map[string]string
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"]
	for i := 0; i < 2; i++ {
		w = call("POST", "/api/v1/eval-datasets/"+id+"/items", `{"run_id":"run"}`, "dev")
		want(w, 200)
		if !strings.Contains(w.Body.String(), `"count":1`) {
			t.Fatal(w.Body.String())
		}
	}
	want(call("POST", "/api/v1/eval-datasets/"+id+"/items", `{"run_id":"hidden-run"}`, "dev"), 200)
	w = call("GET", "/api/v1/eval-datasets?agent_id=agent", "", "dev")
	want(w, 200)
	if !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatal(w.Body.String())
	}
	w = call("POST", "/api/v1/agent-runs/run/annotations", `{"score":4,"note":"human observation"}`, "dev")
	want(w, 201)
	want(call("POST", "/api/v1/agent-runs/run/annotations", `{"score":6}`, "dev"), 422)
	w = call("GET", "/api/v1/agent-runs?range=1h&agent=agent&version=v1&trigger=manual", "", "dev")
	want(w, 200)
	if !strings.Contains(w.Body.String(), `"id":"run"`) || strings.Contains(w.Body.String(), `"id":"old-run"`) || strings.Contains(w.Body.String(), `"id":"hidden-run"`) {
		t.Fatal(w.Body.String())
	}
	w = call("POST", "/api/v1/prompts", `{"agent_id":"agent","name":"Prompt","text":"first"}`, "dev")
	want(w, 201)
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	promptID := created["id"]
	want(call("POST", "/api/v1/prompts/"+promptID+"/versions", `{"text":"second"}`, "dev"), 201)
	w = call("GET", "/api/v1/prompts/"+promptID+"/versions", "", "dev")
	want(w, 200)
	if !strings.Contains(w.Body.String(), `"version":"v1"`) || !strings.Contains(w.Body.String(), `"version":"v2"`) {
		t.Fatal(w.Body.String())
	}
	want(call("POST", "/api/v1/eval-datasets/"+id+"/items", `{"run_id":"run"}`, "stage"), 404)
	editor, err := st.CreateUser(ctx, core.User{Email: "scoped@example.invalid", Name: "Scoped", Role: "viewer"}, "test-scoped-password")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO rbac_roles(id,name) VALUES('interaction-editor','Interaction Editor')`)
	exec(`INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'interaction-editor')`, editor.ID)
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('interaction-editor','environment','view','environment','dev'),('interaction-editor','agents','change','environment','dev'),('interaction-editor','agents','view','application','app')`)
	exec(`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, store.Hash("scoped-token"), editor.ID)
	scopedCall := func(runID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/eval-datasets/"+id+"/items", strings.NewReader(`{"run_id":"`+runID+`"}`))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "scoped-token"})
		r.Header.Set("X-AIOps-Environment", "dev")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	want(scopedCall("hidden-run"), 404)
	w = scopedCall("run")
	want(w, 200)
	if !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatal("dataset count leaked hidden run", w.Body.String())
	}
	var failedAudits int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE outcome='failed' AND action='v3.agent.run.annotated'`).Scan(&failedAudits); err != nil || failedAudits < 1 {
		t.Fatal("failure audit missing", err, failedAudits)
	}
	exec(`ALTER TABLE management_audit RENAME TO unavailable_audit`)
	want(call("POST", "/api/v1/eval-datasets", `{"name":"must rollback","source":"manual"}`, "dev"), 503)
	var rolledBack int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_eval_datasets WHERE name='must rollback'`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatal("audit failure retained business write", err, rolledBack)
	}
	exec(`ALTER TABLE unavailable_audit RENAME TO management_audit`)

	exec(`UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, userID)
	want(call("POST", "/api/v1/agent-runs/run/annotations", `{"score":3}`, "dev"), 403)
}

func TestRunRangeTrendUsesSelectedWindow(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	done := now.Add(-time.Minute)
	rows := []runRecord{{Value: run{ID: "inside", Started: now.Add(-10 * time.Minute), Finished: &done, Status: "success"}}}
	view := buildRunsView(rows, map[string]*runFeedback{}, map[string]int64{}, "all", now, 15*time.Minute)
	trend, ok := view["trend"].(runTrend)
	if !ok {
		t.Fatalf("trend type %T", view["trend"])
	}
	if trend.IntervalMinutes != 1 || len(trend.OK) != 15 || trend.WindowStartAt != runTimeString(now.Add(-15*time.Minute)) || trend.OK[5] != 1 {
		t.Fatalf("selected range lost: %+v", trend)
	}
}
