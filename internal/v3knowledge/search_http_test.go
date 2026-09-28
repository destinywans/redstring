package v3knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func isolatedKnowledgeSearchDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates an isolated random schema")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public"); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "test_v3knowledge_" + store.ID()
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
	_, err = db.Exec(ctx, `
CREATE TABLE users(id text PRIMARY KEY,email text NOT NULL,name text NOT NULL,role text NOT NULL,disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz);
CREATE TABLE sessions(token_hash text PRIMARY KEY,user_id text NOT NULL,expires_at timestamptz NOT NULL);
CREATE TABLE rbac_platform_administrators(user_id text PRIMARY KEY);
CREATE TABLE rbac_user_roles(user_id text NOT NULL,role_id text NOT NULL);
CREATE TABLE applications(id text PRIMARY KEY,key text NOT NULL,name text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
CREATE TABLE v3_application_environments(application_id text NOT NULL,environment_id text NOT NULL,PRIMARY KEY(application_id,environment_id));
CREATE TABLE application_environments(id text PRIMARY KEY,application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false);
-- The production object is a view over current role assignments. This
-- isolated route test inserts its effective rows directly, then exercises
-- the same policy SQL as the production route.
CREATE TABLE v3_effective_role_grants(user_id text NOT NULL,module text NOT NULL,action text NOT NULL,scope_kind text NOT NULL,scope_id text NOT NULL,v3_identity_kind text NOT NULL DEFAULT 'human',role_kind text NOT NULL DEFAULT 'human');
CREATE TABLE knowledge_documents(id text PRIMARY KEY,application_id text,title text NOT NULL,source text NOT NULL,version integer NOT NULL,body text NOT NULL,metadata jsonb NOT NULL,lifecycle text NOT NULL,active_generation bigint);
CREATE TABLE knowledge_generations(id bigserial PRIMARY KEY,document_id text NOT NULL,provider text NOT NULL,model text NOT NULL,dimensions integer NOT NULL,status text NOT NULL,document_version integer NOT NULL);
CREATE TABLE knowledge_chunks(generation_id bigint NOT NULL,ordinal integer NOT NULL,content text NOT NULL,embedding vector NOT NULL);
-- Test-double the matcher; the full pinyin dictionary is not needed to
-- prove SQL scope and ranking of ordinary keyword matches here.
CREATE FUNCTION aiops_search_match(value text, query text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$ SELECT strpos(lower(value),lower(query))>0 $$;
INSERT INTO users(id,email,name,role) VALUES('viewer','viewer@local','Viewer','viewer');
INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('`+store.Hash("search-session")+`','viewer',now()+interval '1 hour');
INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES
('viewer','environment','view','environment','test'),
('viewer','environment','view','environment','dev'),
('viewer','knowledge','view','application','z-visible');
INSERT INTO applications(id,key,name) VALUES('z-visible','visible','可见应用');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('z-visible','test'),('z-visible','dev');
INSERT INTO application_environments(id,application_id,key) VALUES('visible-test','z-visible','test'),('visible-dev','z-visible','dev');
INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle) VALUES
('visible-test','z-visible','测试环境故障','test',1,'故障排查内容','{"visibility":"application","environment_ids":["test"]}','published'),
('visible-dev','z-visible','开发环境故障','dev',1,'故障排查内容','{"visibility":"application","environment_ids":["dev"]}','published'),
('visible-prod','z-visible','生产环境故障','prod',1,'故障排查内容','{"visibility":"application","environment_ids":["prod"]}','published');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type searchEmbedder struct{ fail bool }

func (searchEmbedder) Descriptor() inference.EmbeddingDescriptor {
	return inference.EmbeddingDescriptor{Provider: "test", Model: "test"}
}
func (e searchEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	if e.fail {
		return nil, errors.New("embedding unavailable")
	}
	return [][]float32{{1, 0}}, nil
}

type failingSearchIndex struct{}

func (failingSearchIndex) Search(context.Context, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error) {
	return nil, errors.New("vector unavailable")
}
func (failingSearchIndex) SearchScoped(context.Context, string, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error) {
	return nil, errors.New("vector unavailable")
}

type revokingV3Index struct {
	db       *pgxpool.Pool
	acted    bool
	document knowledge.Hit
}

func (i *revokingV3Index) Search(context.Context, string, inference.EmbeddingDescriptor, []float32, int) ([]knowledge.Hit, error) {
	return nil, errors.New("unscoped vector search must not be called")
}
func (i *revokingV3Index) V3SearchScoped(ctx context.Context, _, _, _ string, _ inference.EmbeddingDescriptor, _ []float32, _ int) ([]knowledge.Hit, error) {
	if !i.acted {
		i.acted = true
		if _, err := i.db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE user_id='viewer' AND module='knowledge' AND scope_kind='team'`); err != nil {
			return nil, err
		}
	}
	return []knowledge.Hit{i.document}, nil
}

