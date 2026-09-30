package v3changes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type testResolver struct{ token string }

func (r testResolver) Resolve(_ context.Context, ref string) (string, error) {
	if ref != "vault://isolated/gitlab#token" || r.token == "" {
		return "", errors.New("missing credential")
	}
	return r.token, nil
}

func TestGitLabPipelineProtocolBoundedAndReadOnly(t *testing.T) {
	const secret = "synthetic-gitlab-token"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/42/pipelines" ||
			r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "20" ||
			r.Header.Get("PRIVATE-TOKEN") != secret {
			t.Errorf("unexpected GitLab request method=%s path=%s query=%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("X-Next-Page", "3")
		_, _ = w.Write([]byte(`[{"id":9,"project_id":42,"status":"success","ref":"main","sha":"abcd","source":"push","created_at":"2026-09-28T10:00:00.123Z","updated_at":"2026-09-28T10:01:00.456Z"}]`))
	}))
	defer server.Close()
	s := Service{Resolver: testResolver{token: secret}}
	target := target{ProjectID: "42", Endpoint: server.URL, AuthMethod: "pat", CredentialRef: "vault://isolated/gitlab#token"}
	items, next, err := s.fetch(t.Context(), target, 2)
	if err != nil || next != 3 || len(items) != 1 || items[0].ID != 9 ||
		items[0].CreatedAt.Nanosecond() != 123000000 || requests != 1 {
		t.Fatalf("pipeline result items=%+v next=%d requests=%d err=%v", items, next, requests, err)
	}
	for _, endpoint := range []string{"http://example.invalid", "https://user:pass@example.invalid", "https://example.invalid?token=bad"} {
		target.Endpoint = endpoint
		if _, _, err := s.fetch(t.Context(), target, 1); !errors.Is(err, errDisabled) {
			t.Fatalf("unsafe endpoint %q: %v", endpoint, err)
		}
	}
	if requests != 1 {
		t.Fatalf("unsafe endpoint caused HTTP request: %d", requests)
	}
}

func TestGitLabPipelineRejectsRedirectOversizeAndWrongProject(t *testing.T) {
	redirectFollowed := false
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectFollowed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer targetServer.Close()
	mode := "redirect"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "redirect":
			http.Redirect(w, r, targetServer.URL, http.StatusFound)
		case "oversize":
			_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
		case "wrong_project":
			_, _ = w.Write([]byte(`[{"id":9,"project_id":43,"status":"success","created_at":"2026-09-28T10:00:00Z","updated_at":"2026-09-28T10:01:00Z"}]`))
		case "invalid_status":
			_, _ = w.Write([]byte(`[{"id":9,"project_id":42,"status":"invented","created_at":"2026-09-28T10:00:00Z","updated_at":"2026-09-28T10:01:00Z"}]`))
		case "inverted_time":
			_, _ = w.Write([]byte(`[{"id":9,"project_id":42,"status":"success","created_at":"2026-09-28T10:01:00Z","updated_at":"2026-09-28T10:00:00Z"}]`))
		case "future_time":
			_, _ = w.Write([]byte(`[{"id":9,"project_id":42,"status":"success","created_at":"2999-01-01T00:00:00Z","updated_at":"2999-01-01T00:00:01Z"}]`))
		}
	}))
	defer server.Close()
	s := Service{Resolver: testResolver{token: "synthetic-token"}}
	target := target{ProjectID: "42", Endpoint: server.URL, AuthMethod: "pat", CredentialRef: "vault://isolated/gitlab#token"}
	for _, selected := range []string{"redirect", "oversize", "wrong_project", "invalid_status", "inverted_time", "future_time"} {
		mode = selected
		if _, _, err := s.fetch(t.Context(), target, 1); !errors.Is(err, errUpstream) {
			t.Fatalf("%s: %v", selected, err)
		}
	}
	if redirectFollowed {
		t.Fatal("redirect was followed with credential")
	}
}

