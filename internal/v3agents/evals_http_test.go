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

// pauseAfterEvalAuthorization holds a request after its transactional policy
// check so the test can attempt a concurrent grant revoke before commit.
type pauseAfterEvalAuthorization struct {
	policy     *rbac.V3Policy
	authorized chan<- struct{}
	resume     <-chan struct{}
}

func (a *pauseAfterEvalAuthorization) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	return a.policy.Authorize(ctx, user, decision)
}

func (a *pauseAfterEvalAuthorization) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, decision rbac.V3Decision) error {
	if err := a.policy.AuthorizeWithTx(ctx, tx, user, decision); err != nil {
		return err
	}
	if decision.Module == "agents" && decision.Action == "change" {
		a.authorized <- struct{}{}
		<-a.resume
	}
	return nil
}

func TestCreateEvalPendingHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses one isolated disposable schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_eval_http_test_" + store.ID()
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
		`INSERT INTO rbac_roles(id,name) VALUES('eval-editor','Eval Editor')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('eval-editor','agents','view','application','eval-app'),
 ('eval-editor','agents','change','application','eval-app')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "editor@example.invalid", Name: "Eval Editor", Role: "viewer"}, "isolated-editor-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'eval-editor')`, viewer.ID); err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err = db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(db, policy)
	for _, v := range []string{"v1", "v2"} {
		if err = svc.RegisterCatalogAgent(ctx, "eval-agent", "eval-app", "dev", "Eval Agent", v, "git:"+v, adminID); err != nil {
			t.Fatal(err)
		}
	}
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "editor@example.invalid", "isolated-editor-password")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"agent_id":"eval-agent","title":"Release comparison","baseline_version":"v1","candidate_version":"v2"}`
	request := func(env, payload string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/evals", strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertResponse := func(w *httptest.ResponseRecorder, status int, marker string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), marker) {
			t.Fatalf("status=%d body=%s, want %d and %s", w.Code, w.Body.String(), status, marker)
		}
	}
	counts := func() (int, int) {
		t.Helper()
		var evals, audits int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_agent_eval_experiments`).Scan(&evals); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit
 WHERE action='v3.agent.eval.created'`).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		return evals, audits
	}
	assertNoWrite := func(w *httptest.ResponseRecorder, status int, marker string, want int) {
		t.Helper()
		assertResponse(w, status, marker)
		if evals, audits := counts(); evals != want || audits != want {
			t.Fatalf("denied eval persisted or audited: evals=%d audits=%d, want %d each", evals, audits, want)
		}
	}
	assertNoWrite(request("all", body), 409, `"code":"env_view_only"`, 0)
	assertNoWrite(request("dev", `{"agent_id":"eval-agent","title":"Same","baseline_version":"v1","candidate_version":"v1"}`),
		422, `"code":"invalid_agent_request"`, 0)
	assertNoWrite(request("dev", `{"agent_id":"eval-agent","title":"Unknown","baseline_version":"v1","candidate_version":"v3"}`),
		404, `"code":"not_found"`, 0)
	assertNoWrite(request("dev", `{"agent_id":"missing","title":"Unknown","baseline_version":"v1","candidate_version":"v2"}`),
		404, `"code":"not_found"`, 0)

	svc.Access = &revokingPromptAccess{policy: policy, afterAuthorize: func() error {
		_, changeErr := adminDB.Exec(ctx, `UPDATE `+quoted+`.v3_agents SET application_id='other-app' WHERE id='eval-agent'`)
		return changeErr
	}}
	assertNoWrite(request("dev", body), 404, `"code":"not_found"`, 0)
	svc.Access = policy
	if _, err = adminDB.Exec(ctx, `UPDATE `+quoted+`.v3_agents SET application_id='eval-app' WHERE id='eval-agent'`); err != nil {
		t.Fatal(err)
	}

	svc.Access = &revokingPromptAccess{policy: policy, afterAuthorize: func() error {
		_, archiveErr := adminDB.Exec(ctx, `UPDATE `+quoted+`.applications SET archived=true WHERE id='eval-app'`)
		return archiveErr
	}}
	assertNoWrite(request("dev", body), 404, `"code":"not_found"`, 0)
	svc.Access = policy
	if _, err = adminDB.Exec(ctx, `UPDATE `+quoted+`.applications SET archived=false WHERE id='eval-app'`); err != nil {
		t.Fatal(err)
	}

	svc.Access = &revokingPromptAccess{policy: policy, afterAuthorize: func() error {
		_, archiveErr := adminDB.Exec(ctx, `UPDATE `+quoted+`.application_environments SET archived=true WHERE id='eval-app-dev'`)
		return archiveErr
	}}
	assertNoWrite(request("dev", body), 404, `"code":"not_found"`, 0)
	svc.Access = policy
	if _, err = adminDB.Exec(ctx, `UPDATE `+quoted+`.application_environments SET archived=false WHERE id='eval-app-dev'`); err != nil {
		t.Fatal(err)
	}

	svc.Access = &revokingPromptAccess{policy: policy, afterAuthorize: func() error {
		_, disableErr := adminDB.Exec(ctx, `UPDATE `+quoted+`.users SET disabled=true WHERE id=$1`, viewer.ID)
		return disableErr
	}}
	assertNoWrite(request("dev", body), 403, `"code":"forbidden"`, 0)
	svc.Access = policy
	if _, err = adminDB.Exec(ctx, `UPDATE `+quoted+`.users SET disabled=false WHERE id=$1`, viewer.ID); err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec(ctx, `CREATE FUNCTION reject_eval_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.agent.eval.created' THEN RAISE EXCEPTION 'eval audit unavailable'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER reject_eval_audit BEFORE INSERT ON management_audit
 FOR EACH ROW EXECUTE FUNCTION reject_eval_audit()`); err != nil {
		t.Fatal(err)
	}
	assertNoWrite(request("dev", body), 503, `"code":"agent_console_unavailable"`, 0)
	if _, err = db.Exec(ctx, `DROP TRIGGER reject_eval_audit ON management_audit; DROP FUNCTION reject_eval_audit()`); err != nil {
		t.Fatal(err)
	}

	created := request("dev", body)
	assertResponse(created, 201, `"execution_available":false`)
	var result map[string]any
	if err = json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	id, idOK := result["id"].(string)
	if !idOK || id == "" || len(result) != 3 || result["status"] != "pending" || result["execution_available"] != false {
		t.Fatalf("unexpected pending-only response: %s", created.Body.String())
	}
	var agentID, title, baseline, candidate, status, createdBy, auditedApp, auditedActor, auditedTarget string
	var metrics []byte
	if err = db.QueryRow(ctx, `SELECT agent_id,title,baseline_version,candidate_version,status,created_by,metrics
 FROM v3_agent_eval_experiments WHERE id=$1`, id).Scan(&agentID, &title, &baseline, &candidate, &status, &createdBy, &metrics); err != nil {
		t.Fatal(err)
	}
	if agentID != "eval-agent" || title != "Release comparison" || baseline != "v1" || candidate != "v2" ||
		status != "pending" || createdBy != viewer.ID || metrics != nil {
		t.Fatalf("unexpected persisted eval: agent=%s title=%s baseline=%s candidate=%s status=%s creator=%s metrics=%s",
			agentID, title, baseline, candidate, status, createdBy, metrics)
	}
	if err = db.QueryRow(ctx, `SELECT application_id,actor_id,target FROM management_audit
 WHERE action='v3.agent.eval.created'`).Scan(&auditedApp, &auditedActor, &auditedTarget); err != nil {
		t.Fatal(err)
	}
	if auditedApp != "eval-app" || auditedActor != viewer.ID || auditedTarget != id {
		t.Fatalf("eval audit scope/target mismatch: app=%s actor=%s target=%s", auditedApp, auditedActor, auditedTarget)
	}
	if evals, audits := counts(); evals != 1 || audits != 1 {
		t.Fatalf("successful eval not atomically persisted and audited: evals=%d audits=%d", evals, audits)
	}
	readRequest := httptest.NewRequest(http.MethodGet, "/api/v1/evals/"+id, nil)
	readRequest.Header.Set("X-AIOps-Environment", "dev")
	readRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	readResult := httptest.NewRecorder()
	api.ServeHTTP(readResult, readRequest)
	assertResponse(readResult, 200, `"status":"pending"`)
	var pendingDetail map[string]any
	if err = json.Unmarshal(readResult.Body.Bytes(), &pendingDetail); err != nil {
		t.Fatal(err)
	}
	if pendingDetail["id"] != id || pendingDetail["partial"] != true ||
		pendingDetail["metrics"] != nil || pendingDetail["radar"] != nil {
		t.Fatalf("created eval did not read back as partial pending metadata: %s", readResult.Body.String())
	}

	svc.Access = &revokingPromptAccess{policy: policy, afterAuthorize: func() error {
		_, revokeErr := adminDB.Exec(ctx, `DELETE FROM `+quoted+`.v3_role_grants
 WHERE role_id='eval-editor' AND module='agents' AND action='change'`)
		return revokeErr
	}}
	assertNoWrite(request("dev", body), 403, `"code":"forbidden"`, 1)
	svc.Access = policy
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('eval-editor','agents','change','application','eval-app')`); err != nil {
		t.Fatal(err)
	}
	gotAuthorization := make(chan struct{}, 1)
	resume := make(chan struct{})
	svc.Access = &pauseAfterEvalAuthorization{policy: policy, authorized: gotAuthorization, resume: resume}
	createdCh := make(chan *httptest.ResponseRecorder, 1)
	go func() { createdCh <- request("dev", body) }()
	select {
	case <-gotAuthorization:
	case <-time.After(5 * time.Second):
		close(resume)
		t.Fatal("eval did not reach the transactional authorization barrier")
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
 WHERE role_id='eval-editor' AND module='agents' AND action='change'`)
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
	case created = <-createdCh:
		assertResponse(created, 201, `"execution_available":false`)
	case <-time.After(5 * time.Second):
		t.Fatal("eval did not finish after the authorization barrier opened")
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
			t.Fatal("grant revoke did not finish after eval commit")
		}
	}
	if revokeOvertook {
		t.Fatal("grant revoke completed before the authorized eval committed")
	}
	if !revokeBlocked {
		t.Fatal("grant revoke never waited on the authorized eval transaction")
	}
	if evals, audits := counts(); evals != 2 || audits != 2 {
		t.Fatalf("concurrent eval/revoke persistence mismatch: evals=%d audits=%d", evals, audits)
	}
	assertNoWrite(request("dev", body), 403, `"code":"forbidden"`, 2)
}
