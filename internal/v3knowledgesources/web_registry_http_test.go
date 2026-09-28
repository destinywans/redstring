package v3knowledgesources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

func TestPublicWebRegistryProbeAndObservationHTTP(t *testing.T) {
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
	schema := "v3_public_web_" + store.ID()
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
	for _, statement := range []string{
		`INSERT INTO applications(id,key,name,environment,archived) VALUES
 ('visible-app','visible-app','Visible','dev',false),('hidden-app','hidden-app','Hidden','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('visible-app-dev','visible-app','dev','Dev',false),('hidden-app-dev','hidden-app','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('visible-app','dev'),('hidden-app','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	var fetches atomic.Int32
	var revokeOnFetch atomic.Bool
	revokeResult := make(chan error, 1)
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if revokeOnFetch.Swap(false) {
			_, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='manage'`)
			revokeResult <- err
		}
		if r.URL.Path == "/missing.md" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Public guide\n\nCheck status.\n"))
	}))
	defer server.Close()
	reader := PublicWebObservationReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	v3sourceregistry.NewWithReaders(db, policy, map[string]v3sourceregistry.Reader{"web": reader}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	const address = "https://example.com/guide.md"
	probe := request("/api/v1/knowledge-sources/test", `{"type":"web","url":"`+address+`","secret_ref":""}`)
	if probe.Code != http.StatusNotImplemented || !strings.Contains(probe.Body.String(), `"code":"source_connection_test_unavailable"`) || fetches.Load() != 0 {
		t.Fatalf("unscoped public Web probe unexpectedly reached the page: %d %s fetches=%d", probe.Code, probe.Body.String(), fetches.Load())
	}
	for _, invalidAddress := range []string{"https://example.com/guide.md?", "https://example.com/guide.md#",
		"https://example.com:8443/guide.md", "https://example.com/" + strings.Repeat("a", v3sourceregistry.MaxPublicWebURLBytes)} {
		body, err := json.Marshal(map[string]any{"name": "Invalid public page", "type": "web", "url": invalidAddress,
			"secret_ref": "", "spaces": []string{}, "schedule": "manual", "chunk": "fixed", "inherit_acl": true})
		if err != nil {
			t.Fatal(err)
		}
		invalid := request("/api/v1/knowledge-sources/registrations", string(body))
		if invalid.Code != 422 || !strings.Contains(invalid.Body.String(), `"code":"invalid_source_registration"`) {
			t.Fatalf("reader-incompatible public URL was persisted: %q %d %s", invalidAddress, invalid.Code, invalid.Body.String())
		}
	}
	created := request("/api/v1/knowledge-sources/registrations", `{"name":"Public guide","type":"web","url":"`+address+`",`+
		`"secret_ref":"","spaces":[],"schedule":"manual","chunk":"fixed","inherit_acl":true,"application_id":"visible-app"}`)
	if created.Code != 201 || !strings.Contains(created.Body.String(), `"registration_status":"unverified"`) {
		t.Fatalf("public registration: %d %s", created.Code, created.Body.String())
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil || saved.ID == "" {
		t.Fatalf("registration ID: %s %v", created.Body.String(), err)
	}
	var secret string
	if err := db.QueryRow(ctx, `SELECT secret_ref FROM v3_knowledge_source_registrations WHERE id=$1`, saved.ID).Scan(&secret); err != nil || secret != "" {
		t.Fatalf("public registration saved a credential: %q %v", secret, err)
	}
	read := request("/api/v1/knowledge-sources/registrations/"+saved.ID+"/sync", "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"observed_count":1`) ||
		!strings.Contains(read.Body.String(), `"index_state":"not_proven"`) {
		t.Fatalf("public read was not bounded observation: %d %s", read.Code, read.Body.String())
	}
	var documents, observations int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if documents != 0 || observations != 1 || fetches.Load() != 1 {
		t.Fatalf("observation falsely imported or disappeared: docs=%d observations=%d fetches=%d", documents, observations, fetches.Load())
	}
	unscoped := request("/api/v1/knowledge-sources/registrations", `{"name":"Unscoped guide","type":"web","url":"`+address+`",`+
		`"secret_ref":"","spaces":[],"schedule":"manual","chunk":"fixed","inherit_acl":true}`)
	if unscoped.Code != http.StatusCreated {
		t.Fatalf("create legacy environment-only registration: %d %s", unscoped.Code, unscoped.Body.String())
	}
	var unscopedSaved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(unscoped.Body.Bytes(), &unscopedSaved); err != nil || unscopedSaved.ID == "" {
		t.Fatalf("environment-only registration ID: %s %v", unscoped.Body.String(), err)
	}
	blocked := request("/api/v1/knowledge-sources/registrations/"+unscopedSaved.ID+"/sync", "")
	if blocked.Code != http.StatusUnprocessableEntity || !strings.Contains(blocked.Body.String(), `"code":"source_application_required"`) || fetches.Load() != 1 {
		t.Fatalf("environment-only source reached external page: %d %s fetches=%d", blocked.Code, blocked.Body.String(), fetches.Load())
	}

	viewer, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, viewerToken, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','knowledge','view','application','visible-app'),
 ('viewer','knowledge','manage','application','visible-app')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	createScoped := func(app string) string {
		t.Helper()
		created := request("/api/v1/knowledge-sources/registrations", `{"name":"Scoped guide","type":"web","url":"`+address+`",`+
			`"secret_ref":"","spaces":[],"schedule":"manual","chunk":"fixed","inherit_acl":true,"application_id":"`+app+`"}`)
		if created.Code != http.StatusCreated {
			t.Fatalf("create %s source: %d %s", app, created.Code, created.Body.String())
		}
		var saved struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil || saved.ID == "" {
			t.Fatalf("create %s source ID: %s %v", app, created.Body.String(), err)
		}
		return saved.ID
	}
	visibleID := saved.ID
	hiddenID := createScoped("hidden-app")
	requestViewer := func(method, path, environment string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: viewerToken})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	listed := requestViewer(http.MethodGet, "/api/v1/knowledge-sources/registrations", "dev")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), visibleID) ||
		strings.Contains(listed.Body.String(), hiddenID) || strings.Contains(listed.Body.String(), unscopedSaved.ID) {
		t.Fatalf("viewer source list escaped application grant: %d %s", listed.Code, listed.Body.String())
	}
	if denied := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+hiddenID+"/sync", "dev"); denied.Code != http.StatusNotFound {
		t.Fatalf("viewer read hidden application: %d %s", denied.Code, denied.Body.String())
	}
	if denied := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "staging"); denied.Code != http.StatusForbidden {
		t.Fatalf("viewer read outside environment grant: %d %s", denied.Code, denied.Body.String())
	}
	if synced := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "dev"); synced.Code != http.StatusOK || !strings.Contains(synced.Body.String(), `"observed_count":1`) {
		t.Fatalf("viewer scoped public Web read: %d %s", synced.Code, synced.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_observations WHERE application_id='visible-app'`).Scan(&observations); err != nil || observations != 2 {
		t.Fatalf("viewer observation scope: %d %v", observations, err)
	}
	var readsBeforeRevocation, auditsBeforeRevocation int
	if err := db.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1),
 (SELECT count(*) FROM management_audit WHERE action='v3.knowledge_source.read' AND target=$1)`, visibleID).
		Scan(&readsBeforeRevocation, &auditsBeforeRevocation); err != nil {
		t.Fatal(err)
	}
	revokeOnFetch.Store(true)
	if denied := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "dev"); denied.Code != http.StatusForbidden {
		t.Fatalf("viewer sync after in-flight manage revocation: %d %s", denied.Code, denied.Body.String())
	}
	select {
	case err := <-revokeResult:
		if err != nil {
			t.Fatalf("revoke during local TLS GET: %v", err)
		}
	default:
		t.Fatal("local TLS GET did not reach the in-flight revocation hook")
	}
	var readsAfterRevocation, auditsAfterRevocation int
	if err := db.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1),
 (SELECT count(*) FROM management_audit WHERE action='v3.knowledge_source.read' AND target=$1)`, visibleID).
		Scan(&readsAfterRevocation, &auditsAfterRevocation); err != nil {
		t.Fatal(err)
	}
	if readsAfterRevocation != readsBeforeRevocation || auditsAfterRevocation != auditsBeforeRevocation {
		t.Fatalf("in-flight revocation persisted read or audit: reads=%d/%d audits=%d/%d",
			readsAfterRevocation, readsBeforeRevocation, auditsAfterRevocation, auditsBeforeRevocation)
	}
	if denied := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "dev"); denied.Code != http.StatusNotFound {
		t.Fatalf("viewer sync after manage revocation: %d %s", denied.Code, denied.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_observations WHERE application_id='visible-app'`).Scan(&observations); err != nil || observations != 2 {
		t.Fatalf("revoked sync persisted a new observation: %d %v", observations, err)
	}
	var readsBefore, auditsBefore int
	if err := db.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1),
 (SELECT count(*) FROM management_audit WHERE action='v3.knowledge_source.read' AND target=$1)`, visibleID).
		Scan(&readsBefore, &auditsBefore); err != nil {
		t.Fatal(err)
	}
	fetchesBefore := fetches.Load()
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='visible-app'`); err != nil {
		t.Fatal(err)
	}
	archived := request("/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "")
	if archived.Code != http.StatusConflict || !strings.Contains(archived.Body.String(), `"code":"source_scope_changed"`) || fetches.Load() != fetchesBefore {
		t.Fatalf("archived application reached external page: %d %s fetches=%d before=%d", archived.Code, archived.Body.String(), fetches.Load(), fetchesBefore)
	}
	var readsAfter, auditsAfter, observationsAfter int
	if err := db.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1),
 (SELECT count(*) FROM management_audit WHERE action='v3.knowledge_source.read' AND target=$1),
 (SELECT count(*) FROM v3_knowledge_source_observations WHERE application_id='visible-app')`, visibleID).
		Scan(&readsAfter, &auditsAfter, &observationsAfter); err != nil {
		t.Fatal(err)
	}
	if readsAfter != readsBefore || auditsAfter != auditsBefore || observationsAfter != observations {
		t.Fatalf("archived sync persisted read, audit or observation: reads=%d/%d audits=%d/%d observations=%d/%d",
			readsAfter, readsBefore, auditsAfter, auditsBefore, observationsAfter, observations)
	}
}
