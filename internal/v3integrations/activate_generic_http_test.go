package v3integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type genericActivationResolver struct{}

func (genericActivationResolver) Resolve(_ context.Context, ref string) (string, error) {
	switch ref {
	case "vault://isolated/prometheus#token":
		return "isolated-token", nil
	case "vault://isolated/elasticsearch#password":
		return "es-password", nil
	}
	return "", ErrCredentialUnavailable
}

func TestGenericConnectorDraftActivationCompletesHTTPChain(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_generic_activation_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','environment','dev'),
 ('viewer','connectors','manage','environment','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method)
 VALUES('foreign-draft','test','prometheus','Foreign','observe','https://foreign.example.invalid','none')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.CreateUser(ctx, core.User{Email: "generic-viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "generic-viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Bool
	var probes atomic.Int32
	probeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/-/ready" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "" && r.Header.Get("Authorization") != "Bearer isolated-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	upstream := httptest.NewServer(probeHandler)
	defer upstream.Close()
	bearerUpstream := httptest.NewServer(probeHandler)
	defer bearerUpstream.Close()
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc := New(db, "nonprod", NewBoundResolver(genericActivationResolver{}, nil))
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, environment, cookie, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", environment)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("status=%d body=%s; want %d %q", w.Code, w.Body.String(), status, code)
		}
	}
	created := request(http.MethodPost, "/api/v1/connectors", "dev", session,
		fmt.Sprintf(`{"type":"prometheus","url":%q,"auth":"none","webhook":true,"tools":["query_metrics"]}`, upstream.URL))
	assert(created, 201, "")
	var draft struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &draft); err != nil || draft.ID == "" {
		t.Fatalf("registration response: %s %v", created.Body.String(), err)
	}
	path := "/api/v1/connectors/" + draft.ID
	assert(request(http.MethodGet, path, "dev", session, ""), 200, "")
	list := request(http.MethodGet, "/api/v1/connectors?category=observe", "dev", session, "")
	assert(list, 200, "")
	if !strings.Contains(list.Body.String(), draft.ID) || strings.Contains(list.Body.String(), "foreign-draft") {
		t.Fatalf("draft list scope: %s", list.Body.String())
	}
	assert(request(http.MethodPost, path+"/test", "dev", session, ""), 409, "connector_disabled")
	assert(request(http.MethodPatch, path, "dev", session, `{"tools":[{"name":"query_metrics","enabled":true}]}`), 409, "connector_disabled")
	assert(request(http.MethodPost, path+"/activate", "dev", "", `{}`), 401, "unauthorized")
	assert(request(http.MethodPost, path+"/activate", "all", session, `{}`), 409, "env_view_only")
	assert(request(http.MethodPost, path+"/activate", "test", session, `{}`), 403, "forbidden")
	assert(request(http.MethodPost, "/api/v1/connectors/foreign-draft/activate", "dev", session, `{}`), 404, "not_found")
	if probes.Load() != 0 {
		t.Fatalf("disabled or unauthorized draft contacted upstream %d times", probes.Load())
	}
	unavailable.Store(true)
	failed := request(http.MethodPost, path+"/activate", "dev", session, `{}`)
	assert(failed, 502, "connector_test_failed")
	var enabled bool
	var auditCount int
	if err = db.QueryRow(ctx, `SELECT enabled FROM v3_connectors WHERE id=$1`, draft.ID).Scan(&enabled); err != nil || enabled {
		t.Fatalf("failed probe activated connector: enabled=%v err=%v", enabled, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action='connector.activated'`, draft.ID).Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("failed probe wrote activation audit: count=%d err=%v", auditCount, err)
	}
	var failureChanges string
	if err = db.QueryRow(ctx, `SELECT count(*),COALESCE(max(changes::text),'') FROM management_audit
		WHERE target=$1 AND action='connector.activation.attempted' AND outcome='failure'`, draft.ID).Scan(&auditCount, &failureChanges); err != nil || auditCount != 1 ||
		!strings.Contains(failureChanges, "read_probe_failed") || strings.Contains(failureChanges, "vault://") || strings.Contains(failureChanges, "isolated-token") {
		t.Fatalf("failed activation attempt audit: count=%d changes=%s err=%v", auditCount, failureChanges, err)
	}
	unavailable.Store(false)
	// A preexisting disabled registration may carry legacy true preferences.
	// Activation must reset them in the same transaction as enabled=true.
	if _, err = db.Exec(ctx, `UPDATE v3_connectors SET tools='[{"name":"query_metrics","enabled":true}]'::jsonb WHERE id=$1`, draft.ID); err != nil {
		t.Fatal(err)
	}
	activated := request(http.MethodPost, path+"/activate", "dev", session, `{}`)
	assert(activated, 200, "")
	if !strings.Contains(activated.Body.String(), `"verified_projects":0`) || !strings.Contains(activated.Body.String(), `"webhook_active":false`) ||
		!strings.Contains(activated.Body.String(), `"credential_verified":false`) ||
		!strings.Contains(activated.Body.String(), `"enabled":true`) {
		t.Fatalf("activation response: %s", activated.Body.String())
	}
	var activatedTools []byte
	if err = db.QueryRow(ctx, `SELECT tools FROM v3_connectors WHERE id=$1`, draft.ID).Scan(&activatedTools); err != nil {
		t.Fatal(err)
	}
	var toolsAfterActivation []tool
	if err = json.Unmarshal(activatedTools, &toolsAfterActivation); err != nil || len(toolsAfterActivation) != 1 || toolsAfterActivation[0].Enabled {
		t.Fatalf("legacy enabled tool survived activation: %s %v", activatedTools, err)
	}
	detail := request(http.MethodGet, path, "dev", session, "")
	assert(detail, 200, "")
	if !strings.Contains(detail.Body.String(), `"status":"ok"`) || strings.Contains(detail.Body.String(), "vault://") {
		t.Fatalf("activated detail: %s", detail.Body.String())
	}
	assert(request(http.MethodPost, path+"/test", "dev", session, ""), 200, "")
	settingsPath := path + "/tool-settings"
	settings := request(http.MethodGet, settingsPath, "dev", session, "")
	assert(settings, 200, "")
	if !strings.Contains(settings.Body.String(), `"connector_enabled":true`) || !strings.Contains(settings.Body.String(), `"enabled":false`) {
		t.Fatalf("activation enabled registered tools before PATCH: %s", settings.Body.String())
	}
	assert(request(http.MethodPatch, path, "dev", session, `{"tools":[{"name":"query_metrics","enabled":true}]}`), 200, "")
	settings = request(http.MethodGet, settingsPath, "dev", session, "")
	assert(settings, 200, "")
	if !strings.Contains(settings.Body.String(), `"enabled":true`) || !strings.Contains(settings.Body.String(), `"agent_execution_available":false`) {
		t.Fatalf("tool preference not read back: %s", settings.Body.String())
	}
	// The detail shows the saved switches (PA-19 P3 stage 2 step 7), which
	// still grant no Agent execution.
	if body := request(http.MethodGet, path, "dev", session, "").Body.String(); !strings.Contains(body, `"name":"query_metrics"`) ||
		!strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"agent_execution_available":false`) {
		t.Fatalf("detail tools: %s", body)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action='connector.activated'`, draft.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("activation audit count=%d err=%v", auditCount, err)
	}
	assert(request(http.MethodPost, path+"/activate", "dev", session, `{}`), 409, "connector_enabled")

	// A credentialed registration without a Vault reference remains disabled; activation
	// must resolve and probe the supplied reference before storing it.
	credentialed := request(http.MethodPost, "/api/v1/connectors", "dev", session,
		fmt.Sprintf(`{"type":"prometheus","url":%q,"auth":"bearer","tools":["query_metrics"]}`, bearerUpstream.URL))
	assert(credentialed, 201, "")
	var bearer struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(credentialed.Body.Bytes(), &bearer); err != nil || bearer.ID == "" {
		t.Fatalf("bearer registration response: %s %v", credentialed.Body.String(), err)
	}
	bearerPath := "/api/v1/connectors/" + bearer.ID
	assert(request(http.MethodPost, bearerPath+"/activate", "dev", session, `{}`), 422, "invalid_credential_ref")
	assert(request(http.MethodPost, bearerPath+"/activate", "dev", session,
		`{"credential_ref":"vault://isolated/missing#token"}`), 503, "credential_not_authorized")
	beforeApproval := probes.Load()
	assert(request(http.MethodPost, bearerPath+"/activate", "dev", session,
		`{"credential_ref":"vault://isolated/prometheus#token"}`), 503, "credential_not_authorized")
	if probes.Load() != beforeApproval {
		t.Fatal("unbound credential was sent to the upstream endpoint")
	}
	svc.Prober.Resolver = NewBoundResolver(genericActivationResolver{}, []CredentialBinding{{
		Tier: "nonprod", EnvironmentID: "dev", ConnectorID: bearer.ID,
		Provider: "prometheus", Endpoint: bearerUpstream.URL,
		CredentialRef: "vault://isolated/prometheus#token",
	}})
	assert(request(http.MethodPost, bearerPath+"/activate", "dev", session,
		`{"credential_ref":"vault://isolated/prometheus#token"}`), 200, "")
	var ref string
	if err = db.QueryRow(ctx, `SELECT enabled,credential_ref FROM v3_connectors WHERE id=$1`, bearer.ID).Scan(&enabled, &ref); err != nil || !enabled || ref != "vault://isolated/prometheus#token" {
		t.Fatalf("bearer activation persistence: enabled=%v ref=%s err=%v", enabled, ref, err)
	}
	// Removing a server binding must immediately make even an already-enabled
	// registration inert without exposing the reference or contacting upstream.
	svc.Prober.Resolver = NewBoundResolver(genericActivationResolver{}, nil)
	beforeRemovalProbe := probes.Load()
	assert(request(http.MethodPost, bearerPath+"/test", "dev", session, ""), 503, "credential_not_authorized")
	assert(request(http.MethodPatch, bearerPath, "dev", session,
		`{"tools":[{"name":"query_metrics","enabled":true}]}`), 409, "connector_disabled")
	if probes.Load() != beforeRemovalProbe {
		t.Fatal("removed credential binding still reached upstream")
	}
	unboundDetail := request(http.MethodGet, bearerPath, "dev", session, "")
	assert(unboundDetail, 200, "")
	if !strings.Contains(unboundDetail.Body.String(), `"status":"unverified"`) ||
		!strings.Contains(unboundDetail.Body.String(), "server binding not configured") ||
		strings.Contains(unboundDetail.Body.String(), "vault://") {
		t.Fatalf("unbound connector detail: %s", unboundDetail.Body.String())
	}
	unboundList := request(http.MethodGet, "/api/v1/connectors?category=observe", "dev", session, "")
	assert(unboundList, 200, "")
	if !strings.Contains(unboundList.Body.String(), "server credential binding not configured") ||
		strings.Contains(unboundList.Body.String(), "vault://") {
		t.Fatalf("unbound connector list: %s", unboundList.Body.String())
	}
	unboundSettings := request(http.MethodGet, bearerPath+"/tool-settings", "dev", session, "")
	assert(unboundSettings, 200, "")
	if !strings.Contains(unboundSettings.Body.String(), `"connector_enabled":false`) {
		t.Fatalf("unbound connector settings: %s", unboundSettings.Body.String())
	}
	if probes.Load() < 4 {
		t.Fatalf("expected real upstream probes during activation and test, got %d", probes.Load())
	}

	// PA-19 P3 stage 2 decision 3: an imported Elasticsearch basic connector
	// names its user in options.username; activation and test send it with
	// the Vault password.
	var esAuth []string
	esUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		esAuth = append(esAuth, user+"|"+pass)
		w.WriteHeader(http.StatusOK)
	}))
	defer esUpstream.Close()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled,tools,options)
 VALUES('es-imported','dev','elasticsearch','Elasticsearch','observe',$1,'basic',false,'[{"name":"search_logs","desc":"","enabled":false}]','{"username":"reader"}')`,
		esUpstream.URL); err != nil {
		t.Fatal(err)
	}
	svc.Prober.Resolver = NewBoundResolver(genericActivationResolver{}, []CredentialBinding{{
		Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "es-imported", Provider: "elasticsearch", Endpoint: esUpstream.URL,
		CredentialRef: "vault://isolated/elasticsearch#password",
	}})
	assert(request(http.MethodPost, "/api/v1/connectors/es-imported/activate", "dev", session,
		`{"credential_ref":"vault://isolated/elasticsearch#password"}`), 200, "")
	assert(request(http.MethodPost, "/api/v1/connectors/es-imported/test", "dev", session, ""), 200, "")
	if len(esAuth) != 2 || esAuth[0] != "reader|es-password" || esAuth[1] != "reader|es-password" {
		t.Fatalf("elasticsearch basic probes: %v", esAuth)
	}
}
