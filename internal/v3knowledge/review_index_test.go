package v3knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRunbookApprovalReportsSemanticIndexFailureAndCanRetry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public"); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "v3_review_index_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	know := knowledge.New(db, searchEmbedder{fail: true})
	if err := know.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Production registration installs a legacy ACL that may differ from the
	// already verified V3 knowledge manager grant.
	know.Authorize = func(context.Context, string, string, string) error { return store.ErrForbidden }
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO rbac_roles(id,name) VALUES('index-reviewer','Index Reviewer')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('index-reviewer','knowledge','manage','application','app-1')`,
		`INSERT INTO applications(id,name,key,environment) VALUES('app-1','App One','app-one','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-1-dev','app-1','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-1','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	maintainer, err := st.CreateUser(ctx, core.User{Email: "maintainer@example.invalid", Name: "Maintainer", Role: "viewer"}, "isolated-maintainer-password")
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := st.CreateUser(ctx, core.User{Email: "reviewer@example.invalid", Name: "Reviewer", Role: "viewer"}, "isolated-reviewer-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'index-reviewer')`, reviewer.ID); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(knowledge.Metadata{MaintainerID: maintainer.ID, Visibility: "application", EnvironmentIDs: []string{"dev"}})
	otherEnvMetadata, _ := json.Marshal(knowledge.Metadata{MaintainerID: maintainer.ID, Visibility: "application", EnvironmentIDs: []string{"test"}})
	if _, err := db.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
	VALUES
 ('runbook-index','app-1','Review indexing','A verified read-only diagnostic procedure.','runbook://v3/runbook-index',1,'hash',$1,'review',$2),
 ('runbook-other-env','app-1','Wrong environment','Another procedure.','runbook://v3/runbook-other-env',1,'hash',$1,'published',$3),
 ('not-runbook','app-1','Wrong source','Another document.','import://not-runbook',1,'hash',$1,'published',$2)`, maintainer.ID, metadata, otherEnvMetadata); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "reviewer@example.invalid", "isolated-reviewer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	(&Service{DB: db, Knowledge: know, Policy: policy}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session,
		func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	reindex := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks/"+id+"/reindex", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if early := reindex("runbook-index"); early.Code != 409 || !strings.Contains(early.Body.String(), `"code":"invalid_state"`) {
		t.Fatalf("review state must not be reindexed: %d %s", early.Code, early.Body.String())
	}
	for _, id := range []string{"runbook-other-env", "not-runbook"} {
		if hidden := reindex(id); hidden.Code != 404 || !strings.Contains(hidden.Body.String(), `"code":"not_found"`) {
			t.Fatalf("%s must be excluded by persisted scope or source: %d %s", id, hidden.Code, hidden.Body.String())
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks/runbook-index/reviews", strings.NewReader(`{"decision":"approve"}`))
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"published"`) ||
		!strings.Contains(w.Body.String(), `"index_status":"failed"`) ||
		!strings.Contains(w.Body.String(), `"semantic_search_ready":false`) ||
		!strings.Contains(w.Body.String(), `"index_retry_required":true`) {
		t.Fatalf("publication must disclose failed semantic indexing: %d %s", w.Code, w.Body.String())
	}
	var lifecycle, generationStatus string
	var active *int64
	if err := db.QueryRow(ctx, `SELECT d.lifecycle,d.active_generation,g.status FROM knowledge_documents d
 JOIN knowledge_generations g ON g.document_id=d.id WHERE d.id='runbook-index'`).Scan(&lifecycle, &active, &generationStatus); err != nil ||
		lifecycle != "published" || active != nil || generationStatus != "failed" {
		t.Fatalf("review and failed generation must both be durable, got %q/%v/%q: %v", lifecycle, active, generationStatus, err)
	}
	failedRetry := reindex("runbook-index")
	if failedRetry.Code != 200 || !strings.Contains(failedRetry.Body.String(), `"index_status":"failed"`) ||
		!strings.Contains(failedRetry.Body.String(), `"semantic_search_ready":false`) ||
		!strings.Contains(failedRetry.Body.String(), `"index_retry_required":true`) {
		t.Fatalf("failed retry must disclose semantic failure: %d %s", failedRetry.Code, failedRetry.Body.String())
	}
	know.Embedding = searchEmbedder{}
	retried := reindex("runbook-index")
	if retried.Code != 200 || !strings.Contains(retried.Body.String(), `"index_status":"ready"`) ||
		!strings.Contains(retried.Body.String(), `"semantic_search_ready":true`) ||
		!strings.Contains(retried.Body.String(), `"index_retry_required":false`) {
		t.Fatalf("V3 manager retry did not activate semantic index: %d %s", retried.Code, retried.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT active_generation FROM knowledge_documents WHERE id='runbook-index'`).Scan(&active); err != nil || active == nil {
		t.Fatalf("retry did not activate a ready generation: %v %v", active, err)
	}
	var retryAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target='runbook-index' AND action='runbook.reindex.requested' AND actor_id=$1`, reviewer.ID).Scan(&retryAudits); err != nil || retryAudits != 2 {
		t.Fatalf("both V3 retry attempts must be audited: %d %v", retryAudits, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
 VALUES('runbook-ready','app-1','Ready on review','A second verified diagnostic procedure.','runbook://v3/runbook-ready',1,'hash',$1,'review',$2)`, maintainer.ID, metadata); err != nil {
		t.Fatal(err)
	}
	readyRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks/runbook-ready/reviews", strings.NewReader(`{"decision":"approve"}`))
	readyRequest.Header.Set("X-AIOps-Environment", "dev")
	readyRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	readyResponse := httptest.NewRecorder()
	api.ServeHTTP(readyResponse, readyRequest)
	if readyResponse.Code != 200 || !strings.Contains(readyResponse.Body.String(), `"index_status":"ready"`) ||
		!strings.Contains(readyResponse.Body.String(), `"semantic_search_ready":true`) ||
		!strings.Contains(readyResponse.Body.String(), `"index_retry_required":false`) {
		t.Fatalf("successful publication must activate semantic indexing: %d %s", readyResponse.Code, readyResponse.Body.String())
	}
	active = nil
	if err := db.QueryRow(ctx, `SELECT active_generation FROM knowledge_documents WHERE id='runbook-ready'`).Scan(&active); err != nil || active == nil {
		t.Fatalf("approval did not activate its ready generation: %v %v", active, err)
	}
}
