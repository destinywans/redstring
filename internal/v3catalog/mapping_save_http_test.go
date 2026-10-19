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

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceMappingSaveVersionedAndAtomic(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_mapping_save_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 6
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-mapping-password"); err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-mapping-password")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := st.CreateUser(ctx, core.User{Email: "mapping-writer@example.invalid", Name: "Mapping Writer", Role: "viewer"}, "isolated-writer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, writerToken, err := st.Login(ctx, writer.Email, "isolated-writer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment) VALUES
 ('app-checkout','Checkout','checkout','dev'),('app-payments','Payments','payments','dev')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('env-checkout','app-checkout','dev','Dev'),('env-payments','app-payments','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-checkout','dev'),('app-payments','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('prom','dev','prometheus','Prometheus','observe','http://127.0.0.1:19090','none','',true,'[{"name":"query_metrics","desc":"","enabled":true}]')`,
		`INSERT INTO rbac_roles(id,name) VALUES('mapping-writer','Mapping Writer')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('mapping-writer','environment','view','environment','dev'),
 ('mapping-writer','environment','view','environment','test'),
 ('mapping-writer','services','view','environment','dev'),
 ('mapping-writer','services','change','environment','dev'),
 ('mapping-writer','connectors','view','environment','dev'),
 ('mapping-writer','connectors','manage','environment','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'mapping-writer')`, writer.ID); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"queries":[
 {"connector_id":"prom","application_id":"app-checkout","environment_id":"dev","resource_id":"checkout-api","query_id":"request_rate","expression":"sum(rate(x[5m]))"},
 {"connector_id":"prom","application_id":"app-payments","environment_id":"dev","resource_id":"payments-api","query_id":"request_rate","expression":"sum(rate(y[5m]))"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	svc := New(st, policy, "dev", "test")
	svc.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)}
	svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev",
		EnvironmentIDs: []string{"dev", "test"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, environment, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", environment)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	put := func(environment, token, body string) *httptest.ResponseRecorder {
		return request(http.MethodPut, "/service-mappings", environment, token, body)
	}
	binding := func(resource string) string {
		return fmt.Sprintf(`{"system_id":"metrics","kind":"prometheus","connector_id":"prom","resource":%q}`, resource)
	}
	item := func(appID, envID string, revision int64, bindings string) string {
		return fmt.Sprintf(`{"service_id":%q,"service_environment_id":%q,"binding_revision":%d,"bindings":%s}`,
			appID, envID, revision, bindings)
	}
	batch := func(items ...string) string {
		return `{"environment_id":"dev","items":[` + strings.Join(items, ",") + `]}`
	}
	// The token is the target revision (migration 172), 1 until the first
	// change of the service environment's targets or stored bindings.
	revisionOf := func(appID string) int64 {
		t.Helper()
		var revision int64
		if err := db.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM v3_service_target_revisions
 WHERE application_id=$1 AND environment_id='dev'),1)`, appID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	// assertState checks the revision and the metrics target the save wrote.
	assertState := func(appID string, revision int64, resource string) {
		t.Helper()
		var actual string
		err := db.QueryRow(ctx, `SELECT external_id FROM v3_connector_targets
 WHERE application_id=$1 AND environment_id='dev' AND target_kind='metric_resource'`, appID).Scan(&actual)
		if err != nil && err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		if got := revisionOf(appID); got != revision || actual != resource {
			t.Fatalf("%s: revision=%d metrics target=%q; want %d %q", appID, got, actual, revision, resource)
		}
	}
	storedBindings := func(envID string) string {
		t.Helper()
		var stored string
		if err := db.QueryRow(ctx, `SELECT bindings::text FROM application_environments WHERE id=$1`, envID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		return stored
	}
	auditCount := func() int {
		t.Helper()
		var count int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.service_mapping.saved'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for _, body := range []string{
		"", `{}`, `{"rows":[]}`, `{"environment_id":"dev","items":[]}`,
		batch(`{"service_id":"app-checkout","service_environment_id":"env-checkout","binding_revision":1}`),
		batch(`{"service_id":"app-checkout","service_environment_id":"env-checkout","binding_revision":1,"bindings":null}`),
		batch(item("app-checkout", "env-checkout", 1, `["not-a-binding"]`)),
		`{"environment_id":"dev","Environment_ID":"test","items":[]}`,
		`{"environment_id":"dev","environment_id":"test","items":[]}`,
		batch(item("app-checkout", "env-checkout", 1, `[`+binding("checkout-api")+`,`+binding("payments-api")+`]`)),
	} {
		if w := put("dev", writerToken, body); w.Code != 422 {
			t.Fatalf("invalid mapping save status=%d body=%s request=%s", w.Code, w.Body.String(), body)
		}
	}
	assertState("app-checkout", 1, "")
	if got := auditCount(); got != 0 {
		t.Fatalf("invalid requests wrote %d audits", got)
	}
	valid := batch(item("app-payments", "env-payments", 1, `[`+binding("payments-api")+`]`),
		item("app-checkout", "env-checkout", 1, `[`+binding("checkout-api")+`]`))
	w := put("dev", writerToken, valid)
	if w.Code != 200 {
		t.Fatalf("valid mapping save status=%d body=%s", w.Code, w.Body.String())
	}
	var saved struct {
		EnvironmentID string                     `json:"environment_id"`
		Items         []mappingSaveCommittedItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.EnvironmentID != "dev" || len(saved.Items) != 2 ||
		saved.Items[0].ServiceID != "app-checkout" || saved.Items[0].BindingRevision != 2 ||
		saved.Items[1].ServiceID != "app-payments" || saved.Items[1].BindingRevision != 2 {
		t.Fatalf("saved response is not committed identities: %+v err=%v", saved, err)
	}
	assertState("app-checkout", 2, "checkout-api")
	assertState("app-payments", 2, "payments-api")
	if got := auditCount(); got != 2 {
		t.Fatalf("valid save audits=%d, want 2", got)
	}
	get := request(http.MethodGet, "/service-mappings", "dev", writerToken, "")
	if get.Code != 200 {
		t.Fatalf("GET mappings status=%d body=%s", get.Code, get.Body.String())
	}
	var view struct {
		Rows []mappingRow `json:"rows"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &view); err != nil || len(view.Rows) != 2 {
		t.Fatalf("GET mappings malformed: %+v err=%v", view, err)
	}
	for _, row := range view.Rows {
		if row.Save == nil || !row.Save.Editable || row.Save.BindingRevision != 2 || len(row.Save.Bindings) != 1 ||
			row.Save.Bindings[0].SystemID != "metrics" || row.Save.Bindings[0].ConnectorID != "prom" {
			t.Fatalf("GET omitted save identity: %+v", row)
		}
	}
	all := request(http.MethodGet, "/service-mappings", "all", adminToken, "")
	if all.Code != 200 || strings.Contains(all.Body.String(), `"save"`) {
		t.Fatalf("all-environment aggregate exposed save identity: %d %s", all.Code, all.Body.String())
	}
	for _, grant := range []struct {
		module, action string
	}{
		{"services", "change"},
		{"connectors", "manage"},
	} {
		if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='mapping-writer' AND module=$1 AND action=$2`,
			grant.module, grant.action); err != nil {
			t.Fatal(err)
		}
		withoutSave := request(http.MethodGet, "/service-mappings", "dev", writerToken, "")
		if withoutSave.Code != 200 || strings.Contains(withoutSave.Body.String(), `"save"`) {
			t.Fatalf("GET exposed save without %s.%s: %d %s", grant.module, grant.action,
				withoutSave.Code, withoutSave.Body.String())
		}
		if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('mapping-writer',$1,$2,'environment','dev')`, grant.module, grant.action); err != nil {
			t.Fatal(err)
		}
	}
	if w := put("dev", writerToken, batch(item("app-checkout", "env-checkout", 2, `[`+binding("checkout-api")+`]`))); w.Code != 200 {
		t.Fatalf("same-value save status=%d body=%s", w.Code, w.Body.String())
	}
	assertState("app-checkout", 2, "checkout-api")
	if got := auditCount(); got != 2 {
		t.Fatalf("same-value save created audit: %d", got)
	}
	for _, tc := range []struct {
		name, environment, body string
		status                  int
	}{
		{"stale", "dev", batch(item("app-checkout", "env-checkout", 1, `[]`)), 409},
		{"wrong environment identity", "dev", batch(item("app-checkout", "env-payments", 2, `[]`)), 409},
		{"header and body mismatch", "test", batch(item("app-checkout", "env-checkout", 2, `[]`)), 422},
		{"GitLab bypass", "dev", batch(item("app-checkout", "env-checkout", 2,
			`[`+binding("checkout-api")+`,{"system_id":"code","kind":"gitlab","connector_id":"gitlab-1","resource":"123"}]`)), 409},
		{"unknown service", "dev", batch(item("app-hidden", "env-hidden", 1, `[]`)), 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := put(tc.environment, writerToken, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	// A later stale item rolls back a valid earlier unbinding and its audit.
	if w := put("dev", writerToken, batch(
		item("app-checkout", "env-checkout", 2, `[]`),
		item("app-payments", "env-payments", 1, `[]`))); w.Code != 409 {
		t.Fatalf("mixed stale batch status=%d body=%s", w.Code, w.Body.String())
	}
	assertState("app-checkout", 2, "checkout-api")
	assertState("app-payments", 2, "payments-api")
	if got := auditCount(); got != 2 {
		t.Fatalf("rejected saves created audit: %d", got)
	}
	// A stored dashboard binding is read-only: a save cannot drop it. It is
	// not checked against legacy registrations (PA-19 P4), so the snapshot
	// stays editable.
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"grafana","connector_id":"grafana","resource":"dash-1"}]'
 WHERE id='env-payments'`); err != nil {
		t.Fatal(err)
	}
	get = request(http.MethodGet, "/service-mappings", "dev", writerToken, "")
	if get.Code != 200 || strings.Contains(get.Body.String(), `"unavailable_reason"`) {
		t.Fatalf("a stored dashboard binding made the mapping read-only: %d %s", get.Code, get.Body.String())
	}
	if w := put("dev", writerToken, batch(item("app-payments", "env-payments", revisionOf("app-payments"), `[`+binding("payments-api")+`]`))); w.Code != 422 ||
		!strings.Contains(w.Body.String(), `"system_not_supported"`) {
		t.Fatalf("stored dashboard binding was dropped: %d %s", w.Code, w.Body.String())
	}
	if stored := storedBindings("env-payments"); !strings.Contains(stored, "dash-1") {
		t.Fatalf("stored dashboard binding changed: %s", stored)
	}
	// A historical binding may carry fields unknown to core.Binding. The
	// display can still read its known fields, but a save cannot drop the
	// unknown field while serializing the stored bindings.
	if _, err := db.Exec(ctx, `UPDATE application_environments
 SET bindings='[{"kind":"grafana","connector_id":"grafana","resource":"dash-1","legacy_note":"must preserve"}]'
 WHERE id='env-payments'`); err != nil {
		t.Fatal(err)
	}
	get = request(http.MethodGet, "/service-mappings", "dev", writerToken, "")
	if get.Code != 200 {
		t.Fatalf("GET legacy bindings status=%d body=%s", get.Code, get.Body.String())
	}
	view = struct {
		Rows []mappingRow `json:"rows"`
	}{}
	if err := json.Unmarshal(get.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	var legacySave *mappingSaveView
	for _, row := range view.Rows {
		if row.Service == "payments" {
			legacySave = row.Save
		}
	}
	if legacySave == nil || legacySave.Editable || legacySave.UnavailableReason != "unsupported_persisted_bindings" {
		t.Fatalf("legacy binding exposed as editable: %+v", legacySave)
	}
	legacyRawBefore, legacyRevisionBefore := storedBindings("env-payments"), revisionOf("app-payments")
	if w := put("dev", writerToken, batch(item("app-payments", "env-payments", legacyRevisionBefore, `[`+binding("payments-api")+`]`))); w.Code != 409 {
		t.Fatalf("legacy field was dropped by save: %d %s", w.Code, w.Body.String())
	}
	if storedBindings("env-payments") != legacyRawBefore || revisionOf("app-payments") != legacyRevisionBefore || auditCount() != 2 {
		t.Fatalf("legacy raw JSON changed: before=%s/%d after=%s/%d", legacyRawBefore, legacyRevisionBefore,
			storedBindings("env-payments"), revisionOf("app-payments"))
	}
	// Audit failure must roll back the target and revision in the same tx.
	if _, err := db.Exec(ctx, `CREATE FUNCTION deny_mapping_save_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.service_mapping.saved' THEN RAISE EXCEPTION 'blocked test audit'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER deny_mapping_save_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION deny_mapping_save_audit()`); err != nil {
		t.Fatal(err)
	}
	if w := put("dev", writerToken, batch(item("app-checkout", "env-checkout", 2, `[]`))); w.Code != 503 {
		t.Fatalf("audit failure status=%d body=%s", w.Code, w.Body.String())
	}
	assertState("app-checkout", 2, "checkout-api")
	if got := auditCount(); got != 2 {
		t.Fatalf("audit failure changed count: %d", got)
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER deny_mapping_save_audit ON management_audit; DROP FUNCTION deny_mapping_save_audit()`); err != nil {
		t.Fatal(err)
	}
	// An explicit empty array is an authorized clear, with a new revision and
	// one durable audit entry; omitted or null bindings were rejected above.
	w = put("dev", writerToken, batch(item("app-checkout", "env-checkout", 2, `[]`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"binding_revision":3`) ||
		!strings.Contains(w.Body.String(), `"bindings":[]`) {
		t.Fatalf("explicit clear status=%d body=%s", w.Code, w.Body.String())
	}
	assertState("app-checkout", 3, "")
	if got := auditCount(); got != 3 {
		t.Fatalf("explicit clear audit count=%d", got)
	}
	// A role removal while the save waits on its target must be observed by
	// the in-transaction post-lock permission check.
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	var blockerPID int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `SELECT id FROM application_environments WHERE id='env-checkout' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- put("dev", writerToken, batch(item("app-checkout", "env-checkout", 3,
			`[`+binding("checkout-api")+`]`)))
	}()
	waitForMappingServiceLock(t, db, blockerPID)
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='mapping-writer' AND module='services' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	w = <-response
	if w.Code != 404 {
		t.Fatalf("revoked save status=%d body=%s", w.Code, w.Body.String())
	}
	assertState("app-checkout", 3, "")
	if got := auditCount(); got != 3 {
		t.Fatalf("revoked save created audit: %d", got)
	}
	// A confirmed code project (D20) may be retained unchanged or removed.
	// PUT cannot add it back after removal; source acceptance is deliberately
	// outside this replacement route.
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('mapping-writer','services','change','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[]' WHERE id='env-payments';
 INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools)
 VALUES('gitlab-1','dev','gitlab','GitLab','delivery','https://gitlab.example.invalid','pat','vault://kv/gitlab#token',true,'[]');
 INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 SELECT 't-code','app-payments','dev','gitlab-1','project','123',id FROM users LIMIT 1;
 INSERT INTO v3_service_code_projects(application_id,environment_id,target_id,source) VALUES('app-payments','dev','t-code','migrated_binding')`); err != nil {
		t.Fatal(err)
	}
	code := `{"system_id":"code","kind":"gitlab","connector_id":"gitlab-1","resource":"123"}`
	revision := revisionOf("app-payments")
	if w := put("dev", writerToken, batch(item("app-payments", "env-payments", revision, `[`+binding("payments-api")+`,`+code+`]`))); w.Code != 200 {
		t.Fatalf("persisted GitLab retention status=%d body=%s", w.Code, w.Body.String())
	}
	if got := auditCount(); got != 3 {
		t.Fatalf("identical GitLab retention wrote audit: %d", got)
	}
	w = put("dev", writerToken, batch(item("app-payments", "env-payments", revision, `[`+binding("payments-api")+`]`)))
	if w.Code != 200 {
		t.Fatalf("GitLab removal status=%d body=%s", w.Code, w.Body.String())
	}
	assertState("app-payments", revision+1, "payments-api")
	if stored := storedBindings("env-payments"); stored != "[]" {
		t.Fatalf("GitLab removal left %s", stored)
	}
	if got := auditCount(); got != 4 {
		t.Fatalf("GitLab removal audit count=%d", got)
	}
	if w := put("dev", writerToken, batch(item("app-payments", "env-payments", revision+1, `[`+binding("payments-api")+`,`+code+`]`))); w.Code != 409 {
		t.Fatalf("GitLab re-add bypass status=%d body=%s", w.Code, w.Body.String())
	}
	assertState("app-payments", revision+1, "payments-api")
}

func TestMappingStoredBindingShapeRejectsLossyHistoricalJSON(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"empty array", `[]`, true},
		{"exact", `[{"kind":"prometheus","connector_id":"prometheus","resource":"job=api"}]`, true},
		{"unknown field", `[{"kind":"prometheus","connector_id":"prometheus","resource":"job=api","legacy":"keep"}]`, false},
		{"missing field", `[{"kind":"prometheus","resource":"job=api"}]`, false},
		{"null field", `[{"kind":"prometheus","connector_id":null,"resource":"job=api"}]`, false},
		{"object instead of array", `{"kind":"prometheus","connector_id":"prometheus","resource":"job=api"}`, false},
		{"null instead of array", `null`, false},
		{"null entry", `[null]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mappingStoredBindingShape([]byte(tc.raw)); got != tc.valid {
				t.Fatalf("canonical=%t, want %t for %s", got, tc.valid, tc.raw)
			}
		})
	}
}
