package v3integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

type activationResolver struct{}

func (activationResolver) Resolve(_ context.Context, ref string) (string, error) {
	if ref != "vault://isolated/gitlab#token" {
		return "", ErrCredentialUnavailable
	}
	return "isolated-token", nil
}

func TestGitLabActivationRequiresCredentialProjectAndVerifiedRead(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_activation_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
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
		`INSERT INTO applications(id,name,key,environment,archived) VALUES
 ('activation-app','Activation App','activation-app','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('activation-dev','activation-app','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('activation-app','dev')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.CreateUser(ctx, core.User{Email: "activation-viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "activation-viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	var verified atomic.Int32
	var mismatch atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verified.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/12345" ||
			r.Header.Get("PRIVATE-TOKEN") != "isolated-token" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if mismatch.Load() {
			_, _ = w.Write([]byte(`{"id":54321}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":12345}`))
	}))
	defer upstream.Close()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors
 (id,environment_id,provider,name,category,endpoint,auth_method,webhook_requested,tools)
 VALUES('draft','dev','gitlab','Draft GitLab','delivery',$1,'pat',true,'[{"name":"list_pipelines","enabled":false}]')`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc := New(db, "nonprod", activationResolver{})
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(environment, cookie, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/connectors/draft/activate", strings.NewReader(body))
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
	body := `{"credential_ref":"vault://isolated/gitlab#token"}`
	assert(request("dev", "", body), 401, "unauthorized")
	assert(request("all", session, body), 409, "env_view_only")
	assert(request("test", session, body), 403, "forbidden")
	assert(request("dev", session, `{}`), 422, "invalid_credential_ref")
	assert(request("dev", session, body), 409, "target_required")
	if verified.Load() != 0 {
		t.Fatal("GitLab contacted before target binding")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('binding','activation-app','dev','draft','project','12345',
 (SELECT id FROM users WHERE email='admin@example.invalid'))`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='activation-app'`); err != nil {
		t.Fatal(err)
	}
	assert(request("dev", session, body), 409, "inactive_target")
	if verified.Load() != 0 {
		t.Fatal("archived application binding reached GitLab")
	}
	if _, err = db.Exec(ctx, `UPDATE applications SET archived=false WHERE id='activation-app'`); err != nil {
		t.Fatal(err)
	}
	mismatch.Store(true)
	assert(request("dev", session, body), 502, "project_unavailable")
	var enabled bool
	if err = db.QueryRow(ctx, `SELECT enabled FROM v3_connectors WHERE id='draft'`).Scan(&enabled); err != nil || enabled {
		t.Fatalf("failed verification activated connector: %v %v", enabled, err)
	}
	mismatch.Store(false)
	activated := request("dev", session, body)
	assert(activated, 200, "")
	if !strings.Contains(activated.Body.String(), `"webhook_active":false`) || strings.Contains(activated.Body.String(), "vault://") {
		t.Fatalf("activation response leaked credentials or claimed webhook: %s", activated.Body.String())
	}
	var ref, status, tools string
	if err = db.QueryRow(ctx, `SELECT credential_ref,enabled,status,tools::text FROM v3_connectors WHERE id='draft'`).Scan(&ref, &enabled, &status, &tools); err != nil ||
		ref != "vault://isolated/gitlab#token" || !enabled || status != "ok" || !strings.Contains(tools, `"enabled": false`) {
		t.Fatalf("activation persistence: enabled=%v status=%s tools=%s err=%v", enabled, status, tools, err)
	}
	var count int
	var changes string
	if err = db.QueryRow(ctx, `SELECT count(*),COALESCE(max(changes::text),'') FROM management_audit WHERE target='draft' AND action='connector.activated'`).Scan(&count, &changes); err != nil || count != 1 || strings.Contains(changes, "vault://") || strings.Contains(changes, "isolated-token") {
		t.Fatalf("activation audit: count=%d changes=%s err=%v", count, changes, err)
	}
	assert(request("dev", session, body), 409, "connector_enabled")
	if verified.Load() < 1 {
		t.Fatalf("expected exact project verification, got %d requests", verified.Load())
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','services','change','application','activation-app')`); err != nil {
		t.Fatal(err)
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("activation-app\x00dev"))
	bind := httptest.NewRequest(http.MethodPost,
		"/api/v1/services/"+serviceID+"/external-targets",
		strings.NewReader(`{"connector_id":"draft","target_kind":"project","external_id":"12346"}`))
	bind.Header.Set("X-AIOps-Environment", "dev")
	bind.Header.Set("Content-Type", "application/json")
	bind.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
	bound := httptest.NewRecorder()
	api.ServeHTTP(bound, bind)
	assert(bound, 201, "")
	var added externalTarget
	if err = json.Unmarshal(bound.Body.Bytes(), &added); err != nil || added.ID == "" {
		t.Fatalf("new binding response: %s %v", bound.Body.String(), err)
	}
	if err = db.QueryRow(ctx, `SELECT enabled,status,tools::text FROM v3_connectors WHERE id='draft'`).Scan(&enabled, &status, &tools); err != nil ||
		enabled || status != "unverified" || !strings.Contains(tools, `"enabled": false`) {
		t.Fatalf("scope expansion left GitLab active: enabled=%v status=%s tools=%s err=%v", enabled, status, tools, err)
	}
	removeAdded := httptest.NewRequest(http.MethodDelete,
		"/api/v1/services/"+serviceID+"/external-targets/"+added.ID, nil)
	removeAdded.Header.Set("X-AIOps-Environment", "dev")
	removeAdded.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
	removedAdded := httptest.NewRecorder()
	api.ServeHTTP(removedAdded, removeAdded)
	assert(removedAdded, 204, "")
	assert(request("dev", session, body), 200, "")
	unbind := httptest.NewRequest(http.MethodDelete,
		"/api/v1/services/"+serviceID+"/external-targets/binding", nil)
	unbind.Header.Set("X-AIOps-Environment", "dev")
	unbind.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
	unbound := httptest.NewRecorder()
	api.ServeHTTP(unbound, unbind)
	assert(unbound, 204, "")
	if err = db.QueryRow(ctx, `SELECT enabled,status,tools::text FROM v3_connectors WHERE id='draft'`).Scan(&enabled, &status, &tools); err != nil ||
		enabled || status != "unverified" || !strings.Contains(tools, `"enabled": false`) {
		t.Fatalf("last binding removal left GitLab active: enabled=%v status=%s tools=%s err=%v", enabled, status, tools, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target='draft' AND action='connector.deactivated'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("deactivation audit count=%d err=%v", count, err)
	}
}
