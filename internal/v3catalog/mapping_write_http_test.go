package v3catalog

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// PA-19 P3 stage 2 step 6: with mapping writes switched, a save changes the
// registry targets of metrics, logs, traces and workload (catalog resources
// only, one target per system, audited), keeps code on its stored binding,
// leaves deploy and dashboard read-only, and is guarded by the target
// revision of migration 172.
func TestServiceMappingWritesTheRegistry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mapping_write_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, core.User{Email: "editor@example.invalid", Name: "Editor", Role: "viewer"}, "isolated-editor-password")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	tool := func(name string) string {
		raw, _ := json.Marshal([]map[string]any{{"name": name, "desc": "", "enabled": true}})
		return string(raw)
	}
	legacy := `[{"kind":"prometheus","connector_id":"prometheus","resource":"checkout-legacy"},
 {"kind":"loki","connector_id":"loki","resource":"checkout"},
 {"kind":"argocd","connector_id":"argo","resource":"checkout-app"}]`
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer'`)
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','services','view','environment','dev'),('viewer','services','change','environment','dev'),
 ('viewer','connectors','view','environment','dev'),('viewer','connectors','manage','environment','dev')`)
	exec(`INSERT INTO applications(id,key,name) VALUES('app','checkout','Checkout'),('app2','orders','Orders')`)
	exec(`INSERT INTO application_environments(id,application_id,key,name,bindings,binding_revision) VALUES
 ('app-dev','app','dev','Dev','` + legacy + `',3),('app2-dev','app2','dev','Dev','[{"kind":"loki","connector_id":"loki","resource":"orders"}]',1)`)
	exec(`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('prom','dev','prometheus','Prometheus','observe','http://127.0.0.1:19090','none','',true,'` + tool("query_metrics") + `'),
 ('loki-1','dev','loki','Loki','observe','http://127.0.0.1:13100','none','',true,'` + tool("search_logs") + `'),
 ('es-1','dev','elasticsearch','Elasticsearch','observe','http://127.0.0.1:19200','none','',true,'` + tool("search_logs") + `'),
 ('tempo-1','dev','tempo','Tempo','observe','http://127.0.0.1:13200','none','',true,'` + tool("query_traces") + `'),
 ('k8s','dev','kubernetes','Lab','infra','https://127.0.0.1:6443','bearer','',true,'` + tool("read_workloads") + `'),
 ('gitlab-1','dev','gitlab','GitLab','delivery','https://gitlab.example.invalid','bearer','vault://kv/gitlab#token',true,'[]')`)

	// Migration 172 starts each service environment at binding_revision + 1.
	// Re-run it on rows that existed before it, as on the acceptance data.
	exec(`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('app2','dev')`)
	exec(`DROP TRIGGER v3_connector_targets_revision ON v3_connector_targets;
 DROP TRIGGER application_environments_target_revision ON application_environments;
 DROP TRIGGER v3_service_code_projects_revision ON v3_service_code_projects;
 DROP FUNCTION v3_target_revision_bump();
 DROP FUNCTION v3_service_target_revision_bump(text,text);
 DROP TABLE v3_service_target_revisions;
 DELETE FROM schema_migrations WHERE name='172_v3_target_revision.sql'`)
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Migration 174 hooks the code projects on the function 172 defines.
	exec(`CREATE TRIGGER v3_service_code_projects_revision
 AFTER INSERT OR UPDATE OR DELETE ON v3_service_code_projects
 FOR EACH ROW EXECUTE FUNCTION v3_target_revision_bump()`)
	revisionOf := func(app string) int64 {
		t.Helper()
		var revision int64
		if err := db.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM v3_service_target_revisions
 WHERE application_id=$1 AND environment_id='dev'),1)`, app).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	if revisionOf("app") != 4 || revisionOf("app2") != 2 {
		t.Fatalf("migration 172 revisions: %d %d", revisionOf("app"), revisionOf("app2"))
	}
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('t-metrics','app','dev','prom','metric_resource','checkout-api',$1),
 ('t-logs','app','dev','loki-1','log_stream','checkout',$1),
 ('t-workload','app','dev','k8s','workload','checkout-api',$1),
 ('t-code','app','dev','gitlab-1','project','42',$1),
 ('t2-logs','app2','dev','loki-1','log_stream','orders',$1)`, user.ID)
	// D20: the code project is a registry row, which raises the revision too.
	exec(`INSERT INTO v3_service_code_projects(application_id,environment_id,target_id,source) VALUES('app','dev','t-code','migrated_binding')`)
	if revisionOf("app") != 9 || revisionOf("app2") != 3 {
		t.Fatalf("target writes must raise the revision: %d %d", revisionOf("app"), revisionOf("app2"))
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"queries":[
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout-api","query_id":"request_rate","expression":"sum(rate(x[5m]))"},
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout-canary","query_id":"request_rate","expression":"sum(rate(y[5m]))"}],
 "scopes":[
 {"connector_id":"loki-1","application_id":"app","environment_id":"dev","resource_id":"checkout","provider":"loki","loki_labels":{"app":"checkout"}},
 {"connector_id":"loki-1","application_id":"app","environment_id":"dev","resource_id":"orders","provider":"loki","loki_labels":{"app":"orders"}},
 {"connector_id":"es-1","application_id":"app","environment_id":"dev","resource_id":"checkout-es","provider":"elasticsearch","elasticsearch_index":"checkout","elasticsearch_filters":{"service":"checkout"}},
 {"connector_id":"tempo-1","application_id":"app","environment_id":"dev","resource_id":"checkout","provider":"tempo","tempo_service":"checkout","tempo_attributes":{"deployment.environment":"dev"}}],
 "workloads":[{"connector_id":"k8s","application_id":"app","environment_id":"dev","resource_id":"checkout-api","namespace":"aiops-lab","deployment":"checkout-api"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(st, policy, "dev")
	svc.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)}
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session,
		func(ctx context.Context, u core.User, module, action, env string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "editor@example.invalid", "isolated-editor-password")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	snapshot := func() mappingSaveView {
		t.Helper()
		w := call("GET", "/service-mappings", "")
		var page struct {
			Rows []struct {
				Service string           `json:"service"`
				Save    *mappingSaveView `json:"save"`
			} `json:"rows"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("mappings: %d %s", w.Code, w.Body.String())
		}
		for _, row := range page.Rows {
			if row.Service == "checkout" && row.Save != nil {
				return *row.Save
			}
		}
		t.Fatalf("no checkout snapshot: %s", w.Body.String())
		return mappingSaveView{}
	}
	// with returns the snapshot's bindings with system replaced (nil removes it).
	with := func(view mappingSaveView, system string, binding *mappingSaveBinding) []mappingSaveBinding {
		out := []mappingSaveBinding{}
		for _, b := range view.Bindings {
			if b.SystemID != system {
				out = append(out, b)
			}
		}
		if binding != nil {
			out = append(out, *binding)
		}
		return out
	}
	save := func(view mappingSaveView, bindings []mappingSaveBinding) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"environment_id": "dev", "items": []any{map[string]any{
			"service_id": view.ServiceID, "service_environment_id": view.ServiceEnvironmentID,
			"binding_revision": view.BindingRevision, "bindings": bindings}}})
		return call("PUT", "/service-mappings", string(raw))
	}
	expect := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("want %d %s: %d %s", status, code, w.Code, w.Body.String())
		}
	}
	targetOf := func(kind string) (string, string, string) {
		t.Helper()
		var id, connector, external string
		err := db.QueryRow(ctx, `SELECT id,connector_id,external_id FROM v3_connector_targets
 WHERE application_id='app' AND environment_id='dev' AND target_kind=$1`, kind).Scan(&id, &connector, &external)
		if err == pgx.ErrNoRows {
			return "", "", ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return id, connector, external
	}
	audits := func(action string) int {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action=$1 AND application_id='app'`, action).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// The snapshot carries the target revision; the options make the
	// registry systems editable.
	view := snapshot()
	if view.BindingRevision != 9 || !view.Editable {
		t.Fatalf("snapshot: %+v", view)
	}

	// Edit metrics to the catalog's other resource, then restore it.
	canary := &mappingSaveBinding{SystemID: "metrics", Kind: "prometheus", ConnectorID: "prom", Resource: "checkout-canary"}
	w := save(view, with(view, "metrics", canary))
	expect(w, 200, "")
	var saved struct {
		Items []mappingSaveCommittedItem `json:"items"`
	}
	if json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Items[0].BindingRevision <= view.BindingRevision {
		t.Fatalf("save response: %s", w.Body.String())
	}
	if id, connector, external := targetOf("metric_resource"); id == "t-metrics" || connector != "prom" || external != "checkout-canary" {
		t.Fatalf("metrics target after the edit: %s %s %s", id, connector, external)
	}
	var legacyRevision int64
	var stored string
	if err := db.QueryRow(ctx, `SELECT binding_revision,bindings::text FROM application_environments WHERE id='app-dev'`).Scan(&legacyRevision, &stored); err != nil ||
		legacyRevision != 3 || !strings.Contains(stored, "checkout-legacy") {
		t.Fatalf("stored bindings must stay as they were: %d %s %v", legacyRevision, stored, err)
	}
	if audits("v3.connector.target_unbound") != 1 || audits("v3.connector.target_bound") != 1 || audits("v3.service_mapping.saved") != 1 {
		t.Fatalf("audit after the edit: %d %d %d", audits("v3.connector.target_unbound"), audits("v3.connector.target_bound"), audits("v3.service_mapping.saved"))
	}
	var record string
	if err := db.QueryRow(ctx, `SELECT changes::text FROM management_audit WHERE action='v3.service_mapping.saved'`).Scan(&record); err != nil ||
		!strings.Contains(record, `"source": "registry"`) || !strings.Contains(record, `"action": "rebound"`) || !strings.Contains(record, "checkout-canary") {
		t.Fatalf("save audit: %s %v", record, err)
	}
	view = snapshot()
	expect(save(view, with(view, "metrics", &mappingSaveBinding{SystemID: "metrics", Kind: "prometheus", ConnectorID: "prom", Resource: "checkout-api"})), 200, "")
	if _, _, external := targetOf("metric_resource"); external != "checkout-api" {
		t.Fatal("metrics not restored")
	}

	// Concurrent editors: the second save with the old revision is refused.
	view = snapshot()
	traces := &mappingSaveBinding{SystemID: "traces", Kind: "tempo", ConnectorID: "tempo-1", Resource: "checkout"}
	expect(save(view, with(view, "traces", traces)), 200, "")
	expect(save(view, with(view, "traces", nil)), 409, "stale_mapping")
	// A target written elsewhere (integration page, import) raises it too.
	view = snapshot()
	exec(`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-extra','app','dev','gitlab-1','project','43',$1)`, user.ID)
	expect(save(view, view.Bindings), 409, "stale_mapping")
	exec(`DELETE FROM v3_connector_targets WHERE id='t-extra'`)

	// Refusals, none of which writes.
	view = snapshot()
	before := revisionOf("app")
	for name, tc := range map[string]struct {
		bindings []mappingSaveBinding
		status   int
		code     string
	}{
		"resource outside the catalog": {with(view, "metrics", &mappingSaveBinding{SystemID: "metrics", Kind: "prometheus", ConnectorID: "prom", Resource: "not-in-catalog"}), 422, "resource_not_in_catalog"},
		"connector of another system":  {with(view, "logs", &mappingSaveBinding{SystemID: "logs", Kind: "loki", ConnectorID: "prom", Resource: "checkout"}), 422, "connector_unavailable"},
		"unknown connector":            {with(view, "logs", &mappingSaveBinding{SystemID: "logs", Kind: "loki", ConnectorID: "loki-9", Resource: "checkout"}), 422, "connector_unavailable"},
		"another service's resource":   {with(view, "logs", &mappingSaveBinding{SystemID: "logs", Kind: "loki", ConnectorID: "loki-1", Resource: "orders"}), 409, "target_owned_by_other_service"},
		"deploy changed":               {with(view, "deploy", &mappingSaveBinding{SystemID: "deploy", Kind: "argocd", ConnectorID: "argo", Resource: "other"}), 422, "system_not_supported"},
		"code added elsewhere":         {with(view, "code", &mappingSaveBinding{SystemID: "code", Kind: "gitlab", ConnectorID: "gitlab-1", Resource: "43"}), 409, "code_mapping_requires_accept"},
	} {
		w := save(view, tc.bindings)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if revisionOf("app") != before {
		t.Fatal("a refused save changed the mapping")
	}

	// Logs moved from Loki to Elasticsearch, then removed; code removed.
	es := &mappingSaveBinding{SystemID: "logs", Kind: "elasticsearch", ConnectorID: "es-1", Resource: "checkout-es"}
	expect(save(view, with(view, "logs", es)), 200, "")
	if _, connector, _ := targetOf("log_stream"); connector != "" {
		t.Fatal("the Loki target survived the move")
	}
	if _, connector, external := targetOf("log_index"); connector != "es-1" || external != "checkout-es" {
		t.Fatal("no Elasticsearch target")
	}
	view = snapshot()
	withoutLogs := view
	withoutLogs.Bindings = with(view, "logs", nil)
	expect(save(view, with(withoutLogs, "code", nil)), 200, "")
	if _, connector, _ := targetOf("log_index"); connector != "" {
		t.Fatal("logs not removed")
	}
	// Removing the code project leaves the stored bindings as they are.
	var codeRows int
	if err := db.QueryRow(ctx, `SELECT binding_revision,bindings::text,(SELECT count(*) FROM v3_service_code_projects)
 FROM application_environments WHERE id='app-dev'`).Scan(&legacyRevision, &stored, &codeRows); err != nil ||
		legacyRevision != 3 || codeRows != 0 || !strings.Contains(stored, "checkout-legacy") {
		t.Fatalf("code removal: %d %s rows=%d %v", legacyRevision, stored, codeRows, err)
	}
	// A snapshot sent back as it is saves nothing.
	view = snapshot()
	w = save(view, view.Bindings)
	expect(w, 200, "")
	if json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Items[0].BindingRevision != view.BindingRevision {
		t.Fatalf("unchanged save moved the revision: %s", w.Body.String())
	}
	if got := fmt.Sprint(audits("v3.service_mapping.saved")); got != "5" {
		t.Fatalf("save audits: %s", got)
	}
}
