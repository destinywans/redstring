package v3knowledgesources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3knowledge"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

type sourceEvidenceBarrierAccess struct {
	policy *rbac.V3Policy
	mu     sync.Mutex
	before func()
}

func (a *sourceEvidenceBarrierAccess) setBeforeTx(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.before = f
}

func (a *sourceEvidenceBarrierAccess) Authorize(ctx context.Context, user core.User, decision rbac.V3Decision) error {
	return a.policy.Authorize(ctx, user, decision)
}

func (a *sourceEvidenceBarrierAccess) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, decision rbac.V3Decision) error {
	a.mu.Lock()
	f := a.before
	a.before = nil
	a.mu.Unlock()
	if f != nil {
		f()
	}
	return a.policy.AuthorizeWithTx(ctx, tx, user, decision)
}

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
	var changeSourceOnFetch atomic.Bool
	var sourceToChange atomic.Value
	sourceToChange.Store("")
	var pageBody atomic.Value
	pageBody.Store("# Public guide\n\nCheck status.\n")
	revokeResult := make(chan error, 1)
	changeResult := make(chan error, 1)
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if revokeOnFetch.Swap(false) {
			_, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='manage'`)
			revokeResult <- err
		}
		if changeSourceOnFetch.Swap(false) {
			_, err := db.Exec(ctx, `UPDATE v3_knowledge_source_registrations SET name='Changed during fetch' WHERE id=$1`, sourceToChange.Load().(string))
			changeResult <- err
		}
		if r.URL.Path == "/missing.md" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(pageBody.Load().(string)))
	}))
	defer server.Close()
	reader := PublicWebObservationReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	v3sourceregistry.NewWithReaders(db, policy, map[string]v3sourceregistry.Reader{"web": reader}).Register(api)
	evidenceAccess := &sourceEvidenceBarrierAccess{policy: policy}
	New(db, evidenceAccess).Register(api)
	(&v3knowledge.Service{DB: db, Knowledge: knowledge.NewWithRepository(&knowledge.PGRepository{DB: db}, nil, nil),
		Policy: policy}).RegisterSearch(api)
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
	requestStates := func(session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-sources/registered-evidence", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	readStates := func(session string) (registeredEvidenceResponse, *httptest.ResponseRecorder) {
		t.Helper()
		w := requestStates(session)
		var result registeredEvidenceResponse
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode registered evidence: %v: %s", err, w.Body.String())
			}
		}
		return result, w
	}
	findState := func(items []RegisteredEvidence, id string) *RegisteredEvidence {
		t.Helper()
		for i := range items {
			if items[i].ID == id {
				return &items[i]
			}
		}
		return nil
	}
	const address = "https://example.com/guide.md"
	probe := request("/api/v1/knowledge-sources/test", `{"type":"web","url":"`+address+`","secret_ref":""}`)
	if probe.Code != http.StatusOK || !strings.Contains(probe.Body.String(), `"ok":true`) ||
		!strings.Contains(probe.Body.String(), `"scope_verified":false`) || fetches.Load() != 1 {
		t.Fatalf("public Web probe did not read the page exactly once: %d %s fetches=%d", probe.Code, probe.Body.String(), fetches.Load())
	}
	unsupportedProbe := request("/api/v1/knowledge-sources/test", `{"type":"notion","url":"https://notion.example.com","secret_ref":"vault://kv/notion#token"}`)
	if unsupportedProbe.Code != http.StatusNotImplemented ||
		!strings.Contains(unsupportedProbe.Body.String(), `"code":"source_connection_test_unavailable"`) || fetches.Load() != 1 {
		t.Fatalf("unconfigured source type claimed a probe: %d %s fetches=%d", unsupportedProbe.Code, unsupportedProbe.Body.String(), fetches.Load())
	}
	missingCredential := request("/api/v1/knowledge-sources/test", `{"type":"notion","url":"https://notion.example.com","secret_ref":""}`)
	if missingCredential.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(missingCredential.Body.String(), `"code":"invalid_source_connection"`) || fetches.Load() != 1 {
		t.Fatalf("missing private-source credential claimed a probe: %d %s fetches=%d", missingCredential.Code, missingCredential.Body.String(), fetches.Load())
	}
	credentialedWeb := request("/api/v1/knowledge-sources/test", `{"type":"web","url":"`+address+`","secret_ref":"vault://kv/web#token"}`)
	if credentialedWeb.Code != http.StatusServiceUnavailable ||
		!strings.Contains(credentialedWeb.Body.String(), `"code":"source_connection_failed"`) || fetches.Load() != 1 {
		t.Fatalf("credentialed Web reached the public reader: %d %s fetches=%d", credentialedWeb.Code, credentialedWeb.Body.String(), fetches.Load())
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
	initialStates, initialResponse := readStates(token)
	initialState := findState(initialStates.Items, saved.ID)
	if initialResponse.Code != http.StatusOK || initialState == nil || initialState.LastRead != nil ||
		initialState.KeywordContent != nil || initialState.ConnectionStatus != nil || initialState.DocumentCount != nil ||
		initialState.IndexStatus != nil || initialStates.Index != nil || initialStates.CompleteV3DTO || !initialStates.Partial {
		t.Fatalf("registration was presented as connected or indexed: %d %s", initialResponse.Code, initialResponse.Body.String())
	}
	read := request("/api/v1/knowledge-sources/registrations/"+saved.ID+"/sync", "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"observed_count":1`) ||
		!strings.Contains(read.Body.String(), `"index_state":"not_proven"`) {
		t.Fatalf("public read was not bounded observation: %d %s", read.Code, read.Body.String())
	}
	var documents, observations, publicDocuments int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM knowledge_documents`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_public_web_documents`).Scan(&publicDocuments); err != nil {
		t.Fatal(err)
	}
	if documents != 0 || publicDocuments != 1 || observations != 1 || fetches.Load() != 2 {
		t.Fatalf("public content escaped legacy knowledge or disappeared: legacy=%d public=%d observations=%d fetches=%d",
			documents, publicDocuments, observations, fetches.Load())
	}
	observedStates, observedResponse := readStates(token)
	observedState := findState(observedStates.Items, saved.ID)
	if observedResponse.Code != http.StatusOK || observedState == nil || observedState.LastRead == nil ||
		observedState.LastRead.ObservedCount != 1 || observedState.KeywordContent == nil ||
		observedState.KeywordContent.PersistedDocumentCount != 1 || observedState.KeywordContent.Version != 1 ||
		observedState.DocumentCount != nil || observedState.ChunkCount != nil || observedState.SyncStatus != nil ||
		observedState.LastSyncAt != nil || observedState.IndexStatus != nil ||
		strings.Contains(observedResponse.Body.String(), "Check status.") || strings.Contains(observedResponse.Body.String(), address) {
		t.Fatalf("registered source evidence lost provenance or claimed full DTO: %d %s", observedResponse.Code, observedResponse.Body.String())
	}
	mainRequest := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-sources", nil)
	mainRequest.Header.Set("X-AIOps-Environment", "dev")
	mainRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	mainResponse := httptest.NewRecorder()
	api.ServeHTTP(mainResponse, mainRequest)
	var mainStates registeredEvidenceResponse
	if mainResponse.Code != http.StatusOK || json.Unmarshal(mainResponse.Body.Bytes(), &mainStates) != nil ||
		mainStates.CompleteV3DTO || !mainStates.Partial || mainStates.Index != nil || len(mainStates.Items) != 1 ||
		findState(mainStates.Items, saved.ID) == nil ||
		findState(mainStates.Items, saved.ID).KeywordContent == nil ||
		findState(mainStates.Items, saved.ID).KeywordContent.PersistedDocumentCount != 1 {
		t.Fatalf("main list lost real registration/read evidence or claimed a complete DTO: %d %s", mainResponse.Code, mainResponse.Body.String())
	}
	var content, hash, sourceURL, applicationID, environmentID, lastReadID string
	var version int
	if err := db.QueryRow(ctx, `SELECT body,content_hash,source_url,application_id,environment_id,last_read_id,version
 FROM v3_public_web_documents WHERE registration_id=$1`, saved.ID).
		Scan(&content, &hash, &sourceURL, &applicationID, &environmentID, &lastReadID, &version); err != nil {
		t.Fatal(err)
	}
	if content != "# Public guide\n\nCheck status.\n" || hash != store.Hash(content) || sourceURL != address ||
		applicationID != "visible-app" || environmentID != "dev" || lastReadID == "" || version != 1 {
		t.Fatalf("imported public content lacks exact bytes or scope: body=%q hash=%q url=%q app=%q env=%q read=%q version=%d",
			content, hash, sourceURL, applicationID, environmentID, lastReadID, version)
	}
	searched := request("/api/v1/knowledge/search", `{"q":"status","service":"visible-app"}`)
	if searched.Code != http.StatusOK || !strings.Contains(searched.Body.String(), "Check status.") ||
		!strings.Contains(searched.Body.String(), `"mode":"keyword"`) || strings.Contains(searched.Body.String(), `"Vector":1`) {
		t.Fatalf("imported public body not in scoped keyword path: %d %s", searched.Code, searched.Body.String())
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
	if blocked.Code != http.StatusUnprocessableEntity || !strings.Contains(blocked.Body.String(), `"code":"source_application_required"`) || fetches.Load() != 2 {
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
	sourceToChange.Store(visibleID)
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
	viewerStates, viewerResponse := readStates(viewerToken)
	if viewerResponse.Code != http.StatusOK || findState(viewerStates.Items, visibleID) == nil ||
		findState(viewerStates.Items, hiddenID) != nil || findState(viewerStates.Items, unscopedSaved.ID) != nil {
		t.Fatalf("registered evidence escaped application grant: %d %s", viewerResponse.Code, viewerResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO teams(id,name) VALUES('ops','Operations'),('other-team','Other Team');
UPDATE applications SET owner_team_id='ops' WHERE id='visible-app'`); err != nil {
		t.Fatal(err)
	}
	teamCreated := request("/api/v1/knowledge-sources/registrations", `{"name":"Team guide","type":"web","url":"`+address+`",`+
		`"secret_ref":"","spaces":[],"schedule":"manual","chunk":"fixed","inherit_acl":true,`+
		`"application_id":"visible-app","team_id":"ops"}`)
	var teamSaved struct {
		ID string `json:"id"`
	}
	if teamCreated.Code != http.StatusCreated || json.Unmarshal(teamCreated.Body.Bytes(), &teamSaved) != nil || teamSaved.ID == "" {
		t.Fatalf("team source registration: %d %s", teamCreated.Code, teamCreated.Body.String())
	}
	teamHiddenStates, teamHiddenResponse := readStates(viewerToken)
	if teamHiddenResponse.Code != http.StatusOK || findState(teamHiddenStates.Items, teamSaved.ID) != nil {
		t.Fatalf("team source escaped current team grant: %d %s", teamHiddenResponse.Code, teamHiddenResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
VALUES('viewer','knowledge','view','team','ops')`); err != nil {
		t.Fatal(err)
	}
	teamVisibleStates, teamVisibleResponse := readStates(viewerToken)
	if teamVisibleResponse.Code != http.StatusOK || findState(teamVisibleStates.Items, teamSaved.ID) == nil {
		t.Fatalf("team-granted source was hidden: %d %s", teamVisibleResponse.Code, teamVisibleResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='other-team' WHERE id='visible-app'`); err != nil {
		t.Fatal(err)
	}
	teamMovedStates, teamMovedResponse := readStates(viewerToken)
	if teamMovedResponse.Code != http.StatusOK || findState(teamMovedStates.Items, teamSaved.ID) != nil {
		t.Fatalf("source with changed owner team remained visible: %d %s", teamMovedResponse.Code, teamMovedResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET owner_team_id='ops' WHERE id='visible-app'`); err != nil {
		t.Fatal(err)
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
	var repeatReadID string
	var revisions int
	if err := db.QueryRow(ctx, `SELECT last_read_id,version FROM v3_public_web_documents WHERE registration_id=$1`, visibleID).
		Scan(&repeatReadID, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_public_web_document_revisions WHERE registration_id=$1`, visibleID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if repeatReadID == lastReadID || version != 1 || revisions != 1 {
		t.Fatalf("unchanged source did not retain version 1: read=%q/%q version=%d revisions=%d",
			repeatReadID, lastReadID, version, revisions)
	}
	pageBody.Store("# Public guide\n\nCheck the new signal.\n")
	changed := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "dev")
	if changed.Code != http.StatusOK || !strings.Contains(changed.Body.String(), `"content_version":2`) {
		t.Fatalf("changed public content did not advance version: %d %s", changed.Code, changed.Body.String())
	}
	var changedBody, previousBody string
	if err := db.QueryRow(ctx, `SELECT body,last_read_id,version FROM v3_public_web_documents WHERE registration_id=$1`, visibleID).
		Scan(&changedBody, &repeatReadID, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT body FROM v3_public_web_document_revisions WHERE registration_id=$1 AND version=1`, visibleID).
		Scan(&previousBody); err != nil {
		t.Fatal(err)
	}
	if changedBody != pageBody.Load().(string) || previousBody != content || version != 2 {
		t.Fatalf("changed source lost exact version history: current=%q previous=%q version=%d", changedBody, previousBody, version)
	}
	changedStates, changedResponse := readStates(viewerToken)
	changedState := findState(changedStates.Items, visibleID)
	if changedResponse.Code != http.StatusOK || changedState == nil || changedState.LastRead == nil ||
		changedState.LastRead.ID != repeatReadID || changedState.KeywordContent == nil ||
		changedState.KeywordContent.ReadID != repeatReadID || changedState.KeywordContent.Version != 2 {
		t.Fatalf("registered evidence did not follow current content revision: %d %s", changedResponse.Code, changedResponse.Body.String())
	}
	staleSearch := request("/api/v1/knowledge/search", `{"q":"status","service":"visible-app"}`)
	if staleSearch.Code != http.StatusOK || !strings.Contains(staleSearch.Body.String(), `"hits":[]`) {
		t.Fatalf("old public content remained searchable: %d %s", staleSearch.Code, staleSearch.Body.String())
	}
	updatedSearch := request("/api/v1/knowledge/search", `{"q":"new signal","service":"visible-app"}`)
	if updatedSearch.Code != http.StatusOK || !strings.Contains(updatedSearch.Body.String(), "new signal") {
		t.Fatalf("new public content not searchable: %d %s", updatedSearch.Code, updatedSearch.Body.String())
	}
	evidence := requestViewer(http.MethodGet, "/api/v1/knowledge-sources/registrations/"+visibleID+"/evidence", "dev")
	if evidence.Code != http.StatusOK || !strings.Contains(evidence.Body.String(), `"content_persisted":true`) ||
		!strings.Contains(evidence.Body.String(), `"version":2`) || !strings.Contains(evidence.Body.String(), `"index":null`) ||
		strings.Contains(evidence.Body.String(), changedBody) {
		t.Fatalf("public source evidence claimed vector state or exposed body: %d %s", evidence.Code, evidence.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE v3_public_web_document_revisions SET read_id=$2
 WHERE registration_id=$1 AND version=2`, visibleID, lastReadID); err != nil {
		t.Fatal(err)
	}
	misattributed := requestViewer(http.MethodGet, "/api/v1/knowledge-sources/registrations/"+visibleID+"/evidence", "dev")
	if misattributed.Code != http.StatusOK || !strings.Contains(misattributed.Body.String(), `"keyword":null`) ||
		strings.Contains(misattributed.Body.String(), `"content_persisted":true`) {
		t.Fatalf("wrong revision read was reported as imported content: %d %s", misattributed.Code, misattributed.Body.String())
	}
	misattributedStates, misattributedResponse := readStates(viewerToken)
	misattributedState := findState(misattributedStates.Items, visibleID)
	if misattributedResponse.Code != http.StatusOK || misattributedState == nil || misattributedState.KeywordContent != nil {
		t.Fatalf("wrong revision read was reported as imported content in source list: %d %s", misattributedResponse.Code, misattributedResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE v3_public_web_document_revisions SET read_id=$2
 WHERE registration_id=$1 AND version=2`, visibleID, repeatReadID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_observations WHERE application_id='visible-app'`).Scan(&observations); err != nil || observations != 3 {
		t.Fatalf("viewer observation scope: %d %v", observations, err)
	}
	var readsBeforeSourceChange int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`, visibleID).Scan(&readsBeforeSourceChange); err != nil {
		t.Fatal(err)
	}
	changeSourceOnFetch.Store(true)
	if rejected := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "dev"); rejected.Code != http.StatusConflict || !strings.Contains(rejected.Body.String(), `"code":"source_changed"`) {
		t.Fatalf("changed source committed fetched content: %d %s", rejected.Code, rejected.Body.String())
	}
	select {
	case err := <-changeResult:
		if err != nil {
			t.Fatalf("change source during local TLS GET: %v", err)
		}
	default:
		t.Fatal("local TLS GET did not reach source-change hook")
	}
	var readsAfterSourceChange, versionAfterSourceChange int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_reads WHERE registration_id=$1`, visibleID).Scan(&readsAfterSourceChange); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT version FROM v3_public_web_documents WHERE registration_id=$1`, visibleID).Scan(&versionAfterSourceChange); err != nil {
		t.Fatal(err)
	}
	if readsAfterSourceChange != readsBeforeSourceChange || versionAfterSourceChange != 2 {
		t.Fatalf("changed source wrote read or version: reads=%d/%d version=%d", readsAfterSourceChange, readsBeforeSourceChange, versionAfterSourceChange)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_knowledge_source_registrations SET name='Public guide' WHERE id=$1`, visibleID); err != nil {
		t.Fatal(err)
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
	var readAfterRevocation string
	if err := db.QueryRow(ctx, `SELECT last_read_id FROM v3_public_web_documents WHERE registration_id=$1`, visibleID).
		Scan(&readAfterRevocation); err != nil || readAfterRevocation != repeatReadID {
		t.Fatalf("in-flight revocation changed public content provenance: %q/%q %v", readAfterRevocation, repeatReadID, err)
	}
	if denied := requestViewer(http.MethodPost, "/api/v1/knowledge-sources/registrations/"+visibleID+"/sync", "dev"); denied.Code != http.StatusNotFound {
		t.Fatalf("viewer sync after manage revocation: %d %s", denied.Code, denied.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_knowledge_source_observations WHERE application_id='visible-app'`).Scan(&observations); err != nil || observations != 3 {
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
	archivedStates, archivedResponse := readStates(token)
	if archivedResponse.Code != http.StatusOK || findState(archivedStates.Items, visibleID) != nil {
		t.Fatalf("archived source remained visible: %d %s", archivedResponse.Code, archivedResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=false WHERE id='visible-app'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, change, restore, hiddenID string
		args                            []any
	}{
		{name: "application grant revoked",
			change: `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='view'
AND scope_kind='application' AND scope_id='visible-app'`,
			restore: `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
VALUES('viewer','knowledge','view','application','visible-app')`, hiddenID: visibleID},
		{name: "account disabled", change: `UPDATE users SET disabled=true WHERE id=$1`,
			restore: `UPDATE users SET disabled=false WHERE id=$1`, args: []any{viewer.ID}, hiddenID: visibleID},
		{name: "application archived", change: `UPDATE applications SET archived=true WHERE id='visible-app'`,
			restore: `UPDATE applications SET archived=false WHERE id='visible-app'`, hiddenID: visibleID},
		{name: "owner team changed", change: `UPDATE applications SET owner_team_id='other-team' WHERE id='visible-app'`,
			restore: `UPDATE applications SET owner_team_id='ops' WHERE id='visible-app'`, hiddenID: teamSaved.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, beforeResponse := readStates(viewerToken)
			if beforeResponse.Code != http.StatusOK || findState(before.Items, tc.hiddenID) == nil {
				t.Fatalf("source was not visible before revocation: %d %s", beforeResponse.Code, beforeResponse.Body.String())
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			evidenceAccess.setBeforeTx(func() { close(entered); <-release })
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- requestStates(viewerToken) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("source read did not reach its snapshot authorization barrier")
			}
			mutationCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := db.Exec(mutationCtx, tc.change, tc.args...)
			cancel()
			close(release)
			if err != nil {
				t.Fatalf("revocation could not commit while source snapshot was open: %v", err)
			}
			select {
			case w := <-result:
				if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"source_state_changed"`) ||
					strings.Contains(w.Body.String(), tc.hiddenID) {
					t.Fatalf("revoked source escaped stale snapshot: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("source read did not finish after revocation")
			}
			if _, err := db.Exec(ctx, tc.restore, tc.args...); err != nil {
				t.Fatalf("restore isolated test authority: %v", err)
			}
		})
	}
}
