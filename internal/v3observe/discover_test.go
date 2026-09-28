package v3observe

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

type discoveryAccessFunc func(rbac.V3Decision) error

func (f discoveryAccessFunc) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	return f(d)
}

func TestDiscoveryRequiresObjectScopeAndPreservesEvidence(t *testing.T) {
	calls := []rbac.V3Decision{}
	s := &Service{Access: discoveryAccessFunc(func(d rbac.V3Decision) error {
		calls = append(calls, d)
		if d.TeamID == "other" {
			return store.ErrForbidden
		}
		return nil
	})}
	for _, item := range []struct {
		team string
		want bool
	}{{"team-one", true}, {"other", false}} {
		allowed, err := s.canDiscoverWorkload(context.Background(), core.User{ID: "u"}, workloadCandidate{EnvironmentID: "dev", TeamID: item.team})
		if err != nil || allowed != item.want {
			t.Fatalf("team=%q allowed=%v error=%v", item.team, allowed, err)
		}
	}
	for _, decision := range calls {
		if (decision.Module != "services" && decision.Module != "resources") || decision.Action != "view" || decision.EnvironmentID != "dev" || decision.TeamID == "" || decision.ApplicationID != "" {
			t.Fatalf("object scope lost: %+v", decision)
		}
	}
	s.Access = discoveryAccessFunc(func(d rbac.V3Decision) error {
		if d.Module == "resources" {
			return store.ErrForbidden
		}
		return nil
	})
	if allowed, err := s.canDiscoverWorkload(context.Background(), core.User{ID: "u"}, workloadCandidate{EnvironmentID: "dev", TeamID: "team-one"}); err != nil || allowed {
		t.Fatalf("services-only grant exposed resource: allowed=%v error=%v", allowed, err)
	}
	item, ok := projectWorkload(workloadCandidate{ResourceID: "r", Name: "checkout", ExternalID: "uid", EnvironmentID: "dev", TeamName: "Platform"}, workloadAttributes{
		Cluster: "cluster", Namespace: "commerce", ResourceType: "Deployment", Images: []string{"registry/checkout:v1"},
		Labels: map[string]string{"tier": "T1", "team": "unverified-label"},
	})
	if !ok || item.ResourceID != "r" || item.OwnerHint != "Platform" || item.TierHint != "T1" || item.Replicas != nil || len(item.Matched) != 0 || len(item.Missing) != 0 {
		t.Fatalf("discovery invented or lost evidence: %+v", item)
	}
	item, ok = projectWorkload(workloadCandidate{Name: "api", ExternalID: "uid"}, workloadAttributes{Cluster: "c", Namespace: "n", ResourceType: "StatefulSet", Labels: map[string]string{"team": "payments", "tier": "high"}})
	if !ok || item.OwnerHint != "未识别" || item.OwnerLabelHint != "team=payments" || item.TierHint != "" {
		t.Fatalf("label was asserted as a verified owner or tier: %+v", item)
	}
}

