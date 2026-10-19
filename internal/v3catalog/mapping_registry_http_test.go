package v3catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PA-19 P3 stage 2 step 2: metrics, logs, traces and workload come from
// registry targets (real connector IDs); code is the confirmed project of
// v3_service_code_projects (D20);
// deploy and dash are read-only; the options route lists what the editor may
// offer. Writes of the registry systems: mapping_write_http_test.go.
func TestServiceMappingFromTheRegistry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mapping_registry_" + store.ID()
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
	tool := func(name string, on bool) string {
		raw, _ := json.Marshal([]map[string]any{{"name": name, "desc": "", "enabled": on}})
		return string(raw)
	}
	legacy := `[{"kind":"prometheus","connector_id":"prometheus","resource":"checkout-legacy"},
 {"kind":"loki","connector_id":"loki","resource":"checkout"},
 {"kind":"argocd","connector_id":"argo","resource":"checkout-app"}]`
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','services','view','environment','dev'),('viewer','services','change','environment','dev'),
 ('viewer','connectors','view','environment','dev'),('viewer','connectors','manage','environment','dev')`,
		`INSERT INTO applications(id,key,name) VALUES('app','checkout','Checkout')`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings,binding_revision) VALUES('app-dev','app','dev','Dev','` + legacy + `',3)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('prom','dev','prometheus','Prometheus','observe','http://127.0.0.1:19090','none','',true,'` + tool("query_metrics", true) + `'),
 ('loki-1','dev','loki','Loki','observe','http://127.0.0.1:13100','none','',false,'` + tool("search_logs", false) + `'),
 ('k8s','dev','kubernetes','Lab','infra','https://127.0.0.1:6443','bearer','',true,'` + tool("read_workloads", true) + `'),
 ('gitlab-1','dev','gitlab','GitLab','delivery','https://gitlab.example.invalid','bearer','vault://kv/gitlab#token',true,'[]')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('t-metrics','app','dev','prom','metric_resource','checkout-api',$1),
 ('t-logs','app','dev','loki-1','log_stream','checkout',$1),
 ('t-workload','app','dev','k8s','workload','checkout-api',$1),
 ('t-code','app','dev','gitlab-1','project','42',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_service_code_projects(application_id,environment_id,target_id,source)
 VALUES('app','dev','t-code','migrated_binding')`); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"queries":[{"connector_id":"prom","application_id":"app","environment_id":"dev",
 "resource_id":"checkout-api","query_id":"request_rate","expression":"sum(rate(x[5m]))"}],
 "scopes":[{"connector_id":"loki-1","application_id":"app","environment_id":"dev","resource_id":"checkout","provider":"loki","loki_labels":{"app":"checkout"}}],
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
	type mappingPage struct {
		Rows []struct {
			Service string                 `json:"service"`
			Cells   map[string]mappingCell `json:"cells"`
			Save    *mappingSaveView       `json:"save"`
		} `json:"rows"`
	}
	read := func() mappingPage {
		t.Helper()
		w := call("GET", "/service-mappings", "")
		var page mappingPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Rows) != 1 {
			t.Fatalf("mappings: %d %s", w.Code, w.Body.String())
		}
		return page
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("app\x00dev"))
	type optionsPage struct {
		Systems []mappingOptionSystem `json:"systems"`
	}
	options := func() map[string]mappingOptionSystem {
		t.Helper()
		w := call("GET", "/services/"+serviceID+"/external-targets/options", "")
		var page optionsPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("options: %d %s", w.Code, w.Body.String())
		}
		out := map[string]mappingOptionSystem{}
		for _, system := range page.Systems {
			out[system.SystemID] = system
		}
		return out
	}
	reason := func(s mappingOptionSystem) string {
		if s.UnavailableReason == nil {
			return ""
		}
		return *s.UnavailableReason
	}

	page := read()
	cells := page.Rows[0].Cells
	if cells["metrics"].Value != "checkout-api" || cells["metrics"].Brand != "prometheus" ||
		cells["logs"].Value != "checkout" || cells["logs"].Brand != "loki" ||
		cells["workload"].Value != "checkout-api" || cells["workload"].Brand != "kubernetes" ||
		cells["code"].Value != "42" || cells["deploy"].Value != "checkout-app" || cells["traces"].State != "missing" {
		t.Fatalf("registry cells: %+v", cells)
	}
	save := page.Rows[0].Save
	connectors := map[string]string{}
	for _, b := range save.Bindings {
		connectors[b.SystemID] = b.ConnectorID
	}
	var targetRevision int64
	if err := db.QueryRow(ctx, `SELECT revision FROM v3_service_target_revisions WHERE application_id='app' AND environment_id='dev'`).Scan(&targetRevision); err != nil {
		t.Fatal(err)
	}
	if save == nil || !save.Editable || save.BindingRevision != targetRevision || connectors["metrics"] != "prom" || connectors["logs"] != "loki-1" ||
		connectors["workload"] != "k8s" || connectors["code"] != "gitlab-1" || connectors["deploy"] != "argo" {
		t.Fatalf("save snapshot: %+v", save)
	}
	put := func(bindings []mappingSaveBinding) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"environment_id": "dev", "items": []map[string]any{{
			"service_id": save.ServiceID, "service_environment_id": save.ServiceEnvironmentID,
			"binding_revision": save.BindingRevision, "bindings": bindings}}})
		return call("PUT", "/service-mappings", string(raw))
	}
	replace := func(system, resource string) []mappingSaveBinding {
		out := []mappingSaveBinding{}
		for _, b := range save.Bindings {
			if b.SystemID == system {
				if resource == "" {
					continue
				}
				b.Resource = resource
			}
			out = append(out, b)
		}
		return out
	}
	expectCode := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("status %d body %s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	revisionField := fmt.Sprintf(`"binding_revision":%d`, targetRevision)
	expectCode(put(replace("metrics", "checkout-other")), 422, "resource_not_in_catalog")
	expectCode(put(replace("deploy", "other-app")), 422, "system_not_supported")
	if w := put(save.Bindings); w.Code != 200 || !strings.Contains(w.Body.String(), revisionField) {
		t.Fatalf("unchanged save: %d %s", w.Code, w.Body.String())
	}
	// The save snapshot sent back exactly as GET returned it is accepted;
	// editable and unavailable_reason are ignored.
	snapshot, _ := json.Marshal(map[string]any{"environment_id": "dev", "items": []any{save}})
	if !strings.Contains(string(snapshot), `"editable":true`) {
		t.Fatalf("snapshot shape: %s", snapshot)
	}
	if w := call("PUT", "/service-mappings", string(snapshot)); w.Code != 200 || !strings.Contains(w.Body.String(), revisionField) {
		t.Fatalf("snapshot sent back: %d %s", w.Code, w.Body.String())
	}
	if w := call("PUT", "/service-mappings", strings.Replace(string(snapshot), `"editable":true`, `"editable":"yes"`, 1)); w.Code != 422 {
		t.Fatalf("editable must stay a boolean: %d", w.Code)
	}
	// Removing the code project deletes its row and keeps the target; the
	// stored bindings stay as they are.
	w := put(replace("code", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"binding_revision":%d`, targetRevision+1)) || !strings.Contains(w.Body.String(), `"connector_id":"prom"`) {
		t.Fatalf("remove code: %d %s", w.Code, w.Body.String())
	}
	var stored string
	if err := db.QueryRow(ctx, `SELECT bindings::text FROM application_environments WHERE id='app-dev'`).Scan(&stored); err != nil ||
		!strings.Contains(stored, "checkout-legacy") || !strings.Contains(stored, "checkout-app") {
		t.Fatalf("stored legacy bindings: %s %v", stored, err)
	}
	var codeRows, targetRows, removals int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM v3_service_code_projects),
 (SELECT count(*) FROM v3_connector_targets WHERE id='t-code'),
 (SELECT count(*) FROM management_audit WHERE action='v3.service_code_project.removed' AND target='t-code')`).Scan(&codeRows, &targetRows, &removals); err != nil ||
		codeRows != 0 || targetRows != 1 || removals != 1 {
		t.Fatalf("code removal: rows=%d targets=%d audits=%d err=%v", codeRows, targetRows, removals, err)
	}

	// Options: per system, in mapping order, with readiness and catalog
	// resources; code lists the service's GitLab projects.
	o := options()
	metrics, logs, workload, code, deploy, dash := o["metrics"], o["logs"], o["workload"], o["code"], o["deploy"], o["dash"]
	if !metrics.Editable || reason(metrics) != "" || metrics.Current == nil || *metrics.Current.TargetID != "t-metrics" ||
		metrics.Current.ConnectorID != "prom" || metrics.Current.ResourceID != "checkout-api" || len(metrics.Options) != 1 ||
		!metrics.Options[0].Ready || fmt.Sprint(metrics.Options[0].Resources) != "[checkout-api]" {
		t.Fatalf("metrics options: %+v", metrics)
	}
	if len(logs.Options) != 1 || logs.Options[0].Ready || fmt.Sprint(logs.Options[0].NotReady) != "[connector_disabled tool_disabled]" ||
		fmt.Sprint(logs.Options[0].Resources) != "[checkout]" {
		t.Fatalf("logs options: %+v", logs)
	}
	if len(workload.Options) != 1 || fmt.Sprint(workload.Options[0].NotReady) != "[credential_missing]" || workload.Current == nil {
		t.Fatalf("workload options: %+v", workload)
	}
	if !code.Editable || code.Current != nil || len(code.Options) != 1 || fmt.Sprint(code.Options[0].NotReady) != "[credential_not_approved]" ||
		fmt.Sprint(code.Options[0].Resources) != "[42]" {
		t.Fatalf("code options: %+v", code)
	}
	if deploy.Editable || reason(deploy) != "system_not_supported" || deploy.Current == nil || deploy.Current.ResourceID != "checkout-app" ||
		deploy.Current.TargetID != nil || reason(dash) != "system_not_supported" || dash.Current != nil {
		t.Fatalf("read-only systems: %+v %+v", deploy, dash)
	}
	if o["traces"].Current != nil || len(o["traces"].Options) != 0 {
		t.Fatalf("traces options: %+v", o["traces"])
	}

	// Permissions: without change, code is forbidden; without connectors.view
	// the options are refused; an unknown service is 404.
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='services' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	if c := options()["code"]; c.Editable || reason(c) != "forbidden" {
		t.Fatalf("code without change: %+v", c)
	}
	unknown := base64.RawURLEncoding.EncodeToString([]byte("missing\x00dev"))
	if w := call("GET", "/services/"+unknown+"/external-targets/options", ""); w.Code != 404 {
		t.Fatalf("unknown service: %d", w.Code)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/services/"+serviceID+"/external-targets/options", ""); w.Code != 403 {
		t.Fatalf("without connectors.view: %d %s", w.Code, w.Body.String())
	}
}
