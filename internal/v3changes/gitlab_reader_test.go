package v3changes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// fakeGitLab serves project 42 of a synthetic GitLab: three deployments of
// environment aiops-lab, pipelines on main, on a merge request and on another
// branch, merge requests into main, and one pipeline with its jobs, commit
// and diff. Responses can be replaced per path.
type fakeGitLab struct {
	mu       sync.Mutex
	server   *httptest.Server
	override map[string]func(http.ResponseWriter, *http.Request)
	queries  map[string]string
	token    string
}

const (
	shaMerged  = "1111111111111111111111111111111111111111"
	shaFailed  = "2222222222222222222222222222222222222222"
	shaHead    = "3333333333333333333333333333333333333333"
	shaParent  = "4444444444444444444444444444444444444444"
	fakeSecret = "synthetic-gitlab-token"
)

func newFakeGitLab(t *testing.T) *fakeGitLab {
	t.Helper()
	f := &fakeGitLab{override: map[string]func(http.ResponseWriter, *http.Request){}, queries: map[string]string{}, token: fakeSecret}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitLab) set(path string, handler func(http.ResponseWriter, *http.Request)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override[path] = handler
}

func (f *fakeGitLab) query(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries[path]
}

func (f *fakeGitLab) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.queries[r.URL.Path] = r.URL.RawQuery
	handler := f.override[r.URL.Path]
	token := f.token
	f.mu.Unlock()
	if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if handler != nil {
		handler(w, r)
		return
	}
	at := func(minutes int) string {
		return time.Now().UTC().Add(time.Duration(-minutes) * time.Minute).Format(time.RFC3339)
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v4/projects/42":
		fmt.Fprint(w, `{"id":42,"path_with_namespace":"aiops-lab/checkout-api","web_url":"https://gitlab.example.invalid/aiops-lab/checkout-api"}`)
	case "/api/v4/projects/42/deployments":
		fmt.Fprintf(w, `[
 {"id":503,"iid":3,"ref":"main","sha":%q,"status":"success","created_at":%q,"updated_at":%q,
  "user":{"username":"maintainer-a","name":"Maintainer A","email":"hidden@example.invalid"},
  "environment":{"id":7,"name":"aiops-lab"},
  "deployable":{"id":91,"name":"deploy-aiops-lab","finished_at":%q,"web_url":"https://gitlab.example.invalid/aiops-lab/checkout-api/-/jobs/91","pipeline":{"id":11}}},
 {"id":502,"iid":2,"ref":"main","sha":%q,"status":"failed","created_at":%q,"updated_at":%q,
  "user":{"username":"maintainer-a"},"environment":{"id":7,"name":"aiops-lab"},
  "deployable":{"id":81,"name":"deploy-aiops-lab","web_url":"https://gitlab.example.invalid/aiops-lab/checkout-api/-/jobs/81","pipeline":{"id":10}}}]`,
			shaMerged, at(30), at(27), at(29), shaFailed, at(60), at(58))
	case "/api/v4/projects/42/pipelines":
		fmt.Fprintf(w, `[
 {"id":11,"project_id":42,"status":"success","ref":"main","sha":%q,"source":"push","created_at":%q,"updated_at":%q,"web_url":"https://gitlab.example.invalid/p/11"},
 {"id":12,"project_id":42,"status":"failed","ref":"refs/merge-requests/5/head","sha":%q,"source":"merge_request_event","created_at":%q,"updated_at":%q,"web_url":"https://gitlab.example.invalid/p/12"},
 {"id":13,"project_id":42,"status":"success","ref":"feature/other","sha":%q,"source":"push","created_at":%q,"updated_at":%q,"web_url":"https://gitlab.example.invalid/p/13"},
 {"id":14,"project_id":42,"status":"failed","ref":"broken","sha":%q,"source":"push","created_at":%q,"updated_at":%q,"web_url":"https://gitlab.example.invalid/p/14"}]`,
			shaMerged, at(40), at(30), shaHead, at(20), at(19), shaHead, at(15), at(14), shaHead, at(21), at(19))
	case "/api/v4/projects/42/merge_requests":
		if branch := r.URL.Query().Get("source_branch"); branch != "" {
			if branch == "broken" {
				fmt.Fprintf(w, `[{"iid":5,"project_id":42,"title":"Broken unit test","state":"opened","source_branch":"broken","target_branch":"main","created_at":%q,"updated_at":%q,"sha":%q}]`,
					at(25), at(20), shaHead)
			} else {
				fmt.Fprint(w, `[]`)
			}
			return
		}
		fmt.Fprintf(w, `[
 {"iid":4,"project_id":42,"title":"Use the pooled client\u0007 for checkout","state":"merged","source_branch":"pool","target_branch":"main",
  "author":{"username":"dev-a"},"merged_by":null,"merge_user":{"username":"maintainer-a"},"created_at":%q,"updated_at":%q,"merged_at":%q,"closed_at":null,
  "sha":%q,"merge_commit_sha":null,"squash_commit_sha":null,"web_url":"https://gitlab.example.invalid/mr/4"},
 {"iid":5,"project_id":42,"title":"Broken unit test","state":"opened","source_branch":"broken","target_branch":"main",
  "author":{"username":"dev-a"},"merged_by":null,"created_at":%q,"updated_at":%q,"merged_at":null,"closed_at":null,
  "sha":%q,"merge_commit_sha":null,"squash_commit_sha":null,"web_url":"https://gitlab.example.invalid/mr/5"}]`,
			at(50), at(41), at(41), shaMerged, at(25), at(20), shaHead)
	case "/api/v4/projects/42/pipelines/11":
		fmt.Fprintf(w, `{"id":11,"project_id":42,"status":"success","ref":"main","sha":%q,"source":"push","created_at":%q,"updated_at":%q,
 "started_at":%q,"finished_at":%q,"duration":152.5,"user":{"username":"dev-a"},"web_url":"https://gitlab.example.invalid/p/11"}`,
			shaMerged, at(40), at(30), at(40), at(37))
	case "/api/v4/projects/42/pipelines/11/jobs":
		fmt.Fprint(w, `[
 {"id":91,"name":"deploy-aiops-lab","stage":"deploy","status":"success","duration":14,"web_url":"https://gitlab.example.invalid/j/91"},
 {"id":90,"name":"build-image","stage":"build","status":"success","duration":55,"web_url":"https://gitlab.example.invalid/j/90"},
 {"id":89,"name":"unit","stage":"test","status":"success","duration":91,"web_url":"https://gitlab.example.invalid/j/89"}]`)
	case "/api/v4/projects/42/pipelines/14":
		fmt.Fprintf(w, `{"id":14,"project_id":42,"status":"failed","ref":"broken","sha":%q,"source":"push","created_at":%q,"updated_at":%q,"web_url":"https://gitlab.example.invalid/p/14"}`,
			shaHead, at(21), at(19))
	case "/api/v4/projects/42/pipelines/14/jobs":
		fmt.Fprint(w, `[{"id":95,"name":"unit","stage":"test","status":"failed","web_url":"https://gitlab.example.invalid/j/95"}]`)
	case "/api/v4/projects/42/repository/commits/" + shaHead:
		fmt.Fprintf(w, `{"id":%q,"title":"Broken unit test","authored_date":%q,"parent_ids":[%q],"stats":{"additions":1,"deletions":0}}`,
			shaHead, at(22), shaMerged)
	case "/api/v4/projects/42/repository/commits/" + shaHead + "/diff":
		w.Header().Set("X-Total", "1")
		fmt.Fprint(w, `[{"new_path":"apps/checkout/handler_test.go"}]`)
	case "/api/v4/projects/42/pipelines/13":
		fmt.Fprintf(w, `{"id":13,"project_id":42,"status":"success","ref":"feature/other","sha":%q,"source":"push","created_at":%q,"updated_at":%q,"web_url":"https://gitlab.example.invalid/p/13"}`,
			shaHead, at(15), at(14))
	case "/api/v4/projects/42/repository/commits/" + shaMerged:
		fmt.Fprintf(w, `{"id":%q,"title":"Use the pooled client","message":"secret body","authored_date":%q,"parent_ids":[%q],"stats":{"additions":10,"deletions":2,"total":12}}`,
			shaMerged, at(45), shaParent)
	case "/api/v4/projects/42/repository/commits/" + shaMerged + "/diff":
		w.Header().Set("X-Total", "3")
		fmt.Fprint(w, `[{"new_path":"apps/checkout/handler.go","diff":"@@ secret"},{"new_path":"apps/checkout/pool.go"},{"new_path":"go.mod"}]`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type changesFixture struct {
	db        *pgxpool.Pool
	service   *Service
	viewer    core.User
	serviceID string
	gitlab    *fakeGitLab
	catalog   v3telemetry.Catalog
}

func allTools() string {
	tools := []string{ToolDeployments, ToolPipelines, ToolMergeRequests, ToolPipeline, ToolCommit, ToolCommitDiff}
	items := make([]map[string]any, 0, len(tools))
	for _, name := range tools {
		items = append(items, map[string]any{"name": name, "enabled": true})
	}
	raw, _ := json.Marshal(items)
	return string(raw)
}

func newChangesFixture(t *testing.T) *changesFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_changes_reader_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	f := &changesFixture{}
	t.Cleanup(func() {
		if f.db != nil {
			f.db.Close()
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
	if f.db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: f.db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	if f.viewer, err = st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	f.gitlab = newFakeGitLab(t)
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),('viewer','services','view','environment','dev'),
 ('viewer','connectors','view','environment','dev')`,
		`INSERT INTO applications(id,name,key,environment,archived) VALUES ('app','App','checkout','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES ('app-dev','app','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app','dev')`,
	} {
		if _, err := f.db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools)
 VALUES ('gitlab-dev','dev','gitlab','GitLab','delivery',$1,'pat','vault://isolated/gitlab#token',true,$2)`, f.gitlab.server.URL, allTools()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES ('t-code','app','dev','gitlab-dev','project','42',(SELECT id FROM users WHERE email='admin@example.invalid'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(ctx, `INSERT INTO v3_service_code_projects(application_id,environment_id,target_id,source) VALUES ('app','dev','t-code','migrated_binding')`); err != nil {
		t.Fatal(err)
	}
	f.catalog, err = v3telemetry.ParseCatalog([]byte(`{"queries":[],"gitlab":[{"connector_id":"gitlab-dev","application_id":"app",
 "environment_id":"dev","project_id":"42","deploy_environment":"aiops-lab","refs":["main"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	f.service = New(f.db, "nonprod", &rbac.V3Policy{Store: st}, testResolver{token: fakeSecret})
	f.service.Catalog = func() v3telemetry.Catalog { return f.catalog }
	f.serviceID = base64.RawURLEncoding.EncodeToString([]byte("app\x00dev"))
	return f
}

func (f *changesFixture) read(t *testing.T) (ServiceChanges, error) {
	t.Helper()
	now := time.Now()
	return f.service.ReadServiceChanges(t.Context(), f.viewer, "app", "dev", f.serviceID, now.Add(-24*time.Hour), now)
}

func TestReadServiceChangesFromTheCodeProject(t *testing.T) {
	f := newChangesFixture(t)
	got, err := f.read(t)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project.Path != "aiops-lab/checkout-api" || got.Project.DeployEnvironment != "aiops-lab" || got.Project.ProjectID != "42" ||
		len(got.Unavailable) != 0 || got.CapturedAt.IsZero() {
		t.Fatalf("project: %+v unavailable=%v", got.Project, got.Unavailable)
	}
	// The reviewed environment and window go to GitLab; one bounded page.
	query := f.gitlab.query("/api/v4/projects/42/deployments")
	for _, part := range []string{"environment=aiops-lab", "order_by=updated_at", "per_page=100", "page=1", "updated_after=", "updated_before="} {
		if !strings.Contains(query, part) {
			t.Fatalf("deployment query %q lacks %q", query, part)
		}
	}
	if q := f.gitlab.query("/api/v4/projects/42/merge_requests"); !strings.Contains(q, "target_branch=main") || !strings.Contains(q, "state=all") {
		t.Fatalf("merge request query: %q", q)
	}
	if len(got.Deployments) != 2 || got.Deployments[0].IID != 3 || got.Deployments[0].Status != "success" ||
		got.Deployments[0].PipelineID == nil || *got.Deployments[0].PipelineID != 11 || got.Deployments[0].JobName != "deploy-aiops-lab" ||
		got.Deployments[0].User == nil || got.Deployments[0].User.Username != "maintainer-a" || got.Deployments[0].FinishedAt == nil {
		t.Fatalf("deployments: %+v", got.Deployments)
	}
	// GitLab 19 deployments carry no finished_at: the job's is used, or the
	// update time of an ended deployment without one.
	if !got.Deployments[0].FinishedAt.Equal(got.Deployments[0].UpdatedAt.Add(-2*time.Minute)) ||
		got.Deployments[1].FinishedAt == nil || !got.Deployments[1].FinishedAt.Equal(got.Deployments[1].UpdatedAt) {
		t.Fatalf("deployment ends: %+v", got.Deployments)
	}
	// main, merge request refs and the window's MR source branches; the
	// feature/other pipeline is outside the scope.
	if len(got.Pipelines) != 3 || got.Pipelines[0].ID != 11 || got.Pipelines[1].ID != 12 || got.Pipelines[2].ID != 14 {
		t.Fatalf("pipelines: %+v", got.Pipelines)
	}
	if iid, ok := got.Pipelines[1].MergeRequestIID(); !ok || iid != 5 {
		t.Fatalf("merge request pipeline: %d %v", iid, ok)
	}
	// Titles are data: control characters become spaces. merge_user stands
	// in for the deprecated merged_by. A fast-forward merge lands as its head.
	if len(got.MergeRequests) != 2 || got.MergeRequests[0].Title != "Use the pooled client  for checkout" ||
		got.MergeRequests[0].MergedBy == nil || got.MergeRequests[0].MergedBy.Username != "maintainer-a" ||
		!got.MergeRequests[0].LandedAs(shaMerged) || got.MergeRequests[1].LandedAs(shaHead) {
		t.Fatalf("merge requests: %+v", got.MergeRequests)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "hidden@example.invalid") || strings.Contains(string(raw), "Maintainer A") {
		t.Fatalf("read user details beyond the username: %s", raw)
	}

	detail, err := f.service.ReadPipeline(t.Context(), f.viewer, "app", "dev", f.serviceID, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Stages) != 3 || detail.Stages[0].Name != "test" || detail.Stages[2].Name != "deploy" ||
		detail.Stages[0].Status != "success" || detail.DurationSeconds == nil || *detail.DurationSeconds != 152.5 ||
		detail.Commit == nil || detail.Commit.Additions != 10 || detail.Commit.Files == nil || *detail.Commit.Files != 3 ||
		len(detail.Commit.Paths) != 3 || detail.Commit.Title != "Use the pooled client" || len(detail.Unavailable) != 0 {
		t.Fatalf("pipeline detail: %+v commit=%+v", detail, detail.Commit)
	}
	raw, _ = json.Marshal(detail)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("pipeline detail carries a commit body or diff text: %s", raw)
	}
	// A pipeline of an MR's source branch is in scope.
	if branch, err := f.service.ReadPipeline(t.Context(), f.viewer, "app", "dev", f.serviceID, 14); err != nil ||
		branch.Commit == nil || len(branch.Stages) != 1 || branch.Stages[0].Status != "failed" {
		t.Fatalf("source branch pipeline: %+v %v", branch, err)
	}
	// A pipeline of another branch is not this service's change.
	if _, err := f.service.ReadPipeline(t.Context(), f.viewer, "app", "dev", f.serviceID, 13); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pipeline outside the scope: %v", err)
	}
}

func TestReadServiceChangesFailsClosed(t *testing.T) {
	f := newChangesFixture(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A tool that is off leaves its list unknown, not empty.
	exec(`UPDATE v3_connectors SET tools='[{"name":"list_pipelines","enabled":true},{"name":"list_merge_requests","enabled":true}]' WHERE id='gitlab-dev'`)
	got, err := f.read(t)
	if err != nil || got.Deployments != nil || got.Unavailable["deployments"] != "not_configured" || got.Pipelines == nil {
		t.Fatalf("deployments tool off: %+v %v", got, err)
	}
	if _, err := f.service.ReadPipeline(ctx, f.viewer, "app", "dev", f.serviceID, 11); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("pipeline tool off: %v", err)
	}
	exec(`UPDATE v3_connectors SET tools=$1 WHERE id='gitlab-dev'`, allTools())

	// More than one page is too large, never a truncated list.
	f.gitlab.set("/api/v4/projects/42/pipelines", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Next-Page", "2")
		fmt.Fprint(w, `[]`)
	})
	if _, err := f.read(t); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("second page: %v", err)
	}
	for status, want := range map[int]error{http.StatusUnauthorized: ErrCredential, http.StatusInternalServerError: ErrUnavailable} {
		f.gitlab.set("/api/v4/projects/42/pipelines", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		if _, err := f.read(t); !errors.Is(err, want) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
	// An upstream object of another project is refused.
	f.gitlab.set("/api/v4/projects/42/pipelines", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `[{"id":1,"project_id":43,"status":"success","ref":"main","sha":%q,"source":"push","created_at":"2026-10-11T00:00:00Z","updated_at":"2026-10-11T00:00:00Z"}]`, shaMerged)
	})
	if _, err := f.read(t); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("other project: %v", err)
	}
	f.gitlab.set("/api/v4/projects/42/pipelines", nil)

	// The source changes while GitLab is read: nothing is returned.
	f.gitlab.set("/api/v4/projects/42/merge_requests", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := f.db.Exec(context.Background(), `UPDATE v3_connectors SET credential_ref='vault://isolated/rotated#token' WHERE id='gitlab-dev'`); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `[]`)
	})
	if _, err := f.read(t); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("source changed during the read: %v", err)
	}
	f.gitlab.set("/api/v4/projects/42/merge_requests", nil)
	exec(`UPDATE v3_connectors SET credential_ref='vault://isolated/gitlab#token' WHERE id='gitlab-dev'`)

	// No catalog scope, no code project or no grant: no read at all.
	saved := f.catalog
	f.catalog = v3telemetry.Catalog{}
	if _, err := f.read(t); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no catalog scope: %v", err)
	}
	f.catalog = saved
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors'`)
	if _, err := f.read(t); !errors.Is(err, ErrNotVisible) {
		t.Fatalf("no connector grant: %v", err)
	}
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','connectors','view','environment','dev')`)
	exec(`DELETE FROM v3_service_code_projects`)
	if _, err := f.read(t); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no code project: %v", err)
	}
	// A window beyond seven days is refused before any read.
	now := time.Now()
	if _, err := f.service.ReadServiceChanges(ctx, f.viewer, "app", "dev", f.serviceID, now.Add(-8*24*time.Hour), now); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("eight day window: %v", err)
	}
}
