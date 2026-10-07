package v3memory

import (
	"context"
	"encoding/json"
	"errors"
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

type testAccess struct{}

func (testAccess) AuthorizeEnvironment(_ context.Context, user core.User, env string) error {
	if user.ID == "editor" && env == "dev" {
		return nil
	}
	return store.ErrForbidden
}
func (testAccess) Authorize(_ context.Context, user core.User, d rbac.V3Decision) error {
	if user.ID != "editor" || d.EnvironmentID != "dev" || d.Action != "manage" && d.Action != "view" {
		return store.ErrForbidden
	}
	return nil
}
func (a testAccess) AuthorizeWithTx(ctx context.Context, _ pgx.Tx, user core.User, d rbac.V3Decision) error {
	return a.Authorize(ctx, user, d)
}

func TestPersistedFactProvenanceScopeAndAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_memory_" + store.ID()
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
	for _, statement := range []string{
		`CREATE TABLE users(id text PRIMARY KEY,email text,name text,role text,password_hash text,v3_identity_kind text NOT NULL DEFAULT 'human',source text NOT NULL DEFAULT 'local',disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz)`,
		`CREATE TABLE sessions(token_hash text PRIMARY KEY,user_id text,expires_at timestamptz)`,
		`CREATE TABLE rbac_platform_administrators(user_id text)`,
		`CREATE TABLE rbac_user_roles(user_id text,role_id text)`,
		`CREATE TABLE rbac_department_roles(user_id text,role_id text)`,
		`CREATE TABLE v3_role_grants(user_id text)`,
		`CREATE TABLE applications(id text PRIMARY KEY,key text NOT NULL DEFAULT '',name text NOT NULL DEFAULT '',owner_team_id text,archived boolean NOT NULL DEFAULT false)`,
		`CREATE TABLE application_environments(application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false)`,
		`CREATE TABLE v3_application_environments(application_id text,environment_id text)`,
		`CREATE TABLE v3_effective_role_grants(user_id text,module text,action text,scope_kind text,scope_id text)`,
		`CREATE TABLE knowledge_documents(id text PRIMARY KEY,application_id text,body text,source text NOT NULL DEFAULT '',version integer,content_hash text,lifecycle text,metadata jsonb)`,
		`CREATE TABLE v3_runbooks(document_id text PRIMARY KEY)`,
		`CREATE TABLE management_audit(id bigserial PRIMARY KEY,actor_id text,actor_kind text,application_id text,action text,target text,outcome text,changes jsonb)`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"113_v3_memory_facts.sql", "114_v3_memory_conflicts.sql"} {
		sql, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO users(id,email,name,role,password_hash) VALUES('editor','editor@example.invalid','Editor','viewer',''),('stranger','stranger@example.invalid','Stranger','viewer','')`,
		`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('` + store.Hash("test-token") + `','editor',now()+interval '1 hour')`,
		`INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('editor','knowledge','view','global','*')`,
		`INSERT INTO applications(id,owner_team_id) VALUES('app','ops')`,
		`INSERT INTO application_environments(application_id,key) VALUES('app','dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO knowledge_documents(id,application_id,body,version,content_hash,lifecycle,metadata)
		  VALUES('doc','app','Rollback restored traffic. Latency normalized.',2,'source-hash','published',
		  '{"environment_ids":["dev"],"team_id":"ops"}')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	s := New(db, testAccess{})
	editor := core.User{ID: "editor"}
	if _, err = s.ProposeFromDocument(ctx, editor, CandidateInput{"doc", "prod", "Rollback restored traffic."}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong environment: %v", err)
	}
	if _, err = s.ProposeFromDocument(ctx, editor, CandidateInput{"doc", "dev", "Fabricated conclusion"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not an exact source excerpt: %v", err)
	}
	if _, err = s.ProposeFromDocument(ctx, core.User{ID: "stranger"}, CandidateInput{"doc", "dev", "Rollback restored traffic."}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("unauthorized candidate: %v", err)
	}
	a, err := s.ProposeFromDocument(ctx, editor, CandidateInput{"doc", "dev", "Rollback restored traffic."})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ProposeFromDocument(ctx, editor, CandidateInput{"doc", "dev", "Latency normalized."})
	if err != nil {
		t.Fatal(err)
	}
	if a.SourceVersion != 2 || a.State != "candidate" || a.ApplicationID != "app" || a.TeamID != "ops" {
		t.Fatalf("incorrect source binding: %+v", a)
	}
	c, err := s.RecordConflict(ctx, editor, a.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State != "open" || c.EnvironmentID != "dev" {
		t.Fatalf("incorrect conflict: %+v", c)
	}
	var auditChanges string
	if err = db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE action='memory.fact.proposed' AND target=$1`, a.ID).Scan(&auditChanges); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditChanges, a.Fact) || !strings.Contains(auditChanges, "source_version") {
		t.Fatalf("audit contains content or lost provenance: %s", auditChanges)
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_memory_facts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("failed writes leaked across transaction: count=%d err=%v", count, err)
	}
	st := &store.Store{DB: db}
	api := httpapi.New(st, nil, true)
	s.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, testAccess{}, st.Session,
		func(_ context.Context, user core.User, module, action, env string) error {
			if user.ID == "editor" && module == "knowledge" && action == "view" && env == "dev" {
				return nil
			}
			return store.ErrForbidden
		}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "test-token"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	facts := call("GET", "/api/v1/memory/facts", "")
	if facts.Code != 200 || !strings.Contains(facts.Body.String(), "\"partial\":true") {
		t.Fatalf("partial facts: %d %s", facts.Code, facts.Body.String())
	}
	var listed struct {
		Items []Fact `json:"items"`
	}
	if err = json.Unmarshal(facts.Body.Bytes(), &listed); err != nil || len(listed.Items) != 2 {
		t.Fatalf("fact projection: %+v %v", listed, err)
	}
	if w := call("GET", "/api/v1/memory", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"partial":true`) ||
		!strings.Contains(w.Body.String(), `"fact_ids"`) || !strings.Contains(w.Body.String(), `"conflicts"`) {
		t.Fatalf("source-backed view unavailable: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/memory/facts/"+a.ID+"/confirm", ""); w.Code != 200 {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	// Hold the source row update open. Resolve must wait for the knowledge
	// writer, then observe the new version and reject the stale winner.
	sourceTx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sourceTx.Exec(ctx, `UPDATE knowledge_documents SET version=3 WHERE id='doc'`); err != nil {
		sourceTx.Rollback(ctx)
		t.Fatal(err)
	}
	resolved := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		resolved <- call("POST", "/api/v1/memory/conflicts/resolve", `{"keep":"`+a.ID+`","drop":"`+b.ID+`"}`)
	}()
	select {
	case w := <-resolved:
		sourceTx.Rollback(ctx)
		t.Fatalf("resolve escaped source lock: %d %s", w.Code, w.Body.String())
	case <-time.After(150 * time.Millisecond):
	}
	if err = sourceTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-resolved:
		if w.Code != 409 {
			t.Fatalf("resolve after source update: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resolve did not complete after source update")
	}
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET version=3 WHERE id='doc'`); err != nil {
		t.Fatal(err)
	}
	facts = call("GET", "/api/v1/memory/facts", "")
	if facts.Code != 200 || !strings.Contains(facts.Body.String(), "\"items\":[]") ||
		strings.Contains(facts.Body.String(), a.Fact) {
		t.Fatalf("stale source projection: %d %s", facts.Code, facts.Body.String())
	}
	if w := call("POST", "/api/v1/memory/facts/"+b.ID+"/confirm", ""); w.Code != 409 {
		t.Fatalf("stale source confirm: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/memory/conflicts/resolve", `{"keep":"`+a.ID+`","drop":"`+b.ID+`"}`); w.Code != 409 {
		t.Fatalf("stale source resolve: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET version=2 WHERE id='doc'`); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		change, restore string
	}{
		{`UPDATE knowledge_documents SET lifecycle='archived' WHERE id='doc'`, `UPDATE knowledge_documents SET lifecycle='published' WHERE id='doc'`},
		{`UPDATE knowledge_documents SET metadata='{"environment_ids":["dev"],"team_id":"other"}' WHERE id='doc'`, `UPDATE knowledge_documents SET metadata='{"environment_ids":["dev"],"team_id":"ops"}' WHERE id='doc'`},
		{`UPDATE knowledge_documents SET metadata='{"environment_ids":["stage"],"team_id":"ops"}' WHERE id='doc'`, `UPDATE knowledge_documents SET metadata='{"environment_ids":["dev"],"team_id":"ops"}' WHERE id='doc'`},
		{`UPDATE knowledge_documents SET application_id=NULL WHERE id='doc'`, `UPDATE knowledge_documents SET application_id='app' WHERE id='doc'`},
		{`UPDATE knowledge_documents SET body='The excerpt was removed.' WHERE id='doc'`, `UPDATE knowledge_documents SET body='Rollback restored traffic. Latency normalized.' WHERE id='doc'`},
		{`DELETE FROM v3_application_environments WHERE application_id='app' AND environment_id='dev'`, `INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`},
	} {
		if _, err = db.Exec(ctx, mutation.change); err != nil {
			t.Fatal(err)
		}
		facts = call("GET", "/api/v1/memory/facts", "")
		if facts.Code != 200 || !strings.Contains(facts.Body.String(), "\"items\":[]") || strings.Contains(facts.Body.String(), a.Fact) {
			t.Fatalf("stale source leaked after %s: %d %s", mutation.change, facts.Code, facts.Body.String())
		}
		if _, err = db.Exec(ctx, mutation.restore); err != nil {
			t.Fatal(err)
		}
	}
	if w := call("POST", "/api/v1/memory/conflicts/resolve", `{"keep":"`+a.ID+`","drop":"`+b.ID+`"}`); w.Code != 200 {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET lifecycle='archived' WHERE id='doc'`); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/v1/memory/facts/"+a.ID+"/expire", ""); w.Code != 200 ||
		!strings.Contains(w.Body.String(), "\"fact_redacted\":true") ||
		strings.Contains(w.Body.String(), a.Fact) || strings.Contains(w.Body.String(), "\"source\":") {
		t.Fatalf("expire: %d %s", w.Code, w.Body.String())
	}
	var confirmedAt *string
	if err = db.QueryRow(ctx, `SELECT confirmed_at::text FROM v3_memory_facts WHERE id=$1`, a.ID).Scan(&confirmedAt); err != nil || confirmedAt == nil {
		t.Fatalf("expiration lost confirmed provenance: %v %v", confirmedAt, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action IN ('memory.fact.confirm','memory.conflict.resolved')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("governance audit: count=%d err=%v", count, err)
	}
	if err = db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE action='memory.conflict.resolved' AND target=$1`, c.ID).Scan(&auditChanges); err != nil || strings.Contains(auditChanges, a.Fact) {
		t.Fatalf("conflict audit contains content: %s %v", auditChanges, err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM knowledge_documents WHERE id='doc'`); err != nil {
		t.Fatal(err)
	}
	facts = call("GET", "/api/v1/memory/facts", "")
	if facts.Code != 200 || !strings.Contains(facts.Body.String(), "\"items\":[]") || strings.Contains(facts.Body.String(), a.Fact) {
		t.Fatalf("deleted source leaked: %d %s", facts.Code, facts.Body.String())
	}
}