func TestDiscoverUsesFreshScopedInventoryAndSuppressesAmbiguousNames(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; uses a disposable schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_discover_test_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-discovery-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','resources','view','global','*') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-discovery-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO teams(id,name) VALUES('platform','Platform'),('other','Other');
INSERT INTO cmdb_sync_sources(id,kind,name,last_success_at,environment_key)
 VALUES('src','kubernetes','Cluster One',now(),'dev'),
 ('stale','kubernetes','Old Cluster',now()-interval '1 day','dev'),
 ('prod-source','kubernetes','Production Cluster',now(),'prod');
INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,owner_team_id,observed_at,attributes) VALUES
 ('single','solo','deployment','kubernetes','src','uid-single','platform',now(),'{"uid":"uid-single","cluster":"cluster-one","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment","images":["registry/solo:v1"],"replicas":2}'),
 ('dup-a','shared','deployment','kubernetes','src','uid-dup-a',NULL,now(),'{"uid":"uid-dup-a","cluster":"cluster-one","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('dup-b','shared','deployment','kubernetes','src','uid-dup-b',NULL,now(),'{"uid":"uid-dup-b","cluster":"cluster-one","namespace":"b","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('old','old','deployment','kubernetes','stale','uid-old',NULL,now()-interval '1 day','{"uid":"uid-old","cluster":"old-cluster","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('missing','missing','deployment','kubernetes','src','uid-missing',NULL,now(),'{"uid":"uid-missing","cluster":"cluster-one","namespace":"a","environment_key":"dev","inventory_presence":"missing","resource_type":"Deployment"}'),
 ('unowned','unowned','deployment','kubernetes','src','uid-unowned',NULL,now(),'{"uid":"uid-unowned","cluster":"cluster-one","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"StatefulSet"}'),
 ('other','other','deployment','kubernetes','src','uid-other','other',now(),'{"uid":"uid-other","cluster":"cluster-one","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"DaemonSet"}'),
 ('archived-workload','archived','deployment','kubernetes','src','uid-archived',NULL,now(),'{"uid":"uid-archived","cluster":"cluster-one","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('registered-elsewhere','elsewhere','deployment','kubernetes','src','uid-elsewhere',NULL,now(),'{"uid":"uid-elsewhere","cluster":"cluster-one","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}'),
 ('prod','private','deployment','kubernetes','prod-source','uid-prod',NULL,now(),'{"uid":"uid-prod","cluster":"cluster-prod","namespace":"a","environment_key":"prod","inventory_presence":"present","resource_type":"Deployment"}');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment,owner_team_id)
 VALUES('registered-platform','Registered Platform','already','dev','platform'),
 ('registered-other','Registered Other','hidden','dev','other');
INSERT INTO applications(id,name,key,environment,archived) VALUES('archived-service','Archived','archived','dev',true);
INSERT INTO applications(id,name,key,environment) VALUES('other-env-service','Elsewhere','elsewhere','prod');
INSERT INTO application_environments(id,application_id,key,name)
 VALUES('registered-platform-env','registered-platform','dev','Dev'),
 ('registered-other-env','registered-other','dev','Dev'),
 ('other-env-service-env','other-env-service','prod','Prod');
INSERT INTO v3_application_environments(application_id,environment_id)
 VALUES('registered-platform','dev'),('registered-other','dev'),('other-env-service','prod')`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, nil).Register(api)
	api.MarkV3ObjectRoute("GET /api/v1/services/discover", "services", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Cluster    string              `json:"cluster"`
		Items      []discoveredService `json:"items"`
		Ambiguous  int                 `json:"ambiguous_workloads"`
		Registered int                 `json:"registered"`
	}
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/services/discover"+query, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		response.Items = nil
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	decode(request(""))
	if response.Cluster != "cluster-one" || response.Ambiguous != 2 || response.Registered != 2 || len(response.Items) != 3 {
		t.Fatalf("unexpected scoped inventory: %+v", response)
	}
	first := map[string]discoveredService{}
	for _, item := range response.Items {
		first[item.ID] = item
	}
	if first["solo"].OwnerHint != "Platform" || first["unowned"].OwnerHint != "未识别" || first["other"].OwnerHint != "Other" || first["private"].ID != "" {
		t.Fatalf("collector or owner scope was misrepresented: %+v", response.Items)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='resources' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	decode(request(""))
	if len(response.Items) != 0 || response.Registered != 2 || response.Ambiguous != 0 {
		t.Fatalf("services grant bypassed resources.view: %+v", response)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','resources','view','global','*')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='view';
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('admin','services','view','team','platform')`); err != nil {
		t.Fatal(err)
	}
	decode(request(""))
	if response.Cluster != "cluster-one" || response.Ambiguous != 0 || response.Registered != 1 || len(response.Items) != 1 || response.Items[0].ID != "solo" {
		t.Fatalf("team grant did not restrict discovered workloads: %+v", response)
	}
	// The new namesake is invisible to this team and in another cluster, but
	// the current POST /services lookup ignores cluster and owner team.
	if _, err := db.Exec(ctx, `INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,owner_team_id,observed_at,attributes)
 VALUES('hidden-namesake','solo','deployment','kubernetes','src','uid-hidden','other',now(),
 '{"uid":"uid-hidden","cluster":"cluster-two","namespace":"a","environment_key":"dev","inventory_presence":"present","resource_type":"Deployment"}')`); err != nil {
		t.Fatal(err)
	}
	decode(request("?cluster=cluster-one"))
	if response.Ambiguous != 1 || len(response.Items) != 0 || response.Registered != 1 {
		t.Fatalf("hidden same-name workload remained registrable or leaked count: %+v", response)
	}
	// A bounded count must fail explicitly instead of returning a truncated
	// number after the first thousand registered service/environment rows.
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment,owner_team_id)
 SELECT 'bulk-app-'||n,'Bulk '||n,'bulk-'||n,'dev','platform' FROM generate_series(1,1001) n;
INSERT INTO application_environments(id,application_id,key,name)
 SELECT 'bulk-env-'||n,'bulk-app-'||n,'dev','Dev' FROM generate_series(1,1001) n;
INSERT INTO v3_application_environments(application_id,environment_id)
 SELECT 'bulk-app-'||n,'dev' FROM generate_series(1,1001) n`); err != nil {
		t.Fatal(err)
	}
	if w := request("?cluster=cluster-one"); w.Code != 422 {
		t.Fatalf("unbounded registered count: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_application_environments WHERE application_id LIKE 'bulk-app-%';
DELETE FROM application_environments WHERE application_id LIKE 'bulk-app-%';
DELETE FROM applications WHERE id LIKE 'bulk-app-%'`); err != nil {
		t.Fatal(err)
	}
	// A broad scan through hidden/off-cluster rows also fails explicitly.
	if _, err := db.Exec(ctx, `INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,observed_at,attributes)
 SELECT 'load-'||n,'load-'||n,'deployment','kubernetes','src','uid-load-'||n,now(),
 jsonb_build_object('uid','uid-load-'||n,'cluster','cluster-other','namespace','a',
 'environment_key','dev','inventory_presence','present','resource_type','Deployment')
 FROM generate_series(1,2001) n`); err != nil {
		t.Fatal(err)
	}
	if w := request("?cluster=cluster-one"); w.Code != 422 {
		t.Fatalf("unbounded inventory scan: status=%d body=%s", w.Code, w.Body.String())
	}
}
