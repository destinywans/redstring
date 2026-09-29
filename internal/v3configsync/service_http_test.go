package v3configsync

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
)

func isolatedConfigStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates an isolated random schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_v3configsync_" + store.ID()
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
	cfg.MaxConns = 1 // catch queries that borrow a second connection while rows or tx are open
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
CREATE TABLE users(id text PRIMARY KEY,email text NOT NULL,name text NOT NULL,role text NOT NULL,
 disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz,source text NOT NULL DEFAULT 'local');
CREATE TABLE sessions(token_hash text PRIMARY KEY,user_id text NOT NULL,expires_at timestamptz NOT NULL);
CREATE TABLE rbac_platform_administrators(user_id text PRIMARY KEY);
CREATE TABLE rbac_user_roles(user_id text NOT NULL,role_id text NOT NULL);
CREATE TABLE teams(id text PRIMARY KEY,name text NOT NULL);
CREATE TABLE team_members(team_id text NOT NULL,user_id text NOT NULL,PRIMARY KEY(team_id,user_id));
CREATE TABLE applications(id text PRIMARY KEY,name text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
CREATE TABLE application_environments(id text PRIMARY KEY,application_id text NOT NULL,key text NOT NULL,
 name text NOT NULL,archived boolean NOT NULL DEFAULT false);
CREATE TABLE v3_application_environments(application_id text NOT NULL,environment_id text NOT NULL,
 PRIMARY KEY(application_id,environment_id));
CREATE TABLE v3_effective_role_grants(user_id text NOT NULL,module text NOT NULL,action text NOT NULL,
 scope_kind text NOT NULL,scope_id text NOT NULL,v3_identity_kind text NOT NULL DEFAULT 'human',role_kind text NOT NULL DEFAULT 'human');
CREATE TABLE v3_canvas_templates(id text PRIMARY KEY,owner_id text,environment_id text,
 kind text NOT NULL,name text NOT NULL,description text NOT NULL DEFAULT '',nodes jsonb NOT NULL,edges jsonb NOT NULL,
 is_default boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE management_audit(id bigserial PRIMARY KEY,actor_id text NOT NULL,action text NOT NULL,
 target text NOT NULL,outcome text NOT NULL,changes jsonb NOT NULL DEFAULT '{}');
CREATE TABLE knowledge_documents(id text PRIMARY KEY,application_id text,title text NOT NULL,body text NOT NULL,
 source text NOT NULL,version integer NOT NULL,content_hash text NOT NULL,created_by text NOT NULL,
 lifecycle text NOT NULL,metadata jsonb NOT NULL DEFAULT '{}');
CREATE TABLE knowledge_revisions(document_id text NOT NULL,version integer NOT NULL,snapshot jsonb NOT NULL,
 created_by text NOT NULL,PRIMARY KEY(document_id,version));
CREATE TABLE v3_runbooks(document_id text PRIMARY KEY,source_kind text NOT NULL,steps jsonb NOT NULL);
INSERT INTO users(id,email,name,role) VALUES
 ('owner','owner@example.invalid','Owner','viewer'),
 ('admin','admin@example.invalid','Admin','admin'),
 ('viewer','viewer@example.invalid','Viewer','viewer');
INSERT INTO rbac_platform_administrators(user_id) VALUES('admin');
INSERT INTO rbac_user_roles(user_id,role_id) VALUES('owner','viewer'),('viewer','viewer'),('admin','admin');
INSERT INTO teams(id,name) VALUES('team-ops','Operations');
INSERT INTO team_members(team_id,user_id) VALUES('team-ops','owner');
INSERT INTO applications(id,name,owner_team_id) VALUES('app-a','App A','team-ops');
INSERT INTO application_environments(id,application_id,key,name)
 VALUES('app-a-test','app-a','test','Test');
INSERT INTO sessions(token_hash,user_id,expires_at) VALUES
 ('`+store.Hash("owner-token")+`','owner',now()+interval '1 hour'),
 ('`+store.Hash("admin-token")+`','admin',now()+interval '1 hour'),
 ('`+store.Hash("viewer-token")+`','viewer',now()+interval '1 hour');
INSERT INTO v3_effective_role_grants(user_id,module,action,scope_kind,scope_id) VALUES
 ('owner','environment','view','environment','test'),
 ('owner','canvas','change','environment','test'),
 ('admin','environment','view','environment','prod'),
 ('admin','workspace','manage','global','*'),
 ('viewer','environment','view','environment','prod');
INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name,nodes,edges)
VALUES('CT-source','owner','test','plan','Safe plan',
 '[{"id":"node-1","x":16,"y":140,"w":260,"icon":"layers","tone":"info","title":"节点 1","sub":"","blocks":[]}]','[]');`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/120_v3_config_promotion.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	fenceMigration, err := os.ReadFile("../../migrations/121_v3_config_export_attempt_fence.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(fenceMigration)); err != nil {
		t.Fatal(err)
	}
	readMigration, err := os.ReadFile("../../migrations/124_v3_config_sync_read_kinds.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(readMigration)); err != nil {
		t.Fatal(err)
	}
	return &store.Store{DB: db}
}

type fakeRepository struct {
	lastBranch, lastPath string
	payload              []byte
	mr                   MR
	merged               bool
	failOnce             bool
	calls                int
	cancelOnExport       context.CancelFunc
}

type exportGateCall struct {
	branch, path string
	payload      []byte
	reply        chan exportGateResult
}

type exportGateResult struct {
	mr  MR
	err error
}

// Each call stays in flight until the test chooses its remote outcome.
type gatedRepository struct {
	*fakeRepository
	calls chan exportGateCall
}

func (g *gatedRepository) Export(ctx context.Context, branch, path string, payload []byte, _ string) (MR, error) {
	call := exportGateCall{branch: branch, path: path, payload: append([]byte(nil), payload...),
		reply: make(chan exportGateResult, 1)}
	select {
	case g.calls <- call:
	case <-ctx.Done():
		return MR{}, ctx.Err()
	}
	select {
	case result := <-call.reply:
		return result.mr, result.err
	case <-ctx.Done():
		return MR{}, ctx.Err()
	}
}

func gatedMR(iid int64, branch string) MR {
	return MR{IID: iid, URL: fmt.Sprintf("https://gitlab.example.test/ops/config/-/merge_requests/%d", iid),
		State: "opened", SourceBranch: branch, TargetBranch: "main", SourceProjectID: 77, TargetProjectID: 77}
}

func (*fakeRepository) ProjectID() int64     { return 77 }
func (*fakeRepository) BranchPrefix() string { return "aiops-promotion/" }
func (f *fakeRepository) Export(_ context.Context, branch, path string, payload []byte, _ string) (MR, error) {
	f.calls++
	if f.lastBranch != "" && (f.lastBranch != branch || f.lastPath != path || string(f.payload) != string(payload)) {
		return MR{}, ErrConflict
	}
	f.lastBranch, f.lastPath, f.payload = branch, path, append([]byte(nil), payload...)
	f.mr = MR{IID: 42, URL: "https://gitlab.example.test/ops/config/-/merge_requests/42",
		State: "opened", Title: "Promote canvas template CT-source", SourceBranch: branch, TargetBranch: "main", SourceProjectID: 77,
		TargetProjectID: 77, SourceCommitSHA: strings.Repeat("a", 40)}
	f.mr.Author.Name = "Config Bot"
	if f.failOnce {
		f.failOnce = false
		if f.cancelOnExport != nil {
			f.cancelOnExport()
		}
		return MR{}, errors.New("ambiguous upstream response")
	}
	return f.mr, nil
}
func (f *fakeRepository) ListMerged(_ context.Context, _ int) ([]MR, error) {
	if !f.merged {
		return []MR{}, nil
	}
	mr := f.mr
	mr.State, mr.MergeCommitSHA, mr.ChangesCount = "merged", strings.Repeat("b", 40), "1"
	return []MR{mr}, nil
}
func (f *fakeRepository) LookupPromotionMR(_ context.Context, iid int64) (MR, error) {
	if iid <= 0 || iid != f.mr.IID {
		return MR{}, store.ErrNotFound
	}
	mr := f.mr
	if f.merged {
		mr.State, mr.MergeCommitSHA, mr.ChangesCount = "merged", strings.Repeat("b", 40), "1"
	}
	return mr, nil
}
func (f *fakeRepository) Merged(_ context.Context, iid int64) (MR, error) {
	if !f.merged || iid != f.mr.IID {
		return MR{}, store.ErrNotFound
	}
	mr := f.mr
	mr.State, mr.MergeCommitSHA, mr.ChangesCount = "merged", strings.Repeat("b", 40), "1"
	return mr, nil
}
func (f *fakeRepository) ReadMerged(_ context.Context, iid int64, path string) (MR, []byte, error) {
	if !f.merged || iid != f.mr.IID || path != f.lastPath {
		return MR{}, nil, store.ErrNotFound
	}
	mr := f.mr
	mr.State, mr.MergeCommitSHA, mr.ChangesCount = "merged", strings.Repeat("b", 40), "1"
	return mr, append([]byte(nil), f.payload...), nil
}
func (f *fakeRepository) ReadMergedManifest(ctx context.Context, iid int64) (MR, string, []byte, error) {
	mr, payload, err := f.ReadMerged(ctx, iid, f.lastPath)
	if err != nil {
		return MR{}, "", nil, err
	}
	return mr, f.lastPath, payload, nil
}

func configServer(t *testing.T, s *store.Store, repo Repository, tier string) *httpapi.Server {
	t.Helper()
	policy := &rbac.V3Policy{Store: s}
	api := httpapi.New(s, nil, true)
	service := New(s.DB, policy, repo)
	service.SourceInstanceID = "aiops-test"
	service.SigningKey = testSigningKey()
	service.TrustedSources = map[string]ed25519.PublicKey{"aiops-test": testSigningKey().Public().(ed25519.PublicKey)}
	service.Register(api)
	env := "test"
	if tier == "production" {
		env = "prod"
	}
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: tier, DefaultEnv: env,
		EnvironmentIDs: []string{env}}, policy, s.Session,
		func(ctx context.Context, user core.User, module, action, environmentID string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environmentID})
		}); err != nil {
		t.Fatal(err)
	}
	return api
}

func testSigningKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("wan124 isolated fixture only"))
	return ed25519.NewKeyFromSeed(seed[:])
}

func configRequest(api *httpapi.Server, method, path, env, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-AIOps-Environment", env)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestConfigTemplatePromotionAndImportRegisteredHTTP(t *testing.T) {
	s := isolatedConfigStore(t)
	repo := &fakeRepository{failOnce: true}
	nonprod := configServer(t, s, repo, "nonprod")
	prod := configServer(t, s, repo, "production")
	if w := configRequest(nonprod, "POST", "/api/v1/promotions", "test", "", `{"kind":"templates","id":"CT-source"}`); w.Code != 401 {
		t.Fatalf("anonymous promotion: %d %s", w.Code, w.Body.String())
	}
	if w := configRequest(prod, "POST", "/api/v1/promotions", "prod", "admin-token", `{"kind":"templates","id":"CT-source"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "already_production") {
		t.Fatalf("production export: %d %s", w.Code, w.Body.String())
	}
	if w := configRequest(nonprod, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"workflows","id":"CT-source"}`); w.Code != 501 {
		t.Fatalf("unsupported kind: %d %s", w.Code, w.Body.String())
	}
	if w := configRequest(nonprod, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"missing"}`); w.Code != 404 {
		t.Fatalf("missing source: %d %s", w.Code, w.Body.String())
	}
	first := configRequest(nonprod, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"CT-source"}`)
	if first.Code != 503 || !strings.Contains(first.Body.String(), "config_sync_unavailable") {
		t.Fatalf("uncertain export: %d %s", first.Code, first.Body.String())
	}
	var exportID, state string
	if err := s.DB.QueryRow(t.Context(), `SELECT id,state FROM v3_config_exports`).Scan(&exportID, &state); err != nil || state != "uncertain" {
		t.Fatalf("uncertain ledger id=%q state=%q err=%v first=%s", exportID, state, err, first.Body.String())
	}
	second := configRequest(nonprod, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"CT-source"}`)
	if second.Code != 200 || !strings.Contains(second.Body.String(), `"mr":"!42"`) {
		t.Fatalf("retry export: %d %s", second.Code, second.Body.String())
	}
	if repo.calls != 2 || repo.lastBranch != repo.BranchPrefix()+exportID || repo.lastPath != promotionPath(exportID) {
		t.Fatalf("retry identity changed: calls=%d branch=%q path=%q", repo.calls, repo.lastBranch, repo.lastPath)
	}
	var manifest Manifest
	if err := json.Unmarshal(repo.payload, &manifest); err != nil || manifest.verifyTrusted(exportID, map[string]ed25519.PublicKey{"aiops-test": testSigningKey().Public().(ed25519.PublicKey)}) != nil || manifest.Template.Nodes[0].Blocks == nil {
		t.Fatalf("exported non-structural data or invalid manifest: %v", err)
	}
	if strings.Contains(string(repo.payload), "owner-token") || strings.Contains(string(repo.payload), "runtime") || strings.Contains(string(repo.payload), "evidence") {
		t.Fatalf("payload leaked runtime content: %s", repo.payload)
	}
	listed := configRequest(nonprod, "GET", "/api/v1/config-sync", "test", "owner-token", "")
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"direction":"export"`) || !strings.Contains(listed.Body.String(), `"status":"exported"`) {
		t.Fatalf("nonprod list: %d %s", listed.Code, listed.Body.String())
	}
	if w := configRequest(prod, "POST", "/api/v1/config-sync/mr-42/import", "prod", "viewer-token", ""); w.Code != 403 {
		t.Fatalf("viewer import: %d %s", w.Code, w.Body.String())
	}
	repo.merged = true
	if w := configRequest(prod, "GET", "/api/v1/config-sync", "prod", "admin-token", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"merged"`) {
		t.Fatalf("verified merged list: %d %s", w.Code, w.Body.String())
	}
	// An audit write failure rolls back both the new system template and the
	// import ledger. After removing the test trigger the same MR can retry.
	if _, err := s.DB.Exec(t.Context(), `CREATE FUNCTION reject_config_import_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.config_import.completed' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_config_import_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_config_import_audit()`); err != nil {
		t.Fatal(err)
	}
	failed := configRequest(prod, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", "")
	if failed.Code != 503 {
		t.Fatalf("audit failure: %d %s", failed.Code, failed.Body.String())
	}
	var count int
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_config_imports`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("import committed after audit failure: count=%d err=%v", count, err)
	}
	if _, err := s.DB.Exec(t.Context(), `DROP TRIGGER reject_config_import_audit ON management_audit; DROP FUNCTION reject_config_import_audit()`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		imported := configRequest(prod, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", "")
		if imported.Code != 200 || !strings.Contains(imported.Body.String(), `"status":"imported"`) {
			t.Fatalf("import attempt %d: %d %s", i, imported.Code, imported.Body.String())
		}
	}
	if listed := configRequest(prod, "GET", "/api/v1/config-sync", "prod", "admin-token", ""); listed.Code != 200 || !strings.Contains(listed.Body.String(), `"status":"imported"`) {
		t.Fatalf("verified template import list: %d %s", listed.Code, listed.Body.String())
	}
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_config_imports`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("idempotent import count=%d err=%v", count, err)
	}
	var owner, environment, title, nodes string
	if err := s.DB.QueryRow(t.Context(), `SELECT COALESCE(owner_id,''),COALESCE(environment_id,''),name,nodes::text
 FROM v3_canvas_templates WHERE id=$1`, "CTP-"+exportID).Scan(&owner, &environment, &title, &nodes); err != nil || owner != "" || environment != "" || title != "Safe plan" || strings.Contains(nodes, "evidence") {
		t.Fatalf("imported template owner=%q env=%q title=%q nodes=%q err=%v", owner, environment, title, nodes, err)
	}
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='v3.config_import.completed'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("import success audit count=%d err=%v", count, err)
	}
	// The ledger cannot turn a deleted or modified production target into a
	// successful retry. Operators need an explicit conflict to investigate.
	if _, err := s.DB.Exec(t.Context(), `UPDATE v3_canvas_templates SET name='drifted' WHERE id=$1`, "CTP-"+exportID); err != nil {
		t.Fatal(err)
	}
	if retried := configRequest(prod, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", ""); retried.Code != 409 || !strings.Contains(retried.Body.String(), "promotion_conflict") {
		t.Fatalf("modified target retry: %d %s", retried.Code, retried.Body.String())
	}
	if _, err := s.DB.Exec(t.Context(), `DELETE FROM v3_canvas_templates WHERE id=$1`, "CTP-"+exportID); err != nil {
		t.Fatal(err)
	}
	if retried := configRequest(prod, "POST", "/api/v1/config-sync/mr-42/import", "prod", "admin-token", ""); retried.Code != 409 || !strings.Contains(retried.Body.String(), "promotion_conflict") {
		t.Fatalf("deleted target retry: %d %s", retried.Code, retried.Body.String())
	}
}

