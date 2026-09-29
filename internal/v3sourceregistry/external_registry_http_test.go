package v3sourceregistry

import (
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

type externalFixtureReader struct {
	result ExternalReadResult
	probe  ReadSpec
	read   ReadSpec
	err    error
	onRead func() error
}

type externalEvidenceAccess struct {
	policy        *rbac.V3Policy
	beforeCurrent func() error
}

func (a *externalEvidenceAccess) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	if decision.Module == "knowledge" && decision.Action == "view" && a.beforeCurrent != nil {
		change := a.beforeCurrent
		a.beforeCurrent = nil
		if err := change(); err != nil {
			return err
		}
	}
	return a.policy.Authorize(ctx, user, decision)
}

func (a *externalEvidenceAccess) AuthorizeWithTx(ctx context.Context, tx pgx.Tx,
	user core.User, decision rbac.V3Decision) error {
	return a.policy.AuthorizeWithTx(ctx, tx, user, decision)
}

func (f *externalFixtureReader) Read(ctx context.Context, spec ReadSpec) (ReadResult, error) {
	result, err := f.ReadExternalContent(ctx, spec)
	if err != nil {
		return ReadResult{}, err
	}
	items := make([]Observation, len(result.Documents))
	for i, doc := range result.Documents {
		items[i] = doc.Observation
	}
	return ReadResult{Items: items, Complete: result.Complete}, nil
}

func (f *externalFixtureReader) ReadExternalContent(_ context.Context, spec ReadSpec) (ExternalReadResult, error) {
	f.read = spec
	if f.onRead != nil {
		if err := f.onRead(); err != nil {
			return ExternalReadResult{}, err
		}
	}
	return f.result, f.err
}

func (f *externalFixtureReader) Probe(_ context.Context, spec ReadSpec) error {
	f.probe = spec
	return f.err
}

func (f *externalFixtureReader) BindingEvidenceForCreate(spec ReadSpec) (string, json.RawMessage, bool) {
	if spec.ApplicationID != "app" || spec.EnvironmentID != "dev" ||
		!validExternalSpaces(spec.SourceType, spec.Spaces) {
		return "", nil, false
	}
	return strings.Repeat("a", 64), json.RawMessage(`{"version":1,"scope":"fixture"}`), true
}

