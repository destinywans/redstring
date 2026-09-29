package v3topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// This exercises the registered route, persisted CMDB scope and the real
// Prometheus reader. Only the Prometheus HTTP endpoint is a controlled fixture.
func TestFullTopologyHTTPSourceAndScope(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated TEST_DATABASE_URL required")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_topology_full_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-topology-password"); err != nil {
		t.Fatal(err)
	}
	user, token, err := st.Login(ctx, "admin@example.invalid", "isolated-topology-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO cmdb_sync_sources(id,kind,name,last_success_at,environment_key) VALUES('source','kubernetes','Cluster',now(),'dev')`,
		`INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,observed_at,attributes) VALUES
 ('checkout','Checkout','service','kubernetes','source','uid-checkout',now(),'{"cluster":"c","namespace":"shop","zone":"z1","environment_key":"dev","inventory_presence":"present"}'),
 ('orders','Orders','service','kubernetes','source','uid-orders',now(),'{"cluster":"c","namespace":"shop","zone":"z1","environment_key":"dev","inventory_presence":"present"}')`,
		`INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES
 ('checkout','app','app-dev'),('orders','app','app-dev')`,
		`INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id)
 VALUES('rel','checkout','orders','depends_on','discovered','kubernetes','source')`,
		`INSERT INTO cmdb_sync_sources(id,kind,name,last_success_at,environment_key) VALUES('other-source','kubernetes','Other cluster',now(),'other')`,
		`INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id)
 VALUES('cross-env-rel','checkout','orders','calls','discovered','kubernetes','other-source')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	// Many unrelated, currently visible resources in the same cluster must not
	// make a two-node center neighborhood fail on a pre-authorization count.
	if _, err = db.Exec(ctx, `INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,observed_at,attributes)
 SELECT 'unrelated-'||g,'Unrelated '||g,'service','kubernetes','source','uid-unrelated-'||g,now(),
 '{"cluster":"c","namespace":"shop","zone":"z1","environment_key":"dev","inventory_presence":"present"}'::jsonb
 FROM generate_series(1,1001) g`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id)
 SELECT 'unrelated-'||g,'app','app-dev' FROM generate_series(1,1001) g`); err != nil {
		t.Fatal(err)
	}
	var revision time.Time
	if err = db.QueryRow(ctx, `SELECT updated_at FROM cmdb_resource_relations WHERE id='rel'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	var mode atomic.Int32
	var revoked atomic.Bool
	var calls atomic.Int32
	const expression = `topology_v1{application_id="app",environment_id="dev"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" || r.URL.Query().Get("query") != expression {
			t.Errorf("unexpected query: %s", r.URL)
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		start, parseErr := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		if parseErr != nil {
			t.Error(parseErr)
			http.Error(w, "bad start", http.StatusBadRequest)
			return
		}
		series := fullTopologyTestSeries(start, revision)
		switch mode.Load() {
		case 1: // Incomplete evidence must not become a numeric zero.
			series = series[1:]
		case 2: // A cross-environment label cannot be borrowed.
			series[0]["metric"].(map[string]string)["environment_id"] = "other"
		case 3: // A stale relation identity cannot describe the current graph.
			for _, line := range series {
				labels := line["metric"].(map[string]string)
				if labels["relation_id"] == "rel" {
					labels["relation_revision"] = revision.Add(-time.Second).Format(time.RFC3339Nano)
				}
			}
		case 4: // The graph grant disappears while the remote query is in flight.
			revoked.Store(true)
		case 5: // A CMDB relation changes after the first graph read.
			if _, updateErr := db.Exec(context.Background(), `UPDATE cmdb_resource_relations SET updated_at=now()+interval '1 second' WHERE id='rel'`); updateErr != nil {
				t.Error(updateErr)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success", "data": map[string]any{"resultType": "matrix", "result": series},
		})
	}))
	defer upstream.Close()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,tools,enabled)
 VALUES('connector','dev','prometheus','Prometheus','observe',$1,'none','[{"name":"query_metrics","enabled":true}]',true)`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('target','app','dev','connector','metric_resource','app-metrics',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	reader, err := v3integrations.NewPrometheusReader(db, "nonprod", nil, map[v3integrations.PrometheusQueryKey]string{
		{ConnectorID: "connector", ApplicationID: "app", EnvironmentID: "dev", ResourceID: "app-metrics", QueryID: "topology_v1"}: expression,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	access := accessFunc(func(d rbac.V3Decision) error {
		if revoked.Load() {
			return store.ErrForbidden
		}
		return policy.Authorize(ctx, user, d)
	})
	api := httpapi.New(st, nil, true)
	NewWithMetrics(db, access, reader).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "other"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(env, query string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/topology"+query, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	checkError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Code != code {
			t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	const query = "?cluster=c&around=orders&depth=1&group_by=namespace"
	checkError(request("dev", "?cluster=c"), 422, "invalid_topology_query")
	checkError(request("other", query), 404, "topology_center_not_found")
	w := request("dev", query)
	if w.Code != http.StatusOK {
		t.Fatalf("complete topology: %d %s", w.Code, w.Body.String())
	}
	var full fullTopology
	if err = json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.Groups) != 1 || len(full.Nodes) != 2 || len(full.Edges) != 1 ||
		full.Nodes[0].Status != "ok" || full.Nodes[1].Status != "danger" ||
		full.Edges[0].Tone != "danger" || full.Detail.ID != "orders" ||
		full.Detail.Conns.Used != 8 || full.Detail.Conns.Max != 10 ||
		full.Detail.FromService.Service != "Checkout" || len(full.Detail.Wait) != 6 ||
		len(full.Sankey.Links) != 1 || full.Sankey.Links[0].Value != 50 ||
		full.Blast != "1 observed impacted resources / 2 visible resources" ||
		w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("wrong complete topology: %+v", full)
	}
	for _, state := range []int32{1, 2, 3} {
		mode.Store(state)
		checkError(request("dev", query), 503, "topology_source_incomplete")
	}
	mode.Store(4)
	checkError(request("dev", query), 404, "topology_center_not_found")
	revoked.Store(false)
	mode.Store(5)
	checkError(request("dev", query), 503, "topology_source_incomplete")
	mode.Store(0)
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("neighbor-%d", i)
		if _, err = db.Exec(ctx, `INSERT INTO cmdb_resources(id,name,kind,source_kind,source_id,external_id,observed_at,attributes)
 VALUES($1,$1,'service','kubernetes','source',$2,now(),'{"cluster":"c","namespace":"shop","zone":"z1","environment_key":"dev","inventory_presence":"present"}')`, id, "uid-"+id); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES($1,'app','app-dev')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id)
 VALUES($1,$2,'orders','depends_on','discovered','kubernetes','source')`, "relation-"+id, id); err != nil {
			t.Fatal(err)
		}
	}
	checkError(request("dev", query), 413, "topology_scope_too_broad")
	if _, err = db.Exec(ctx, `UPDATE cmdb_sync_sources SET last_success_at=now()-interval '1 day' WHERE id='source'`); err != nil {
		t.Fatal(err)
	}
	checkError(request("dev", query), 404, "topology_center_not_found")
	if calls.Load() != 6 {
		t.Fatalf("source queried %d times, expected only after authorized fresh graph", calls.Load())
	}
}