func TestConfigImportRejectsTamperedManifestAndMissingRepository(t *testing.T) {
	s := isolatedConfigStore(t)
	noRepo := configServer(t, s, nil, "production")
	if w := configRequest(noRepo, "GET", "/api/v1/config-sync", "prod", "admin-token", ""); w.Code != 501 || !strings.Contains(w.Body.String(), "config_repository_unavailable") {
		t.Fatalf("no repository: %d %s", w.Code, w.Body.String())
	}
	repo := &fakeRepository{merged: true}
	id := store.ID()
	template := Template{Kind: "plan", Name: "Valid", Nodes: []TemplateNode{{ID: "node-1", X: 0, Y: 0, W: 100,
		Icon: "layers", Tone: "info", Title: "节点 1", Blocks: []json.RawMessage{}}}, Edges: []TemplateEdge{}}
	digest, err := templateDigest(template)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Schema: 1, PromotionID: id, Kind: "template", SourceID: "CT-source", SourceEnvironment: "test",
		SourceInstance: "aiops-test", SourceDigest: digest, Template: template}
	if err := m.sign(testSigningKey()); err != nil {
		t.Fatal(err)
	}
	repo.payload, _ = json.Marshal(m)
	repo.lastBranch, repo.lastPath = repo.BranchPrefix()+id, promotionPath(id)
	repo.mr = MR{IID: 43, Title: "Promote canvas template CT-source", URL: "https://gitlab.example.test/ops/config/-/merge_requests/43",
		SourceBranch: repo.lastBranch, TargetBranch: "main", SourceProjectID: 77, TargetProjectID: 77}
	repo.mr.Author.Name = "Config Bot"
	api := configServer(t, s, repo, "production")
	// A merged file can still contain a hand-edited run context. The strict
	// decoder must reject unknown fields instead of copying them into prod.
	repo.payload = []byte(strings.Replace(string(repo.payload), `"schema":1`, `"schema":1,"evidence":"runtime-secret"`, 1))
	w := configRequest(api, "POST", "/api/v1/config-sync/mr-43/import", "prod", "admin-token", "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "config_source_untrusted") {
		t.Fatalf("tampered manifest: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_config_imports`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tampered import persisted count=%d err=%v", count, err)
	}
	// A hand-edited environment claim has the correct shape and digest but
	// fails the trusted source signature.
	manifestBytes, _ := json.Marshal(m)
	repo.payload = []byte(strings.Replace(string(manifestBytes), `"source_environment":"test"`, `"source_environment":"dev"`, 1))
	w = configRequest(api, "POST", "/api/v1/config-sync/mr-43/import", "prod", "admin-token", "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "config_source_untrusted") {
		t.Fatalf("forged source environment: %d %s", w.Code, w.Body.String())
	}
}