func TestExternalRegistryCompleteScopedReadAndRollback(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
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
	schema := "v3_external_source_" + store.ID()
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
	cfg.MaxConns = 2
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
	viewer, err := st.CreateUser(ctx, core.User{Email: "source-viewer@example.invalid", Name: "Source Viewer", Role: "viewer"}, "isolated-viewer-password")
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
 ('app','app','App','dev',false),('other-app','other-app','Other App','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('app-dev','app','dev','Dev',false),('other-app-dev','other-app','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app','dev'),('other-app','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	confluenceSource, confluenceResult := externalFixture("confluence")
	notionSource, notionResult := externalFixture("notion")
	confluence := &externalFixtureReader{result: confluenceResult}
	notion := &externalFixtureReader{result: notionResult}
	api := httpapi.New(st, nil, true)
	policy := &rbac.V3Policy{Store: st}
	access := &externalEvidenceAccess{policy: policy}
	NewWithReaders(db, access, map[string]Reader{
		"confluence": confluence, "notion": notion,
	}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev",
		EnvironmentIDs: []string{"dev"}}, policy, st.Session,
		func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(payload)))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	unauthorizedCreate := map[string]any{"type": "confluence", "url": "https://wiki.example.test/wiki",
		"secret_ref": "vault://kv/read#token", "spaces": []string{"12345"},
		"name": "Hidden source", "schedule": "manual", "chunk": "heading", "inherit_acl": true}
	unauthorizedCreate["application_id"] = "other-app"
	existing := call(http.MethodPost, "/api/v1/knowledge-sources/registrations", unauthorizedCreate)
	unauthorizedCreate["application_id"] = "missing-app"
	missing := call(http.MethodPost, "/api/v1/knowledge-sources/registrations", unauthorizedCreate)
	if existing.Code != http.StatusForbidden || missing.Code != existing.Code ||
		missing.Body.String() != existing.Body.String() {
		t.Fatalf("application existence leaked through create: existing=%d %s missing=%d %s",
			existing.Code, existing.Body.String(), missing.Code, missing.Body.String())
	}
	for _, tc := range []struct {
		kind   string
		url    string
		spaces []string
		reader *externalFixtureReader
	}{
		{"confluence", "https://wiki.example.test/wiki", confluenceSource.Spaces, confluence},
		{"notion", "https://www.notion.so/notes-0123456789abcdef0123456789abcdef", notionSource.Spaces, notion},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			connection := map[string]any{"type": tc.kind, "url": tc.url,
				"secret_ref": "vault://kv/read#token", "spaces": tc.spaces, "application_id": "app"}
			w := call(http.MethodPost, "/api/v1/knowledge-sources/test", connection)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"connected":true`) ||
				tc.reader.probe.ApplicationID != "app" || tc.reader.probe.EnvironmentID != "dev" {
				t.Fatalf("connection test: %d %s %+v", w.Code, w.Body.String(), tc.reader.probe)
			}
			registration := map[string]any{"type": tc.kind, "url": tc.url,
				"secret_ref": "vault://kv/read#token", "spaces": tc.spaces, "application_id": "app",
				"name": tc.kind + " source", "schedule": "manual", "chunk": "heading", "inherit_acl": true}
			w = call(http.MethodPost, "/api/v1/knowledge-sources/registrations", registration)
			if w.Code != 201 {
				t.Fatalf("registration: %d %s", w.Code, w.Body.String())
			}
			var saved struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.ID == "" {
				t.Fatalf("registration ID: %v %s", err, w.Body.String())
			}
			path := "/api/v1/knowledge-sources/registrations/" + saved.ID
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"last_read":null`) ||
				strings.Contains(w.Body.String(), "vault:") {
				t.Fatalf("unobserved source evidence: %d %s", w.Code, w.Body.String())
			}
			w = call(http.MethodPost, path+"/sync", nil)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"keyword_content_persisted":true`) ||
				!strings.Contains(w.Body.String(), `"document_count":1`) {
				t.Fatalf("sync: %d %s", w.Code, w.Body.String())
			}
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"content_persisted":true`) ||
				!strings.Contains(w.Body.String(), `"source_type":"`+tc.kind+`"`) ||
				strings.Contains(w.Body.String(), "vault:") || strings.Contains(w.Body.String(), "Bounded page") {
				t.Fatalf("evidence: %d %s", w.Code, w.Body.String())
			}
			var originalHash string
			if err := db.QueryRow(ctx, `SELECT d.content_hash FROM v3_external_source_documents d
 JOIN v3_external_source_syncs s ON s.read_id=d.read_id WHERE s.registration_id=$1`,
				saved.ID).Scan(&originalHash); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `UPDATE v3_external_source_documents SET content_hash=repeat('f',64)
 WHERE read_id IN (SELECT read_id FROM v3_external_source_syncs WHERE registration_id=$1)`, saved.ID); err != nil {
				t.Fatal(err)
			}
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"external_source":null`) ||
				strings.Contains(w.Body.String(), `"content_persisted":true`) {
				t.Fatalf("corrupt content claimed persisted: %d %s", w.Code, w.Body.String())
			}
			if _, err := db.Exec(ctx, `UPDATE v3_external_source_documents SET content_hash=$2
 WHERE read_id IN (SELECT read_id FROM v3_external_source_syncs WHERE registration_id=$1)`,
				saved.ID, originalHash); err != nil {
				t.Fatal(err)
			}
			var before int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`,
				saved.ID).Scan(&before); err != nil || before != 1 {
				t.Fatalf("initial read count: %d %v", before, err)
			}
			tc.reader.result.Documents[0].Body = "# Modified\n"
			tc.reader.result.Documents[0].Observation.ContentHash = store.Hash("# Modified\n")
			w = call(http.MethodPost, path+"/sync", nil)
			if w.Code != 409 {
				t.Fatalf("same-version drift accepted: %d %s", w.Code, w.Body.String())
			}
			if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`,
				saved.ID).Scan(&before); err != nil || before != 1 {
				t.Fatalf("drift persisted a partial read: %d %v", before, err)
			}
			if tc.kind == "confluence" {
				tc.reader.result.Documents[0].SourceVersion = "2"
			} else {
				tc.reader.result.Documents[0].SourceVersion = "2026-09-29T12:35:56.000Z"
			}
			w = call(http.MethodPost, path+"/sync", nil)
			if w.Code != 200 {
				t.Fatalf("updated version: %d %s", w.Code, w.Body.String())
			}
			var version int
			if err := db.QueryRow(ctx, `SELECT d.content_version FROM v3_external_source_documents d
 JOIN v3_external_source_syncs s ON s.read_id=d.read_id
 WHERE s.registration_id=$1 ORDER BY s.created_at DESC LIMIT 1`, saved.ID).Scan(&version); err != nil || version != 2 {
				t.Fatalf("content version: %d %v", version, err)
			}
			tc.reader.result = ExternalReadResult{Complete: true}
			w = call(http.MethodPost, path+"/sync", nil)
			if tc.kind == "confluence" && w.Code != 409 || tc.kind == "notion" && w.Code != 422 {
				t.Fatalf("missing observed page accepted: %d %s", w.Code, w.Body.String())
			}
			if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`,
				saved.ID).Scan(&before); err != nil || before != 2 {
				t.Fatalf("missing page persisted a read: %d %v", before, err)
			}
			_, restored := externalFixture(tc.kind)
			doc := restored.Documents[0]
			doc.Body = "# Modified\n"
			doc.Observation.ContentHash = store.Hash(doc.Body)
			if tc.kind == "confluence" {
				doc.SourceVersion = "2"
			} else {
				doc.SourceVersion = "2026-09-29T12:35:56.000Z"
			}
			tc.reader.result.Documents = []ExternalDocument{doc}
			tc.reader.onRead = func() error {
				tc.reader.onRead = nil
				_, err := db.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id='viewer' AND module='knowledge' AND action='manage'`)
				return err
			}
			w = call(http.MethodPost, path+"/sync", nil)
			if w.Code != http.StatusForbidden {
				t.Fatalf("grant revoked during read: %d %s", w.Code, w.Body.String())
			}
			if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`,
				saved.ID).Scan(&before); err != nil || before != 2 {
				t.Fatalf("revoked read persisted: %d %v", before, err)
			}
			if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','knowledge','manage','application','app')`); err != nil {
				t.Fatal(err)
			}
			// Each callback runs after the Repeatable Read evidence snapshot,
			// immediately before its fresh authorization/state projection.
			access.beforeCurrent = func() error {
				_, err := db.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id='viewer' AND module='knowledge' AND action='view'`)
				return err
			}
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 409 || strings.Contains(w.Body.String(), saved.ID) || access.beforeCurrent != nil {
				t.Fatalf("revoked evidence disclosed stale state: %d %s", w.Code, w.Body.String())
			}
			if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','knowledge','view','application','app')`); err != nil {
				t.Fatal(err)
			}
			access.beforeCurrent = func() error {
				_, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='app'`)
				return err
			}
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 409 || strings.Contains(w.Body.String(), saved.ID) || access.beforeCurrent != nil {
				t.Fatalf("archived app evidence disclosed stale state: %d %s", w.Code, w.Body.String())
			}
			if _, err := db.Exec(ctx, `UPDATE applications SET archived=false WHERE id='app'`); err != nil {
				t.Fatal(err)
			}
			lateID := "late-" + store.ID()
			access.beforeCurrent = func() error {
				_, err := db.Exec(ctx, `INSERT INTO v3_knowledge_source_reads
 (id,registration_id,environment_id,observed_count,reader_type,requested_by,observed_at)
 VALUES($1,$2,'dev',0,$3,$4,clock_timestamp()+interval '1 second')`,
					lateID, saved.ID, tc.kind, viewer.ID)
				return err
			}
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 409 || strings.Contains(w.Body.String(), saved.ID) || access.beforeCurrent != nil {
				t.Fatalf("new read evidence disclosed stale state: %d %s", w.Code, w.Body.String())
			}
			if _, err := db.Exec(ctx, `DELETE FROM v3_knowledge_source_reads WHERE id=$1`, lateID); err != nil {
				t.Fatal(err)
			}
			var latestReadID, latestHash string
			if err := db.QueryRow(ctx, `SELECT d.read_id,d.content_hash FROM v3_external_source_documents d
 JOIN v3_external_source_syncs s ON s.read_id=d.read_id
 WHERE s.registration_id=$1 ORDER BY s.created_at DESC LIMIT 1`, saved.ID).
				Scan(&latestReadID, &latestHash); err != nil {
				t.Fatal(err)
			}
			access.beforeCurrent = func() error {
				_, err := db.Exec(ctx, `UPDATE v3_external_source_documents SET content_hash=repeat('f',64)
 WHERE read_id=$1`, latestReadID)
				return err
			}
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 409 || strings.Contains(w.Body.String(), saved.ID) || access.beforeCurrent != nil {
				t.Fatalf("proof changed after snapshot: %d %s", w.Code, w.Body.String())
			}
			if _, err := db.Exec(ctx, `UPDATE v3_external_source_documents SET content_hash=$2
 WHERE read_id=$1`, latestReadID, latestHash); err != nil {
				t.Fatal(err)
			}
			access.beforeCurrent = func() error {
				_, err := db.Exec(ctx, `UPDATE users SET disabled=true WHERE id=$1`, viewer.ID)
				return err
			}
			w = call(http.MethodGet, path+"/evidence", nil)
			if w.Code != 409 || strings.Contains(w.Body.String(), saved.ID) || access.beforeCurrent != nil {
				t.Fatalf("disabled user evidence disclosed stale state: %d %s", w.Code, w.Body.String())
			}
			if _, err := db.Exec(ctx, `UPDATE users SET disabled=false WHERE id=$1`, viewer.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}