func TestRegisteredKnowledgeSearchScopesBeforeLimitAndDegrades(t *testing.T) {
	db := isolatedKnowledgeSearchDB(t)
	ctx := context.Background()
	// More than the former 200-row pre-authorization cap sorts before the
	// one visible application. None may displace it or leak into results.
	for i := range 205 {
		id := fmt.Sprintf("a-hidden-%03d", i)
		if _, err := db.Exec(ctx, `INSERT INTO applications(id,key,name) VALUES($1,$1,$1)`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,'test')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO application_environments(id,application_id,key) VALUES($1,$2,'test')`, id+"-test", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle) VALUES('hidden','a-hidden-000','隐藏故障','hidden',1,'故障秘密','{"visibility":"application","environment_ids":["test"]}','published')`); err != nil {
		t.Fatal(err)
	}
	repository := &store.Store{DB: db}
	policy := &rbac.V3Policy{Store: repository}
	knowledgeService := knowledge.NewWithRepository(&knowledge.PGRepository{DB: db}, searchEmbedder{}, failingSearchIndex{})
	service := &Service{DB: db, Knowledge: knowledgeService, Policy: policy}
	server := httpapi.New(repository, nil, true)
	service.RegisterSearch(server)
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "dev"}}, policy, repository.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	post := func(environment, body string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge/search", strings.NewReader(body))
		req.Header.Set("X-AIOps-Environment", environment)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "search-session"})
		}
		result := httptest.NewRecorder()
		server.ServeHTTP(result, req)
		return result
	}
	if got := post("test", `{"q":"故障"}`, false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", got.Code, got.Body.String())
	}
	type response struct {
		Hits     []RetrievalHit `json:"hits"`
		Mode     string         `json:"mode"`
		Degraded bool           `json:"degraded"`
	}
	assertResults := func(got *httptest.ResponseRecorder, expected []string) {
		t.Helper()
		if got.Code != http.StatusOK {
			t.Fatalf("search: %d %s", got.Code, got.Body.String())
		}
		var result response
		if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Mode != "keyword" || !result.Degraded || len(result.Hits) != len(expected) {
			t.Fatalf("unexpected search response: %+v", result)
		}
		for i, title := range expected {
			if result.Hits[i].Title != title || result.Hits[i].Keyword <= 0 || result.Hits[i].Vector != 0 || result.Hits[i].Score <= 0 {
				t.Fatalf("unexpected hit %d: %+v", i, result.Hits[i])
			}
		}
	}
	assertResults(post("test", `{"q":"故障"}`, true), []string{"测试环境故障"})
	assertResults(post("test", `{"q":"故障","service":"visible"}`, true), []string{"测试环境故障"})
	assertResults(post("all", `{"q":"开发"}`, true), []string{"开发环境故障"})
	assertResults(post("all", `{"q":"生产"}`, true), []string{})
	assertResults(post("test", `{"q":"没有匹配"}`, true), []string{})
	_, err := db.Exec(ctx, `
UPDATE applications SET owner_team_id='team-ops' WHERE id='z-visible';
INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle) VALUES
('team-restricted','z-visible','团队限制','team',1,'团队限制内容','{"visibility":"application","team_id":"team-ops","environment_ids":["test"]}','published'),
('team-mismatch','z-visible','伪造归属','team',1,'wrong_scope_marker','{"visibility":"application","team_id":"team-other","environment_ids":["test"]}','published'),
('old-unscoped','z-visible','未知范围','legacy',1,'未知范围内容','{"visibility":"application","origin":"historical_unknown"}','published'),
('old-empty-scope','z-visible','空白范围','legacy',1,'空白范围内容','{"visibility":"application","environment_ids":[]}','published');`)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(post("test", `{"q":"团队限制"}`, true), []string{})
	assertResults(post("test", `{"q":"wrong_scope_marker"}`, true), []string{})
	assertResults(post("test", `{"q":"未知范围"}`, true), []string{})
	assertResults(post("test", `{"q":"空白范围"}`, true), []string{})
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id)
VALUES('viewer','knowledge','view','team','team-ops')`); err != nil {
		t.Fatal(err)
	}
	assertResults(post("test", `{"q":"团队限制"}`, true), []string{"团队限制"})
	assertResults(post("test", `{"q":"wrong_scope_marker"}`, true), []string{})
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE module='knowledge' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	assertResults(post("test", `{"q":"团队限制"}`, true), []string{})
	if _, err := db.Exec(ctx, `UPDATE application_environments SET archived=true WHERE application_id='z-visible' AND key='test'`); err != nil {
		t.Fatal(err)
	}
	if got := post("test", `{"q":"测试环境故障"}`, true); got.Code != 200 ||
		!strings.Contains(got.Body.String(), `"hits":[]`) || !strings.Contains(got.Body.String(), `"mode":"none"`) {
		t.Fatalf("archived environment mapping remained searchable: %d %s", got.Code, got.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET archived=false WHERE application_id='z-visible' AND key='test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE module='environment' AND scope_id='dev'`); err != nil {
		t.Fatal(err)
	}
	assertResults(post("all", `{"q":"开发"}`, true), []string{})
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if got := post("test", `{"q":"故障","service":"a-hidden-000"}`, true); got.Code != http.StatusNotFound {
		t.Fatalf("hidden service: %d %s", got.Code, got.Body.String())
	}
	if got := post("prod", `{"q":"故障"}`, true); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("nonprod must not select prod: %d %s", got.Code, got.Body.String())
	}
	knowledgeService.Embedding = nil
	knowledgeService.Vectors = nil
	assertResults(post("test", `{"q":"故障"}`, true), []string{"测试环境故障"})
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE module='knowledge'`); err != nil {
		t.Fatal(err)
	}
	if got := post("all", `{"q":"故障"}`, true); got.Code != http.StatusOK {
		t.Fatalf("revoked all search: %d %s", got.Code, got.Body.String())
	} else {
		var result response
		if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Hits) != 0 || result.Mode != "none" || result.Degraded {
			t.Fatalf("revoked all search must not call embedding: %+v", result)
		}
	}
	if got := post("test", `{"q":"故障","service":"visible"}`, true); got.Code != http.StatusNotFound {
		t.Fatalf("revoked application: %d %s", got.Code, got.Body.String())
	}
	_, err = db.Exec(ctx, `
INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES
('viewer','knowledge','view','team','team-ops'),
('viewer','knowledge','view','application','team-owned');
INSERT INTO applications(id,key,name,owner_team_id) VALUES
('team-owned','owned','本团队应用','team-ops'),
('team-foreign','foreign','其他团队应用','team-other');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES
('team-owned','test'),('team-foreign','test');
INSERT INTO application_environments(id,application_id,key) VALUES
('team-owned-test','team-owned','test'),('team-foreign-test','team-foreign','test');
INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle) VALUES
('team-owned-test','team-owned','本团队知识','team',1,'团队记录','{"visibility":"application","team_id":"team-ops","environment_ids":["test"]}','published'),
('team-owned-dev','team-owned','未映射环境知识','team',1,'团队记录','{"visibility":"application","team_id":"team-ops","environment_ids":["dev"]}','published'),
('team-foreign-test','team-foreign','其他团队知识','team',1,'团队记录','{"visibility":"application","team_id":"team-other","environment_ids":["test"]}','published');`)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(post("test", `{"q":"本团队"}`, true), []string{"本团队知识"})
	assertResults(post("all", `{"q":"本团队"}`, true), []string{"本团队知识"})
	assertResults(post("test", `{"q":"本团队","service":"owned"}`, true), []string{"本团队知识"})
	if got := post("test", `{"q":"本团队","service":"foreign"}`, true); got.Code != http.StatusNotFound {
		t.Fatalf("foreign team must remain hidden: %d %s", got.Code, got.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES('viewer','knowledge','view','global','*')`); err != nil {
		t.Fatal(err)
	}
	if got := post("test", `{"q":"故障"}`, true); got.Code != http.StatusRequestEntityTooLarge || !strings.Contains(got.Body.String(), "scope_too_large") {
		t.Fatalf("authorized collection over limit: %d %s", got.Code, got.Body.String())
	}
}

