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

type pauseAfterEvalDetailAuthorization struct {
	policy     *rbac.V3Policy
	authorized chan<- struct{}
	resume     <-chan struct{}
}

func (a *pauseAfterEvalDetailAuthorization) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	return a.policy.Authorize(ctx, user, decision)
}

func (a *pauseAfterEvalDetailAuthorization) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, decision rbac.V3Decision) error {
	if err := a.policy.AuthorizeWithTx(ctx, tx, user, decision); err != nil {
		return err
	}
	if decision.Module == "agents" && decision.Action == "view" {
		a.authorized <- struct{}{}
		<-a.resume
	}
	return nil
}

func TestEvalDetailPersistedReadAndRevocationHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses one isolated disposable schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_eval_detail_test_" + store.ID()
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
		if _, dropErr := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); dropErr != nil {
			t.Error(dropErr)
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
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES
 ('eval-app','Eval App','eval-app','dev'),('other-app','Other App','other-app','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('eval-app-dev','eval-app','dev','Dev'),('other-app-dev','other-app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('eval-app','dev'),('other-app','dev')`,
		`INSERT INTO rbac_roles(id,name) VALUES('eval-reader','Eval Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('eval-reader','agents','view','application','eval-app')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Eval Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'eval-reader')`, reader.ID); err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err = db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(db, policy)
	for _, version := range []string{"v1", "v2"} {
		if err = svc.RegisterCatalogAgent(ctx, "eval-agent", "eval-app", "dev", "Eval Agent", version, "git:"+version, adminID); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.RegisterCatalogAgent(ctx, "hidden-agent", "other-app", "dev", "Hidden", "v1", "git:hidden", adminID); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO v3_agent_eval_experiments(id,agent_id,title,baseline_version,candidate_version,created_by)
 VALUES('visible-eval','eval-agent','Release comparison','v1','v2',$1)`,
		`INSERT INTO v3_agent_eval_experiments(id,agent_id,title,baseline_version,candidate_version,created_by)
 VALUES('hidden-eval','hidden-agent','Secret comparison','v1','v1',$1)`,
		`INSERT INTO v3_agent_eval_experiments(id,agent_id,title,baseline_version,candidate_version,created_by)
 VALUES('unaudited-eval','eval-agent','Unproven comparison','v1','v2',$1)`,
		`INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES
 ($1,'user','eval-app','v3.agent.eval.created','visible-eval','recorded','{"environment_id":"dev"}'),
 ($1,'user','other-app','v3.agent.eval.created','hidden-eval','recorded','{"environment_id":"dev"}')`,
	} {
		if _, err = db.Exec(ctx, sql, adminID); err != nil {
			t.Fatal(err)
		}
	}
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	get := func(id, env string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/evals/"+id, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	wantNotFound := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Fatalf("status=%d body=%s, want hidden 404", w.Code, w.Body.String())
		}
	}
	got := get("visible-eval", "dev")
	if got.Code != 200 {
		t.Fatalf("pending detail status=%d body=%s", got.Code, got.Body.String())
	}
	var detail map[string]any
	if err = json.Unmarshal(got.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{
		"id": "visible-eval", "title": "Release comparison", "status": "pending",
		"baseline_version": "v1", "candidate_version": "v2", "partial": true,
	} {
		if detail[key] != value {
			t.Fatalf("pending detail %s=%v, want %v: %s", key, detail[key], value, got.Body.String())
		}
	}
	if detail["metrics"] != nil || detail["radar"] != nil {
		t.Fatalf("pending detail fabricated results: %s", got.Body.String())
	}
	if missing, ok := detail["missing"].([]any); !ok || len(missing) == 0 {
		t.Fatalf("pending detail lacks explicit missing fields: %s", got.Body.String())
	}
	wantNotFound(t, get("absent-eval", "dev"))
	wantNotFound(t, get("hidden-eval", "dev"))
	wantNotFound(t, get("unaudited-eval", "dev"))
	if got := get("visible-eval", "all"); got.Code != 200 {
		t.Fatalf("all-selected readable detail status=%d body=%s", got.Code, got.Body.String())
	}

	// Every lookup must use the current persisted target and grants.
	cases := []struct {
		name, change, restore string
		status                int
	}{
		{"grant revoked", `DELETE FROM ` + quoted + `.v3_role_grants WHERE role_id='eval-reader' AND module='agents' AND action='view'`,
			`INSERT INTO ` + quoted + `.v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('eval-reader','agents','view','application','eval-app')`, 404},
		{"agent reassigned", `UPDATE ` + quoted + `.v3_agents SET application_id='other-app' WHERE id='eval-agent'`,
			`UPDATE ` + quoted + `.v3_agents SET application_id='eval-app' WHERE id='eval-agent'`, 404},
		{"application archived", `UPDATE ` + quoted + `.applications SET archived=true WHERE id='eval-app'`,
			`UPDATE ` + quoted + `.applications SET archived=false WHERE id='eval-app'`, 404},
		{"environment archived", `UPDATE ` + quoted + `.application_environments SET archived=true WHERE id='eval-app-dev'`,
			`UPDATE ` + quoted + `.application_environments SET archived=false WHERE id='eval-app-dev'`, 404},
		{"account disabled", `UPDATE ` + quoted + `.users SET disabled=true WHERE id='` + reader.ID + `'`,
			`UPDATE ` + quoted + `.users SET disabled=false WHERE id='` + reader.ID + `'`, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, changeErr := adminDB.Exec(ctx, tc.change); changeErr != nil {
				t.Fatal(changeErr)
			}
			got := get("visible-eval", "dev")
			if tc.status == 404 {
				wantNotFound(t, got)
			} else if got.Code != tc.status || !strings.Contains(got.Body.String(), `"code":"unauthorized"`) {
				t.Fatalf("status=%d body=%s, want disabled-session %d", got.Code, got.Body.String(), tc.status)
			}
			if _, restoreErr := adminDB.Exec(ctx, tc.restore); restoreErr != nil {
				t.Fatal(restoreErr)
			}
		})
	}
	// A new application's grant cannot transfer an earlier application's
	// evaluation history with the Agent's current catalog ownership.
	if _, err = adminDB.Exec(ctx, `INSERT INTO `+quoted+`.v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('eval-reader','agents','view','application','other-app')`); err != nil {
		t.Fatal(err)
	}
	if _, err = adminDB.Exec(ctx, `UPDATE `+quoted+`.v3_agents SET application_id='other-app' WHERE id='eval-agent'`); err != nil {
		t.Fatal(err)
	}
	wantNotFound(t, get("visible-eval", "dev"))
	if _, err = adminDB.Exec(ctx, `UPDATE `+quoted+`.v3_agents SET application_id='eval-app' WHERE id='eval-agent'`); err != nil {
		t.Fatal(err)
	}
	if _, err = adminDB.Exec(ctx, `DELETE FROM `+quoted+`.v3_role_grants
 WHERE role_id='eval-reader' AND module='agents' AND action='view' AND scope_id='other-app'`); err != nil {
		t.Fatal(err)
	}
	// The grant source lock must serialize a revoke attempted after the final
	// authorization, so it cannot overtake a detail response already authorized.
	gotAuthorization := make(chan struct{}, 1)
	resume := make(chan struct{})
	svc.Access = &pauseAfterEvalDetailAuthorization{policy: policy, authorized: gotAuthorization, resume: resume}
	readCh := make(chan *httptest.ResponseRecorder, 1)
	go func() { readCh <- get("visible-eval", "dev") }()
	select {
	case <-gotAuthorization:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("detail did not reach transactional authorization barrier")
	}
	revokeConn, err := adminDB.Acquire(ctx)
	if err != nil {
		close(resume)
		t.Fatal(err)
	}
	defer revokeConn.Release()
	var revokePID int
	if err = revokeConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&revokePID); err != nil {
		close(resume)
		t.Fatal(err)
	}
	revokeCh := make(chan error, 1)
	go func() {
		_, revokeErr := revokeConn.Exec(ctx, `DELETE FROM `+quoted+`.v3_role_grants
 WHERE role_id='eval-reader' AND module='agents' AND action='view'`)
		revokeCh <- revokeErr
	}()
	revokeOvertook, revokeBlocked := false, false
	var earlyRevokeErr error
	deadline := time.Now().Add(5 * time.Second)
	for !revokeOvertook && !revokeBlocked && time.Now().Before(deadline) {
		select {
		case earlyRevokeErr = <-revokeCh:
			revokeOvertook = true
		default:
			if err = adminDB.QueryRow(ctx, `SELECT COALESCE(wait_event_type='Lock',false)
 FROM pg_stat_activity WHERE pid=$1`, revokePID).Scan(&revokeBlocked); err != nil {
				close(resume)
				t.Fatal(err)
			}
			if !revokeBlocked {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	close(resume)
	select {
	case got = <-readCh:
		if got.Code != 200 {
			t.Fatalf("authorized detail status=%d body=%s", got.Code, got.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detail did not finish after authorization barrier opened")
	}
	svc.Access = policy
	if revokeOvertook {
		if earlyRevokeErr != nil {
			t.Fatal(earlyRevokeErr)
		}
	} else {
		select {
		case revokeErr := <-revokeCh:
			if revokeErr != nil {
				t.Fatal(revokeErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("grant revoke did not finish after detail read")
		}
	}
	if revokeOvertook || !revokeBlocked {
		t.Fatalf("grant revoke overtook or never waited on authorized read: overtook=%v blocked=%v", revokeOvertook, revokeBlocked)
	}
	wantNotFound(t, get("visible-eval", "dev"))
	if _, err = adminDB.Exec(ctx, `INSERT INTO `+quoted+`.v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('eval-reader','agents','view','application','eval-app')`); err != nil {
		t.Fatal(err)
	}
	if got = get("visible-eval", "dev"); got.Code != 200 {
		t.Fatalf("detail unavailable after restoring grants and scope: %d %s", got.Code, got.Body.String())
	}
	// A trusted producer can later persist a result. Detail must expose only
	// that persisted metric payload and must still mark absent rich DTO fields.
	if err = svc.RecordEvaluationOutcome(ctx, EvaluationOutcome{
		ID: "visible-eval", Status: "passed", ResultRef: "git:eval-proof",
		Metrics: map[string]float64{"score": 0.9},
	}); err != nil {
		t.Fatal(err)
	}
	got = get("visible-eval", "dev")
	if got.Code != 200 {
		t.Fatalf("completed detail status=%d body=%s", got.Code, got.Body.String())
	}
	if err = json.Unmarshal(got.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	metrics, ok := detail["metrics"].(map[string]any)
	if !ok || metrics["score"] != 0.9 || detail["status"] != "passed" || detail["radar"] != nil || detail["partial"] != true {
		t.Fatalf("completed detail did not preserve exact known result and limits: %s", got.Body.String())
	}
}
