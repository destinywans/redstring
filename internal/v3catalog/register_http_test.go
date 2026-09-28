package v3catalog

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

func TestRegisterServiceHTTPV3ScopePersistenceAndAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_service_register_" + store.ID()
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
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES('service-writer','Service Writer')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('service-writer','services','change','environment','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := st.CreateUser(ctx, core.User{Email: "writer@example.invalid", Name: "Service Writer", Role: "viewer"}, "isolated-writer-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'service-writer')`, writer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-reader-password"); err != nil {
		t.Fatal(err)
	}
	_, writerToken, err := st.Login(ctx, "writer@example.invalid", "isolated-writer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, readerToken, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, "dev", "staging").Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(environment, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/services", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", environment)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	body := `{"items":[{"id":"checkout-api","namespace":"trade","tier":"T1","owner":"Commerce","slo":"99.9","p99":"250"}]}`
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("status=%d body=%s; want status=%d code=%q", w.Code, w.Body.String(), status, code)
		}
	}
	assert(request("dev", "", body), 401, "")
	assert(request("dev", readerToken, body), 403, "forbidden")
	assert(request("all", writerToken, body), 409, "env_view_only")
	assert(request("staging", writerToken, body), 403, "forbidden")
	created := request("dev", writerToken, body)
	assert(created, 201, "")
	var result map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || len(result) != 1 || result["created"] != float64(1) {
		t.Fatalf("unexpected V3 create DTO: %s (%v)", created.Body.String(), err)
	}
	assert(request("dev", writerToken, body), 409, "service_exists")
	var key, environment, tier, owner, namespace string
	var slo float64
	var p99 int
	if err := db.QueryRow(ctx, `SELECT a.key,ae.environment_id,p.tier,p.owner_label,p.namespace,p.slo_target_percent,p.p99_target_ms
 FROM applications a JOIN v3_application_environments ae ON ae.application_id=a.id
 JOIN v3_service_environment_profiles p ON p.application_id=a.id AND p.environment_id=ae.environment_id
 WHERE a.key='checkout-api'`).Scan(&key, &environment, &tier, &owner, &namespace, &slo, &p99); err != nil {
		t.Fatal(err)
	}
	if key != "checkout-api" || environment != "dev" || tier != "T1" || owner != "Commerce" || namespace != "trade" || slo != 99.9 || p99 != 250 {
		t.Fatalf("incorrect persisted service: %s %s %s %s %s %f %d", key, environment, tier, owner, namespace, slo, p99)
	}
	var auditCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.service.created' AND outcome='success' AND changes->>'environment'='dev'`, writer.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("created service must have exactly one audit entry, got %d", auditCount)
	}
}