func fullTopologyTestSeries(start, revision time.Time) []map[string]any {
	base := map[string]string{"schema": "topology_v1", "application_id": "app", "environment_id": "dev"}
	clone := func() map[string]string {
		out := make(map[string]string, len(base)+6)
		for k, v := range base {
			out[k] = v
		}
		return out
	}
	series := make([]map[string]any, 0, 11)
	add := func(field, unit, value string, extra map[string]string) {
		labels := clone()
		labels["field"], labels["unit"] = field, unit
		for k, v := range extra {
			labels[k] = v
		}
		values := make([]any, 6)
		for i := range values {
			values[i] = []any{strconv.FormatInt(start.Add(time.Duration(i)*time.Minute).Unix(), 10), value}
		}
		series = append(series, map[string]any{"metric": labels, "values": values})
	}
	for _, item := range []struct{ id, health, impacted string }{{"checkout", "0", "0"}, {"orders", "2", "1"}} {
		extra := map[string]string{"resource_id": item.id, "external_uid": "uid-" + item.id,
			"source_kind": "kubernetes", "source_id": "source"}
		add("node_health", "state", item.health, extra)
		add("node_impacted", "bool", item.impacted, extra)
		if item.id == "orders" {
			add("center_connections_used", "count", "8", extra)
			add("center_connections_max", "count", "10", extra)
			add("center_wait_p95_ms", "milliseconds", "15", extra)
			caller := make(map[string]string, len(extra)+1)
			for k, v := range extra {
				caller[k] = v
			}
			caller["caller_id"] = "checkout"
			add("center_from_used", "count", "4", caller)
			add("center_from_max", "count", "5", caller)
		}
	}
	rel := map[string]string{"relation_id": "rel", "from_resource_id": "checkout", "to_resource_id": "orders",
		"relation_revision": revision.UTC().Format(time.RFC3339Nano)}
	add("edge_rps", "requests_per_second", "50", rel)
	add("edge_error_pct", "percent", "8", rel)
	if len(series) != 11 {
		panic(fmt.Sprintf("test topology series=%d", len(series)))
	}
	return series
}