func TestRegisteredKnowledgeSearchFiltersRealVectorCandidates(t *testing.T) {
	db := isolatedKnowledgeSearchDB(t)
	ctx := t.Context()
	_, err := db.Exec(ctx, `
UPDATE applications SET owner_team_id='team-ops' WHERE id='z-visible';
INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle) VALUES
('vector-allowed','z-visible','Allowed vector','test',1,'allowed vector evidence','{"visibility":"application","environment_ids":["test"]}','published'),
('vector-team','z-visible','Team vector','test',1,'team vector evidence','{"visibility":"application","environment_ids":["test"],"team_id":"team-ops"}','published'),
('vector-dev','z-visible','Dev vector','test',1,'dev vector evidence','{"visibility":"application","environment_ids":["dev"]}','published'),
('vector-unknown','z-visible','Unknown vector','test',1,'unknown vector evidence','{"visibility":"application"}','published'),
('vector-source','z-visible','Source vector','test',1,'source vector evidence','{"visibility":"application","environment_ids":["test"],"source_registration_id":"unverified"}','published');
INSERT INTO knowledge_generations(document_id,provider,model,dimensions,status,document_version)
 SELECT id,'test','test',2,'ready',1 FROM knowledge_documents WHERE id LIKE 'vector-%';
UPDATE knowledge_documents d SET active_generation=g.id FROM knowledge_generations g WHERE g.document_id=d.id;
INSERT INTO knowledge_chunks(generation_id,ordinal,content,embedding)
 SELECT id,0,'vector evidence','[1,0]'::vector FROM knowledge_generations;`)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	policy := &rbac.V3Policy{Store: st}
	know := knowledge.NewWithRepository(&knowledge.PGRepository{DB: db}, searchEmbedder{}, knowledge.PGVector{DB: db})
	service := &Service{DB: db, Knowledge: know, Policy: policy}
	api := httpapi.New(st, nil, true)
	know.Register(api, true)
	service.RegisterSearch(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	search := func() []RetrievalHit {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge/search", strings.NewReader(`{"q":"no-keyword-match"}`))
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "search-session"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("vector search: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			Hits     []RetrievalHit `json:"hits"`
			Mode     string         `json:"mode"`
			Degraded bool           `json:"degraded"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Mode != "hybrid" || result.Degraded {
			t.Fatalf("real scoped vector search degraded: %+v", result)
		}
		for _, hit := range result.Hits {
			if hit.Vector <= 0 || hit.Keyword != 0 {
				t.Fatalf("not a vector-only result: %+v", hit)
			}
		}
		return result.Hits
	}
	for _, path := range []string{"/api/v1/knowledge?application_id=z-visible", "/api/v1/knowledge/vector-allowed",
		"/api/v1/knowledge/vector-allowed/revisions", "/api/v1/knowledge/vector-allowed/export"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "search-session"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 501 || !strings.Contains(w.Body.String(), `"code":"v3_not_implemented"`) {
			t.Fatalf("legacy body route bypassed V3 gate: %s %d %s", path, w.Code, w.Body.String())
		}
	}
	if got := search(); len(got) != 1 || got[0].Title != "Allowed vector" {
		t.Fatalf("unscoped vector body reached HTTP: %+v", got)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id)
VALUES('viewer','knowledge','view','team','team-ops')`); err != nil {
		t.Fatal(err)
	}
	if got := search(); len(got) != 2 {
		t.Fatalf("team grant did not permit exact team vector: %+v", got)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_effective_role_grants WHERE module='knowledge' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	if got := search(); len(got) != 1 || got[0].Title != "Allowed vector" {
		t.Fatalf("revoked team vector remained visible: %+v", got)
	}
}

func TestRegisteredKnowledgeSearchRechecksRevocationAfterRetrieval(t *testing.T) {
	db := isolatedKnowledgeSearchDB(t)
	ctx := t.Context()
	_, err := db.Exec(ctx, `
UPDATE applications SET owner_team_id='team-ops' WHERE id='z-visible';
INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id)
VALUES('viewer','knowledge','view','team','team-ops');
INSERT INTO knowledge_documents(id,application_id,title,source,version,body,metadata,lifecycle)
VALUES('race-team','z-visible','Revoked team passage','test',1,'revocation_proof',
'{"visibility":"application","environment_ids":["test"],"team_id":"team-ops"}','published');`)
	if err != nil {
		t.Fatal(err)
	}
	index := &revokingV3Index{db: db, document: knowledge.Hit{DocumentID: "race-team", ApplicationID: "z-visible",
		Title: "Revoked team passage", Content: "revocation_proof", Version: 1, Similarity: 1}}
	st := &store.Store{DB: db}
	policy := &rbac.V3Policy{Store: st}
	know := knowledge.NewWithRepository(&knowledge.PGRepository{DB: db}, searchEmbedder{}, index)
	api := httpapi.New(st, nil, true)
	(&Service{DB: db, Knowledge: know, Policy: policy}).RegisterSearch(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge/search", strings.NewReader(`{"q":"revocation_proof","service":"visible"}`))
	r.Header.Set("X-AIOps-Environment", "test")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "search-session"})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if !index.acted || w.Code != 200 || !strings.Contains(w.Body.String(), `"hits":[]`) ||
		strings.Contains(w.Body.String(), "revocation_proof") || strings.Contains(w.Body.String(), "Revoked team passage") {
		t.Fatalf("revoked passage escaped final check: index_acted=%t status=%d body=%s", index.acted, w.Code, w.Body.String())
	}
}
