package v3events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3changes"
)

// fakeIncidentChanges answers around start: a deployment 3 minutes before,
// one 2 hours before, and revisions 2 minutes before (the deployment's),
// 5 minutes after (not GitLab's) and 5 hours before.
type fakeIncidentChanges struct {
	mu             sync.Mutex
	start          time.Time
	changesErr     error
	revisionsErr   error
	since, until   time.Time
	app, env, user string
}

const incidentSHA = "1111111111111111111111111111111111111111"

func (f *fakeIncidentChanges) ReadServiceChanges(_ context.Context, user core.User, app, env, _ string, since, until time.Time) (v3changes.ServiceChanges, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since, f.until, f.app, f.env, f.user = since, until, app, env, user.ID
	if f.changesErr != nil {
		return v3changes.ServiceChanges{}, f.changesErr
	}
	near, far := f.start.Add(-3*time.Minute), f.start.Add(-2*time.Hour)
	eleven := int64(11)
	merged := f.start.Add(-20 * time.Minute)
	return v3changes.ServiceChanges{Project: v3changes.Project{ProjectID: "4"},
		Deployments: []v3changes.Deployment{
			{ID: 503, IID: 3, Status: "success", SHA: incidentSHA, Ref: "main", PipelineID: &eleven,
				CreatedAt: near.Add(-time.Minute), UpdatedAt: near, FinishedAt: &near, WebURL: "https://gitlab.example.invalid/jobs/25"},
			{ID: 400, IID: 2, Status: "failed", SHA: incidentSHA, Ref: "main", CreatedAt: far, UpdatedAt: far, FinishedAt: &far}},
		MergeRequests: []v3changes.MergeRequest{{IID: 4, Title: "Use the pooled client", State: "merged", SHA: incidentSHA,
			TargetBranch: "main", MergedAt: &merged, CreatedAt: merged.Add(-time.Hour), UpdatedAt: merged}},
	}, nil
}

func (f *fakeIncidentChanges) ReadRevisions(context.Context, core.User, string, string) ([]v3changes.KubernetesRevision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revisionsErr != nil {
		return nil, f.revisionsErr
	}
	cause := "gitlab pipeline 11 11111111"
	eleven := int64(11)
	return []v3changes.KubernetesRevision{
		{Revision: "22", CreatedAt: f.start.Add(-2 * time.Minute), ChangeCause: &cause, GitLabPipelineID: &eleven},
		{Revision: "23", CreatedAt: f.start.Add(5 * time.Minute)},
		{Revision: "10", CreatedAt: f.start.Add(-5 * time.Hour)}}, nil
}

