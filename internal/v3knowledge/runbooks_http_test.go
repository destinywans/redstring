package v3knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestRunbookSubmitAndDryRunRegisteredHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminDB.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public"); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	schema := "v3_runbook_http_" + store.ID()
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
	// pgvector may already be installed in public; keep all test tables in the
	// isolated schema while allowing the extension type to resolve.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	know := knowledge.New(db, nil)
	if err := know.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES('runbook-manager','Runbook Manager'),('runbook-reader','Runbook Reader'),('runbook-team-manager','Runbook Team Manager'),('runbook-creator','Runbook Creator')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('runbook-creator','knowledge','manage','environment','dev'),
 ('runbook-manager','knowledge','manage','application','app-1'),
 ('runbook-manager','knowledge','view','application','app-1'),
	 ('runbook-reader','knowledge','view','application','app-1'),
	 ('runbook-team-manager','knowledge','view','team','ops'),
	 ('runbook-team-manager','knowledge','manage','team','ops')`,
		`INSERT INTO teams(id,name) VALUES('ops','Operations'),('other-team','Other Team')`,
		`INSERT INTO applications(id,name,key,environment) VALUES
 ('app-1','App One','app-one','dev'),('app-2','App Two','app-two','dev')`,
		`UPDATE applications SET owner_team_id='ops' WHERE id='app-1'`,
		`UPDATE applications SET owner_team_id='other-team' WHERE id='app-2'`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('app-1-dev','app-1','dev','Dev'),('app-1-test','app-1','test','Test'),('app-2-dev','app-2','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-1','dev'),('app-1','test'),('app-2','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	maintainer, err := st.CreateUser(ctx, core.User{Email: "maintainer@example.invalid", Name: "Maintainer", Role: "viewer"}, "isolated-maintainer-password")
	if err != nil {
		t.Fatal(err)
	}
	otherManager, err := st.CreateUser(ctx, core.User{Email: "other@example.invalid", Name: "Other Manager", Role: "viewer"}, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	teamMaintainer, err := st.CreateUser(ctx, core.User{Email: "team@example.invalid", Name: "Team Maintainer", Role: "viewer"}, "isolated-team-password")
	if err != nil {
		t.Fatal(err)
	}
	creator, err := st.CreateUser(ctx, core.User{Email: "creator@example.invalid", Name: "Creator", Role: "viewer"}, "isolated-creator-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES
	 ($1,'runbook-manager'),($2,'runbook-manager'),($3,'runbook-reader'),($4,'runbook-team-manager'),($5,'runbook-creator')`, maintainer.ID, otherManager.ID, reader.ID, teamMaintainer.ID, creator.ID); err != nil {
		t.Fatal(err)
	}
	seed := func(id, app, env, lifecycle, owner string, steps []Step) {
		t.Helper()
		if steps == nil {
			steps = []Step{}
		}
		// Deliberately misleading TeamID proves authorization resolves the
		// application's real owner_team_id, not document metadata.
		environments := []string{env}
		if env == "" {
			environments = nil
		}
		metadata, _ := json.Marshal(knowledge.Metadata{MaintainerID: owner, TeamID: "ops", ActorID: owner, Origin: "human", Visibility: "application", EnvironmentIDs: environments})
		stepJSON, _ := json.Marshal(steps)
		if _, err := db.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
 VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9)`, id, app, id, "# "+id, "runbook://v3/"+id, store.Hash(id), owner, lifecycle, metadata); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO v3_runbooks(document_id,source_kind,steps) VALUES($1,'blank',$2::jsonb)`, id, string(stepJSON)); err != nil {
			t.Fatal(err)
		}
	}
	seed("runbook-main", "app-1", "dev", "draft", maintainer.ID, []Step{
		{N: 1, Kind: "query", Title: "Read metrics", Readonly: true, Connectors: []string{"prometheus"}},
		{N: 2, Kind: "action", Title: "Restart workload", Code: "kubectl rollout restart", Approval: true},
	})
	seed("runbook-other-app", "app-2", "dev", "draft", maintainer.ID, nil)
	seed("runbook-other-env", "app-1", "test", "draft", maintainer.ID, nil)
	seed("runbook-unknown-env", "app-1", "", "published", maintainer.ID, nil)
	seed("runbook-review", "app-1", "dev", "review", maintainer.ID, nil)
	seed("runbook-public", "app-1", "dev", "published", maintainer.ID, []Step{{N: 1, Kind: "query", Title: "Read metrics", Readonly: true}})
	seed("runbook-team", "app-1", "dev", "draft", teamMaintainer.ID, []Step{{N: 1, Kind: "query", Title: "Read metrics", Readonly: true, Connectors: []string{"prometheus"}}})
	login := func(email, password string) string {
		t.Helper()
		_, token, err := st.Login(ctx, email, password)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	maintainerToken := login("maintainer@example.invalid", "isolated-maintainer-password")
	otherToken := login("other@example.invalid", "isolated-other-password")
	readerToken := login("reader@example.invalid", "isolated-reader-password")
	teamToken := login("team@example.invalid", "isolated-team-password")
	creatorToken := login("creator@example.invalid", "isolated-creator-password")
	for _, upload := range []struct{ id, owner, env string }{
		{"upload-dev", creator.ID, "dev"},
		{"upload-other", otherManager.ID, "dev"},
		{"upload-test", creator.ID, "test"},
	} {
		content := []byte("# Imported source")
		if _, err := db.Exec(ctx, `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,content)
 VALUES($1,$2,$3,'file','source.md',$4,'text/markdown',$5)`, upload.id, upload.owner, upload.env, len(content), content); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	(&Service{DB: db, Knowledge: know, Policy: policy}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(id, operation, env, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks/"+id+"/"+operation, nil)
		r.Header.Set("X-AIOps-Environment", env)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("status=%d body=%s; want status=%d code=%q", w.Code, w.Body.String(), status, code)
		}
	}
	create := func(source, from, env, token string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(createInput{Title: "Source checked", Source: source, From: from,
			Steps: []Step{{N: 1, Kind: "query", Title: "Read source", Readonly: true, Connectors: []string{"prometheus"}}}})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", env)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	templateDraft := httptest.NewRequest(http.MethodGet, "/api/v1/runbooks/draft?source=template&from=investigation", nil)
	templateDraft.Header.Set("X-AIOps-Environment", "dev")
	templateDraft.AddCookie(&http.Cookie{Name: "aiops_session", Value: creatorToken})
	templateResponse := httptest.NewRecorder()
	api.ServeHTTP(templateResponse, templateDraft)
	assert(templateResponse, 200, "")
	if !strings.Contains(templateResponse.Body.String(), `"draft_mode":"template_outline"`) ||
		!strings.Contains(templateResponse.Body.String(), `"steps":[]`) ||
		strings.Contains(templateResponse.Body.String(), `"approval":true`) {
		t.Fatalf("template outline invented executable steps: %s", templateResponse.Body.String())
	}
	assert(create("template", "fabricated", "dev", creatorToken), 422, "invalid_source")
	assert(create("import", "upload-other", "dev", creatorToken), 422, "invalid_source")
	assert(create("import", "upload-test", "dev", creatorToken), 422, "invalid_source")
	assert(create("import", "upload-dev", "all", creatorToken), 409, "env_view_only")
	for _, source := range []struct{ kind, ref string }{{"template", "investigation"}, {"import", "upload-dev"}} {
		w := create(source.kind, source.ref, "dev", creatorToken)
		assert(w, 201, "")
		var saved struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.ID == "" {
			t.Fatalf("create response: %s (%v)", w.Body.String(), err)
		}
		var kind, ref string
		if err := db.QueryRow(ctx, `SELECT source_kind,source_ref FROM v3_runbooks WHERE document_id=$1`, saved.ID).Scan(&kind, &ref); err != nil || kind != source.kind || ref != source.ref {
			t.Fatalf("source provenance %q/%q: got %q/%q: %v", source.kind, source.ref, kind, ref, err)
		}
	}
	assert(request("runbook-main", "submit", "dev", ""), 401, "")
	assert(request("runbook-main", "submit", "all", maintainerToken), 409, "env_view_only")
	assert(request("runbook-main", "dry-run", "all", maintainerToken), 409, "env_view_only")
	assert(request("runbook-other-env", "submit", "test", maintainerToken), 403, "forbidden")
	assert(request("runbook-other-env", "submit", "dev", maintainerToken), 404, "not_found")
	assert(request("runbook-other-app", "submit", "dev", maintainerToken), 403, "forbidden")
	assert(request("runbook-main", "submit", "dev", readerToken), 403, "forbidden")
	assert(request("runbook-main", "submit", "dev", otherToken), 403, "forbidden")
	assert(request("runbook-main", "submit", "dev", teamToken), 403, "forbidden")
	assert(request("runbook-other-app", "submit", "dev", teamToken), 403, "forbidden")
	assert(request("runbook-other-app", "dry-run", "dev", teamToken), 404, "not_found")
	assert(request("runbook-unknown-env", "dry-run", "dev", maintainerToken), 404, "not_found")
	// A published document is readable by this user, but a validation
	// preview creates permanent preview and audit rows.
	assert(request("runbook-public", "dry-run", "dev", readerToken), 404, "not_found")
	var rejectedPreviews, rejectedAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_runbook_validation_previews WHERE document_id='runbook-public'`).Scan(&rejectedPreviews); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target='runbook-public' AND action='runbook.validation_preview'`).Scan(&rejectedAudits); err != nil {
		t.Fatal(err)
	}
	if rejectedPreviews != 0 || rejectedAudits != 0 {
		t.Fatalf("viewer persisted a preview or audit: previews=%d audits=%d", rejectedPreviews, rejectedAudits)
	}
	assert(request("runbook-public", "dry-run", "dev", otherToken), 200, "")
	assert(request("runbook-other-env", "submit", "test", teamToken), 403, "forbidden")
	assert(request("runbook-review", "submit", "dev", maintainerToken), 409, "invalid_state")
	assert(request("runbook-team", "dry-run", "dev", teamToken), 200, "")
	assert(request("runbook-team", "submit", "dev", teamToken), 200, "")
	assert(request("runbook-main", "dry-run", "dev", readerToken), 404, "not_found")
	preview := request("runbook-main", "dry-run", "dev", maintainerToken)
	assert(preview, 200, "")
	var result struct {
		Steps  int              `json:"steps"`
		OK     bool             `json:"ok"`
		Mode   string           `json:"mode"`
		Checks []map[string]any `json:"checks"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil || result.Steps != 2 || result.OK || result.Mode != "validation_only" || len(result.Checks) != 2 ||
		result.Checks[0]["validated"] != true || result.Checks[1]["validated"] != false || result.Checks[1]["requires_approval"] != true {
		t.Fatalf("dry-run falsely validated or executed action: %s (%v)", preview.Body.String(), err)
	}
	for _, check := range result.Checks {
		if check["executed"] != false {
			t.Fatalf("dry-run executed a step: %+v", check)
		}
	}
	var beforeAudit, beforeHistory int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target='runbook-main'`).Scan(&beforeAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_history WHERE document_id='runbook-main'`).Scan(&beforeHistory); err != nil {
		t.Fatal(err)
	}
	if beforeAudit != 1 || beforeHistory != 0 {
		t.Fatalf("validation preview must be audited without changing document history: %d/%d", beforeAudit, beforeHistory)
	}
	var previews int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_runbook_validation_previews WHERE document_id='runbook-main' AND readonly_valid=false`).Scan(&previews); err != nil || previews != 1 {
		t.Fatalf("validation preview was not recorded: %d %v", previews, err)
	}
	submitted := request("runbook-main", "submit", "dev", maintainerToken)
	assert(submitted, 200, "")
	var accepted struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(submitted.Body.Bytes(), &accepted); err != nil || !accepted.OK || accepted.Status != "review" {
		t.Fatalf("incorrect submit response: %s (%v)", submitted.Body.String(), err)
	}
	assert(request("runbook-main", "submit", "dev", maintainerToken), 409, "invalid_state")
	var lifecycle string
	if err := db.QueryRow(ctx, `SELECT lifecycle FROM knowledge_documents WHERE id='runbook-main'`).Scan(&lifecycle); err != nil || lifecycle != "review" {
		t.Fatalf("submit did not persist review lifecycle: %q %v", lifecycle, err)
	}
	var history, audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_history WHERE document_id='runbook-main' AND operation='lifecycle.review' AND actor_id=$1`, maintainer.ID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target='runbook-main' AND action='runbook.submitted' AND actor_id=$1`, maintainer.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if history != 1 || audits != 1 {
		t.Fatalf("submit history/audit must be exactly once, got %d/%d", history, audits)
	}
	review := func(token, decision string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks/runbook-main/reviews", strings.NewReader(`{"decision":"`+decision+`"}`))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert(review(maintainerToken, "approve"), 403, "self_review_forbidden")
	assert(review(readerToken, "approve"), 404, "not_found")
	assert(review(otherToken, "approve"), 200, "")
	assert(review(otherToken, "approve"), 409, "invalid_state")
	recordRequest := httptest.NewRequest(http.MethodGet, "/api/v1/runbooks/runbook-main/record", nil)
	recordRequest.Header.Set("X-AIOps-Environment", "dev")
	recordRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: readerToken})
	record := httptest.NewRecorder()
	api.ServeHTTP(record, recordRequest)
	assert(record, 200, "")
	if !strings.Contains(record.Body.String(), `"partial":true`) || !strings.Contains(record.Body.String(), `"status":"published"`) {
		t.Fatalf("runbook record lacks real publication status or partial boundary: %s", record.Body.String())
	}
	historyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/runbooks/runbook-main/history", nil)
	historyRequest.Header.Set("X-AIOps-Environment", "dev")
	historyRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: readerToken})
	historyResponse := httptest.NewRecorder()
	api.ServeHTTP(historyResponse, historyRequest)
	assert(historyResponse, 200, "")
	if !strings.Contains(historyResponse.Body.String(), `"decision":"approve"`) ||
		!strings.Contains(historyResponse.Body.String(), `"readonly_valid":false`) ||
		!strings.Contains(historyResponse.Body.String(), `"execution_verification_available":false`) {
		t.Fatalf("runbook history omitted recorded review or mislabeled validation: %s", historyResponse.Body.String())
	}
}
