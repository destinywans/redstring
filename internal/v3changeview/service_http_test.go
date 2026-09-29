package v3changeview

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/delivery"
	"github.com/wans/aiops-canvas/internal/deployment"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type mutatingReviewSource struct {
	AuditReviewSource
	db             *pgxpool.Pool
	mutate         bool
	revokeOnSecond bool
	reads          int
}

func (m *mutatingReviewSource) Read(ctx context.Context, app, env, run, digest string) (ReviewEvidence, error) {
	evidence, err := m.AuditReviewSource.Read(ctx, app, env, run, digest)
	m.reads++
	if err == nil && m.mutate {
		m.mutate = false
		_, err = m.db.Exec(ctx, `UPDATE delivery_runs SET status='failed',updated_at=now() WHERE id=$1`, run)
	}
	if err == nil && m.revokeOnSecond && m.reads == 2 {
		_, err = m.db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='events' AND action='view'`)
	}
	return evidence, err
}

func TestChangesViewHTTPSourceBackedAndFailClosed(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_change_view_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	for _, migrate := range []func(context.Context) error{
		st.Migrate,
		(&delivery.PostgresRepository{DB: db}).Migrate,
		(&deployment.Service{DB: db}).Migrate,
		(&operations.Service{DB: db}).Migrate,
	} {
		if err = migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err = db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','test'),
 ('viewer','services','view','application','app'),
 ('viewer','events','view','application','app')`,
		`INSERT INTO applications(id,name,key,environment,archived) VALUES('app','Checkout API','checkout','test',false)`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-test','app','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','test')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	run := nativeRun()
	// The release gate refers to an actual immutable approval -> deploy path.
	run.Snapshot.Definition.Nodes[0].Outputs = []delivery.Output{
		{Name: "commit-sha", Path: "/workspace/commit-sha"},
		{Name: "artifact-digest", Path: "/workspace/artifact-digest"},
		{Name: "artifact-url", Path: "/workspace/artifact-url"},
	}
	run.Snapshot.Definition.Nodes = append(run.Snapshot.Definition.Nodes,
		delivery.Node{ID: "approval", Name: "Approve", Kind: "approval", SourceNodeID: "build", TargetID: "gitops-target", DependsOn: []delivery.Dependency{{NodeID: "build"}}},
		delivery.Node{ID: "deploy", Name: "Deploy", Kind: "deploy", ApprovalNodeID: "approval", DependsOn: []delivery.Dependency{{NodeID: "approval"}}},
	)
	run.Projection.Nodes = append(run.Projection.Nodes,
		delivery.NodeResult{ID: "pod-approval", Name: "aiops-run-1.approval", Status: "succeeded"},
		delivery.NodeResult{ID: "pod-deploy", Name: "aiops-run-1.deploy", Status: "succeeded"},
	)
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	commitSHA := strings.Repeat("b", 40)
	run.Projection.Nodes[0].Outputs = map[string]string{"artifact-digest": artifactDigest, "commit-sha": commitSHA}
	definitionJSON, _ := json.Marshal(run.Snapshot.Definition)
	sum := sha256.Sum256(definitionJSON)
	run.Snapshot.Digest = hex.EncodeToString(sum[:])
	now := time.Now().UTC().Truncate(time.Second)
	run.CreatedAt, run.UpdatedAt = now.Add(-12*time.Minute), now.Add(-10*time.Minute)
	run.Projection.ObservedAt = run.UpdatedAt
	run.ApplicationID, run.EnvironmentID, run.Snapshot.ApplicationID, run.Snapshot.EnvironmentID = "app", "app-test", "app", "app-test"
	run.CreatedBy = adminID
	snapshot, _ := json.Marshal(run.Snapshot)
	projection, _ := json.Marshal(run.Projection)
	for _, insert := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO delivery_pipelines(id,application_id,environment_id,version) VALUES($1,'app','app-test',2)`, []any{run.PipelineID}},
		{`INSERT INTO delivery_versions(pipeline_id,version,snapshot) VALUES($1,2,$2)`, []any{run.PipelineID, snapshot}},
		{`INSERT INTO delivery_runs(id,pipeline_id,version,application_id,environment_id,provider,status,external_id,snapshot,projection,created_by,idempotency_key,request_digest,created_at,updated_at)
 VALUES($1,$2,2,'app','app-test','native','succeeded',$3,$4,$5,$6,'isolated-key','digest',$7,$8)`, []any{run.ID, run.PipelineID, run.ExternalID, snapshot, projection, adminID, run.CreatedAt, run.UpdatedAt}},
		{`INSERT INTO delivery_gates(run_id,node_id,plan_id,execution_id) VALUES($1,'deploy','plan-1','execution-1')`, []any{run.ID}},
	} {
		if _, err = db.Exec(ctx, insert.sql, insert.args...); err != nil {
			t.Fatal(err)
		}
	}
	deployedAt := now.Add(-9 * time.Minute)
	alertAt := deployedAt.Add(time.Minute)
	source := map[string]string{"run_id": run.ID, "node_id": run.Projection.Nodes[0].Name,
		"pipeline_digest": run.Snapshot.Digest, "artifact_digest": artifactDigest, "commit_sha": commitSHA,
		"application_id": "app", "environment_id": "app-test"}
	planSnapshot, _ := json.Marshal(map[string]any{"target_kind": "gitops", "source": source})
	for _, insert := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO deployment_plans(id,application_id,environment_id,target_id,request_key,digest,snapshot,status,created_by)
 VALUES('plan-1','app','app-test','gitops-target','plan-key','plan-digest',$1,'approved',$2)`, []any{planSnapshot, adminID}},
		{`INSERT INTO deployment_executions(id,plan_id,status,created_by) VALUES('execution-1','plan-1','succeeded',$1)`, []any{adminID}},
		{`INSERT INTO deployment_events(execution_id,type,payload,created_at) VALUES('execution-1','succeeded','{}',$1)`, []any{deployedAt}},
		{`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at)
 VALUES('incident-1','alert-source','app','app-test','fp-1','Checkout alert','warning','open','firing',$1)`, []any{alertAt}},
	} {
		if _, err = db.Exec(ctx, insert.sql, insert.args...); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	reviews := &mutatingReviewSource{AuditReviewSource: AuditReviewSource{DB: db}, db: db}
	api := httpapi.New(st, nil, true)
	New(st, policy, reviews, func(app, env string, start, end time.Time) bool {
		return app == "app" && env == "test" && !start.Before(now.Add(-24*time.Hour)) && !end.After(now.Add(time.Minute))
	}).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))
	path := "/api/v1/changes?" + url.Values{"service_id": {serviceID}, "run_id": {run.ID}, "range": {"24h"}, "sources": {"native"}}.Encode()
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
	}
	assert(request(path), 503) // No review producer has published the detail assertions yet.
	var incidentUpdatedAt time.Time
	if err = db.QueryRow(ctx, `SELECT updated_at FROM ops_incidents WHERE id='incident-1'`).Scan(&incidentUpdatedAt); err != nil {
		t.Fatal(err)
	}
	diff := map[string]string{"path": "config/checkout.yaml", "minus": "- replicas: 2", "plus": "+ replicas: 3"}
	reviewPayload := map[string]any{
		"service_id": serviceID, "decision": "accept_for_display",
		"expected_run_digest": run.Snapshot.Digest, "expected_run_updated_at": run.UpdatedAt,
		"execution_id": "execution-1", "expected_plan_digest": "plan-digest",
		"expected_artifact_digest": artifactDigest, "expected_commit_sha": commitSHA,
		"incident_id": "incident-1", "expected_incident_updated_at": incidentUpdatedAt,
		"expected_diff_digest": reviewedDiffDigest(diff["path"], diff["minus"], diff["plus"]),
		"diff":                 diff,
		"risk":                 map[string]string{"title": "Reviewed deployment risk", "meta": "Human review", "text": "Potential capacity impact; causality not asserted."},
		"alert_text":           "Reviewed alert association; causality not asserted.",
	}
	writeForRun := func(targetRun string, payload map[string]any) *httptest.ResponseRecorder {
		raw, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/changes/"+targetRun+"/review", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://example.com")
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: adminToken})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	write := func(payload map[string]any) *httptest.ResponseRecorder { return writeForRun(run.ID, payload) }
	copyPayload := func() map[string]any {
		copy := map[string]any{}
		for key, value := range reviewPayload {
			copy[key] = value
		}
		return copy
	}
	bad := copyPayload()
	bad["service_id"] = base64.RawURLEncoding.EncodeToString([]byte("app\x00other"))
	assert(write(bad), 404)
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assert(write(reviewPayload), 404)
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','services','view','global','*')`); err != nil {
		t.Fatal(err)
	}
	bad = copyPayload()
	bad["expected_run_digest"] = strings.Repeat("c", 64)
	assert(write(bad), 409)
	bad = copyPayload()
	bad["expected_incident_updated_at"] = incidentUpdatedAt.Add(-time.Second)
	assert(write(bad), 409)
	bad = copyPayload()
	bad["expected_plan_digest"] = "stale-plan-digest"
	assert(write(bad), 409)
	bad = copyPayload()
	bad["expected_previous_review_id"] = "01"
	assert(write(bad), 422)
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	assert(write(reviewPayload), 403)
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','services','change','global','*')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, adminID); err != nil {
		t.Fatal(err)
	}
	assert(write(reviewPayload), 403)
	if _, err = db.Exec(ctx, `UPDATE users SET v3_identity_kind='human' WHERE id=$1`, adminID); err != nil {
		t.Fatal(err)
	}
	accepted := write(reviewPayload)
	assert(accepted, 201)
	var saved struct {
		ReviewID   string `json:"review_id"`
		RunID      string `json:"run_id"`
		ReviewedAt string `json:"reviewed_at"`
		Status     string `json:"status"`
	}
	if err = json.Unmarshal(accepted.Body.Bytes(), &saved); err != nil || saved.ReviewID == "" ||
		saved.RunID != run.ID || saved.Status != "accepted" || saved.ReviewedAt == "" {
		t.Fatalf("invalid accepted review response: %s err=%v", accepted.Body.String(), err)
	}
	retried := write(reviewPayload)
	assert(retried, 200)
	if retried.Body.String() != accepted.Body.String() {
		t.Fatalf("idempotent review changed: first=%s retry=%s", accepted.Body.String(), retried.Body.String())
	}
	bad = copyPayload()
	bad["risk"] = map[string]string{"title": "Changed", "meta": "Human review", "text": "Different claim"}
	assert(write(bad), 409)
	var acceptedCount int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit
 WHERE application_id='app' AND target=$1 AND action='change.review.accepted'`, run.ID).Scan(&acceptedCount); err != nil || acceptedCount != 1 {
		t.Fatalf("accepted review count=%d err=%v", acceptedCount, err)
	}
	w := request(path)
	assert(w, 200)
	var body struct {
		KPIs      []kpi      `json:"kpis"`
		Lanes     []lane     `json:"lanes"`
		Pipelines []pipeline `json:"pipelines"`
		Detail    detail     `json:"detail"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Pipelines) != 1 || body.Detail.ID != body.Pipelines[0].ID || body.Detail.ID != run.ID ||
		len(body.KPIs) != 4 || body.KPIs[0].Value != "1" || body.KPIs[3].Value != "1" ||
		len(body.Lanes) != 1 || len(body.Lanes[0].Marks) != 1 || body.Lanes[0].Marks[0].Tone != "ok" ||
		len(body.Lanes[0].Alerts) != 1 || body.Detail.Alert.Incident != "incident-1" ||
		body.Detail.Risk.Text != "Potential capacity impact; causality not asserted." {
		t.Fatalf("incorrect source projection: %s", w.Body.String())
	}
	// A deployment completed in this window still counts when its source run
	// started before the window. It must not invalidate the selected run.
	oldRun := run
	oldRun.ID, oldRun.ExternalID, oldRun.Projection.ExternalID = "run-old", "aiops-run-old", "aiops-run-old"
	oldRun.CreatedAt, oldRun.UpdatedAt = now.Add(-25*time.Hour), now.Add(-23*time.Hour)
	oldRun.Projection.ObservedAt = oldRun.UpdatedAt
	oldRun.Projection.Nodes = append([]delivery.NodeResult(nil), run.Projection.Nodes...)
	for i := range oldRun.Projection.Nodes {
		oldRun.Projection.Nodes[i].Name = strings.Replace(oldRun.Projection.Nodes[i].Name, "aiops-run-1", "aiops-run-old", 1)
	}
	oldSnapshot, _ := json.Marshal(oldRun.Snapshot)
	oldProjection, _ := json.Marshal(oldRun.Projection)
	oldSource := map[string]string{"run_id": oldRun.ID, "node_id": oldRun.Projection.Nodes[0].Name,
		"pipeline_digest": oldRun.Snapshot.Digest, "artifact_digest": artifactDigest, "commit_sha": commitSHA,
		"application_id": "app", "environment_id": "app-test"}
	oldPlan, _ := json.Marshal(map[string]any{"target_kind": "gitops", "source": oldSource})
	for _, insert := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO delivery_runs(id,pipeline_id,version,application_id,environment_id,provider,status,external_id,snapshot,projection,created_by,idempotency_key,request_digest,created_at,updated_at)
 VALUES($1,$2,2,'app','app-test','native','succeeded',$3,$4,$5,$6,'old-key','old-digest',$7,$8)`,
			[]any{oldRun.ID, oldRun.PipelineID, oldRun.ExternalID, oldSnapshot, oldProjection, adminID, oldRun.CreatedAt, oldRun.UpdatedAt}},
		{`INSERT INTO delivery_gates(run_id,node_id,plan_id,execution_id) VALUES($1,'deploy','plan-old','execution-old')`, []any{oldRun.ID}},
		{`INSERT INTO deployment_plans(id,application_id,environment_id,target_id,request_key,digest,snapshot,status,created_by)
 VALUES('plan-old','app','app-test','gitops-target','old-plan-key','old-plan-digest',$1,'approved',$2)`, []any{oldPlan, adminID}},
		{`INSERT INTO deployment_executions(id,plan_id,status,created_by) VALUES('execution-old','plan-old','succeeded',$1)`, []any{adminID}},
		{`INSERT INTO deployment_events(execution_id,type,payload,created_at) VALUES('execution-old','succeeded','{}',$1)`, []any{now.Add(-8 * time.Minute)}},
	} {
		if _, err = db.Exec(ctx, insert.sql, insert.args...); err != nil {
			t.Fatal(err)
		}
	}
	w = request(path)
	assert(w, 200)
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Pipelines) != 1 || body.Detail.ID != run.ID || body.KPIs[0].Value != "1" ||
		body.KPIs[3].Value != "2" || len(body.Lanes[0].Marks) != 2 {
		t.Fatalf("cross-window deployment was lost: %s", w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE ops_incidents SET summary='triaged',updated_at=now() WHERE id='incident-1'`); err != nil {
		t.Fatal(err)
	}
	assert(request(path), 503) // The immutable prior review is now stale.
	if err = db.QueryRow(ctx, `SELECT updated_at FROM ops_incidents WHERE id='incident-1'`).Scan(&incidentUpdatedAt); err != nil {
		t.Fatal(err)
	}
	reviewAgain := copyPayload()
	reviewAgain["expected_incident_updated_at"] = incidentUpdatedAt
	reviewAgain["expected_previous_review_id"] = saved.ReviewID
	reviewAgain["alert_text"] = "Reviewed updated incident association; causality not asserted."
	bad = map[string]any{}
	for key, value := range reviewAgain {
		bad[key] = value
	}
	bad["expected_previous_review_id"] = "999999"
	assert(write(bad), 409)
	assert(write(reviewAgain), 201)
	assert(write(reviewAgain), 200)
	assert(request(path), 200)
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit
 WHERE application_id='app' AND target=$1 AND action='change.review.accepted'`, run.ID).Scan(&acceptedCount); err != nil || acceptedCount != 2 {
		t.Fatalf("review revision count=%d err=%v", acceptedCount, err)
	}
	// The audit trigger serializes all audit appends. A session can expire
	// while the writer waits on that independent global lock. Its final check
	// must roll back the otherwise valid audit insertion.
	var latestReviewID string
	if err = db.QueryRow(ctx, `SELECT id::text FROM management_audit
 WHERE application_id='app' AND target=$1 AND action='change.review.accepted'
 ORDER BY chain_seq DESC LIMIT 1`, run.ID).Scan(&latestReviewID); err != nil {
		t.Fatal(err)
	}
	thirdReview := map[string]any{}
	for key, value := range reviewAgain {
		thirdReview[key] = value
	}
	thirdReview["expected_previous_review_id"] = latestReviewID
	thirdReview["alert_text"] = "Another human review after the incident update."
	lockTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(context.Background())
	if _, err = lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(734911,37)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '3 seconds'
 WHERE token_hash=$1`, store.Hash(adminToken)); err != nil {
		t.Fatal(err)
	}
	writeResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { writeResult <- write(thirdReview) }()
	waiting := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE wait_event_type='Lock' AND query LIKE '%INSERT INTO management_audit%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("review writer never reached the audit append lock")
	}
	var sessionExpired bool
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err = db.QueryRow(ctx, `SELECT expires_at<clock_timestamp() FROM sessions WHERE token_hash=$1`,
			store.Hash(adminToken)).Scan(&sessionExpired); err != nil {
			t.Fatal(err)
		}
		if sessionExpired {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sessionExpired {
		t.Fatal("review session did not expire during the audit wait")
	}
	if err = lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-writeResult:
		assert(result, 403)
	case <-time.After(5 * time.Second):
		t.Fatal("expired review writer did not finish")
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit
 WHERE application_id='app' AND target=$1 AND action='change.review.accepted'`, run.ID).Scan(&acceptedCount); err != nil || acceptedCount != 2 {
		t.Fatalf("expired review committed: count=%d err=%v", acceptedCount, err)
	}
	if _, err = db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '12 hours'
 WHERE token_hash=$1`, store.Hash(adminToken)); err != nil {
		t.Fatal(err)
	}
	// The review transaction holds this same table lock until commit. Even a
	// grant that was just inserted cannot be revoked between policy read and
	// the accepted audit commit.
	grantTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer grantTx.Rollback(context.Background())
	if err = lockChangeReviewAuthoritySources(ctx, grantTx); err != nil {
		t.Fatal(err)
	}
	grantChange := make(chan error, 1)
	go func() {
		_, changeErr := db.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id='admin' AND module='services' AND action='change'`)
		grantChange <- changeErr
	}()
	select {
	case changeErr := <-grantChange:
		t.Fatalf("grant changed while review authority was locked: %v", changeErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err = grantTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case changeErr := <-grantChange:
		if changeErr != nil {
			t.Fatal(changeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("grant change did not resume after review commit")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','services','change','global','*')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_roles(id,name) VALUES('review-lock-role','Review lock role')`); err != nil {
		t.Fatal(err)
	}
	legacyRole, err := (&rbac.Service{Store: st}).Role(ctx, "review-lock-role")
	if err != nil {
		t.Fatal(err)
	}
	// Run the actual legacy association writer ahead of an HTTP review while
	// both are queued on the shared authority lock. It must finish without a
	// cycle between role-row and association-table locks.
	authorityTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer authorityTx.Rollback(context.Background())
	if _, err = authorityTx.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
		t.Fatal(err)
	}
	writerCtx, cancelWriter := context.WithTimeout(ctx, 10*time.Second)
	defer cancelWriter()
	associationResult := make(chan error, 1)
	go func() {
		_, associationErr := (&rbac.Service{Store: st}).SaveAssociations(
			store.WithPrincipal(writerCtx, core.User{ID: adminID}), legacyRole.ID,
			rbac.Associations{Revision: legacyRole.Revision, Users: rbac.Delta{Add: []string{viewer.ID}}})
		associationResult <- associationErr
	}()
	select {
	case associationErr := <-associationResult:
		t.Fatalf("association writer did not queue: %v", associationErr)
	case <-time.After(100 * time.Millisecond):
	}
	reviewResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { reviewResult <- write(reviewAgain) }()
	select {
	case result := <-reviewResult:
		t.Fatalf("review did not queue behind association writer: %d %s", result.Code, result.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	if err = authorityTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case associationErr := <-associationResult:
		if associationErr != nil {
			t.Fatal(associationErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("association writer stalled with review queued")
	}
	select {
	case result := <-reviewResult:
		assert(result, 200)
	case <-time.After(5 * time.Second):
		t.Fatal("review stalled after association write")
	}
	if _, err = reviews.AuditReviewSource.Read(ctx, "app", "wrong-environment", run.ID, run.Snapshot.Digest); err == nil {
		t.Fatal("review evidence accepted for a different environment")
	}
	assert(request("/api/v1/changes?service_id="+url.QueryEscape(serviceID)+"&run_id="+run.ID+"&range=30d"), 422)
	assert(request("/api/v1/changes?service_id="+url.QueryEscape(serviceID)+"&run_id="+run.ID+"&range=7d"), 503)
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='events' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assert(request(path), 403)
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','events','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	reviews.reads, reviews.revokeOnSecond = 0, true
	assert(request(path), 403)
	reviews.revokeOnSecond = false
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','events','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	reviews.mutate = true
	assert(request(path), 503)
	oldRun.CreatedAt, oldRun.UpdatedAt = now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour+2*time.Minute)
	oldRun.Projection.ObservedAt = oldRun.UpdatedAt
	oldProjection, _ = json.Marshal(oldRun.Projection)
	if _, err = db.Exec(ctx, `UPDATE delivery_runs SET created_at=$2,updated_at=$3,projection=$4 WHERE id=$1`,
		oldRun.ID, oldRun.CreatedAt, oldRun.UpdatedAt, oldProjection); err != nil {
		t.Fatal(err)
	}
	expired := copyPayload()
	expired["expected_run_updated_at"] = oldRun.UpdatedAt
	expired["execution_id"] = "execution-old"
	expired["expected_plan_digest"] = "old-plan-digest"
	expired["expected_incident_updated_at"] = incidentUpdatedAt
	assert(writeForRun(oldRun.ID, expired), 409)
}
