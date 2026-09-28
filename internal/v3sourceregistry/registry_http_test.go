package v3sourceregistry

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
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type revokingAccess struct {
	policy *rbac.V3Policy
	revoke func() error
}

type fixtureReader struct {
	items    []Observation
	last     ReadSpec
	complete bool
}

func (f *fixtureReader) Read(_ context.Context, spec ReadSpec) (ReadResult, error) {
	f.last = spec
	return ReadResult{Items: f.items, Complete: f.complete}, nil
}

func (f *fixtureReader) Probe(_ context.Context, spec ReadSpec) error {
	f.last = spec
	return nil
}

func (a *revokingAccess) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	return a.policy.Authorize(ctx, user, decision)
}

func (a *revokingAccess) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, decision rbac.V3Decision) error {
	if decision.Action == "manage" && a.revoke != nil {
		revoke := a.revoke
		a.revoke = nil
		if err := revoke(); err != nil {
			return err
		}
	}
	return a.policy.AuthorizeWithTx(ctx, tx, user, decision)
}

func TestSourceRegistrySingleConnectionAndRevokedGrants(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
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
	schema := "v3_source_reader_" + store.ID()
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
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (&knowledge.PGRepository{DB: db}).Migrate(ctx); err != nil {
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
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','knowledge','view','application','app'),
 ('viewer','knowledge','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES
 ('app','app','App','dev',false),('hidden','hidden','Hidden','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('app-dev','app','dev','Dev',false),('hidden-dev','hidden','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app','dev'),('hidden','dev')`,
		`INSERT INTO v3_knowledge_source_registrations
 (id,environment_id,application_id,name,source_type,source_url,secret_ref,spaces,schedule,chunk,inherit_acl,created_by)
 SELECT 'visible-source','dev','app','Visible','web','https://wiki.example.invalid','vault:kv/read',
 '[]'::jsonb,'manual','heading',true,id FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO v3_knowledge_source_registrations
 (id,environment_id,application_id,name,source_type,source_url,secret_ref,spaces,schedule,chunk,inherit_acl,created_by)
 SELECT 'hidden-source','dev','hidden','Hidden','web','https://hidden.example.invalid','vault:kv/read',
 '[]'::jsonb,'manual','heading',true,id FROM users WHERE email='admin@example.invalid'`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	access := &revokingAccess{policy: policy}
	reader := &fixtureReader{items: []Observation{{ExternalID: "private/page-1",
		ContentHash: strings.Repeat("a", 64), EnvironmentID: "dev", ApplicationID: "app", ACLVerified: true}}, complete: true}
	api := httpapi.New(st, nil, true)
	NewWithReaders(db, access, map[string]Reader{"web": reader}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		r := httptest.NewRequest(method, "/api/v1/knowledge-sources/registrations", strings.NewReader(body)).WithContext(callCtx)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodGet, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"visible-source"`) ||
		strings.Contains(w.Body.String(), "hidden-source") || strings.Contains(w.Body.String(), "vault:kv/read") {
		t.Fatalf("initial list: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		Items []item `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("unexpected visible registry: %v %s", err, w.Body.String())
	}
	createBody := `{"name":"New source","type":"web","url":"https://wiki.example.invalid",` +
		`"secret_ref":"vault:kv/read","spaces":[],"schedule":"manual","chunk":"heading",` +
		`"inherit_acl":true,"application_id":"app"}`
	if w := request(http.MethodPost, createBody); w.Code != http.StatusCreated ||
		!strings.Contains(w.Body.String(), `"registration_status":"unverified"`) {
		t.Fatalf("create with grant: %d %s", w.Code, w.Body.String())
	}
	admin, adminSession, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil || admin.ID == "" {
		t.Fatalf("admin test session: %v", err)
	}
	probe := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-sources/test", strings.NewReader(
		`{"type":"web","url":"https://wiki.example.invalid","secret_ref":"vault:kv/read"}`))
	probe.Header.Set("X-AIOps-Environment", "dev")
	probe.AddCookie(&http.Cookie{Name: "aiops_session", Value: adminSession})
	probeResponse := httptest.NewRecorder()
	api.ServeHTTP(probeResponse, probe)
	if probeResponse.Code != 200 || !strings.Contains(probeResponse.Body.String(), `"scope_verified":false`) ||
		strings.Contains(probeResponse.Body.String(), "vault:") || reader.last.SourceType != "web" {
		t.Fatalf("read-only source probe: %d %s", probeResponse.Code, probeResponse.Body.String())
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/registrations/visible-source/sync"); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"observed_count":1`) || strings.Contains(w.Body.String(), "private/page-1") {
		t.Fatalf("source sync evidence: %d %s", w.Code, w.Body.String())
	}
	if reader.last.SourceType != "web" || reader.last.SecretRef != "vault:kv/read" || reader.last.ApplicationID != "app" ||
		reader.last.EnvironmentID != "dev" {
		t.Fatalf("reader received wrong source configuration: %+v", reader.last)
	}
	if w := call(http.MethodGet, "/api/v1/knowledge-sources/registrations/visible-source/evidence"); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"observed_count":1`) || !strings.Contains(w.Body.String(), `"complete":false`) ||
		strings.Contains(w.Body.String(), "vault:") || strings.Contains(w.Body.String(), "private/page-1") {
		t.Fatalf("source evidence: %d %s", w.Code, w.Body.String())
	}
	for _, statement := range []string{
		`INSERT INTO knowledge_documents
(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
SELECT 'mismatched','app','Private','private text','source://test',1,repeat('b',64),id,'published',
jsonb_build_object('environment_ids',jsonb_build_array('dev'),'source_registration_id','visible-source',
'source_observation_hash',$1::text) FROM users WHERE email='admin@example.invalid'`,
		`INSERT INTO knowledge_documents
(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
SELECT 'matched','app','Private','private text','source://test',1,repeat('a',64),id,'published',
jsonb_build_object('environment_ids',jsonb_build_array('dev'),'source_registration_id','visible-source',
'source_observation_hash',$1::text) FROM users WHERE email='admin@example.invalid'`,
	} {
		if _, err := db.Exec(ctx, statement, hashID("private/page-1")); err != nil {
			t.Fatal(err)
		}
	}
	for _, docID := range []string{"mismatched", "matched"} {
		var generation int64
		if err := db.QueryRow(ctx, `INSERT INTO knowledge_generations
(document_id,provider,model,dimensions,status,document_version)
VALUES($1,'test','test',3,'ready',1) RETURNING id`, docID).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO knowledge_chunks(generation_id,ordinal,content,embedding)
VALUES($1,0,'private text','[0.1,0.2,0.3]'::vector)`, generation); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE knowledge_documents SET active_generation=$2 WHERE id=$1`, docID, generation); err != nil {
			t.Fatal(err)
		}
	}
	if w := call(http.MethodGet, "/api/v1/knowledge-sources/registrations/visible-source/evidence"); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"attributed_ready_documents":1`) ||
		!strings.Contains(w.Body.String(), `"attributed_ready_chunks":1`) ||
		strings.Contains(w.Body.String(), "private text") {
		t.Fatalf("index proof counted stale or unattributed data: %d %s", w.Code, w.Body.String())
	}
	var audit string
	if err := db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE action='v3.knowledge_source.read' AND target='visible-source'`).Scan(&audit); err != nil ||
		strings.Contains(audit, "vault:") || strings.Contains(audit, "private/page-1") {
		t.Fatalf("unsafe read audit: %q %v", audit, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_knowledge_source_registrations
 (id,environment_id,name,source_type,source_url,secret_ref,spaces,schedule,chunk,inherit_acl,created_by)
 SELECT 'environment-source','dev','Environment source','web','https://workspace.example.invalid',
 'vault:kv/read','[]'::jsonb,'manual','heading',true,id FROM users WHERE email='admin@example.invalid'`); err != nil {
		t.Fatal(err)
	}
	reader.items = []Observation{{ExternalID: "workspace/page", ContentHash: strings.Repeat("c", 64), EnvironmentID: "dev", ACLVerified: true}}
	adminSync := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-sources/registrations/environment-source/sync", nil)
	adminSync.Header.Set("X-AIOps-Environment", "dev")
	adminSync.AddCookie(&http.Cookie{Name: "aiops_session", Value: adminSession})
	adminResponse := httptest.NewRecorder()
	api.ServeHTTP(adminResponse, adminSync)
	if adminResponse.Code != 200 || !strings.Contains(adminResponse.Body.String(), `"observed_count":1`) {
		t.Fatalf("environment-level source read failed: %d %s", adminResponse.Code, adminResponse.Body.String())
	}
	var auditedApp string
	if err := db.QueryRow(ctx, `SELECT application_id FROM management_audit
 WHERE action='v3.knowledge_source.read' AND target='environment-source'`).Scan(&auditedApp); err != nil || auditedApp != "" {
		t.Fatalf("environment-level source audit app=%q err=%v", auditedApp, err)
	}
	reader.items = []Observation{{ExternalID: "private/page-1", ContentHash: strings.Repeat("a", 64),
		EnvironmentID: "dev", ApplicationID: "app", ACLVerified: true}}
	reader.items[0].ACLVerified = false
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/registrations/visible-source/sync"); w.Code != 422 {
		t.Fatalf("unverified source item accepted: %d %s", w.Code, w.Body.String())
	}
	var readCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id='visible-source'`).Scan(&readCount); err != nil || readCount != 1 {
		t.Fatalf("failed sync persisted a read: %d %v", readCount, err)
	}
	reader.items[0].ACLVerified = true
	reader.complete = false
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/registrations/visible-source/sync"); w.Code != 503 {
		t.Fatalf("partial source read accepted: %d %s", w.Code, w.Body.String())
	}
	revokeDB, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer revokeDB.Close()
	// Revoke after create has checked the application row, immediately before
	// its transaction-scoped manage decision. The separate pool commits the
	// change while the request transaction still holds its one connection.
	access.revoke = func() error {
		_, err := revokeDB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='manage'`)
		return err
	}
	if w := request(http.MethodPost, createBody); w.Code != http.StatusForbidden {
		t.Fatalf("create after manage revocation: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_registrations WHERE application_id='app'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("unexpected registration count after revocation: %d %v", count, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("list after view revocation: %d %s", w.Code, w.Body.String())
	}
}
