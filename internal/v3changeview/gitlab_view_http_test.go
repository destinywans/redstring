package v3changeview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3changes"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeChanges is a GitLab change reader with fixed data around now.
type fakeChanges struct {
	mu        sync.Mutex
	now       time.Time
	err       error
	since     time.Time
	until     time.Time
	userID    string
	revisions []v3changes.KubernetesRevision
}

const (
	viewSHAMerged = "1111111111111111111111111111111111111111"
	viewSHAFailed = "2222222222222222222222222222222222222222"
	viewSHAOpen   = "3333333333333333333333333333333333333333"
)

func (f *fakeChanges) ReadServiceChanges(_ context.Context, user core.User, app, env, _ string, since, until time.Time) (v3changes.ServiceChanges, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since, f.until, f.userID = since, until, user.ID
	if f.err != nil {
		return v3changes.ServiceChanges{}, f.err
	}
	at := func(minutes int) time.Time { return f.now.Add(time.Duration(-minutes) * time.Minute) }
	finished, failed, merged := at(30), at(60), at(41)
	eleven, ten, job := int64(11), int64(10), int64(91)
	return v3changes.ServiceChanges{ApplicationID: app, EnvironmentID: env, CapturedAt: f.now, Unavailable: map[string]string{},
		Project: v3changes.Project{ConnectorID: "gitlab-1", ProjectID: "42", Path: "aiops-lab/checkout-api",
			WebURL: "https://gitlab.example.invalid/aiops-lab/checkout-api", DeployEnvironment: "aiops-lab", Refs: []string{"main"}},
		Deployments: []v3changes.Deployment{
			{ID: 503, IID: 3, Status: "success", Environment: "aiops-lab", Ref: "main", SHA: viewSHAMerged, PipelineID: &eleven,
				JobID: &job, JobName: "deploy-aiops-lab", User: &v3changes.User{Username: "maintainer-a"}, CreatedAt: at(32),
				UpdatedAt: finished, FinishedAt: &finished},
			{ID: 502, IID: 2, Status: "failed", Environment: "aiops-lab", Ref: "main", SHA: viewSHAFailed, PipelineID: &ten,
				CreatedAt: at(62), UpdatedAt: failed, FinishedAt: &failed}},
		Pipelines: []v3changes.Pipeline{
			{ID: 11, Status: "success", Ref: "main", SHA: viewSHAMerged, Source: "push", CreatedAt: at(40), UpdatedAt: at(30)},
			{ID: 12, Status: "failed", Ref: "refs/merge-requests/5/head", SHA: viewSHAOpen, Source: "merge_request_event", CreatedAt: at(20), UpdatedAt: at(19)},
			{ID: 13, Status: "failed", Ref: "broken", SHA: viewSHAOpen, Source: "push", CreatedAt: at(21), UpdatedAt: at(19)}},
		MergeRequests: []v3changes.MergeRequest{
			{IID: 4, Title: "Use the pooled client", State: "merged", SourceBranch: "pool", TargetBranch: "main",
				Author: &v3changes.User{Username: "dev-a"}, MergedBy: &v3changes.User{Username: "maintainer-a"},
				CreatedAt: at(50), UpdatedAt: merged, MergedAt: &merged, SHA: viewSHAMerged},
			{IID: 5, Title: "Broken unit test", State: "opened", SourceBranch: "broken", TargetBranch: "main",
				Author: &v3changes.User{Username: "dev-a"}, CreatedAt: at(25), UpdatedAt: at(20), SHA: viewSHAOpen}},
	}, nil
}

