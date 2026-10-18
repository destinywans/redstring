package v3knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	 ('runbook-manager','knowledge','manage','team','ops'),
	 ('runbook-manager','knowledge','view','team','ops'),
	 ('runbook-reader','knowledge','view','application','app-1'),
	 ('runbook-reader','knowledge','view','team','ops'),
	 ('runbook-team-manager','knowledge','view','application','app-1'),
	 ('runbook-team-manager','knowledge','manage','application','app-1'),
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
		teamID := "ops"
		if app == "app-2" {
			teamID = "other-team"
		}
		environments := []string{env}
		if env == "" {
			environments = nil
		}
		metadata, _ := json.Marshal(knowledge.Metadata{MaintainerID: owner, TeamID: teamID, ActorID: owner, Origin: "human", Visibility: "application", EnvironmentIDs: environments})
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
	seed("runbook-mismatch", "app-1", "dev", "published", maintainer.ID, nil)
	if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET metadata=jsonb_set(metadata,'{team_id}','"other-team"') WHERE id='runbook-mismatch'`); err != nil {
		t.Fatal(err)
	}
	seed("runbook-unstructured", "app-1", "dev", "published", maintainer.ID, nil)
	seed("runbook-non-runbook", "app-1", "dev", "published", maintainer.ID, nil)
	seed("runbook-alias", "app-1", "dev", "published", maintainer.ID, []Step{{N: 1, Kind: "act", Title: "Restart", Approval: true}})
	if _, err := db.Exec(ctx, `DELETE FROM v3_runbooks WHERE document_id='runbook-unstructured'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET source='note://other' WHERE id='runbook-non-runbook'`); err != nil {
		t.Fatal(err)
	}
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
	get := func(id, suffix, env, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runbooks/"+id+suffix, nil)
		r.Header.Set("X-AIOps-Environment", env)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	list := func(query, env, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runbooks?"+query, nil)
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
	// A team-only manager may read the postmortem source, but cannot create
	// an app-scoped Runbook that the same actor would immediately lose.
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS ops_incidents(id text PRIMARY KEY,application_id text NOT NULL,environment_id text NOT NULL,investigation_id text NOT NULL,status text NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS investigation_reports(investigation_id text PRIMARY KEY)`,
		`INSERT INTO investigations(id,application_id,title,status) VALUES('runbook-source-inv','app-1','Source','closed')`,
		`INSERT INTO investigation_reports(investigation_id) VALUES('runbook-source-inv')`,
		`INSERT INTO ops_incidents(id,application_id,environment_id,investigation_id,status)
 VALUES('runbook-source-incident','app-1','app-1-dev','runbook-source-inv','resolved')`,
		`INSERT INTO events(investigation_id,type,payload) VALUES('runbook-source-inv','investigation.closed','{}')`,
		`INSERT INTO v3_postmortems(id,incident_id,investigation_id,application_id,environment_id,close_event_seq,sections,actions,status,created_by)
 SELECT 'runbook-source-pm','runbook-source-incident','runbook-source-inv','app-1','dev',seq,'[]','[]','published',(SELECT id FROM users WHERE email='team@example.invalid')
 FROM events WHERE investigation_id='runbook-source-inv' AND type='investigation.closed'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('runbook-team-manager','events','view','team','ops')`,
		`DELETE FROM v3_role_grants WHERE role_id='runbook-team-manager' AND module='knowledge' AND scope_kind='application'`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	assert(create("postmortem", "runbook-source-pm", "dev", teamToken), 403, "forbidden")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('runbook-team-manager','knowledge','view','application','app-1'),
 ('runbook-team-manager','knowledge','manage','application','app-1')`); err != nil {
		t.Fatal(err)
	}
	// Four real sources produce drafts. Only the published postmortem's
	// explicit action becomes an approval step; arbitrary imported commands
	// and conversation prose remain inert text for human review.
	for _, statement := range []string{
		`UPDATE v3_postmortems SET sections='[{"key":"evidence","title":"Observed wait","text":"P95 wait rose"}]',
 actions='[{"text":"Restart worker","owner":"SRE","due":""}]' WHERE id='runbook-source-pm'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('runbook-creator','events','view','application','app-1'),
 ('runbook-creator','knowledge','view','application','app-1'),
 ('runbook-creator','conversation','view','environment','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversations(id,owner_id,environment_id,title)
 VALUES('runbook-conversation',$1,'dev','Observed rollback')`, creator.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state)
 VALUES('runbook-message','runbook-conversation','user','Owner','[{"type":"text","markdown":"Look at queue depth before changing replicas"}]','completed')`); err != nil {
		t.Fatal(err)
	}
	draftIn := func(lang, source, from, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/runbooks/draft?source="+source+"&from="+from, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		if lang != "" {
			r.Header.Set("Accept-Language", lang)
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	draft := func(source, from, token string) *httptest.ResponseRecorder {
		t.Helper()
		return draftIn("", source, from, token)
	}
	for _, source := range []struct{ kind, ref, token, want string }{
		{"template", "investigation", creatorToken, `"draft_mode":"template_outline"`},
		{"postmortem", "runbook-source-pm", creatorToken, `"approval":true`},
		{"conversation", "runbook-conversation", creatorToken, `"Look at queue depth`},
		{"import", "upload-dev", creatorToken, `"Review imported Markdown"`},
	} {
		w := draft(source.kind, source.ref, source.token)
		assert(w, 200, "")
		if !strings.Contains(w.Body.String(), source.want) {
			t.Fatalf("%s draft missing source evidence %s: %s", source.kind, source.want, w.Body.String())
		}
		var generated struct {
			Title        string `json:"title"`
			Steps        []Step `json:"steps"`
			SourceSHA256 string `json:"source_sha256"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &generated); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(createInput{Title: generated.Title, Source: source.kind, From: source.ref, Steps: generated.Steps, ExpectedSourceSHA256: generated.SourceSHA256})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks", bytes.NewReader(body))
		req.Header.Set("X-AIOps-Environment", "dev")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: source.token})
		w = httptest.NewRecorder()
		api.ServeHTTP(w, req)
		assert(w, 201, "")
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
			t.Fatalf("%s create response: %s (%v)", source.kind, w.Body.String(), err)
		}
		var snapshot map[string]string
		if err := db.QueryRow(ctx, `SELECT source_snapshot FROM v3_runbooks WHERE document_id=$1`, created.ID).Scan(&snapshot); err != nil || snapshot["content_sha256"] == "" || snapshot["source_kind"] != source.kind {
			t.Fatalf("%s source snapshot=%v err=%v", source.kind, snapshot, err)
		}
		var version, revisions, history, audit int
		var state string
		if err := db.QueryRow(ctx, `SELECT version,lifecycle FROM knowledge_documents WHERE id=$1`, created.ID).Scan(&version, &state); err != nil || version != 1 || state != "draft" {
			t.Fatalf("%s document version=%d state=%q err=%v", source.kind, version, state, err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_revisions WHERE document_id=$1`, created.ID).Scan(&revisions); err != nil || revisions != 1 {
			t.Fatalf("%s revision count=%d err=%v", source.kind, revisions, err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_history WHERE document_id=$1 AND operation='v3.runbook.created'`, created.ID).Scan(&history); err != nil || history != 1 {
			t.Fatalf("%s history count=%d err=%v", source.kind, history, err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action='runbook.created'`, created.ID).Scan(&audit); err != nil || audit != 1 {
			t.Fatalf("%s audit count=%d err=%v", source.kind, audit, err)
		}
		if source.kind == "postmortem" {
			var sections, actions []byte
			if err := db.QueryRow(ctx, `SELECT sections,actions FROM v3_postmortems WHERE id=$1`, source.ref).Scan(&sections, &actions); err != nil {
				t.Fatal(err)
			}
			if snapshot["content_sha256"] != store.Hash(string(sections)+"\n"+string(actions)) {
				t.Fatalf("postmortem digest does not match exact consumed source")
			}
			var steps []Step
			if err := db.QueryRow(ctx, `SELECT steps FROM v3_runbooks WHERE document_id=$1`, created.ID).Scan(&steps); err != nil || len(steps) != 2 || steps[0].N != 1 || steps[0].Kind != "check" || !steps[0].Readonly || steps[1].N != 2 || steps[1].Kind != "action" || !steps[1].Approval || steps[1].Readonly {
				t.Fatalf("postmortem persisted steps=%+v err=%v", steps, err)
			}
		}
		if source.kind == "conversation" || source.kind == "import" {
			assert(get(created.ID, "/record", "dev", otherToken), 404, "not_found")
		}
	}
	assert(draft("postmortem", "runbook-source-pm", otherToken), 422, "invalid_source")
	// PA-63: a manual incident's published postmortem seeds a Runbook from its
	// recorded resolved status event; reopening the incident or unpublishing
	// the postmortem gives a specific reason instead of "not found".
	for _, statement := range []string{
		`INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,actor_id,request_key,request_hash)
 VALUES('INC-runbook-case','app-1','app-1-dev','dev','Bad release','P2',$1,'case-request-1',repeat('a',64))`,
		`INSERT INTO v3_incident_case_status_events(case_id,revision,from_status,to_status,actor_id,request_id,request_hash)
 VALUES('INC-runbook-case',2,'active','resolved',$1,'case-status-1',repeat('b',64))`,
		`UPDATE v3_incident_cases c SET status='resolved',status_revision=2,resolved_status_event_id=se.id,resolved_at=se.created_at
 FROM v3_incident_case_status_events se WHERE se.case_id=c.id AND c.id='INC-runbook-case' AND $1<>''`,
		`INSERT INTO v3_postmortems(id,incident_id,application_id,environment_id,source_kind,source_case_status_event_id,sections,actions,status,created_by)
 SELECT 'runbook-case-pm','INC-runbook-case','app-1','dev','case',se.id,
  '[{"key":"timeline","title":"What happened","text":"Bad release rolled back"}]','[{"text":"Add a release gate","owner":"SRE","due":""}]','published',$1
 FROM v3_incident_case_status_events se WHERE se.case_id='INC-runbook-case'`,
	} {
		if _, err := db.Exec(ctx, statement, creator.ID); err != nil {
			t.Fatal(err)
		}
	}
	var caseDraft struct {
		Steps        []Step `json:"steps"`
		SourceSHA256 string `json:"source_sha256"`
	}
	caseResponse := draft("postmortem", "INC-runbook-case", creatorToken)
	if err := json.Unmarshal(caseResponse.Body.Bytes(), &caseDraft); err != nil || caseResponse.Code != 200 || len(caseDraft.Steps) != 2 ||
		caseDraft.Steps[1].Title != "Add a release gate" || !caseDraft.Steps[1].Approval || caseDraft.SourceSHA256 == "" {
		t.Fatalf("manual incident postmortem draft: %d %s", caseResponse.Code, caseResponse.Body.String())
	}
	// PA-113: the generated title, outline and review step titles follow the
	// request language; the postmortem's own section title and action text
	// stay as they were saved.
	for _, want := range []struct{ lang, title, outline, step string }{
		{"zh-CN", "由复盘 INC-runbook-case 生成的 Runbook", "已发布复盘的章节和改进项是未经核实的来源记录，使用前请逐步核对。", "核对：What happened"},
		{"en-US", "Runbook from postmortem INC-runbook-case", "Published postmortem sections and actions are unverified source notes. Review every step before use.", "Review: What happened"},
	} {
		var localized struct {
			Title   string `json:"title"`
			Outline string `json:"outline"`
			Steps   []Step `json:"steps"`
		}
		w := draftIn(want.lang, "postmortem", "INC-runbook-case", creatorToken)
		if err := json.Unmarshal(w.Body.Bytes(), &localized); err != nil || w.Code != 200 || len(localized.Steps) != 2 ||
			localized.Title != want.title || localized.Outline != want.outline || localized.Steps[0].Title != want.step ||
			localized.Steps[1].Title != "Add a release gate" {
			t.Fatalf("%s postmortem draft text: %d %s", want.lang, w.Code, w.Body.String())
		}
	}
	assert(create("postmortem", "runbook-case-pm", "dev", creatorToken), 201, "")
	assert(draft("postmortem", "runbook-case-pm", otherToken), 422, "invalid_source")
	if _, err := db.Exec(ctx, `UPDATE v3_postmortems SET status='review' WHERE id='runbook-case-pm'`); err != nil {
		t.Fatal(err)
	}
	assert(draft("postmortem", "runbook-case-pm", creatorToken), 422, "source_not_published")
	if _, err := db.Exec(ctx, `UPDATE v3_postmortems SET status='published' WHERE id='runbook-case-pm';
 UPDATE v3_incident_cases SET status='active',status_revision=3,resolved_status_event_id=NULL,resolved_at=NULL WHERE id='INC-runbook-case'`); err != nil {
		t.Fatal(err)
	}
	assert(draft("postmortem", "runbook-case-pm", creatorToken), 422, "source_not_current")
	assert(draft("conversation", "runbook-conversation", otherToken), 422, "invalid_source")
	assert(draft("import", "upload-other", creatorToken), 422, "invalid_source")
	var previousDraft struct {
		Title        string `json:"title"`
		Steps        []Step `json:"steps"`
		SourceSHA256 string `json:"source_sha256"`
	}
	if err := json.Unmarshal(draft("conversation", "runbook-conversation", creatorToken).Body.Bytes(), &previousDraft); err != nil || previousDraft.SourceSHA256 == "" {
		t.Fatalf("conversation draft did not expose source digest: %+v %v", previousDraft, err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_messages SET parts='[{"type":"text","markdown":"Observed changed source"}]' WHERE id='runbook-message'`); err != nil {
		t.Fatal(err)
	}
	staleBody, _ := json.Marshal(createInput{Title: previousDraft.Title, Source: "conversation", From: "runbook-conversation", Steps: previousDraft.Steps, ExpectedSourceSHA256: previousDraft.SourceSHA256})
	staleRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runbooks", bytes.NewReader(staleBody))
	staleRequest.Header.Set("X-AIOps-Environment", "dev")
	staleRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: creatorToken})
	staleResponse := httptest.NewRecorder()
	api.ServeHTTP(staleResponse, staleRequest)
	assert(staleResponse, 409, "source_changed")
	var beforeDocs, beforeAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents WHERE created_by=$1 AND source LIKE 'runbook://v3/%'`, creator.ID).Scan(&beforeDocs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='runbook.created'`, creator.ID).Scan(&beforeAudits); err != nil {
		t.Fatal(err)
	}
	blocked, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.Exec(ctx, `UPDATE v3_postmortems SET status='review' WHERE id='runbook-source-pm'`); err != nil {
		t.Fatal(err)
	}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- create("postmortem", "runbook-source-pm", "dev", creatorToken) }()
	select {
	case early := <-finished:
		t.Fatalf("create bypassed source state lock: %d %s", early.Code, early.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	if err := blocked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assert(<-finished, 422, "source_not_published")
	var afterDocs, afterAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents WHERE created_by=$1 AND source LIKE 'runbook://v3/%'`, creator.ID).Scan(&afterDocs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='runbook.created'`, creator.ID).Scan(&afterAudits); err != nil {
		t.Fatal(err)
	}
	if afterDocs != beforeDocs || afterAudits != beforeAudits {
		t.Fatalf("source state change left partial document or success audit: docs %d->%d audits %d->%d", beforeDocs, afterDocs, beforeAudits, afterAudits)
	}
	blocked, err = db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.Exec(ctx, `UPDATE v3_conversations SET title=title WHERE id='runbook-conversation'`); err != nil {
		t.Fatal(err)
	}
	go func() { finished <- create("conversation", "runbook-conversation", "dev", creatorToken) }()
	select {
	case early := <-finished:
		t.Fatalf("create bypassed conversation lock: %d %s", early.Code, early.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='runbook-creator' AND module='conversation' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if err := blocked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assert(<-finished, 422, "invalid_source")
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents WHERE created_by=$1 AND source LIKE 'runbook://v3/%'`, creator.ID).Scan(&afterDocs); err != nil || afterDocs != beforeDocs {
		t.Fatalf("grant revocation left a Runbook: docs=%d before=%d err=%v", afterDocs, beforeDocs, err)
	}
	// The main detail route projects persisted fields without inventing
	// citation or incident measurements.
	mainDetail := get("runbook-main", "", "dev", maintainerToken)
	assert(mainDetail, 200, "")
	if !strings.Contains(mainDetail.Body.String(), `"complete_v3_dto":false`) ||
		!strings.Contains(mainDetail.Body.String(), `"refs":null`) ||
		!strings.Contains(mainDetail.Body.String(), `"incidents":null`) ||
		!strings.Contains(mainDetail.Body.String(), `"kind":"action"`) {
		t.Fatalf("Runbook detail lost persisted content or claimed unknown metrics: %s", mainDetail.Body.String())
	}
	assert(get("runbook-main", "", "dev", otherToken), 404, "not_found")
	assert(get("runbook-main", "", "dev", readerToken), 404, "not_found")
	assert(get("runbook-review", "", "dev", otherToken), 200, "")
	assert(get("runbook-review", "", "dev", readerToken), 404, "not_found")
	assert(get("runbook-public", "", "dev", readerToken), 200, "")
	listed := list("q=runbook-public&page=1&per_page=1", "dev", readerToken)
	assert(listed, 200, "")
	if !strings.Contains(listed.Body.String(), `"id":"runbook-public"`) ||
		!strings.Contains(listed.Body.String(), `"total":1`) ||
		!strings.Contains(listed.Body.String(), `"refs_30d":null`) ||
		!strings.Contains(listed.Body.String(), `"complete_v3_dto":false`) {
		t.Fatalf("Runbook list lost scoped item or claimed unknown usage: %s", listed.Body.String())
	}
	var publicList struct {
		Items []runbookSummary `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &publicList); err != nil || len(publicList.Items) != 1 {
		t.Fatalf("public list: %s %v", listed.Body.String(), err)
	}
	facet := publicList.Items[0]
	filtered := list("q=runbook-public&owner="+url.QueryEscape(facet.Owner)+"&scope="+url.QueryEscape(facet.Scope)+"&page=1&per_page=1", "dev", readerToken)
	assert(filtered, 200, "")
	if !strings.Contains(filtered.Body.String(), `"total":1`) {
		t.Fatalf("owner/scope did not precede page: %s", filtered.Body.String())
	}
	assert(list("owner=no-such-maintainer&scope="+url.QueryEscape(facet.Scope), "dev", readerToken), 200, "")
	assert(get("runbook-other-app", "", "dev", maintainerToken), 404, "not_found")
	assert(get("runbook-other-env", "", "dev", maintainerToken), 404, "not_found")
	assert(get("runbook-unknown-env", "", "dev", maintainerToken), 404, "not_found")
	assert(get("runbook-non-runbook", "", "dev", readerToken), 404, "not_found")
	assert(get("missing", "", "dev", readerToken), 404, "not_found")
	assert(get("runbook-unstructured", "", "dev", readerToken), 503, "structured_steps_unavailable")
	assert(get("runbook-unstructured", "/record", "dev", readerToken), 503, "structured_steps_unavailable")
	assert(get("runbook-alias", "", "dev", readerToken), 200, "")
	aliasRecord := get("runbook-alias", "/record", "dev", readerToken)
	assert(aliasRecord, 200, "")
	if !strings.Contains(aliasRecord.Body.String(), `"kind":"act"`) || !strings.Contains(aliasRecord.Body.String(), `"approval":true`) {
		t.Fatalf("partial record lost persisted action kind or approval: %s", aliasRecord.Body.String())
	}
	var originalMetadata []byte
	if err := db.QueryRow(ctx, `SELECT metadata FROM knowledge_documents WHERE id='runbook-public'`).Scan(&originalMetadata); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"team_id":null}`,
		`{"team_id":""}`,
		`{"environment_ids":["dev",""]}`,
		`{"environment_ids":["dev",null]}`,
		`{"source_registration_id":"unverified"}`,
	} {
		if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET metadata=metadata || $2::jsonb WHERE id=$1`, "runbook-public", invalid); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"", "/record", "/history"} {
			assert(get("runbook-public", suffix, "dev", readerToken), 404, "not_found")
		}
		if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET metadata=$2::jsonb WHERE id=$1`, "runbook-public", originalMetadata); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{"", "/record", "/history"} {
		assert(get("runbook-mismatch", suffix, "dev", maintainerToken), 404, "not_found")
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='runbook-reader' AND module='knowledge' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	if revokedList := list("q=runbook-public", "dev", readerToken); revokedList.Code != 200 ||
		!strings.Contains(revokedList.Body.String(), `"items":[]`) ||
		!strings.Contains(revokedList.Body.String(), `"all":0`) {
		t.Fatalf("revoked team grant leaked a Runbook list item: %d %s", revokedList.Code, revokedList.Body.String())
	}
	for _, suffix := range []string{"", "/record", "/history"} {
		assert(get("runbook-public", suffix, "dev", readerToken), 404, "not_found")
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('runbook-reader','knowledge','view','team','ops')`); err != nil {
		t.Fatal(err)
	}
	assert(get("runbook-public", "/record", "dev", readerToken), 200, "")
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='other-team' WHERE id='app-1'`); err != nil {
		t.Fatal(err)
	}
	assert(get("runbook-public", "/record", "dev", readerToken), 404, "not_found")
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='ops' WHERE id='app-1'`); err != nil {
		t.Fatal(err)
	}
	assert(get("runbook-main", "", "all", maintainerToken), 200, "")
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
	// PA-194: the response says why a step failed; a passing step has no reasons.
	if fmt.Sprint(result.Checks[0]["reasons"]) != "[]" || fmt.Sprint(result.Checks[1]["reasons"]) != "[action_step not_readonly no_connector]" {
		t.Fatalf("dry-run reasons: %s", preview.Body.String())
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
	if !strings.Contains(record.Body.String(), `"kind":"action"`) || !strings.Contains(record.Body.String(), `"approval":true`) {
		t.Fatalf("runbook record lost persisted action approval: %s", record.Body.String())
	}
	assert(get("runbook-main", "", "dev", readerToken), 200, "")
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
	if _, err := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='runbook-reader'`, reader.ID); err != nil {
		t.Fatal(err)
	}
	assert(get("runbook-public", "", "dev", readerToken), 404, "not_found")
}
