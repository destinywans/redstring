package v3integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestExternalTargetsHTTPExplicitScopeAndAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_external_targets_" + store.ID()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','environment','dev')`,
		`INSERT INTO applications(id,name,key,environment,archived) VALUES
 ('target-app','Target App','target-app','dev',false),
 ('other-app','Other App','other-app','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('target-dev','target-app','dev','Dev','[{"kind":"gitlab","resource":"legacy/path"}]'),
 ('target-test','target-app','test','Test','[]'),
 ('other-dev','other-app','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('target-app','dev'),('target-app','test'),('other-app','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref) VALUES
 ('gitlab-dev','dev','gitlab','GitLab','delivery','https://gitlab.example.invalid','pat','vault://isolated/gitlab#token'),
 ('gitlab-test','test','gitlab','GitLab test','delivery','https://test.example.invalid','pat','vault://isolated/test#token'),
 ('jenkins-dev','dev','jenkins','Jenkins','delivery','https://jenkins.example.invalid','bearer','vault://isolated/jenkins#token'),
 ('harbor-dev','dev','harbor','Harbor','delivery','https://harbor.example.invalid','bearer','vault://isolated/harbor#token')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_connector_targets`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("legacy bindings were copied into target table: count=%d err=%v", before, err)
	}
	_, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('invalid','target-app','dev','gitlab-test','project','12',(SELECT id FROM users WHERE email='admin@example.invalid'))`)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("cross-environment connector FK was not enforced: %v", err)
	}
	_, err = st.CreateUser(ctx, core.User{Email: "target-viewer@example.invalid", Name: "Target Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "target-viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", nil).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	serviceID := func(app, env string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(app + "\x00" + env))
	}
	path := "/api/v1/services/" + serviceID("target-app", "dev") + "/external-targets"
	otherPath := "/api/v1/services/" + serviceID("other-app", "dev") + "/external-targets"
	testPath := "/api/v1/services/" + serviceID("target-app", "test") + "/external-targets"
	request := func(method, target, environment, session, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", environment)
		r.Header.Set("Content-Type", "application/json")
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
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
	body := `{"connector_id":"gitlab-dev","target_kind":"project","external_id":"12345"}`
	assert(request(http.MethodGet, path, "dev", "", ""), 401, "unauthorized")
	assert(request(http.MethodGet, path, "dev", token, ""), 404, "not_found")
	assert(request(http.MethodPost, path, "all", token, body), 409, "env_view_only")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','services','view','application','target-app')`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, path, "dev", token, ""), 200, "")
	assert(request(http.MethodGet, otherPath, "dev", token, ""), 404, "not_found")
	assert(request(http.MethodGet, testPath, "dev", token, ""), 404, "not_found")
	assert(request(http.MethodPost, path, "dev", token, body), 404, "not_found")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','services','change','application','target-app')`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, path, "dev", token, body), 403, "forbidden")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, path, "dev", token,
		`{"connector_id":"gitlab-test","target_kind":"project","external_id":"99"}`), 404, "not_found")
	assert(request(http.MethodPost, path, "dev", token,
		`{"connector_id":"gitlab-dev","target_kind":"project","external_id":"group/path"}`), 422, "target_unavailable")
	assert(request(http.MethodPost, path, "dev", token,
		`{"connector_id":"jenkins-dev","target_kind":"job","external_id":"reviewed-alias"}`), 422, "target_unavailable")
	assert(request(http.MethodPost, path, "dev", token,
		`{"connector_id":"harbor-dev","target_kind":"repository","external_id":"team/app"}`), 422, "target_unavailable")
	created := request(http.MethodPost, path, "dev", token, body)
	assert(created, 201, "")
	var item externalTarget
	if err := json.Unmarshal(created.Body.Bytes(), &item); err != nil || item.ID == "" ||
		item.Provider != "gitlab" || item.ExternalID != "12345" || item.EnvironmentID != "dev" {
		t.Fatalf("created target=%s err=%v", created.Body.String(), err)
	}
	assert(request(http.MethodPost, path, "dev", token, body), 409, "target_exists")
	_, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('ambiguous','other-app','dev','gitlab-dev','project','12345',
 (SELECT id FROM users WHERE email='admin@example.invalid'))`)
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("shared external project was accepted as two application owners: %v", err)
	}
	listed := request(http.MethodGet, path, "all", token, "")
	assert(listed, 200, "")
	if !strings.Contains(listed.Body.String(), item.ID) || strings.Contains(listed.Body.String(), "vault://") ||
		strings.Contains(listed.Body.String(), "gitlab.example.invalid") || strings.Contains(listed.Body.String(), "legacy/path") {
		t.Fatalf("unsafe or incomplete target list: %s", listed.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, path, "dev", token, ""), 403, "forbidden")
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='services' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodDelete, path+"/"+item.ID, "dev", token, ""), 404, "not_found")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','services','change','application','target-app')`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodDelete, path+"/"+item.ID, "dev", token, ""), 204, "")
	var remaining int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_connector_targets`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("target remained after deletion: count=%d err=%v", remaining, err)
	}
	var audited string
	if err := db.QueryRow(ctx, `SELECT string_agg(changes::text,' ') FROM management_audit
 WHERE target=$1 AND action IN ('v3.connector.target_bound','v3.connector.target_unbound')`, item.ID).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audited, "12345") || strings.Contains(audited, "vault://") ||
		strings.Contains(audited, "gitlab.example.invalid") || strings.Contains(audited, "legacy/path") {
		t.Fatalf("unsafe or missing target audit: %s", audited)
	}
	_, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 SELECT 'bulk-'||n,'target-app','dev','gitlab-dev','project',n::text,
 (SELECT id FROM users WHERE email='admin@example.invalid')
 FROM generate_series(1,100) n`)
	if err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, path, "dev", token, ""), 403, "forbidden")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	listed = request(http.MethodGet, path, "dev", token, "")
	assert(listed, 200, "")
	var targetList struct{ Items []externalTarget `json:"items"` }
	if err := json.Unmarshal(listed.Body.Bytes(), &targetList); err != nil || len(targetList.Items) != 100 {
		t.Fatalf("expected exactly 100 readable targets, got %d: %v", len(targetList.Items), err)
	}
	assert(request(http.MethodPost, path, "dev", token,
		`{"connector_id":"gitlab-dev","target_kind":"project","external_id":"101"}`),
		422, "target_limit")
	_, err = db.Exec(ctx, `DELETE FROM v3_connectors WHERE id='gitlab-dev'`)
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("connector deletion silently removed audited target bindings: %v", err)
	}
	_, err = db.Exec(ctx, `DELETE FROM v3_application_environments
 WHERE application_id='target-app' AND environment_id='dev'`)
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("service environment deletion silently removed audited target bindings: %v", err)
	}
}
