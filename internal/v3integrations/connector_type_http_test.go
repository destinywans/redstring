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

func TestConnectorTypeRegisteredHTTPContract(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_type_" + store.ID()
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
	cfg.MaxConns = 1
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
	if _, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES ('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	instance := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", nil).Register(api)
	if err := api.ConfigureV3Environment(instance, policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
	}); err != nil {
		t.Fatal(err)
	}
	requestLanguage := func(server *httpapi.Server, path, env, auth, language string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.Header.Set("Accept-Language", language)
		if auth != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: auth})
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	request := func(server *httpapi.Server, path, env, auth string) *httptest.ResponseRecorder {
		return requestLanguage(server, path, env, auth, "en-US")
	}
	assertError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("status=%d body=%s; want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	path := "/api/v1/connector-types/gitlab"
	assertError(request(api, path, "dev", ""), http.StatusUnauthorized, "unauthorized")
	assertError(request(api, path, "dev", token), http.StatusForbidden, "forbidden")
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES ('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	assertError(request(api, path, "test", token), http.StatusForbidden, "forbidden")
	assertError(request(api, path, "unknown", token), http.StatusUnprocessableEntity, "invalid_environment")
	w := request(api, path, "dev", token)
	if w.Code != http.StatusOK {
		t.Fatalf("known type: status=%d body=%s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 8 {
		t.Fatalf("unexpected type DTO fields: %s", w.Body.String())
	}
	for _, field := range []string{"brand", "name", "kind", "version", "auth_methods", "scopes", "tools", "webhook_url"} {
		if _, ok := raw[field]; !ok {
			t.Fatalf("missing %s in type DTO: %s", field, w.Body.String())
		}
	}
	var got struct {
		Brand       string `json:"brand"`
		Name        string `json:"name"`
		Kind        string `json:"kind"`
		Version     string `json:"version"`
		AuthMethods []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Desc  string `json:"desc"`
		} `json:"auth_methods"`
		Scopes     []any  `json:"scopes"`
		Tools      []any  `json:"tools"`
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Brand != "gitlab" || got.Name != "GitLab" || got.Kind != "Code and pipelines" {
		t.Fatalf("unexpected type identity: %+v", got)
	}
	if got.Version != "" || got.WebhookURL != "" || got.Scopes == nil || len(got.Scopes) != 0 || got.Tools == nil || len(got.Tools) != 0 {
		t.Fatalf("unverified capability advertised: %s", w.Body.String())
	}
	if len(got.AuthMethods) != 2 || got.AuthMethods[0].ID != "oauth" || got.AuthMethods[1].ID != "pat" {
		t.Fatalf("probe auth methods mismatch: %s", w.Body.String())
	}
	for _, method := range got.AuthMethods {
		if method.Label == "" || method.Desc == "" {
			t.Fatalf("incomplete auth method: %+v", method)
		}
	}
	if !strings.Contains(got.AuthMethods[0].Desc, "no authorization flow") {
		t.Fatalf("OAuth token was described as a live authorization flow: %+v", got.AuthMethods[0])
	}
	for _, tc := range []struct {
		language string
		message  string
	}{
		{language: "en-US", message: "Connector type is not available"},
		{language: "zh-CN", message: "连接器类型不可用"},
	} {
		w := requestLanguage(api, "/api/v1/connector-types/not-a-provider", "dev", token, tc.language)
		assertError(w, http.StatusNotFound, "not_found")
		var result struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Error.Message != tc.message {
			t.Fatalf("unknown type language %q: body=%s err=%v", tc.language, w.Body.String(), err)
		}
	}

	withoutPolicy := httpapi.New(st, nil, true)
	New(db, "nonprod", nil).Register(withoutPolicy)
	if err := withoutPolicy.ConfigureV3Environment(instance, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	assertError(request(withoutPolicy, path, "dev", token), http.StatusServiceUnavailable, "authorization_unavailable")
}
