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
	source := map[string]string{"run_id": run.ID, "pipeline_digest": run.Snapshot.Digest, "artifact_digest": "sha256:" + strings.Repeat("a", 64), "application_id": "app", "environment_id": "app-test"}
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
	reviewPayload := map[string]any{
		"schema_version": 1, "display_approved": true, "environment_id": "app-test", "run_digest": run.Snapshot.Digest,
		"artifact_digest": "sha256:" + strings.Repeat("a", 64),
		"execution_id":    "execution-1", "incident_id": "incident-1",
		"diff":       map[string]string{"path": "config/checkout.yaml", "minus": "- replicas: 2", "plus": "+ replicas: 3"},
		"risk":       map[string]string{"title": "Reviewed deployment risk", "meta": "Human review", "text": "Potential capacity impact; causality not asserted."},
		"alert_text": "Reviewed alert association; causality not asserted.",
	}
	rawReview, _ := json.Marshal(reviewPayload)
	if _, err = db.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user','app','change.review.accepted',$2,'success',$3)`, adminID, run.ID, rawReview); err != nil {
		t.Fatal(err)
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
	oldSnapshot, _ := json.Marshal(oldRun.Snapshot)
	oldProjection, _ := json.Marshal(oldRun.Projection)
	oldSource := map[string]string{"run_id": oldRun.ID, "pipeline_digest": oldRun.Snapshot.Digest,
		"artifact_digest": source["artifact_digest"], "application_id": "app", "environment_id": "app-test"}
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
}
