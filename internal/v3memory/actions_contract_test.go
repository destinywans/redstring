package v3memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/migrations"
)

// This fixture exercises the registered HTTP routes with real session and V3
// policy reads. Its small tables provide only the columns these routes need;
// every test owns and drops its own PostgreSQL schema.
type actionsFixture struct {
	t   *testing.T
	db  *pgxpool.Pool
	svc *Service
	api *httpapi.Server
}

func newActionsFixture(t *testing.T) *actionsFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_memory_actions_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	f := &actionsFixture{t: t, db: db}
	for _, statement := range []string{
		`CREATE TABLE users(id text PRIMARY KEY,email text,name text,role text,password_hash text,source text NOT NULL DEFAULT 'local',disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz)`,
		`CREATE TABLE sessions(token_hash text PRIMARY KEY,user_id text,expires_at timestamptz)`,
		`CREATE TABLE rbac_platform_administrators(user_id text)`,
		`CREATE TABLE rbac_user_roles(user_id text,role_id text)`,
		`CREATE TABLE rbac_department_roles(user_id text,role_id text)`,
		`CREATE TABLE v3_role_grants(user_id text)`,
		`CREATE TABLE applications(id text PRIMARY KEY,key text NOT NULL DEFAULT '',name text NOT NULL DEFAULT '',owner_team_id text,archived boolean NOT NULL DEFAULT false)`,
		`CREATE TABLE application_environments(application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false)`,
		`CREATE TABLE v3_application_environments(application_id text,environment_id text)`,
		`CREATE TABLE v3_effective_role_grants(user_id text,module text,action text,scope_kind text,scope_id text,v3_identity_kind text NOT NULL DEFAULT 'human',role_kind text NOT NULL DEFAULT 'human')`,
		`CREATE TABLE knowledge_documents(id text PRIMARY KEY,application_id text,body text,source text NOT NULL DEFAULT '',version integer,content_hash text,lifecycle text,metadata jsonb)`,
		`CREATE TABLE v3_runbooks(document_id text PRIMARY KEY)`,
		`CREATE TABLE management_audit(id bigserial PRIMARY KEY,actor_id text,actor_kind text,application_id text,action text,target text,outcome text,changes jsonb)`,
	} {
		f.exec(statement)
	}
	for _, name := range []string{"113_v3_memory_facts.sql", "114_v3_memory_conflicts.sql"} {
		sql, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(string(sql))
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,name,role,password_hash) VALUES('editor','editor@example.invalid','Editor','viewer',''),('stranger','stranger@example.invalid','Stranger','viewer','')`,
		`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('` + store.Hash("actions-token") + `','editor',now()+interval '1 hour'),('` + store.Hash("stranger-token") + `','stranger',now()+interval '1 hour')`,
		`INSERT INTO applications(id,owner_team_id) VALUES('app','ops')`,
		`INSERT INTO application_environments(application_id,key) VALUES('app','dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES
		 ('editor','environment','view','global','*'),
		 ('editor','environment','view','environment','stage'),
		 ('editor','knowledge','view','application','app'),
		 ('editor','knowledge','view','team','ops'),
		 ('editor','knowledge','manage','application','app'),
		 ('editor','knowledge','manage','team','ops')`,
		`INSERT INTO knowledge_documents(id,application_id,body,version,content_hash,lifecycle,metadata)
		 VALUES('source','app','Rollback restored traffic. Latency normalized. Error rate decreased.',2,'source-hash','published',
		 '{"environment_ids":["dev"],"team_id":"ops"}')`,
	} {
		f.exec(statement)
	}
	st := &store.Store{DB: db}
	policy := &rbac.V3Policy{Store: st}
	f.svc = New(db, policy)
	f.api = httpapi.New(st, nil, true)
	f.svc.Register(f.api)
	err = f.api.ConfigureV3Environment(
		httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "stage"}},
		policy, st.Session,
		func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *actionsFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(f.t.Context(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f *actionsFixture) call(method, path, body, env, token string) *httptest.ResponseRecorder {
	f.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if env != "" {
		r.Header.Set("X-AIOps-Environment", env)
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}

func (f *actionsFixture) post(path, body string) *httptest.ResponseRecorder {
	return f.call(http.MethodPost, path, body, "dev", "actions-token")
}

func (f *actionsFixture) want(w *httptest.ResponseRecorder, status int, code string) {
	f.t.Helper()
	if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
		f.t.Fatalf("status=%d body=%s; want %d %s", w.Code, w.Body.String(), status, code)
	}
}

func (f *actionsFixture) propose(excerpt string) Fact {
	f.t.Helper()
	fact, err := f.svc.ProposeFromDocument(f.t.Context(), core.User{ID: "editor"}, CandidateInput{
		DocumentID: "source", EnvironmentID: "dev", ExactExcerpt: excerpt,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return fact
}

func (f *actionsFixture) record(a, b Fact) Conflict {
	f.t.Helper()
	c, err := f.svc.RecordConflict(f.t.Context(), core.User{ID: "editor"}, a.ID, b.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *actionsFixture) state(id string) (string, int64) {
	f.t.Helper()
	var state string
	var revision int64
	if err := f.db.QueryRow(f.t.Context(), `SELECT state,revision FROM v3_memory_facts WHERE id=$1`, id).Scan(&state, &revision); err != nil {
		f.t.Fatal(err)
	}
	return state, revision
}

func (f *actionsFixture) auditCount(action string) int {
	f.t.Helper()
	var count int
	if err := f.db.QueryRow(f.t.Context(), `SELECT count(*) FROM management_audit WHERE action=$1`, action).Scan(&count); err != nil {
		f.t.Fatal(err)
	}
	return count
}

func TestMemoryActionsHTTPTransitionsAndCurrentGrants(t *testing.T) {
	f := newActionsFixture(t)
	confirmed := f.propose("Rollback restored traffic.")
	rejected := f.propose("Latency normalized.")
	expired := f.propose("Error rate decreased.")

	path := "/api/v1/memory/facts/" + confirmed.ID + "/confirm"
	w := f.post(path, "")
	f.want(w, 200, "")
	var result struct {
		Fact              Fact     `json:"fact"`
		FactRedacted      bool     `json:"fact_redacted"`
		Partial           bool     `json:"partial"`
		UnavailableFields []string `json:"unavailable_fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Fact.ID != confirmed.ID ||
		result.Fact.State != "confirmed" || result.Fact.Revision != 2 || result.Fact.Confirmed == nil ||
		result.Fact.Source != "source" || result.Fact.SourceVersion != 2 || result.FactRedacted || !result.Partial ||
		len(result.UnavailableFields) != 3 {
		t.Fatalf("confirm response: %+v error=%v body=%s", result, err, w.Body.String())
	}
	if state, rev := f.state(confirmed.ID); state != "confirmed" || rev != 2 || f.auditCount("memory.fact.confirm") != 1 {
		t.Fatalf("confirm persisted state=%s revision=%d", state, rev)
	}
	f.want(f.post(path, ""), 409, "conflict")

	for _, tc := range []struct {
		fact   Fact
		action string
		state  string
	}{
		{rejected, "reject", "rejected"},
		{expired, "expire", "expired"},
	} {
		w = f.post("/api/v1/memory/facts/"+tc.fact.ID+"/"+tc.action, "")
		f.want(w, 200, "")
		if !strings.Contains(w.Body.String(), `"fact_redacted":true`) ||
			strings.Contains(w.Body.String(), tc.fact.Fact) || strings.Contains(w.Body.String(), `"source":`) {
			t.Fatalf("%s leaked source: %s", tc.action, w.Body.String())
		}
		if state, rev := f.state(tc.fact.ID); state != tc.state || rev != 2 || f.auditCount("memory.fact."+tc.action) != 1 {
			t.Fatalf("%s persisted state=%s revision=%d", tc.action, state, rev)
		}
	}
	w = f.post("/api/v1/memory/facts/"+confirmed.ID+"/expire", "")
	f.want(w, 200, "")
	if state, rev := f.state(confirmed.ID); state != "expired" || rev != 3 {
		t.Fatalf("confirmed fact expiration state=%s revision=%d", state, rev)
	}
	f.want(f.post("/api/v1/memory/facts/"+confirmed.ID+"/confirm", ""), 409, "conflict")
	f.want(f.post("/api/v1/memory/facts/"+confirmed.ID+"/forget", ""), 404, "not_found")
	f.want(f.post("/api/v1/memory/facts/missing/confirm", ""), 404, "not_found")
	f.want(f.call(http.MethodPost, path, "", "stage", "actions-token"), 404, "not_found")
	f.want(f.call(http.MethodPost, path, "", "all", "actions-token"), 409, "env_view_only")
	f.want(f.call(http.MethodPost, path, "", "dev", ""), 401, "unauthorized")
}

