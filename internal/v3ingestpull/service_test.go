package v3ingestpull

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

func TestPullConfigRejectsInsecureAndUnreviewedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pull.json")
	if err := os.WriteFile(path, []byte(`{"sources":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("world-readable catalog accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig("relative.json"); err == nil {
		t.Fatal("relative catalog path accepted")
	}
	bad := Config{Sources: []SourceConfig{{ID: "pull1", Name: "Test", ApplicationID: "app",
		EnvironmentID: "dev", ConnectorID: "connector", TargetID: "target", ResourceID: "resource",
		QueryID: "rate", Expression: "up", NameLabels: []string{"__name__"}, LabelKeys: []string{"api_token"}}}}
	if validateConfig(bad) == nil {
		t.Fatal("credential-like label key accepted")
	}
}

type pullFixture struct {
	db        *pgxpool.Pool
	store     *store.Store
	api       *httpapi.Server
	service   *Service
	admin     core.User
	adminSID  string
	viewerSID string
	vaultHits atomic.Int32
	queryHits atomic.Int32
	warn      atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	upstream  *httptest.Server
	vault     *httptest.Server
	source    SourceConfig
}

func newPullFixture(t *testing.T, block bool) *pullFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test creates an isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_pull_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	fixture := &pullFixture{}
	t.Cleanup(func() {
		if fixture.upstream != nil {
			fixture.upstream.Close()
		}
		if fixture.vault != nil {
			fixture.vault.Close()
		}
		if fixture.db != nil {
			fixture.db.Close()
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
	fixture.db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store = &store.Store{DB: fixture.db}
	if err := fixture.store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Bootstrap(ctx, "admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	fixture.admin, fixture.adminSID, err = fixture.store.Login(ctx, "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-password"); err != nil {
		t.Fatal(err)
	}
	// This test grants the built-in viewer narrow scopes itself; drop the global defaults from migration 162 first.
	if _, err := fixture.store.DB.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND scope_kind='global' AND module<>'workspace'`); err != nil {
		t.Fatal(err)
	}
	_, fixture.viewerSID, err = fixture.store.Login(ctx, "viewer@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment,archived) VALUES('checkout','Checkout','checkout','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings)
 VALUES('checkout-dev','checkout','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('checkout','dev')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','services','view','application','checkout'),
 ('viewer','connectors','view','environment','dev')`,
	} {
		if _, err := fixture.db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	fixture.vault = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.vaultHits.Add(1)
		if r.URL.Path != "/v1/isolated/data/pull" || r.Header.Get("X-Vault-Token") != "vault-test-access" {
			w.WriteHeader(403)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"test-bearer-only"}}}`))
	}))
	if block {
		fixture.entered = make(chan struct{}, 1)
		fixture.release = make(chan struct{})
	}
	fixture.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.queryHits.Add(1)
		if r.URL.Path != "/api/v1/query_range" || r.Header.Get("Authorization") != "Bearer test-bearer-only" ||
			r.FormValue("query") != `sum by (__name__,pod) (checkout_requests{application="checkout",environment="dev"})` ||
			r.FormValue("limit") != "100" {
			w.WriteHeader(403)
			return
		}
		if block {
			fixture.entered <- struct{}{}
			<-fixture.release
		}
		if fixture.warn.Load() {
			_, _ = w.Write([]byte(`{"status":"success","warnings":["partial"],"data":{"resultType":"matrix","result":[]}}`))
			return
		}
		start, err := time.Parse(time.RFC3339Nano, r.FormValue("start"))
		if err != nil {
			w.WriteHeader(400)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"__name__":"checkout_requests","pod":"pod-1","secret_token":"never-expose"},
  "values":[[%d,"2.5"],[%d,"3.5"]]},
 {"metric":{"pod":"pod-2"},"values":[[%d,"4.5"]]}
 ]}}`, start.Unix(), start.Add(30*time.Second).Unix(), start.Unix())
	}))
	fixture.source = SourceConfig{ID: "pull-checkout", Name: "Checkout requests",
		ApplicationID: "checkout", EnvironmentID: "dev", ConnectorID: "connector-pull",
		TargetID: "target-pull", ResourceID: "checkout-web", QueryID: "request_rate",
		Expression: `sum by (__name__,pod) (checkout_requests{application="checkout",environment="dev"})`,
		NameLabels: []string{"__name__", "pod"}, LabelKeys: []string{"pod"}}
	if _, err := fixture.db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,
 credential_ref,enabled,tools) VALUES('connector-pull','dev','prometheus','Prometheus','observe',$1,
 'bearer','vault://isolated/pull#token',true,'[{"name":"query_metrics","enabled":true}]')`, fixture.upstream.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('target-pull','checkout','dev','connector-pull','metric_resource','checkout-web',$1)`, fixture.admin.ID); err != nil {
		t.Fatal(err)
	}
	resolver := v3integrations.NewBoundResolver(v3integrations.NewVaultResolver(fixture.vault.URL, "vault-test-access"),
		[]v3integrations.CredentialBinding{{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "connector-pull",
			Provider: "prometheus", Endpoint: fixture.upstream.URL, CredentialRef: "vault://isolated/pull#token"}})
	fixture.service, err = New(fixture.db, "nonprod", resolver, Config{Sources: []SourceConfig{fixture.source}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: fixture.store}
	fixture.api = httpapi.New(fixture.store, nil, true)
	fixture.service.Register(fixture.api)
	if err := fixture.api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev",
		EnvironmentIDs: []string{"dev"}}, policy, fixture.store.Session,
		func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *pullFixture) call(method, path, body, session, env string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-AIOps-Environment", env)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}

func TestPullActualReadMappingBudgetAndAudit(t *testing.T) {
	f := newPullFixture(t, false)
	path := "/api/v1/ingest/pull/" + f.source.ID
	get := f.call("GET", path, "", f.viewerSID, "dev")
	if get.Code != 200 || strings.Contains(get.Body.String(), "never-expose") ||
		strings.Contains(get.Body.String(), "vault://") || strings.Contains(get.Body.String(), f.source.Expression) {
		t.Fatalf("GET response leaked or failed: %d %s", get.Code, get.Body.String())
	}
	var draft struct {
		Sample struct {
			Body       string  `json:"body"`
			Meta       string  `json:"meta"`
			CapturedAt *string `json:"captured_at"`
		} `json:"sample"`
		Fields  []field `json:"fields"`
		Preview struct {
			Rows []row `json:"rows"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &draft); err != nil || len(draft.Preview.Rows) != 2 ||
		!strings.Contains(draft.Sample.Body, "checkout_requests") || len(draft.Fields) < 2 || draft.Fields[1].State != "warn" ||
		draft.Sample.CapturedAt == nil || draft.Sample.Meta != "Normalized preview from a successful upstream read" { // PA-83
		t.Fatalf("unexpected normalized draft: %v %s", err, get.Body.String())
	}
	if w := f.call("POST", path+"/test", `{}`, f.viewerSID, "dev"); w.Code != 403 {
		t.Fatalf("viewer tested without connectors.manage: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.db.Exec(t.Context(), `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(t.Context(), `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors'
 AND action='view' AND scope_kind='environment' AND scope_id='dev'`); err != nil {
		t.Fatal(err)
	}
	if w := f.call("POST", path+"/test", `{}`, f.viewerSID, "dev"); w.Code != 403 || f.queryHits.Load() != 1 {
		t.Fatalf("manage without view reached upstream: %d %s", w.Code, w.Body.String())
	}
	if w := f.call("POST", path+"/test", `{"mapping":{"name_label":"secret_token","label_keys":[]}}`, f.adminSID, "dev"); w.Code != 422 {
		t.Fatalf("unregistered field mapping accepted: %d %s", w.Code, w.Body.String())
	}
	if w := f.call("POST", path+"/test", `null`, f.adminSID, "dev"); w.Code != 422 {
		t.Fatalf("null body accepted: %d %s", w.Code, w.Body.String())
	}
	if w := f.call("POST", path+"/test", `{"mapping":null}`, f.adminSID, "dev"); w.Code != 422 {
		t.Fatalf("null mapping accepted: %d %s", w.Code, w.Body.String())
	}
	if w := f.call("POST", path+"/test", `{"mapping":{"name_label":"__name__","label_keys":null}}`, f.adminSID, "dev"); w.Code != 422 {
		t.Fatalf("null label list accepted: %d %s", w.Code, w.Body.String())
	}
	if w := f.call("POST", path+"/test", `{"mapping":{"name_label":"__name__"}}`, f.adminSID, "dev"); w.Code != 422 {
		t.Fatalf("missing label list accepted: %d %s", w.Code, w.Body.String())
	}
	if w := f.call("GET", path, "", f.adminSID, "all"); w.Code != 409 {
		t.Fatalf("all-view live read accepted: %d %s", w.Code, w.Body.String())
	}
	test := f.call("POST", path+"/test", `{"mapping":{"name_label":"__name__","label_keys":["pod"]}}`, f.adminSID, "dev")
	if test.Code != 200 || !strings.Contains(test.Body.String(), `"rows":2`) || !strings.Contains(test.Body.String(), `"dropped":1`) {
		t.Fatalf("actual test count: %d %s", test.Code, test.Body.String())
	}
	var attempts, audit int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_pull_attempts WHERE status='success'`).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempt records=%d err=%v", attempts, err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.ingest_pull.%' AND outcome='success'`).Scan(&audit); err != nil || audit != 2 {
		t.Fatalf("audit records=%d err=%v", audit, err)
	}
	if f.queryHits.Load() != 2 || f.vaultHits.Load() != 2 {
		t.Fatalf("unexpected real upstream calls query=%d vault=%d", f.queryHits.Load(), f.vaultHits.Load())
	}
	if _, err := f.db.Exec(t.Context(), `INSERT INTO v3_pull_attempts(source_id,actor_id,kind,status)
 SELECT $1,$2,'preview','success' FROM generate_series(1,9)`, f.source.ID, f.admin.ID); err != nil {
		t.Fatal(err)
	}
	if w := f.call("GET", path, "", f.adminSID, "dev"); w.Code != 429 || w.Header().Get("Retry-After") != "60" || f.queryHits.Load() != 2 {
		t.Fatalf("budget did not stop upstream: %d %s", w.Code, w.Body.String())
	}
}

