package v3integrations

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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConnectorCRUDHTTPV3ScopePersistenceAndAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_crud_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
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
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','connectors','view','environment','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method)
 VALUES('other-environment','test','prometheus','Other environment','observe','https://other.example.invalid','none')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
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
	request := func(method, path, environment, session, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept-Language", "zh-CN")
		r.Header.Set("X-AIOps-Environment", environment)
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
	createBody := `{"type":"gitlab","url":"https://gitlab.example.invalid","auth":"pat","credential_ref":"vault://isolated/gitlab#token","tools":["list_pipelines","get_pipeline"],"webhook":false}`
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", "", createBody), 401, "unauthorized")
	assert(request(http.MethodPost, "/api/v1/connectors", "all", token, createBody), 409, "env_view_only")
	assert(request(http.MethodPost, "/api/v1/connectors", "test", token, createBody), 403, "forbidden")
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token, createBody), 403, "forbidden")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token,
		`{"type":"gitlab","url":"https://gitlab.example.invalid","auth":"pat"}`), 422, "credential_ref_required")
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token,
		`{"type":"gitlab","url":"https://gitlab.example.invalid","auth":"pat","credential_ref":"vault://isolated/gitlab#token","webhook":true}`), 422, "webhook_not_supported")
	created := request(http.MethodPost, "/api/v1/connectors", "dev", token, createBody)
	assert(created, 201, "")
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" {
		t.Fatalf("create DTO: %s %v", created.Body.String(), err)
	}
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token, createBody), 409, "connector_exists")
	var environment, credential, status string
	var persistedTools []byte
	if err := db.QueryRow(ctx, `SELECT environment_id,credential_ref,status,tools FROM v3_connectors WHERE id=$1`, result.ID).
		Scan(&environment, &credential, &status, &persistedTools); err != nil {
		t.Fatal(err)
	}
	if environment != "dev" || credential != "vault://isolated/gitlab#token" || status != "unverified" || !strings.Contains(string(persistedTools), "get_pipeline") {
		t.Fatalf("persisted connector mismatch: environment=%s status=%s tools=%s", environment, status, persistedTools)
	}
	listed := request(http.MethodGet, "/api/v1/connectors?category=delivery&q=gitlab", "all", token, "")
	assert(listed, 200, "")
	if strings.Contains(listed.Body.String(), "vault://") || strings.Contains(listed.Body.String(), "other-environment") {
		t.Fatalf("list leaked credential or foreign environment: %s", listed.Body.String())
	}
	detail := request(http.MethodGet, "/api/v1/connectors/"+result.ID, "dev", token, "")
	assert(detail, 200, "")
	if strings.Contains(detail.Body.String(), "vault://") || strings.Contains(detail.Body.String(), "token") {
		t.Fatalf("detail leaked credential: %s", detail.Body.String())
	}
	assert(request(http.MethodGet, "/api/v1/connectors/other-environment", "all", token, ""), 404, "not_found")
	assert(request(http.MethodPatch, "/api/v1/connectors/other-environment", "dev", token,
		`{"tools":[{"name":"query_metrics","enabled":false}]}`), 404, "not_found")
	patched := request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"list_pipelines","enabled":false}]}`)
	assert(patched, 200, "")
	var patchResult struct {
		Tools []tool `json:"tools"`
	}
	if err := json.Unmarshal(patched.Body.Bytes(), &patchResult); err != nil || len(patchResult.Tools) != 2 || patchResult.Tools[0].Desc != "读取流水线列表与状态" {
		t.Fatalf("localized patch DTO: %s %v", patched.Body.String(), err)
	}
	var tools []tool
	if err := db.QueryRow(ctx, `SELECT tools FROM v3_connectors WHERE id=$1`, result.ID).Scan(&persistedTools); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(persistedTools, &tools); err != nil || len(tools) != 2 || tools[0].Enabled || !tools[1].Enabled {
		t.Fatalf("partial tool update lost other selection: %s %v", persistedTools, err)
	}
	assert(request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"unknown","enabled":true}]}`), 422, "invalid_tools")
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action IN ('connector.created','connector.tools.updated')`, result.ID).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	var auditChanges string
	if err := db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE target=$1 AND action='connector.created'`, result.ID).Scan(&auditChanges); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditChanges, "vault://") || strings.Contains(auditChanges, "gitlab.example.invalid") {
		t.Fatalf("audit leaked connection settings: %s", auditChanges)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPatch, "/api/v1/connectors/"+result.ID, "dev", token,
		`{"tools":[{"name":"get_pipeline","enabled":false}]}`), 403, "forbidden")
	assert(request(http.MethodPost, "/api/v1/connectors", "dev", token,
		`{"type":"prometheus","url":"https://metrics.example.invalid","auth":"none"}`), 403, "forbidden")
}
