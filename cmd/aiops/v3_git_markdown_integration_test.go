package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3knowledgesources"
	"github.com/destinywans/redstring/internal/v3sourceregistry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGitMarkdownMainRoutesWithPinnedReadAndRevocation(t *testing.T) {
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
	schema := "v3_git_source_" + store.ID()
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
	if err := (&knowledge.PGRepository{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	_, adminSession, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, viewerSession, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','knowledge','view','application','app'),
 ('viewer','knowledge','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES ('app','app','App','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived)
 VALUES ('app-dev','app','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app','dev')`,
		`INSERT INTO teams(id,name) VALUES ('other-team','Other Team')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const body = "# Recovery\n\nCheck the deployment.\n"
	blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(body))), []byte(body)...))
	blobID := hex.EncodeToString(blob[:])
	var failRaw atomic.Bool
	var revokeOnRead atomic.Bool
	var wrongRequest atomic.Bool
	var gitHits atomic.Int32
	var vaultHits atomic.Int32
	var gitlab *httptest.Server
	gitlab = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gitHits.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "gitlab-token" {
			wrongRequest.Store(true)
			w.WriteHeader(403)
			return
		}
		switch {
		case r.URL.Path == "/api/v4/projects/7":
			_, _ = w.Write([]byte(`{"id":7,"web_url":"` + gitlab.URL + `/group/runbooks"}`))
		case r.URL.Path == "/api/v4/projects/7/repository/branches/main":
			_, _ = w.Write([]byte(`{"name":"main","commit":{"id":"` + sha + `"}}`))
		case r.URL.Path == "/api/v4/projects/7/repository/tree":
			if r.URL.Query().Get("path") != "docs" || r.URL.Query().Get("ref") != sha ||
				r.URL.Query().Get("recursive") != "true" {
				wrongRequest.Store(true)
				w.WriteHeader(400)
				return
			}
			switch r.URL.Query().Get("page") {
			case "1":
				_, _ = w.Write([]byte(`[{"id":"` + blobID + `","path":"docs/recovery.md","type":"blob"}]`))
			case "2":
				_, _ = w.Write([]byte(`[]`))
			default:
				wrongRequest.Store(true)
				w.WriteHeader(400)
			}
		case strings.HasPrefix(r.URL.Path, "/api/v4/projects/7/repository/files/") &&
			strings.HasSuffix(r.URL.Path, "/raw"):
			if r.URL.Query().Get("ref") != sha || !strings.Contains(r.URL.Path, "docs/recovery.md") {
				wrongRequest.Store(true)
				w.WriteHeader(400)
				return
			}
			if revokeOnRead.Swap(false) {
				if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='manage'`); err != nil {
					wrongRequest.Store(true)
				}
			}
			if failRaw.Load() {
				w.WriteHeader(503)
				return
			}
			_, _ = w.Write([]byte(body))
		default:
			wrongRequest.Store(true)
			w.WriteHeader(404)
		}
	}))
	defer gitlab.Close()
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vaultHits.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/ops/data/gitlab" ||
			r.Header.Get("X-Vault-Token") != "vault-access" {
			wrongRequest.Store(true)
			w.WriteHeader(403)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"gitlab-token"}}}`))
	}))
	defer vault.Close()
	binding := v3knowledgesources.GitMarkdownBinding{SourceURL: gitlab.URL + "/group/runbooks",
		Endpoint: gitlab.URL, ProjectID: 7, Branch: "main", PathPrefix: "docs",
		CredentialRef: "vault://ops/gitlab#token", Tier: "nonprod", EnvironmentID: "dev",
		ApplicationID: "app", ConnectorID: "gitlab-runbooks"}
	resolver := v3integrations.NewBoundResolver(v3integrations.NewVaultResolver(vault.URL, "vault-access"),
		[]v3integrations.CredentialBinding{{Tier: binding.Tier, EnvironmentID: binding.EnvironmentID,
			ConnectorID: binding.ConnectorID, Provider: "gitlab", Endpoint: binding.Endpoint,
			CredentialRef: binding.CredentialRef}})
	reader, err := v3knowledgesources.NewGitMarkdownReader([]v3knowledgesources.GitMarkdownBinding{binding}, resolver, gitlab.Client())
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	v3knowledgesources.NewWithGitMarkdownBindings(db, policy, []v3knowledgesources.GitMarkdownBinding{binding}).Register(api)
	registry := v3sourceregistry.NewWithReaders(db, policy, map[string]v3sourceregistry.Reader{"git_markdown": reader})
	registry.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, payload, session string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(payload))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "gitlab-token") || strings.Contains(w.Body.String(), "vault-access") ||
			strings.Contains(w.Body.String(), "vault://") ||
			strings.Contains(w.Body.String(), body) {
			t.Fatalf("source response exposed credentials or private Markdown: %s", w.Body.String())
		}
		return w
	}
	connection := `{"type":"git_markdown","url":"` + binding.SourceURL + `","secret_ref":"` + binding.CredentialRef + `","application_id":"app"}`
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/test", connection, viewerSession); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatalf("Git Markdown main connection test: %d %s unexpected_upstream_request=%t vault_hits=%d git_hits=%d", w.Code, w.Body.String(), wrongRequest.Load(), vaultHits.Load(), gitHits.Load())
	}
	priorGitHits, priorVaultHits := gitHits.Load(), vaultHits.Load()
	crossTeamConnection := strings.TrimSuffix(connection, "}") + `,"team_id":"other-team"}`
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/test", crossTeamConnection, viewerSession); w.Code != 403 ||
		gitHits.Load() != priorGitHits || vaultHits.Load() != priorVaultHits {
		t.Fatalf("cross-team probe reached credentials or upstream: %d %s", w.Code, w.Body.String())
	}
	revokeOnRead.Store(true)
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/test", connection, viewerSession); w.Code != 403 {
		t.Fatalf("post-read probe revocation accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','knowledge','manage','application','app')`); err != nil {
		t.Fatal(err)
	}
	var attempted, succeeded, authorizationFailed int
	if err := db.QueryRow(ctx, `SELECT
 count(*) FILTER (WHERE outcome='attempted'),
 count(*) FILTER (WHERE outcome='success'),
 count(*) FILTER (WHERE outcome='authorization_failed')
 FROM management_audit WHERE action='v3.knowledge_source.connection.test' AND application_id='app'`).
		Scan(&attempted, &succeeded, &authorizationFailed); err != nil ||
		attempted != 2 || succeeded != 1 || authorizationFailed != 1 {
		t.Fatalf("Git probe audit attempts/outcomes=%d/%d/%d err=%v", attempted, succeeded, authorizationFailed, err)
	}
	create := `{"name":"Runbooks","type":"git_markdown","url":"` + binding.SourceURL + `",` +
		`"secret_ref":"` + binding.CredentialRef + `","application_id":"app","spaces":[],` +
		`"schedule":"manual","chunk":"heading","inherit_acl":true}`
	w := call(http.MethodPost, "/api/v1/knowledge-sources", create, adminSession)
	if w.Code != 201 {
		t.Fatalf("Git Markdown main create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("invalid create result: %v %s", err, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+created.ID+"/sync", "", viewerSession); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"document_count":1`) || !strings.Contains(w.Body.String(), sha) {
		t.Fatalf("Git Markdown sync: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/knowledge-sources", "", viewerSession); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"complete_v3_dto":true`) ||
		!strings.Contains(w.Body.String(), `"partial":true`) ||
		!strings.Contains(w.Body.String(), `"document_count":1`) ||
		!strings.Contains(w.Body.String(), `"chunk_count":null`) ||
		!strings.Contains(w.Body.String(), `"index_status":null`) ||
		!strings.Contains(w.Body.String(), `"last_sync_at":"`) {
		t.Fatalf("Git Markdown list evidence: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/knowledge-sources/registrations/"+created.ID+"/evidence", "", viewerSession); w.Code != 200 ||
		!strings.Contains(w.Body.String(), sha) || !strings.Contains(w.Body.String(), `"document_count":1`) ||
		!strings.Contains(w.Body.String(), `"index":null`) {
		t.Fatalf("Git Markdown commit evidence: %d %s", w.Code, w.Body.String())
	}
	var persisted string
	if err := db.QueryRow(ctx, `SELECT d.body FROM v3_git_markdown_documents d
 JOIN v3_git_markdown_syncs s ON s.read_id=d.read_id WHERE s.registration_id=$1`, created.ID).Scan(&persisted); err != nil || persisted != body {
		t.Fatalf("pinned body not persisted: %v %q", err, persisted)
	}
	var registeredFingerprint, readFingerprint, registeredSnapshot, readSnapshot string
	if err := db.QueryRow(ctx, `SELECT r.binding_fingerprint,s.binding_fingerprint,
 r.binding_snapshot::text,s.binding_snapshot::text
 FROM v3_knowledge_source_registrations r JOIN v3_git_markdown_syncs s ON s.registration_id=r.id
 WHERE r.id=$1`, created.ID).Scan(&registeredFingerprint, &readFingerprint, &registeredSnapshot, &readSnapshot); err != nil ||
		registeredFingerprint == "" || registeredFingerprint != readFingerprint ||
		registeredSnapshot != readSnapshot || !strings.Contains(readSnapshot, `"branch": "main"`) ||
		strings.Contains(readSnapshot, "vault://") || strings.Contains(readSnapshot, "gitlab-token") {
		t.Fatalf("binding version evidence invalid: %v fingerprint=%q snapshot=%q", err, readFingerprint, readSnapshot)
	}
	var priorReads int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`, created.ID).Scan(&priorReads); err != nil || priorReads != 1 {
		t.Fatalf("unexpected initial reads: %d %v", priorReads, err)
	}
	driftedBinding := binding
	driftedBinding.Branch = "other"
	driftedReader, err := v3knowledgesources.NewGitMarkdownReader([]v3knowledgesources.GitMarkdownBinding{driftedBinding}, resolver, gitlab.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry.Readers["git_markdown"] = driftedReader
	priorGitHits, priorVaultHits = gitHits.Load(), vaultHits.Load()
	if w := call(http.MethodGet, "/api/v1/knowledge-sources/registrations/"+created.ID+"/evidence", "", viewerSession); w.Code != 409 ||
		gitHits.Load() != priorGitHits || vaultHits.Load() != priorVaultHits {
		t.Fatalf("changed binding exposed saved Git evidence: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+created.ID+"/sync", "", viewerSession); w.Code != 503 ||
		gitHits.Load() != priorGitHits || vaultHits.Load() != priorVaultHits {
		t.Fatalf("changed binding reached credential or GitLab: %d %s", w.Code, w.Body.String())
	}
	registry.Readers["git_markdown"] = reader
	failRaw.Store(true)
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+created.ID+"/sync", "", viewerSession); w.Code != 503 {
		t.Fatalf("GitLab failure accepted: %d %s", w.Code, w.Body.String())
	}
	failRaw.Store(false)
	revokeOnRead.Store(true)
	if w := call(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+created.ID+"/sync", "", viewerSession); w.Code != 403 {
		t.Fatalf("post-read grant revocation accepted: %d %s", w.Code, w.Body.String())
	}
	var reads int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`, created.ID).Scan(&reads); err != nil || reads != priorReads {
		t.Fatalf("failed/revoked sync persisted reads: %d %v", reads, err)
	}
	if wrongRequest.Load() {
		t.Fatal("reader sent a request outside fixed GitLab/Vault API scope")
	}
}