func TestPullRevokedSessionEndsAttemptWithoutReturningObservation(t *testing.T) {
	f := newPullFixture(t, true)
	defer func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}()
	path := "/api/v1/ingest/pull/" + f.source.ID + "/test"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.call("POST", path, `{}`, f.adminSID, "dev") }()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream read did not start")
	}
	if err := f.store.Logout(t.Context(), f.adminSID); err != nil {
		t.Fatal(err)
	}
	close(f.release)
	select {
	case response := <-done:
		if response.Code != 401 || strings.Contains(response.Body.String(), "checkout_requests") {
			t.Fatalf("revoked request returned data: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("revoked request hung")
	}
	var status, code string
	if err := f.db.QueryRow(t.Context(), `SELECT status,error_code FROM v3_pull_attempts WHERE source_id=$1`, f.source.ID).
		Scan(&status, &code); err != nil || status != "failure" || code != "unauthorized" {
		t.Fatalf("rejected attempt status=%q code=%q err=%v", status, code, err)
	}
	var outcome string
	if err := f.db.QueryRow(t.Context(), `SELECT outcome FROM management_audit WHERE action='v3.ingest_pull.rejected'`).Scan(&outcome); err != nil || outcome != "failure" {
		t.Fatalf("rejected audit outcome=%q err=%v", outcome, err)
	}
}

func TestPullExactBindingPartialResponseAndIdentity(t *testing.T) {
	f := newPullFixture(t, false)
	path := "/api/v1/ingest/pull/" + f.source.ID + "/test"
	f.service.Resolver = v3integrations.NewBoundResolver(v3integrations.NewVaultResolver(f.vault.URL, "vault-test-access"), nil)
	w := f.call("POST", path, `{}`, f.adminSID, "dev")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "credential_not_authorized") ||
		f.vaultHits.Load() != 0 || f.queryHits.Load() != 0 {
		t.Fatalf("missing exact binding reached secret or network: %d %s", w.Code, w.Body.String())
	}
	f.service.Resolver = v3integrations.NewBoundResolver(v3integrations.NewVaultResolver(f.vault.URL, "vault-test-access"),
		[]v3integrations.CredentialBinding{{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "connector-pull",
			Provider: "prometheus", Endpoint: f.upstream.URL, CredentialRef: "vault://isolated/pull#token"}})
	f.warn.Store(true)
	w = f.call("POST", path, `{}`, f.adminSID, "dev")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "upstream_failed") {
		t.Fatalf("partial response presented as complete: %d %s", w.Code, w.Body.String())
	}
	f.warn.Store(false)
	changed := f.source
	changed.Expression = "up"
	if _, err := f.service.reserve(t.Context(), changed, f.admin.ID, "test"); err == nil || err.Error() != "source_identity_changed" {
		t.Fatalf("reviewed expression drift reused source identity: %v", err)
	}
	var failures, audits int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_pull_attempts WHERE status='failure'`).Scan(&failures); err != nil || failures != 2 {
		t.Fatalf("failure rows=%d err=%v", failures, err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='v3.ingest_pull.test' AND outcome='failure'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("failure audits=%d err=%v", audits, err)
	}
}

func TestPullGrantRevokedDuringReadFailsAndAudits(t *testing.T) {
	f := newPullFixture(t, true)
	defer func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}()
	path := "/api/v1/ingest/pull/" + f.source.ID + "/test"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.call("POST", path, `{}`, f.adminSID, "dev") }()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream read did not start")
	}
	if _, err := f.db.Exec(t.Context(), `DELETE FROM v3_role_grants
 WHERE role_id='admin' AND module='connectors' AND action='view' AND scope_kind='global'`); err != nil {
		t.Fatal(err)
	}
	close(f.release)
	select {
	case response := <-done:
		if response.Code != 403 || strings.Contains(response.Body.String(), "checkout_requests") {
			t.Fatalf("revoked grant returned data: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("revoked request hung")
	}
	var status, code string
	if err := f.db.QueryRow(t.Context(), `SELECT status,error_code FROM v3_pull_attempts WHERE source_id=$1`, f.source.ID).
		Scan(&status, &code); err != nil || status != "failure" || code != "forbidden" {
		t.Fatalf("grant revoke audit status=%q code=%q err=%v", status, code, err)
	}
}

func TestPullTargetRemovedDuringReadFailsAndAudits(t *testing.T) {
	f := newPullFixture(t, true)
	defer func() {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}()
	path := "/api/v1/ingest/pull/" + f.source.ID + "/test"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.call("POST", path, `{}`, f.adminSID, "dev") }()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream read did not start")
	}
	if _, err := f.db.Exec(t.Context(), `DELETE FROM v3_connector_targets WHERE id=$1`, f.source.TargetID); err != nil {
		t.Fatal(err)
	}
	close(f.release)
	select {
	case response := <-done:
		if response.Code != 409 || strings.Contains(response.Body.String(), "checkout_requests") {
			t.Fatalf("removed target returned data: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("target removal request hung")
	}
	var status, code string
	if err := f.db.QueryRow(t.Context(), `SELECT status,error_code FROM v3_pull_attempts WHERE source_id=$1`, f.source.ID).
		Scan(&status, &code); err != nil || status != "failure" || code != "source_unavailable" {
		t.Fatalf("target removal audit status=%q code=%q err=%v", status, code, err)
	}
}

func TestPullReadErrorClassification(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"storage_unavailable", "storage_unavailable"},
		{"authorization_unavailable", "authorization_unavailable"},
		{"query_unregistered", "source_changed"},
		{"credential_unavailable", "credential_not_authorized"},
	}
	for _, tc := range cases {
		if got := upstreamError(&v3integrations.PrometheusReadError{Code: tc.code}); got.Error() != tc.want {
			t.Errorf("reader %q became %q; want %q", tc.code, got.Error(), tc.want)
		}
	}
}

func TestPullListShowsOnlyOpenableSourcesWithoutSecrets(t *testing.T) {
	f := newPullFixture(t, false)
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("pull list: %d %s", w.Code, w.Body.String())
		}
		for _, secret := range []string{f.source.Expression, "vault://", f.upstream.URL, "never-expose"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("pull list leaked %q: %s", secret, w.Body.String())
			}
		}
		return body
	}
	body := decode(f.call("GET", "/api/v1/ingest/pull", "", f.viewerSID, "dev"))
	items, _ := body["items"].([]any)
	if len(items) != 1 || body["empty_reason"] != nil {
		t.Fatalf("visible source: %+v", body)
	}
	item := items[0].(map[string]any)
	if item["id"] != f.source.ID || item["state"] != "ready" || item["connector_name"] != "Prometheus" ||
		item["href"] != "/ingest/pull/"+f.source.ID || item["query_id"] != "request_rate" {
		t.Fatalf("source item: %+v", item)
	}
	if f.queryHits.Load() != 0 || f.vaultHits.Load() != 0 {
		t.Fatal("listing sources reached the upstream or the vault")
	}
	// A disabled connector is listed as unavailable, not hidden or ready.
	if _, err := f.db.Exec(t.Context(), `UPDATE v3_connectors SET enabled=false WHERE id='connector-pull'`); err != nil {
		t.Fatal(err)
	}
	body = decode(f.call("GET", "/api/v1/ingest/pull", "", f.viewerSID, "dev"))
	if item := body["items"].([]any)[0].(map[string]any); item["state"] != "source_unavailable" || item["connector_name"] != nil {
		t.Fatalf("disabled connector: %+v", item)
	}
	// Without services.view on the application the source is not listed.
	if _, err := f.db.Exec(t.Context(), `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='services'`); err != nil {
		t.Fatal(err)
	}
	body = decode(f.call("GET", "/api/v1/ingest/pull", "", f.viewerSID, "dev"))
	if len(body["items"].([]any)) != 0 || body["empty_reason"] != "no_authorized_target" {
		t.Fatalf("hidden source: %+v", body)
	}
	// No private Pull configuration leaves the list unknown.
	f.service.sources = map[string]SourceConfig{}
	body = decode(f.call("GET", "/api/v1/ingest/pull", "", f.adminSID, "dev"))
	if body["items"] != nil || body["unavailable"].(map[string]any)["items"] != "not_configured" {
		t.Fatalf("unconfigured list: %+v", body)
	}
}
