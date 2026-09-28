package v3catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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
	// Registration must never borrow a second pool connection while holding
	// its transaction and existing-service row lock.
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
	if _, err := db.Exec(ctx, `INSERT INTO cmdb_sync_sources(id,kind,name,last_success_at,environment_key)
 VALUES('live-source','kubernetes','Live',now(),'dev'),
 ('stale-source','kubernetes','Stale',now()-interval '1 day','dev'),
 ('other-source','kubernetes','Other',now(),'staging');
 INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,observed_at,attributes) VALUES
 ('checkout-workload','checkout-api','deployment','kubernetes','live-source','uid-checkout',now(),'{"uid":"uid-checkout","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('stale-source-workload','stale-source-api','deployment','kubernetes','stale-source','uid-stale-source',now(),'{"uid":"uid-stale-source","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('stale-observation-workload','stale-observation-api','deployment','kubernetes','live-source','uid-stale-observation',now()-interval '1 day','{"uid":"uid-stale-observation","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('missing-workload','missing-api','deployment','kubernetes','live-source','uid-missing',now(),'{"uid":"uid-missing","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"missing","resource_type":"Deployment"}'),
 ('bad-uid-workload','bad-uid-api','deployment','kubernetes','live-source','uid-real',now(),'{"uid":"uid-old","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('wrong-source-env-workload','wrong-source-env-api','deployment','kubernetes','other-source','uid-wrong-source',now(),'{"uid":"uid-wrong-source","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('wrong-attribute-env-workload','wrong-attribute-env-api','deployment','kubernetes','live-source','uid-wrong-attribute',now(),'{"uid":"uid-wrong-attribute","cluster":"cluster-a","namespace":"trade","environment_key":"staging","inventory_presence":"present","resource_type":"Deployment"}'),
 ('linked-workload','linked-api','deployment','kubernetes','live-source','uid-linked',now(),'{"uid":"uid-linked","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('duplicate-a','duplicate-api','deployment','kubernetes','live-source','uid-duplicate-a',now(),'{"uid":"uid-duplicate-a","cluster":"cluster-a","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('duplicate-b','duplicate-api','deployment','kubernetes','live-source','uid-duplicate-b',now(),'{"uid":"uid-duplicate-b","cluster":"cluster-b","namespace":"trade","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('other-env-workload','other-env-api','deployment','kubernetes','other-source','uid-other-env',now(),'{"uid":"uid-other-env","cluster":"cluster-c","namespace":"trade","environment_key":"staging","inventory_presence":"present","resource_type":"Deployment"}');
 INSERT INTO applications(id,name,key,environment) VALUES('linked-app','Linked','linked-existing','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('linked-app-env','linked-app','dev','Dev');
 INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id)
 VALUES('linked-workload','linked-app','linked-app-env')`); err != nil {
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
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		r = r.WithContext(requestCtx)
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
	assert(request("dev", writerToken, body), 403, "forbidden") // resources.view is required to link.
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('service-writer','resources','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stale-source-api", "stale-observation-api", "missing-api", "bad-uid-api", "wrong-source-env-api", "wrong-attribute-env-api", "linked-api"} {
		assert(request("dev", writerToken, `{"items":[{"id":"`+name+`","namespace":"trade"}]}`), 409, "workload_unavailable")
	}
	assert(request("dev", writerToken, `{"items":[{"id":"duplicate-api","namespace":"trade"}]}`), 409, "ambiguous_workload")
	assert(request("dev", writerToken, `{"items":[{"id":"stale-source-api","namespace":"trade","resource_id":"stale-source-workload"}]}`), 409, "workload_unavailable")
	assert(request("dev", writerToken, `{"items":[{"id":"other-env-api","namespace":"trade","resource_id":"other-env-workload"}]}`), 409, "workload_unavailable")
	assert(request("dev", writerToken, `{"items":[{"id":"duplicate-api","namespace":"trade","resource_id":"duplicate-a"}]}`), 201, "")
	var explicitLink string
	if err := db.QueryRow(ctx, `SELECT l.resource_id FROM cmdb_resource_applications l
 JOIN applications a ON a.id=l.application_id WHERE a.key='duplicate-api'`).Scan(&explicitLink); err != nil || explicitLink != "duplicate-a" {
		t.Fatalf("explicit resource identity was not linked: %q (%v)", explicitLink, err)
	}
	// The same name in another environment must not be linked into dev.
	assert(request("dev", writerToken, `{"items":[{"id":"other-env-api","namespace":"trade"}]}`), 201, "")
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
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit a
 JOIN applications app ON app.id=a.application_id
 WHERE a.actor_id=$1 AND a.action='v3.service.created' AND a.outcome='success'
 AND a.changes->>'environment'='dev' AND app.key='checkout-api'`, writer.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("created service must have exactly one audit entry, got %d", auditCount)
	}
	var linked, provenance string
	if err := db.QueryRow(ctx, `SELECT l.resource_id,l.provenance FROM cmdb_resource_applications l
	 JOIN applications a ON a.id=l.application_id WHERE a.key='checkout-api'`).Scan(&linked, &provenance); err != nil {
		t.Fatal(err)
	}
	if linked != "checkout-workload" || provenance != "manual" {
		t.Fatalf("unexpected workload link %q (%s)", linked, provenance)
	}
	var manualLinks int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM cmdb_resource_applications l
 JOIN applications a ON a.id=l.application_id WHERE a.key='other-env-api'`).Scan(&manualLinks); err != nil {
		t.Fatal(err)
	}
	if manualLinks != 0 {
		t.Fatalf("other-environment workload was incorrectly linked: %d", manualLinks)
	}
	var manualLinked bool
	if err := db.QueryRow(ctx, `SELECT (changes->>'resource_linked')::boolean FROM management_audit a
 JOIN applications app ON app.id=a.application_id WHERE app.key='other-env-api'
 AND a.action='v3.service.created'`).Scan(&manualLinked); err != nil {
		t.Fatal(err)
	}
	if manualLinked {
		t.Fatal("manual service audit incorrectly claims a resource link")
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','staging'),
 ('service-writer','services','change','environment','staging')`); err != nil {
		t.Fatal(err)
	}
	// Reuse the existing service identity in a second environment. The old
	// identity grant is checked inside the same single-connection transaction.
	assert(request("staging", writerToken, `{"items":[{"id":"checkout-api","namespace":"trade"}]}`), 201, "")
	var envCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_application_environments ve
 JOIN applications app ON app.id=ve.application_id WHERE app.key='checkout-api'`).Scan(&envCount); err != nil {
		t.Fatal(err)
	}
	if envCount != 2 {
		t.Fatalf("existing service identity was not reused across environments: %d", envCount)
	}
}