func TestMemoryActionsHTTPRejectsRevokedScopeAndStaleSource(t *testing.T) {
	f := newActionsFixture(t)
	fact := f.propose("Rollback restored traffic.")
	path := "/api/v1/memory/facts/" + fact.ID + "/confirm"
	listed := f.call(http.MethodGet, "/api/v1/memory/facts", "", "dev", "actions-token")
	f.want(listed, 200, "")
	if !strings.Contains(listed.Body.String(), fact.ID) {
		t.Fatalf("current fact missing from list: %s", listed.Body.String())
	}
	for _, tc := range []struct {
		module, action, kind, id string
	}{
		{"environment", "view", "global", "*"},
		{"knowledge", "manage", "application", "app"},
		{"knowledge", "manage", "team", "ops"},
	} {
		f.exec(`DELETE FROM v3_effective_role_grants WHERE user_id='editor' AND module=$1 AND action=$2 AND scope_kind=$3 AND scope_id=$4`, tc.module, tc.action, tc.kind, tc.id)
		f.want(f.post(path, ""), 403, "forbidden")
		if state, rev := f.state(fact.ID); state != "candidate" || rev != 1 {
			t.Fatalf("revoked %s/%s changed fact: %s %d", tc.module, tc.kind, state, rev)
		}
		f.exec(`INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('editor',$1,$2,$3,$4)`, tc.module, tc.action, tc.kind, tc.id)
	}
	f.exec(`UPDATE users SET disabled=true WHERE id='editor'`)
	f.want(f.post(path, ""), 401, "unauthorized")
	f.exec(`UPDATE users SET disabled=false WHERE id='editor'`)
	for _, tc := range []struct{ mutate, restore string }{
		{`UPDATE knowledge_documents SET version=3 WHERE id='source'`, `UPDATE knowledge_documents SET version=2 WHERE id='source'`},
		{`UPDATE knowledge_documents SET content_hash='revised' WHERE id='source'`, `UPDATE knowledge_documents SET content_hash='source-hash' WHERE id='source'`},
		{`UPDATE knowledge_documents SET lifecycle='archived' WHERE id='source'`, `UPDATE knowledge_documents SET lifecycle='published' WHERE id='source'`},
		{`UPDATE knowledge_documents SET metadata='{"environment_ids":["dev"],"team_id":"other"}' WHERE id='source'`, `UPDATE knowledge_documents SET metadata='{"environment_ids":["dev"],"team_id":"ops"}' WHERE id='source'`},
		{`UPDATE knowledge_documents SET metadata='{"environment_ids":["dev"," dev "],"team_id":"ops"}' WHERE id='source'`, `UPDATE knowledge_documents SET metadata='{"environment_ids":["dev"],"team_id":"ops"}' WHERE id='source'`},
		{`UPDATE applications SET archived=true WHERE id='app'`, `UPDATE applications SET archived=false WHERE id='app'`},
		{`UPDATE applications SET owner_team_id='other' WHERE id='app'`, `UPDATE applications SET owner_team_id='ops' WHERE id='app'`},
		{`UPDATE application_environments SET archived=true WHERE application_id='app' AND key='dev'`, `UPDATE application_environments SET archived=false WHERE application_id='app' AND key='dev'`},
	} {
		f.exec(tc.mutate)
		f.want(f.post(path, ""), 409, "conflict")
		if strings.Contains(tc.mutate, "UPDATE applications") || strings.Contains(tc.mutate, "UPDATE application_environments") || strings.Contains(tc.mutate, `" dev "`) {
			listed := f.call(http.MethodGet, "/api/v1/memory/facts", "", "dev", "actions-token")
			f.want(listed, 200, "")
			if strings.Contains(listed.Body.String(), fact.ID) {
				t.Fatalf("inactive binding remained in fact list after %q: %s", tc.mutate, listed.Body.String())
			}
		}
		if state, rev := f.state(fact.ID); state != "candidate" || rev != 1 {
			t.Fatalf("stale source changed fact after %q: %s %d", tc.mutate, state, rev)
		}
		f.exec(tc.restore)
	}
	f.exec(`ALTER TABLE management_audit ADD CONSTRAINT reject_confirm CHECK (action <> 'memory.fact.confirm')`)
	f.want(f.post(path, ""), 503, "memory_unavailable")
	if state, rev := f.state(fact.ID); state != "candidate" || rev != 1 {
		t.Fatalf("audit failure partially confirmed fact: %s %d", state, rev)
	}
	f.exec(`ALTER TABLE management_audit DROP CONSTRAINT reject_confirm`)
	if f.auditCount("memory.fact.confirm") != 0 {
		t.Fatal("rejected writes created a confirm audit")
	}
}