func (f *fakeChanges) ReadPipeline(_ context.Context, _ core.User, _, _, _ string, id int64) (v3changes.PipelineDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return v3changes.PipelineDetail{}, f.err
	}
	if id != 11 {
		return v3changes.PipelineDetail{}, v3changes.ErrNotFound
	}
	finished := f.now.Add(-30 * time.Minute)
	duration := 152.5
	files := 3
	mr := int64(4) // The reader's PipelineMergeRequest, as for the list row.
	return v3changes.PipelineDetail{ProjectID: "42", Unavailable: map[string]string{}, MergeRequest: &mr,
		Pipeline: v3changes.Pipeline{ID: 11, Status: "success", Ref: "main", SHA: viewSHAMerged, Source: "push",
			CreatedAt: f.now.Add(-40 * time.Minute), UpdatedAt: finished},
		FinishedAt: &finished, DurationSeconds: &duration, User: &v3changes.User{Username: "dev-a"},
		Stages: []v3changes.Stage{{Name: "test", Status: "success", Jobs: []v3changes.Job{{ID: 89, Name: "unit", Status: "success"}}},
			{Name: "deploy", Status: "success", Jobs: []v3changes.Job{{ID: 91, Name: "deploy-aiops-lab", Status: "success"}}}},
		Commit: &v3changes.Commit{SHA: viewSHAMerged, Title: "Use the pooled client", AuthoredAt: f.now.Add(-45 * time.Minute),
			ParentIDs: []string{viewSHAOpen}, Additions: 10, Deletions: 2, Files: &files, Paths: []string{"apps/checkout/pool.go"}},
	}, nil
}

func (f *fakeChanges) ReadRevisions(context.Context, core.User, string, string) ([]v3changes.KubernetesRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revisions, nil
}

func TestGitLabChangesViewHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_change_gitlab_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	for _, migrate := range []func(context.Context) error{st.Migrate, (&operations.Service{DB: db}).Migrate} {
		if err = migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
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
	now := time.Now().UTC().Truncate(time.Second)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer'`)
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','test'),('viewer','services','view','application','app'),
 ('viewer','events','view','application','app')`)
	exec(`INSERT INTO applications(id,name,key,environment,archived) VALUES('app','Checkout API','checkout','test',false)`)
	exec(`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-test','app','test','Test')`)
	exec(`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','test')`)
	// One alert 3 minutes after the successful deployment, one 5 hours before.
	exec(`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at)
 VALUES('incident-near','alert-source','app','app-test','fp-1','Checkout 5xx','critical','open','firing',$1),
 ('incident-far','alert-source','app','app-test','fp-2','Old alert','warning','open','firing',$2)`,
		now.Add(-27*time.Minute), now.Add(-5*time.Hour))
	cause := "gitlab pipeline 11 11111111"
	eleven := int64(11)
	// As the reader sets them (PA-94): new ReplicaSets at their creation,
	// a reused one of unknown time bounded by its neighbours, and one whose
	// bounds lie before the window.
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	reused, stale := "replicaset_reused", "change_cause_stale"
	fake := &fakeChanges{now: now, revisions: []v3changes.KubernetesRevision{
		{Revision: "22", CreatedAt: now.Add(-29 * time.Minute), ChangeCause: &cause, GitLabPipelineID: &eleven,
			At: at(-29 * time.Minute), TimeBasis: v3changes.TimeBasisReplicaSetCreated},
		{Revision: "23", CreatedAt: now.Add(-10 * time.Minute), At: at(-10 * time.Minute), TimeBasis: v3changes.TimeBasisReplicaSetCreated},
		{Revision: "24", CreatedAt: now.Add(-30 * 24 * time.Hour), ReplicaSetReused: true, TimeBasis: v3changes.TimeBasisUnknown,
			TimeUnknownReason: &reused, NotBefore: at(-10 * time.Minute), NotAfter: at(0)},
		{Revision: "8", CreatedAt: now.Add(-40 * 24 * time.Hour), ReplicaSetReused: true, TimeBasis: v3changes.TimeBasisUnknown,
			TimeUnknownReason: &stale, NotBefore: at(-30 * 24 * time.Hour), NotAfter: at(-20 * 24 * time.Hour)}}}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(st, policy, AuditReviewSource{DB: db}, func(_ context.Context, app, env string, start, end time.Time) string {
		if app == "app" && env == "test" && !start.Before(now.Add(-8*24*time.Hour)) {
			return "alert-source"
		}
		return ""
	})
	service.GitLab = fake
	service.Now = func() time.Time { return now }
	service.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))
	viewPath := "/api/v1/changes?" + url.Values{"service_id": {serviceID}, "range": {"24h"}, "sources": {"gitlab"}}.Encode()
	request := func(path, language string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		if language != "" {
			r.Header.Set("Accept-Language", language)
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := func(language string) gitLabView {
		t.Helper()
		w := request(viewPath, language)
		var view gitLabView
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil {
			t.Fatalf("view: %d %s", w.Code, w.Body.String())
		}
		return view
	}
	view := read("")
	if fake.userID != viewer.ID || !fake.until.Equal(now) || fake.until.Sub(fake.since) != 24*time.Hour {
		t.Fatalf("reader call: user=%s %s..%s", fake.userID, fake.since, fake.until)
	}
	if view.Source != "gitlab" || view.Project.Path != "aiops-lab/checkout-api" || view.SeriesStepSeconds != 3600 ||
		view.Coverage.GitLab != "complete" || view.Coverage.Alerts != "complete" || view.Coverage.Kubernetes != "history_limited" ||
		len(view.Unavailable) != 0 {
		t.Fatalf("view header: %+v", view)
	}
	kpis := map[string]kpi{}
	for _, k := range view.KPIs {
		kpis[k.ID] = k
	}
	if kpis["deployments"].Value != "1" || kpis["failed_deployments"].Value != "1" || kpis["pipelines"].Value != "3" ||
		kpis["merged_merge_requests"].Value != "1" || len(kpis["pipelines"].Series) != 24 || kpis["deployments"].Label != "成功部署" {
		t.Fatalf("kpis: %+v", view.KPIs)
	}
	// Exact links: the merge request landed as the deployed commit, the
	// deploy pipeline's cluster revision, and the time-adjacent alert.
	deployment := view.Deployments[0]
	if deployment.ID != "gitlab:deployment:42:503" || deployment.MergeRequestIID == nil || *deployment.MergeRequestIID != 4 ||
		deployment.KubernetesRevision == nil || *deployment.KubernetesRevision != "22" || deployment.Job == nil ||
		len(deployment.AdjacentAlerts) != 1 || deployment.AdjacentAlerts[0].IncidentID != "incident-near" ||
		deployment.AdjacentAlerts[0].OffsetSeconds != 180 || deployment.AdjacentAlerts[0].Basis != "time_adjacent" ||
		deployment.ShortSHA != "11111111" {
		t.Fatalf("deployment links: %+v", deployment)
	}
	// The failed deployment 33 minutes before the alert is adjacent too: the
	// window is 15 minutes before to 60 minutes after a deployment.
	if failed := view.Deployments[1]; failed.MergeRequestIID != nil || failed.KubernetesRevision != nil ||
		len(failed.AdjacentAlerts) != 1 || failed.AdjacentAlerts[0].OffsetSeconds != 1980 {
		t.Fatalf("failed deployment links: %+v", failed)
	}
	if p := view.Pipelines[0]; p.MergeRequestIID == nil || *p.MergeRequestIID != 4 || len(p.DeploymentIDs) != 1 || p.DeploymentIDs[0] != 503 {
		t.Fatalf("pipeline 11 links: %+v", p)
	}
	if p := view.Pipelines[1]; p.MergeRequestIID == nil || *p.MergeRequestIID != 5 || len(p.DeploymentIDs) != 0 {
		t.Fatalf("pipeline 12 links: %+v", p)
	}
	if mr := view.MergeRequests[0]; len(mr.PipelineIDs) != 1 || mr.PipelineIDs[0] != 11 || len(mr.DeploymentIDs) != 1 || mr.DeploymentIDs[0] != 503 {
		t.Fatalf("merge request 4 links: %+v", mr)
	}
	// The MR's branch pipeline belongs to it as well as its MR pipeline.
	if p := view.Pipelines[2]; p.MergeRequestIID == nil || *p.MergeRequestIID != 5 {
		t.Fatalf("source branch pipeline links: %+v", p)
	}
	if mr := view.MergeRequests[1]; len(mr.PipelineIDs) != 2 || mr.PipelineIDs[0] != 12 || mr.PipelineIDs[1] != 13 || len(mr.DeploymentIDs) != 0 {
		t.Fatalf("merge request 5 links: %+v", mr)
	}
	if len(view.Alerts) != 2 || len(view.KubernetesRevisions) != 3 || view.KubernetesRevisions[1].GitLabPipelineID != nil ||
		view.KubernetesRevisions[0].At == nil || view.KubernetesRevisions[0].TimeBasis != "replicaset_created" {
		t.Fatalf("alerts and revisions: %+v %+v", view.Alerts, view.KubernetesRevisions)
	}
	// PA-94: the reused ReplicaSet of unknown time is listed with a null at
	// and its bounds; the one bounded before the window is not.
	if r := view.KubernetesRevisions[2]; r.Revision != "24" || r.At != nil || !r.ReplicaSetReused || r.TimeBasis != "unknown" ||
		r.TimeUnknownReason == nil || *r.TimeUnknownReason != "replicaset_reused" || r.NotBefore == nil || r.NotAfter == nil {
		t.Fatalf("untimed revision: %+v", r)
	}
	if english := read("en-US"); english.KPIs[0].Label != "Successful deployments" {
		t.Fatalf("english labels: %+v", english.KPIs)
	}

	// Without events.view the alerts are unknown, not empty.
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='events'`)
	view = read("")
	if view.Alerts != nil || view.Unavailable["alerts"] != "no_authorized_target" || view.Deployments[0].AdjacentAlerts != nil {
		t.Fatalf("alerts without events.view: %+v", view)
	}
	raw := request(viewPath, "").Body.String()
	if !strings.Contains(raw, `"alerts":null`) || !strings.Contains(raw, `"adjacent_alerts":null`) {
		t.Fatalf("unknown alerts must be null: %s", raw)
	}
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','events','view','application','app')`)

	// Reader failures map to explicit states; nothing partial is returned.
	for err, want := range map[error]int{v3changes.ErrNotConfigured: 503, v3changes.ErrTooLarge: 413, v3changes.ErrNotVisible: 403,
		v3changes.ErrCredential: 503, v3changes.ErrSourceChanged: 409, v3changes.ErrUnavailable: 503} {
		fake.mu.Lock()
		fake.err = err
		fake.mu.Unlock()
		if w := request(viewPath, ""); w.Code != want {
			t.Fatalf("%v: %d %s", err, w.Code, w.Body.String())
		}
	}
	fake.mu.Lock()
	fake.err = nil
	fake.mu.Unlock()
	for _, bad := range []url.Values{
		{"service_id": {serviceID}, "range": {"24h"}, "sources": {"gitlab"}, "run_id": {"run-1"}},
		{"service_id": {serviceID}, "range": {"30d"}, "sources": {"gitlab"}},
		{"range": {"24h"}, "sources": {"gitlab"}},
	} {
		if w := request("/api/v1/changes?"+bad.Encode(), ""); w.Code != 422 {
			t.Fatalf("%v: %d", bad, w.Code)
		}
	}

	// Pipeline detail: stages, commit statistics and adjacent alerts.
	pipelinePath := "/api/v1/changes/pipelines/11?" + url.Values{"service_id": {serviceID}}.Encode()
	w := request(pipelinePath, "")
	var detail gitLabPipelineDetailView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil {
		t.Fatalf("pipeline: %d %s", w.Code, w.Body.String())
	}
	if detail.ID != "gitlab:pipeline:42:11" || len(detail.Stages) != 2 || detail.Commit == nil || detail.Commit.Stats.Additions != 10 ||
		detail.Commit.Stats.Files == nil || *detail.Commit.Stats.Files != 3 || len(detail.AdjacentAlerts) != 1 ||
		detail.AdjacentAlerts[0].OffsetSeconds != 180 || detail.MergeRequestIID == nil || *detail.MergeRequestIID != 4 {
		t.Fatalf("pipeline detail: %+v", detail)
	}
	for path, want := range map[string]int{
		"/api/v1/changes/pipelines/12?" + url.Values{"service_id": {serviceID}}.Encode():             404,
		"/api/v1/changes/pipelines/011?" + url.Values{"service_id": {serviceID}}.Encode():            422,
		"/api/v1/changes/pipelines/11?" + url.Values{"service_id": {serviceID}, "x": {"1"}}.Encode(): 422,
		"/api/v1/changes/pipelines/11?" + url.Values{"service_id": {"not-a-service"}}.Encode():       422,
	} {
		if w := request(path, ""); w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}

	// Without services.view the service does not exist for the caller.
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='services'`)
	if w := request(viewPath, ""); w.Code != 404 {
		t.Fatalf("without services.view: %d %s", w.Code, w.Body.String())
	}
	if w := request(pipelinePath, ""); w.Code != 404 {
		t.Fatalf("pipeline without services.view: %d %s", w.Code, w.Body.String())
	}
}