func TestCancelledExportStillRecordsUnknownResult(t *testing.T) {
	s := isolatedConfigStore(t)
	repo := &fakeRepository{failOnce: true}
	api := configServer(t, s, repo, "nonprod")
	r := httptest.NewRequest("POST", "/api/v1/promotions", strings.NewReader(`{"kind":"templates","id":"CT-source"}`))
	r.Header.Set("X-AIOps-Environment", "test")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "owner-token"})
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	repo.cancelOnExport = cancel
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r.WithContext(ctx))
	var state string
	if err := s.DB.QueryRow(t.Context(), `SELECT state FROM v3_config_exports`).Scan(&state); err != nil || state != "uncertain" {
		t.Fatalf("canceled export ledger state=%q err=%v", state, err)
	}
	var auditCount int
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='v3.config_export.result_unknown' AND outcome='unknown'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("canceled export outcome audit=%d err=%v", auditCount, err)
	}
}

func TestExpiredExportAttemptCannotFinalizeNewLease(t *testing.T) {
	for _, lateResult := range []struct {
		name string
		err  error
	}{
		{name: "late_unknown", err: errors.New("ambiguous upstream response")},
		{name: "late_success"},
	} {
		t.Run(lateResult.name, func(t *testing.T) {
			s := isolatedConfigStore(t)
			repo := &gatedRepository{fakeRepository: &fakeRepository{}, calls: make(chan exportGateCall, 3)}
			api := configServer(t, s, repo, "nonprod")
			request := func() <-chan *httptest.ResponseRecorder {
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					done <- configRequest(api, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"CT-source"}`)
				}()
				return done
			}
			awaitCall := func() exportGateCall {
				t.Helper()
				select {
				case call := <-repo.calls:
					return call
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for remote export")
					return exportGateCall{}
				}
			}
			awaitResponse := func(done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
				t.Helper()
				select {
				case response := <-done:
					return response
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for export response")
					return nil
				}
			}
			aDone := request()
			aCall := awaitCall()
			// Advance the database lease without waiting a wall-clock minute.
			result, err := s.DB.Exec(t.Context(), `UPDATE v3_config_exports
 SET lease_until=now()-interval '1 second'
 WHERE state='exporting' AND attempt_generation=1`)
			if err != nil {
				t.Fatal(err)
			}
			if result.RowsAffected() != 1 {
				t.Fatalf("expire A lease: affected=%d err=%v", result.RowsAffected(), err)
			}
			bDone := request()
			bCall := awaitCall()
			if aCall.branch != bCall.branch || aCall.path != bCall.path || string(aCall.payload) != string(bCall.payload) {
				t.Fatal("takeover changed remote branch, path or signed payload")
			}
			var exportID, state string
			var generation int64
			if err := s.DB.QueryRow(t.Context(), `SELECT id,state,attempt_generation FROM v3_config_exports`).
				Scan(&exportID, &state, &generation); err != nil || state != "exporting" || generation != 2 {
				t.Fatalf("B lease id=%q state=%q attempt=%d err=%v", exportID, state, generation, err)
			}
			aCall.reply <- exportGateResult{mr: gatedMR(41, aCall.branch), err: lateResult.err}
			if response := awaitResponse(aDone); response.Code != 409 || !strings.Contains(response.Body.String(), "promotion_conflict") {
				t.Fatalf("stale A result: %d %s", response.Code, response.Body.String())
			}
			var leaseValid bool
			if err := s.DB.QueryRow(t.Context(), `SELECT state,attempt_generation,lease_until>now()
 FROM v3_config_exports WHERE id=$1`, exportID).Scan(&state, &generation, &leaseValid); err != nil ||
				state != "exporting" || generation != 2 || !leaseValid {
				t.Fatalf("stale A changed B lease: state=%q attempt=%d lease=%v err=%v", state, generation, leaseValid, err)
			}
			if response := configRequest(api, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"CT-source"}`); response.Code != 409 || !strings.Contains(response.Body.String(), "promotion_in_progress") {
				t.Fatalf("third request while B active: %d %s", response.Code, response.Body.String())
			}
			select {
			case <-repo.calls:
				t.Fatal("stale A allowed a third remote export")
			default:
			}
			bCall.reply <- exportGateResult{mr: gatedMR(42, bCall.branch)}
			if response := awaitResponse(bDone); response.Code != 200 || !strings.Contains(response.Body.String(), `"mr":"!42"`) {
				t.Fatalf("B export result: %d %s", response.Code, response.Body.String())
			}
			var iid int64
			if err := s.DB.QueryRow(t.Context(), `SELECT state,attempt_generation,mr_iid FROM v3_config_exports WHERE id=$1`, exportID).
				Scan(&state, &generation, &iid); err != nil || state != "exported" || generation != 2 || iid != 42 {
				t.Fatalf("B final ledger state=%q attempt=%d iid=%d err=%v", state, generation, iid, err)
			}
			var requested, unknown, completed int
			if err := s.DB.QueryRow(t.Context(), `SELECT
 count(*) FILTER (WHERE action='v3.config_export.requested'),
 count(*) FILTER (WHERE action='v3.config_export.result_unknown'),
 count(*) FILTER (WHERE action='v3.config_export.completed')
 FROM management_audit`).Scan(&requested, &unknown, &completed); err != nil ||
				requested != 2 || unknown != 0 || completed != 1 {
				t.Fatalf("fenced audit requested=%d unknown=%d completed=%d err=%v", requested, unknown, completed, err)
			}
		})
	}
}