func TestMemoryActionsHTTPResolveAndAuditRollback(t *testing.T) {
	f := newActionsFixture(t)
	a := f.propose("Rollback restored traffic.")
	b := f.propose("Latency normalized.")
	c := f.propose("Error rate decreased.")
	conflict := f.record(a, b)
	path := "/api/v1/memory/conflicts/resolve"
	valid := `{"keep":"` + a.ID + `","drop":"` + b.ID + `"}`
	for _, invalid := range []struct{ body, code string }{
		{`{"keep":"` + a.ID + `"}`, "invalid_memory_input"},
		{`{"keep":"` + a.ID + `","drop":"` + a.ID + `"}`, "invalid_memory_input"},
		{`{"keep":"` + a.ID + `","drop":"` + b.ID + `","decision":"keep"}`, "invalid_body"},
	} {
		f.want(f.post(path, invalid.body), 422, invalid.code)
	}
	f.want(f.post(path, `{"keep":"`+a.ID+`","drop":"missing"}`), 404, "not_found")
	f.want(f.post(path, `{"keep":"`+a.ID+`","drop":"`+c.ID+`"}`), 404, "not_found")
	f.want(f.call(http.MethodPost, path, valid, "stage", "actions-token"), 404, "not_found")
	f.exec(`ALTER TABLE management_audit ADD CONSTRAINT reject_resolution CHECK (action <> 'memory.conflict.resolved')`)
	f.want(f.post(path, valid), 503, "memory_unavailable")
	for _, fact := range []Fact{a, b} {
		if state, rev := f.state(fact.ID); state != "candidate" || rev != 1 {
			t.Fatalf("audit failure partially updated %s: %s %d", fact.ID, state, rev)
		}
	}
	var state string
	if err := f.db.QueryRow(t.Context(), `SELECT state FROM v3_memory_conflicts WHERE id=$1`, conflict.ID).Scan(&state); err != nil || state != "open" {
		t.Fatalf("audit failure partially resolved conflict: %s %v", state, err)
	}
	f.exec(`ALTER TABLE management_audit DROP CONSTRAINT reject_resolution`)
	w := f.post(path, valid)
	f.want(w, 200, "")
	var result struct{ ID, State, Keep, Drop string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ID != conflict.ID || result.State != "resolved" || result.Keep != a.ID || result.Drop != b.ID {
		t.Fatalf("resolve response: %+v %v %s", result, err, w.Body.String())
	}
	if state, rev := f.state(a.ID); state != "confirmed" || rev != 2 {
		t.Fatalf("winner: %s %d", state, rev)
	}
	if state, rev := f.state(b.ID); state != "rejected" || rev != 2 {
		t.Fatalf("loser: %s %d", state, rev)
	}
	if f.auditCount("memory.conflict.resolved") != 1 {
		t.Fatal("resolve audit count is not one")
	}
	f.want(f.post(path, valid), 409, "conflict")
}

func TestMemoryActionsHTTPRevokedGrantDoesNotRevealInvalidBinding(t *testing.T) {
	f := newActionsFixture(t)
	a := f.propose("Rollback restored traffic.")
	b := f.propose("Latency normalized.")
	conflict := f.record(a, b)
	factPath := "/api/v1/memory/facts/" + a.ID + "/confirm"
	resolveBody := `{"keep":"` + a.ID + `","drop":"` + b.ID + `"}`
	f.exec(`DELETE FROM v3_effective_role_grants WHERE user_id='editor' AND module='knowledge' AND action='manage' AND scope_kind='team' AND scope_id='ops'`)

	// Missing permission has the same response whether the persisted application
	// binding is still active, archived, or reassigned to another owner team.
	f.want(f.post(factPath, ""), 403, "forbidden")
	f.want(f.post("/api/v1/memory/conflicts/resolve", resolveBody), 403, "forbidden")
	for _, tc := range []struct{ mutate, restore string }{
		{`UPDATE applications SET archived=true WHERE id='app'`, `UPDATE applications SET archived=false WHERE id='app'`},
		{`UPDATE applications SET owner_team_id='other' WHERE id='app'`, `UPDATE applications SET owner_team_id='ops' WHERE id='app'`},
		{`UPDATE application_environments SET archived=true WHERE application_id='app' AND key='dev'`, `UPDATE application_environments SET archived=false WHERE application_id='app' AND key='dev'`},
	} {
		f.exec(tc.mutate)
		f.want(f.post(factPath, ""), 403, "forbidden")
		f.want(f.post("/api/v1/memory/conflicts/resolve", resolveBody), 403, "forbidden")
		f.exec(tc.restore)
	}
	for _, fact := range []Fact{a, b} {
		if state, rev := f.state(fact.ID); state != "candidate" || rev != 1 {
			t.Fatalf("revoked writer changed fact %s: %s %d", fact.ID, state, rev)
		}
	}
	var state string
	if err := f.db.QueryRow(t.Context(), `SELECT state FROM v3_memory_conflicts WHERE id=$1`, conflict.ID).Scan(&state); err != nil || state != "open" {
		t.Fatalf("revoked writer changed conflict: %s %v", state, err)
	}
	if f.auditCount("memory.fact.confirm") != 0 || f.auditCount("memory.conflict.resolved") != 0 {
		t.Fatal("revoked writer created action audit")
	}
}

func TestMemoryActionsHTTPSerializesSourceAndGrantRevocation(t *testing.T) {
	f := newActionsFixture(t)
	fact := f.propose("Rollback restored traffic.")
	path := "/api/v1/memory/facts/" + fact.ID + "/confirm"
	sourceTx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sourceTx.Exec(t.Context(), `UPDATE knowledge_documents SET version=3 WHERE id='source'`); err != nil {
		sourceTx.Rollback(t.Context())
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- f.post(path, "") }()
	select {
	case w := <-result:
		sourceTx.Rollback(t.Context())
		t.Fatalf("confirm escaped source lock: %d %s", w.Code, w.Body.String())
	case <-time.After(125 * time.Millisecond):
	}
	if err = sourceTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		f.want(w, 409, "conflict")
	case <-time.After(3 * time.Second):
		t.Fatal("confirm did not complete after source update")
	}
	f.exec(`UPDATE knowledge_documents SET version=2 WHERE id='source'`)

	// A V3 role writer holds an exclusive table lock while it revokes the team
	// grant. The fact mutation must wait and read the committed new grant set.
	grantTx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = grantTx.Exec(t.Context(), `LOCK TABLE v3_role_grants IN ROW EXCLUSIVE MODE`); err != nil {
		grantTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err = grantTx.Exec(t.Context(), `DELETE FROM v3_effective_role_grants WHERE user_id='editor' AND module='knowledge' AND action='manage' AND scope_kind='team'`); err != nil {
		grantTx.Rollback(t.Context())
		t.Fatal(err)
	}
	result = make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- f.post(path, "") }()
	select {
	case w := <-result:
		grantTx.Rollback(t.Context())
		t.Fatalf("confirm escaped grant lock: %d %s", w.Code, w.Body.String())
	case <-time.After(125 * time.Millisecond):
	}
	if err = grantTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		f.want(w, 403, "forbidden")
	case <-time.After(3 * time.Second):
		t.Fatal("confirm did not complete after grant revoke")
	}
	if state, rev := f.state(fact.ID); state != "candidate" || rev != 1 || f.auditCount("memory.fact.confirm") != 0 {
		t.Fatalf("blocked mutations changed fact: %s %d", state, rev)
	}
}