func TestChangesRecordsHTTPPersistedBindingScopeAndRevocation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_changes_records_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-token"
	commitSHA := strings.Repeat("a", 40)
	parentSHA := strings.Repeat("b", 40)
	remoteCalls := 0
	revokeDuringRead := false
	changeBindingDuringRead := false
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls++
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != secret {
			t.Errorf("unexpected GitLab call: %s", r.URL.Path)
		}
		if revokeDuringRead {
			_, err := db.Exec(context.Background(), `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`)
			if err != nil {
				t.Errorf("revoke grant: %v", err)
			}
		}
		if changeBindingDuringRead {
			_, err := db.Exec(context.Background(), `UPDATE v3_connector_targets SET external_id='43' WHERE id='binding-1'`)
			if err != nil {
				t.Errorf("change binding: %v", err)
			}
		}
		switch r.URL.Path {
		case "/api/v4/projects/42/pipelines":
			_, _ = w.Write([]byte(`[{"id":9,"project_id":42,"status":"success","ref":"main","sha":"abcd","source":"push","created_at":"2026-09-28T10:00:00.123Z","updated_at":"2026-09-28T10:01:00.456Z"}]`))
		case "/api/v4/projects/42/merge_requests":
			_, _ = w.Write([]byte(`[{"iid":7,"project_id":42,"target_project_id":42,"state":"merged","source_branch":"topic","target_branch":"main","sha":"abcd","created_at":"2026-09-28T10:00:00Z","updated_at":"2026-09-28T10:02:00Z","merged_at":"2026-09-28T10:01:00Z"}]`))
		case "/api/v4/projects/42/repository/commits/" + commitSHA:
			if r.URL.RawQuery != "stats=false" {
				t.Errorf("commit query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"id":%q,"title":"Bound commit","authored_date":"2026-09-28T10:00:00Z","committed_date":"2026-09-28T10:01:00Z","parent_ids":[%q],"message":"private body"}`, commitSHA, parentSHA)))
		default:
			t.Errorf("unexpected GitLab path: %s", r.URL.Path)
		}
	}))
	defer gitlab.Close()
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','environment','view','environment','dev')`,
		`INSERT INTO applications(id,name,key,environment,archived) VALUES ('app','App','app','dev',false),('other','Other','other','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES ('app-dev','app','dev','Dev','[]'),('other-dev','other','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app','dev'),('other','dev')`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools)
 VALUES ('gitlab-dev','dev','gitlab','GitLab','delivery',$1,'pat','vault://isolated/gitlab#token',true,'[{"name":"list_pipelines","enabled":true}]')`, gitlab.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES ('binding-1','app','dev','gitlab-dev','project','42',(SELECT id FROM users WHERE email='admin@example.invalid'))`)
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", policy, testResolver{token: secret}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("app\x00dev"))
	otherService := base64.RawURLEncoding.EncodeToString([]byte("other\x00dev"))
	path := "/api/v1/changes/records?" + url.Values{"service_id": {serviceID}, "binding_id": {"binding-1"}}.Encode()
	mrPath := "/api/v1/changes/merge-requests?" + url.Values{"service_id": {serviceID}, "binding_id": {"binding-1"}, "state": {"merged"}, "target_branch": {"main"}}.Encode()
	commitPath := "/api/v1/changes/commits/" + commitSHA + "?" + url.Values{"service_id": {serviceID}, "binding_id": {"binding-1"}}.Encode()
	otherPath := "/api/v1/changes/records?" + url.Values{"service_id": {otherService}, "binding_id": {"binding-1"}}.Encode()
	request := func(target, env, cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("X-AIOps-Environment", env)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("status=%d body=%s want=%d/%s", w.Code, w.Body.String(), status, code)
		}
	}
	assert(request(path, "dev", ""), 401, "unauthorized")
	assert(request(mrPath, "dev", ""), 401, "unauthorized")
	assert(request(commitPath, "dev", ""), 401, "unauthorized")
	assert(request(path, "dev", session), 404, "not_found")
	assert(request(mrPath, "dev", session), 404, "not_found")
	assert(request(commitPath, "dev", session), 404, "not_found")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','services','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	assert(request(path, "dev", session), 404, "not_found")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	assert(request(otherPath, "dev", session), 404, "not_found")
	assert(request(path, "test", session), 403, "forbidden")
	assert(request(mrPath, "test", session), 403, "forbidden")
	assert(request(commitPath, "test", session), 403, "forbidden")
	assert(request(mrPath, "dev", session), 503, "source_unavailable")
	assert(request(commitPath, "dev", session), 503, "source_unavailable")
	assert(request(strings.Replace(commitPath, commitSHA, "main", 1), "dev", session), 422, "invalid_sha")
	for _, invalid := range []struct{ query, code string }{
		{"&page=1001", "invalid_page"},
		{"&state=unknown", "invalid_state"},
		{"&target_branch=bad%0Abranch", "invalid_branch"},
	} {
		assert(request(mrPath+invalid.query, "dev", session), 422, invalid.code)
	}
	good := request(path, "dev", session)
	assert(good, 200, "")
	var result struct {
		Items    []record `json:"items"`
		Page     int      `json:"page"`
		PageSize int      `json:"page_size"`
	}
	if err := json.Unmarshal(good.Body.Bytes(), &result); err != nil || len(result.Items) != 1 ||
		result.Items[0].ID != "9" || result.Items[0].CreatedAt != "2026-09-28T10:00:00.123Z" ||
		result.Page != 1 || result.PageSize != 20 {
		t.Fatalf("records response: %s error=%v", good.Body.String(), err)
	}
	if strings.Contains(good.Body.String(), secret) || strings.Contains(good.Body.String(), "vault://") {
		t.Fatalf("credential leaked in response: %s", good.Body.String())
	}
	if remoteCalls != 1 {
		t.Fatalf("remoteCalls=%d; unauthorized request reached GitLab", remoteCalls)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET tools='[{"name":"list_pipelines","enabled":true},{"name":"list_merge_requests","enabled":true},{"name":"get_commit_diff","enabled":true}]' WHERE id='gitlab-dev'`); err != nil {
		t.Fatal(err)
	}
	mergeResponse := request(mrPath, "dev", session)
	assert(mergeResponse, 200, "")
	var mergeResult struct {
		Items    []mergeRequestRecord `json:"items"`
		PageSize int                  `json:"page_size"`
	}
	if err := json.Unmarshal(mergeResponse.Body.Bytes(), &mergeResult); err != nil || len(mergeResult.Items) != 1 ||
		mergeResult.Items[0].IID != "7" || mergeResult.Items[0].MergedAt == nil || mergeResult.PageSize != 20 {
		t.Fatalf("merge request response: %s error=%v", mergeResponse.Body.String(), err)
	}
	if strings.Contains(mergeResponse.Body.String(), secret) || strings.Contains(mergeResponse.Body.String(), "vault://") {
		t.Fatalf("credential leaked in merge response: %s", mergeResponse.Body.String())
	}
	if remoteCalls != 2 {
		t.Fatalf("merge request calls=%d", remoteCalls)
	}
	assert(request(commitPath, "dev", session), 503, "source_unavailable")
	if remoteCalls != 2 {
		t.Fatalf("get_commit_diff enabled a metadata read: calls=%d", remoteCalls)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET tools='[{"name":"list_pipelines","enabled":true},{"name":"list_merge_requests","enabled":true},{"name":"get_commit_diff","enabled":true},{"name":"get_commit","enabled":true}]' WHERE id='gitlab-dev'`); err != nil {
		t.Fatal(err)
	}
	commitResponse := request(commitPath, "dev", session)
	assert(commitResponse, 200, "")
	var commitResult commitRecord
	if err := json.Unmarshal(commitResponse.Body.Bytes(), &commitResult); err != nil ||
		commitResult.SHA != commitSHA || commitResult.Title != "Bound commit" ||
		len(commitResult.ParentSHAs) != 1 || commitResult.ParentSHAs[0] != parentSHA {
		t.Fatalf("commit response: %s error=%v", commitResponse.Body.String(), err)
	}
	if strings.Contains(commitResponse.Body.String(), secret) || strings.Contains(commitResponse.Body.String(), "vault://") ||
		strings.Contains(commitResponse.Body.String(), "private body") {
		t.Fatalf("sensitive commit data leaked: %s", commitResponse.Body.String())
	}
	if remoteCalls != 3 {
		t.Fatalf("commit calls=%d", remoteCalls)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=false WHERE id='gitlab-dev'`); err != nil {
		t.Fatal(err)
	}
	assert(request(path, "dev", session), 404, "not_found")
	assert(request(mrPath, "dev", session), 404, "not_found")
	assert(request(commitPath, "dev", session), 404, "not_found")
	if remoteCalls != 3 {
		t.Fatalf("disabled connector reached GitLab: remoteCalls=%d", remoteCalls)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=true WHERE id='gitlab-dev'`); err != nil {
		t.Fatal(err)
	}
	revokeDuringRead = true
	assert(request(mrPath, "dev", session), 404, "not_found")
	if remoteCalls != 4 {
		t.Fatalf("remoteCalls=%d; missing revocation check", remoteCalls)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	assert(request(path, "dev", session), 404, "not_found")
	if remoteCalls != 5 {
		t.Fatalf("remoteCalls=%d; missing pipeline revocation check", remoteCalls)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	assert(request(commitPath, "dev", session), 404, "not_found")
	if remoteCalls != 6 {
		t.Fatalf("remoteCalls=%d; missing commit revocation check", remoteCalls)
	}
	revokeDuringRead = false
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	changeBindingDuringRead = true
	assert(request(mrPath, "dev", session), 409, "source_changed")
	if remoteCalls != 7 {
		t.Fatalf("remoteCalls=%d; missing binding change check", remoteCalls)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connector_targets SET external_id='42' WHERE id='binding-1'`); err != nil {
		t.Fatal(err)
	}
	assert(request(commitPath, "dev", session), 409, "source_changed")
	if remoteCalls != 8 {
		t.Fatalf("remoteCalls=%d; missing commit binding change check", remoteCalls)
	}
}