func TestIncidentChangesAroundTheAlert(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_incident_changes_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
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
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-changes-password"); err != nil {
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
	start := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer'`)
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),('viewer','events','view','application','app')`)
	exec(`INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev')`)
	exec(`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`)
	exec(`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`)
	exec(`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
 VALUES('incident','test','app','app-dev','incident','Checkout 5xx','critical','firing',$1)`, start)
	exec(`INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,actor_id,request_key,request_hash,created_at,updated_at)
 VALUES('INC-1','app','app-dev','dev','Manual checkout incident','P2',$1,'request-key-1',$2,$3,$3)`, viewer.ID, strings.Repeat("a", 64), start)
	policy := &rbac.V3Policy{Store: st}
	decision := func(app, env, action string) rbac.V3Decision {
		return rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env}
	}
	service := New(db,
		func(ctx context.Context, u core.User, app, env, action string) error {
			return policy.Authorize(ctx, u, decision(app, env, action))
		},
		func(ctx context.Context, tx pgx.Tx, u core.User, app, env, action string) error {
			return policy.AuthorizeWithTx(ctx, tx, u, decision(app, env, action))
		})
	fake := &fakeIncidentChanges{start: start}
	service.Changes = fake
	api := httpapi.New(st, nil, true)
	service.RegisterV3Incidents(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	type item struct {
		Kind               string `json:"kind"`
		ID                 string `json:"id"`
		OffsetSeconds      int64  `json:"offset_seconds"`
		Basis              string `json:"basis"`
		KubernetesRevision *string
		MergeRequest       *incidentChangeMR `json:"merge_request"`
		Revision           string            `json:"revision"`
		GitLabDeploymentID *int64            `json:"gitlab_deployment_id"`
		Raw                map[string]json.RawMessage
	}
	type view struct {
		AlertStartedAt string            `json:"alert_started_at"`
		Window         map[string]string `json:"window"`
		Items          []json.RawMessage `json:"items"`
		Coverage       map[string]string `json:"coverage"`
		Note           string            `json:"note"`
		Unavailable    map[string]string `json:"unavailable"`
	}
	read := func(path string) (view, []item) {
		t.Helper()
		w := request(path)
		var out view
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		items := make([]item, len(out.Items))
		for i, raw := range out.Items {
			if err := json.Unmarshal(raw, &items[i]); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(raw, &items[i].Raw)
			if revision, ok := items[i].Raw["kubernetes_revision"]; ok {
				_ = json.Unmarshal(revision, &items[i].KubernetesRevision)
			}
		}
		return out, items
	}
	out, items := read("/api/v1/incidents/incident/changes")
	if out.AlertStartedAt != start.Format(time.RFC3339) || out.Window["start_at"] != start.Add(-time.Hour).Format(time.RFC3339) ||
		out.Window["end_at"] != start.Add(15*time.Minute).Format(time.RFC3339) || out.Note != "time_adjacent_not_causal" ||
		out.Coverage["gitlab"] != "complete" || out.Coverage["kubernetes"] != "history_limited" || len(out.Unavailable) != 0 {
		t.Fatalf("header: %+v", out)
	}
	if fake.app != "app" || fake.env != "dev" || fake.user != viewer.ID || !fake.since.Equal(start.Add(-time.Hour)) {
		t.Fatalf("reader call: %s %s %s %s", fake.app, fake.env, fake.user, fake.since)
	}
	// The deployment 3 minutes before, its revision 2 minutes before and an
	// unrelated revision 5 minutes after; nothing outside the window.
	if len(items) != 3 || items[0].Kind != "gitlab_deployment" || items[0].OffsetSeconds != -180 ||
		items[0].KubernetesRevision == nil || *items[0].KubernetesRevision != "22" || items[0].MergeRequest == nil ||
		items[0].MergeRequest.IID != 4 || items[0].ID != "gitlab:deployment:4:503" ||
		items[1].Kind != "kubernetes_revision" || items[1].Revision != "22" || items[1].GitLabDeploymentID == nil ||
		*items[1].GitLabDeploymentID != 503 || items[2].Revision != "23" || items[2].GitLabDeploymentID != nil ||
		string(items[2].Raw["gitlab_deployment_id"]) != "null" {
		t.Fatalf("items: %+v", items)
	}
	for _, it := range items {
		if it.Basis != "time_adjacent" {
			t.Fatalf("basis: %+v", it)
		}
	}
	// A manual incident starts at its creation.
	if out, _ := read("/api/v1/incidents/INC-1/changes?before=2h&after=0m"); out.Window["start_at"] != start.Add(-2*time.Hour).Format(time.RFC3339) ||
		out.Window["end_at"] != start.Format(time.RFC3339) {
		t.Fatalf("manual incident window: %+v", out.Window)
	}
	for _, query := range []string{"before=0m", "before=25h", "before=20h&after=5h", "before=90s", "after=-1m", "since=1h", "before=1h&before=2h"} {
		if w := request("/api/v1/incidents/incident/changes?" + query); w.Code != 422 {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	// A source that is not configured or not readable stays explicit; the
	// rest still answers.
	fake.mu.Lock()
	fake.changesErr, fake.revisionsErr = v3changes.ErrNotConfigured, v3changes.ErrRevisionsForbidden
	fake.mu.Unlock()
	out, items = read("/api/v1/incidents/incident/changes")
	if len(items) != 0 || out.Coverage["gitlab"] != "not_configured" || out.Coverage["kubernetes"] != "no_authorized_target" ||
		out.Unavailable["gitlab"] != "not_configured" || out.Unavailable["kubernetes"] != "no_authorized_target" {
		t.Fatalf("unconfigured sources: %+v %+v", out, items)
	}
	fake.mu.Lock()
	fake.changesErr, fake.revisionsErr = v3changes.ErrUnavailable, nil
	fake.mu.Unlock()
	if out, items := read("/api/v1/incidents/incident/changes"); out.Coverage["gitlab"] != "unavailable" || len(items) != 2 {
		t.Fatalf("GitLab unavailable: %+v %+v", out, items)
	}
	service.Changes = nil
	if out, items := read("/api/v1/incidents/incident/changes"); out.Coverage["gitlab"] != "not_configured" ||
		out.Coverage["kubernetes"] != "not_configured" || len(items) != 0 {
		t.Fatalf("no reader: %+v %+v", out, items)
	}
	service.Changes = fake
	// Unknown or invisible incidents are not found.
	if w := request("/api/v1/incidents/missing/changes"); w.Code != 404 {
		t.Fatalf("unknown incident: %d", w.Code)
	}
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='events'`)
	if w := request("/api/v1/incidents/incident/changes"); w.Code != 404 {
		t.Fatalf("without events.view: %d %s", w.Code, w.Body.String())
	}
}
