package v3integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestConnectorDetailProjectsOnlyAuthorizedBoundServices(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_connector_detail_" + store.ID()
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
	cfg.MaxConns = 1 // Per-service authorization must reuse the read transaction.
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
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','environment','view','environment','test'),
 ('viewer','connectors','view','environment','dev'),
 ('viewer','connectors','view','environment','test'),
 ('viewer','services','view','application','allowed'),
 ('viewer','services','view','team','team-a')`,
		`INSERT INTO teams(id,name) VALUES('team-a','Team A')`,
		`INSERT INTO applications(id,key,name,environment,owner_team_id,archived) VALUES
 ('allowed','allowed-key','Allowed','dev',NULL,false),
 ('teamed','team-key','Teamed','dev','team-a',false),
 ('hidden','hidden-key','Hidden','dev',NULL,false),
 ('archived','archived-key','Archived','dev',NULL,true),
 ('closed-env','closed-key','Closed environment','dev',NULL,false),
 ('test-app','test-key','Test','test',NULL,false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('allowed-dev','allowed','dev','Dev',false),
 ('teamed-dev','teamed','dev','Dev',false),
 ('hidden-dev','hidden','dev','Dev',false),
 ('archived-dev','archived','dev','Dev',false),
 ('closed-dev','closed-env','dev','Dev',true),
 ('test-test','test-app','test','Test',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('allowed','dev'),('teamed','dev'),('hidden','dev'),('archived','dev'),('closed-env','dev'),('test-app','test')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,tools) VALUES
 ('gitlab-main','dev','gitlab','Main','delivery','https://main.example.invalid','pat','[{"name":"list_pipelines","enabled":true}]'),
 ('gitlab-second','dev','gitlab','Second','delivery','https://second.example.invalid','pat','[]'),
 ('gitlab-test','test','gitlab','Test','delivery','https://test.example.invalid','pat','[]')`,
		`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 SELECT v.id,v.app,v.env,v.connector,'project',v.external_id,u.id
 FROM (VALUES
 ('allowed-1','allowed','dev','gitlab-main','101'),
 ('allowed-2','allowed','dev','gitlab-main','102'),
 ('team-1','teamed','dev','gitlab-main','103'),
 ('hidden-1','hidden','dev','gitlab-main','104'),
 ('archived-1','archived','dev','gitlab-main','105'),
 ('closed-1','closed-env','dev','gitlab-main','106'),
 ('other-instance','allowed','dev','gitlab-second','107'),
 ('other-environment','test-app','test','gitlab-test','108')
 ) AS v(id,app,env,connector,external_id)
 CROSS JOIN users u WHERE u.email='admin@example.invalid'`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", nil).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
	}); err != nil {
		t.Fatal(err)
	}
	request := func(id, env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/"+id, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := func(id, env string, wantServices []string) {
		t.Helper()
		w := request(id, env)
		if w.Code != http.StatusOK {
			t.Fatalf("connector %s in %s: %d %s", id, env, w.Code, w.Body.String())
		}
		var got struct {
			Services []string `json:"services"`
			Queries  []string `json:"queries"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Services) != len(wantServices) {
			t.Fatalf("connector %s services=%v want=%v", id, got.Services, wantServices)
		}
		for i := range got.Services {
			if got.Services[i] != wantServices[i] {
				t.Fatalf("connector %s services=%v want=%v", id, got.Services, wantServices)
			}
		}
		if len(got.Queries) != 0 {
			t.Fatalf("unbound queries were advertised: %v", got.Queries)
		}
	}
	read("gitlab-main", "all", []string{"allowed-key", "team-key"})
	read("gitlab-second", "dev", []string{"allowed-key"})
	read("gitlab-test", "test", []string{})
	if w := request("gitlab-test", "dev"); w.Code != http.StatusNotFound {
		t.Fatalf("cross-environment connector detail: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='services' AND scope_kind='application'`); err != nil {
		t.Fatal(err)
	}
	read("gitlab-main", "dev", []string{"team-key"})
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='services' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	read("gitlab-main", "dev", []string{})
}
