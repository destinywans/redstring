package v3topology

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

func TestTopologyHTTPExcludesMissingAndDoesNotBorrowLinkedResourceTeam(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a disposable schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_topology_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-topology-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO teams(id,name) VALUES('resource-owner','Resource Owner'),('app-owner','Application Owner')`,
		`INSERT INTO rbac_roles(id,name) VALUES('resource-team-reader','Resource Team Reader')`,
		// The reader keeps the legacy viewer binding. Return the viewer to its
		// pre-162 grants (workspace.view only): the default global resources
		// (162) and environment (167) view would show every resource; access
		// comes from the team-scoped role and the dev grant added here.
		`DELETE FROM v3_role_grants WHERE role_id='viewer' AND module<>'workspace'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('resource-team-reader','resources','view','team','resource-owner')`,
		`INSERT INTO applications(id,name,key,environment,owner_team_id) VALUES('linked-app','Linked App','linked','dev','app-owner')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('linked-app-dev','linked-app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('linked-app','dev')`,
		`INSERT INTO cmdb_sync_sources(id,kind,name,last_success_at,environment_key) VALUES('source','kubernetes','Cluster',now(),'dev')`,
		`INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,owner_team_id,observed_at,attributes) VALUES
 ('linked','Linked','deployment','kubernetes','source','uid-linked','resource-owner',now(),' {"cluster":"c","environment_key":"dev","inventory_presence":"present"}'),
 ('unlinked','Unlinked','deployment','kubernetes','source','uid-unlinked','resource-owner',now(),' {"cluster":"c","environment_key":"dev","inventory_presence":"present"}'),
	('missing','Missing','deployment','kubernetes','source','uid-missing','resource-owner',now(),' {"cluster":"c","environment_key":"dev","inventory_presence":"missing"}'),
	('old-observation','Old observation','deployment','kubernetes','source','uid-old','resource-owner',now()-interval '1 day',' {"cluster":"c","environment_key":"dev","inventory_presence":"present"}'),
	('manual','Manual CMDB','host','manual','','','resource-owner',NULL,'{"cluster":"c"}')`,
		`INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id,provenance)
	VALUES('linked','linked-app','linked-app-dev','discovered'),
	('old-observation','linked-app','linked-app-dev','discovered'),
	('manual','linked-app','linked-app-dev','manual')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	user, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'resource-team-reader')`, user.ID); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, policy).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(token string) projection {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/topology?cluster=c", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET topology status=%d body=%s", w.Code, w.Body.String())
		}
		var result projection
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := request(token)
	if result.CompleteV3DTO || len(result.Nodes) != 1 || result.Nodes[0].ID != "unlinked" || len(result.Edges) != 0 {
		t.Fatalf("topology leaked linked or missing resource: %+v", result)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-topology-password")
	if err != nil {
		t.Fatal(err)
	}
	result = request(adminToken)
	if len(result.Nodes) != 3 || result.Nodes[0].ID != "linked" || result.Nodes[1].ID != "manual" || result.Nodes[2].ID != "unlinked" {
		t.Fatalf("fresh collector and manual CMDB were not distinguished: %+v", result)
	}
	if len(result.Collection) != 1 || result.Collection[0].State != "fresh" || result.Collection[0].SourceKind != "kubernetes" ||
		result.Collection[0].EnvironmentID != "dev" || result.Collection[0].LastSuccessAt == nil {
		t.Fatalf("fresh collection status: %+v", result.Collection)
	}
	if _, err := db.Exec(ctx, `UPDATE cmdb_sync_sources SET last_success_at=now()-interval '1 day' WHERE id='source'`); err != nil {
		t.Fatal(err)
	}
	result = request(adminToken)
	if len(result.Nodes) != 1 || result.Nodes[0].ID != "manual" {
		t.Fatalf("stale source still presented as current topology: %+v", result)
	}
	// PA-61: the empty or reduced graph says why. The team-scoped reader also
	// learns that the collector is stale, without its name.
	for _, who := range []string{adminToken, token} {
		result = request(who)
		if len(result.Collection) != 1 || result.Collection[0].State != "stale" || result.Collection[0].FreshSources != 0 ||
			result.Collection[0].Sources != 1 || result.Collection[0].StaleAfterSeconds != 300 {
			t.Fatalf("stale collection status: %+v", result.Collection)
		}
	}
	if _, err := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE role_id='resource-team-reader'`); err != nil {
		t.Fatal(err)
	}
	if result = request(token); len(result.Nodes) != 0 || len(result.Collection) != 0 {
		t.Fatalf("collector state shown without a resources grant: %+v", result)
	}
}
