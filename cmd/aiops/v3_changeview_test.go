package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/actions"
	"github.com/destinywans/redstring/internal/delivery"
	"github.com/destinywans/redstring/internal/deployment"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfiguredV3ChangeRoutesReviewAndCoverage(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_changes_entry_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		cancel()
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	for _, migrate := range []func(context.Context) error{
		st.Migrate,
		// Production main migrates recovery executions before V3 projections.
		actions.New(db, nil, "test").Migrate,
		(&delivery.PostgresRepository{DB: db}).Migrate,
		(&deployment.Service{DB: db}).Migrate,
		(&operations.Service{DB: db}).Migrate,
	} {
		if err := migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Bootstrap(ctx, "changes-entry@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	admin, session, err := st.Login(ctx, "changes-entry@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	// Registration must not infer a historical interval from the current
	// Alertmanager source binding. An unset coverage file keeps GET closed.
	t.Setenv("AIOPS_V3_SERVICE_ALERT_COVERAGE_CONFIG", "")
	runner := &v3BindingRunner{}
	api := httpapi.New(st, runner, true)
	cfg := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}
	if err := configureV3(ctx, st, runner, api, nil, &v3Instance{}, cfg, operations.Config{}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "test")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))
	get := call(http.MethodGet, "/api/v1/changes?service_id="+serviceID+"&run_id=missing", "")
	if get.Code != http.StatusServiceUnavailable || !strings.Contains(get.Body.String(), "change_view_incomplete") {
		t.Fatalf("formal Changes GET without interval coverage = %d %s", get.Code, get.Body.String())
	}
	post := call(http.MethodPost, "/api/v1/changes/missing/review", `{}`)
	if post.Code != http.StatusUnprocessableEntity || !strings.Contains(post.Body.String(), "invalid_change_review") {
		t.Fatalf("formal Changes review route = %d %s", post.Code, post.Body.String())
	}

	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment,archived) VALUES('app','Checkout API','checkout','test',false)`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-test','app','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','test')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	definition := delivery.Definition{Provider: "native", Nodes: []delivery.Node{
		{ID: "build", Name: "Build", Kind: "container", Image: "alpine:3.23", Command: []string{"true"}, Outputs: []delivery.Output{
			{Name: "commit-sha", Path: "/workspace/commit-sha"},
			{Name: "artifact-digest", Path: "/workspace/artifact-digest"},
			{Name: "artifact-url", Path: "/workspace/artifact-url"},
		}},
		{ID: "approval", Name: "Approve", Kind: "approval", SourceNodeID: "build", TargetID: "gitops-target", DependsOn: []delivery.Dependency{{NodeID: "build"}}},
		{ID: "deploy", Name: "Deploy", Kind: "deploy", ApprovalNodeID: "approval", DependsOn: []delivery.Dependency{{NodeID: "approval"}}},
	}}
	definitionJSON, _ := json.Marshal(definition)
	digest := sha256.Sum256(definitionJSON)
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	commitSHA := strings.Repeat("b", 40)
	run := delivery.Run{
		ID: "run-1", PipelineID: "pipeline-1", Version: 2, ApplicationID: "app", EnvironmentID: "app-test",
		Provider: "native", Status: "succeeded", ExternalID: "aiops-run-1", CreatedBy: admin.ID,
		Snapshot: delivery.Pipeline{ID: "pipeline-1", Version: 2, ApplicationID: "app", EnvironmentID: "app-test",
			Name: "Production release", Digest: hex.EncodeToString(digest[:]), Definition: definition},
		Projection: delivery.Projection{Status: "succeeded", ExternalID: "aiops-run-1", ObservedAt: now.Add(-10 * time.Minute), Nodes: []delivery.NodeResult{
			{ID: "pod-build", Name: "aiops-run-1.build", Status: "succeeded", Outputs: map[string]string{
				"artifact-digest": artifactDigest, "commit-sha": commitSHA,
			}},
			{ID: "pod-approval", Name: "aiops-run-1.approval", Status: "succeeded"},
			{ID: "pod-deploy", Name: "aiops-run-1.deploy", Status: "succeeded"},
		}},
		CreatedAt: now.Add(-12 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute),
	}
	snapshot, _ := json.Marshal(run.Snapshot)
	projection, _ := json.Marshal(run.Projection)
	for _, insert := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO delivery_pipelines(id,application_id,environment_id,version) VALUES($1,'app','app-test',2)`, []any{run.PipelineID}},
		{`INSERT INTO delivery_versions(pipeline_id,version,snapshot) VALUES($1,2,$2)`, []any{run.PipelineID, snapshot}},
		{`INSERT INTO delivery_runs(id,pipeline_id,version,application_id,environment_id,provider,status,external_id,snapshot,projection,created_by,idempotency_key,request_digest,created_at,updated_at)
 VALUES($1,$2,2,'app','app-test','native','succeeded',$3,$4,$5,$6,'isolated-key','digest',$7,$8)`,
			[]any{run.ID, run.PipelineID, run.ExternalID, snapshot, projection, admin.ID, run.CreatedAt, run.UpdatedAt}},
		{`INSERT INTO delivery_gates(run_id,node_id,plan_id,execution_id) VALUES($1,'deploy','plan-1','execution-1')`, []any{run.ID}},
	} {
		if _, err := db.Exec(ctx, insert.sql, insert.args...); err != nil {
			t.Fatal(err)
		}
	}
	deployedAt := now.Add(-9 * time.Minute)
	alertAt := deployedAt.Add(time.Minute)
	planSource := map[string]string{"run_id": run.ID, "node_id": run.Projection.Nodes[0].Name,
		"pipeline_digest": run.Snapshot.Digest, "artifact_digest": artifactDigest, "commit_sha": commitSHA,
		"application_id": "app", "environment_id": "app-test"}
	planSnapshot, _ := json.Marshal(map[string]any{"target_kind": "gitops", "source": planSource})
	for _, insert := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO deployment_plans(id,application_id,environment_id,target_id,request_key,digest,snapshot,status,created_by)
 VALUES('plan-1','app','app-test','gitops-target','plan-key','plan-digest',$1,'approved',$2)`, []any{planSnapshot, admin.ID}},
		{`INSERT INTO deployment_executions(id,plan_id,status,created_by) VALUES('execution-1','plan-1','succeeded',$1)`, []any{admin.ID}},
		{`INSERT INTO deployment_events(execution_id,type,payload,created_at) VALUES('execution-1','succeeded','{}',$1)`, []any{deployedAt}},
		{`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at)
 VALUES('incident-1','alert-source','app','app-test','fp-1','Checkout alert','warning','open','firing',$1)`, []any{alertAt}},
	} {
		if _, err := db.Exec(ctx, insert.sql, insert.args...); err != nil {
			t.Fatal(err)
		}
	}

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "alert-token")
	if err := os.WriteFile(tokenPath, []byte("fixture-alertmanager-token-at-least-24"), 0600); err != nil {
		t.Fatal(err)
	}
	source := operations.Source{ID: "alert-source", TokenFile: tokenPath, Mappings: []operations.Mapping{{
		Labels: map[string]string{"application": "checkout"}, ApplicationID: "app", EnvironmentID: "app-test",
	}}}
	completeFrom := now.Add(-26 * time.Hour)
	coverageFile := filepath.Join(dir, "coverage.json")
	coverage, _ := json.Marshal(map[string]any{"bindings": []map[string]any{{
		"source_id": source.ID, "application_id": "app", "environment_id": "test",
		"labels": source.Mappings[0].Labels, "complete": true, "complete_from": completeFrom,
	}}})
	if err := os.WriteFile(coverageFile, coverage, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIOPS_V3_SERVICE_ALERT_COVERAGE_CONFIG", coverageFile)
	api = httpapi.New(st, runner, true)
	if err := configureV3(ctx, st, runner, api, nil, &v3Instance{}, cfg,
		operations.Config{Sources: []operations.Source{source}}); err != nil {
		t.Fatal(err)
	}
	viewPath := "/api/v1/changes?service_id=" + serviceID + "&run_id=" + run.ID + "&range=24h&sources=native"
	before := call(http.MethodGet, viewPath, "")
	if before.Code != 503 || !strings.Contains(before.Body.String(), "change_view_incomplete") {
		t.Fatalf("unreviewed formal Changes GET = %d %s", before.Code, before.Body.String())
	}
	var incidentUpdatedAt time.Time
	if err := db.QueryRow(ctx, `SELECT updated_at FROM ops_incidents WHERE id='incident-1'`).Scan(&incidentUpdatedAt); err != nil {
		t.Fatal(err)
	}
	diff := struct {
		Path  string `json:"path"`
		Minus string `json:"minus"`
		Plus  string `json:"plus"`
	}{"config/checkout.yaml", "- replicas: 2", "+ replicas: 3"}
	diffJSON, _ := json.Marshal(diff)
	diffSum := sha256.Sum256(diffJSON)
	review, _ := json.Marshal(map[string]any{
		"service_id": serviceID, "decision": "accept_for_display",
		"expected_run_digest": run.Snapshot.Digest, "expected_run_updated_at": run.UpdatedAt,
		"execution_id": "execution-1", "expected_plan_digest": "plan-digest",
		"expected_artifact_digest": artifactDigest, "expected_commit_sha": commitSHA,
		"incident_id": "incident-1", "expected_incident_updated_at": incidentUpdatedAt,
		"expected_diff_digest": hex.EncodeToString(diffSum[:]), "diff": diff,
		"risk":       map[string]string{"title": "Reviewed deployment risk", "meta": "Human review", "text": "Potential capacity impact; causality not asserted."},
		"alert_text": "Reviewed alert association; causality not asserted.",
	})
	accepted := call(http.MethodPost, "/api/v1/changes/"+run.ID+"/review", string(review))
	if accepted.Code != 201 || !strings.Contains(accepted.Body.String(), `"status":"accepted"`) {
		t.Fatalf("formal human review = %d %s", accepted.Code, accepted.Body.String())
	}
	view := call(http.MethodGet, viewPath, "")
	if view.Code != 200 || !strings.Contains(view.Body.String(), `"incident":"incident-1"`) ||
		!strings.Contains(view.Body.String(), `"pipelines"`) {
		t.Fatalf("formal 24h Changes GET after review = %d %s", view.Code, view.Body.String())
	}
	week := call(http.MethodGet, strings.Replace(viewPath, "range=24h", "range=7d", 1), "")
	if week.Code != 503 || !strings.Contains(week.Body.String(), "change_view_incomplete") {
		t.Fatalf("formal 7d Changes GET without full history = %d %s", week.Code, week.Body.String())
	}
}
