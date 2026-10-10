package v3telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
)

func isolatedRegistryDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := context.Background()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_telemetry_registry_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedLab writes labState() into the real schema.
func seedLab(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	bindings := map[string][]map[string]string{}
	for _, b := range labState().Bindings {
		bindings[b.ApplicationID] = append(bindings[b.ApplicationID], map[string]string{"kind": b.Kind, "connector_id": b.ConnectorID, "resource": b.Resource})
	}
	for app, list := range bindings {
		raw, _ := json.Marshal(list)
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO applications(id,name,key,environment) VALUES($1,$1,$1,'prod')`, []any{app}},
			{`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES($1||'-prod',$1,'prod','Prod',$2)`, []any{app, raw}},
			{`INSERT INTO v3_application_environments(application_id,environment_id) VALUES($1,'prod')`, []any{app}},
		} {
			if _, err := db.Exec(ctx, statement.sql, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []string{
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,enabled,tools) VALUES
 ('` + labPrometheus + `','prod','prometheus','Prometheus','observe','http://127.0.0.1:19090','none',true,
  '[{"name":"query_metrics","desc":"Read-only","enabled":true}]')`,
		`INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('target-checkout-metrics','app-lab-checkout','prod','` + labPrometheus + `','metric_resource','lab-checkout-api',(SELECT id FROM users WHERE email='admin@example.invalid')),
 ('target-order-metrics','app-lab-order','prod','` + labPrometheus + `','metric_resource','lab-order-api',(SELECT id FROM users WHERE email='admin@example.invalid'))`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportAppliesTheReviewedPlanAndCompares(t *testing.T) {
	db := isolatedRegistryDB(t)
	seedLab(t, db)
	ctx := context.Background()
	input := labInput()

	// The database state yields exactly the reviewed offline plan.
	state, err := LoadState(ctx, db, input.Environments)
	if err != nil {
		t.Fatal(err)
	}
	reviewed := BuildPlan(input, labState())
	if plan := BuildPlan(input, state); plan.Digest != reviewed.Digest {
		t.Fatalf("database plan differs from the reviewed one:\n%+v\n%+v", plan.Summary, reviewed.Summary)
	}

	// Apply refuses unknown operators and unreviewed plans before writing.
	if _, err := Apply(ctx, db, input, reviewed.Digest, "nobody@example.invalid"); !errors.Is(err, ErrActor) {
		t.Fatalf("unknown actor: %v", err)
	}
	if _, err := Apply(ctx, db, input, strings.Repeat("0", 64), "admin@example.invalid"); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("unreviewed digest: %v", err)
	}
	result, err := Apply(ctx, db, input, reviewed.Digest, "admin@example.invalid")
	if err != nil || result.ConnectorsCreated != 3 || result.TargetsCreated != 4 {
		t.Fatalf("apply: %+v %v", result, err)
	}
	if _, err := Apply(ctx, db, input, reviewed.Digest, "admin@example.invalid"); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("second apply of the same plan: %v", err)
	}
	var enabled int
	var tools, options string
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE enabled),max(tools::text),max(options::text)
 FROM v3_connectors WHERE provider='loki'`).Scan(&enabled, &tools, &options); err != nil ||
		enabled != 0 || !strings.Contains(tools, `"enabled": false`) || options != "{}" {
		t.Fatalf("imported connector: enabled=%d tools=%s options=%s %v", enabled, tools, options, err)
	}
	var metricTargets, audits, targetAudits int
	if err := db.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM v3_connector_targets WHERE target_kind='metric_resource'),
 (SELECT count(*) FROM management_audit WHERE action='connector.imported' AND changes->>'plan_digest'=$1),
 (SELECT count(*) FROM management_audit WHERE action='v3.connector.target_imported' AND application_id<>'' AND changes->>'environment_id'='prod')`,
		reviewed.Digest).Scan(&metricTargets, &audits, &targetAudits); err != nil || metricTargets != 2 || audits != 3 || targetAudits != 4 {
		t.Fatalf("metric targets=%d connector audits=%d target audits=%d %v", metricTargets, audits, targetAudits, err)
	}

	// Compare: what was imported matches but is not readable until the
	// connectors are activated; conflicts stay missing.
	raw, _ := json.Marshal(reviewed.Catalog)
	catalog, err := ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{Tier: "production", Catalog: catalog, Approvals: NewApprovals(nil)}
	if state, err = LoadState(ctx, db, input.Environments); err != nil {
		t.Fatal(err)
	}
	report := CompareAll(ctx, db, resolver, input.Telemetry, state)
	want := map[string]string{
		"app-lab-checkout/prometheus": StateMatch, "app-lab-checkout/loki": StateMatchNotReady, "app-lab-checkout/tempo": StateMatchNotReady,
		"app-lab-order/prometheus": StateMatch, "app-lab-order/loki": StateMissing, "app-lab-order/elasticsearch": StateMissing,
		"app-lab-order/tempo": StateMatchNotReady, "app-lab-payments/prometheus": StateMissing, "app-lab-payments/loki": StateLegacyUnregistered,
		"app-lab-payments/tempo": StateMissing, "app-lab-search/elasticsearch": StateMissing,
	}
	got := map[string]string{}
	for _, item := range report.Items {
		got[item.ApplicationID+"/"+item.LegacyProvider] = item.State
		if item.State == StateMatchNotReady && !reflect.DeepEqual(item.Reasons, []string{"connector_disabled", "tool_disabled"}) {
			t.Fatalf("not ready reasons: %+v", item)
		}
	}
	if !reflect.DeepEqual(got, want) || report.Checked != 11 || report.Differences() != 5 {
		t.Fatalf("compare report: %+v", report)
	}
	// The imported workload resolves to the same Deployment but needs its
	// connector, tool and read credential; the unregistered one stays out.
	workloads := CompareReport{Counts: map[string]int{}}
	workloads.CompareWorkloads(ctx, db, resolver, input.Workloads, state)
	if workloads.Checked != 2 || workloads.Counts[StateMatchNotReady] != 1 || workloads.Counts[StateLegacyUnregistered] != 1 ||
		!reflect.DeepEqual(workloads.Items[0].Reasons, []string{"connector_disabled", "tool_disabled", "credential_missing"}) {
		t.Fatalf("workload compare: %+v", workloads)
	}

	// Activating the imported Loki connector makes it a full match; a
	// changed selector in the catalog is reported by field name only.
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=true,
 tools='[{"name":"search_logs","desc":"Read-only","enabled":true}]' WHERE provider='loki'`); err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	comparer := NewComparer(db, resolver, input.Telemetry, slog.New(slog.NewJSONHandler(&buffer, nil)))
	use := Use{ApplicationID: "app-lab-checkout", EnvironmentID: "prod", Provider: "loki", Resource: "lab-checkout"}
	if result := comparer.Compare(ctx, use); result.State != StateMatch {
		t.Fatalf("activated loki: %+v", result)
	}
	comparer.check(ctx, use)
	comparer.check(ctx, use) // deduplicated: no second check or log line
	if counts, _ := comparer.Counts(); counts[StateMatch] != 1 || strings.Count(buffer.String(), "source registry compare") != 1 {
		t.Fatalf("dedupe: %v %s", counts, buffer.String())
	}
	changed := catalog.Clone()
	for key, scope := range changed.Scopes {
		if scope.Provider == "loki" {
			scope.LokiLabels = map[string]string{"namespace": "lab", "app": "order"}
			changed.Scopes[key] = scope
		}
	}
	comparer.resolver = &Resolver{Tier: "production", Catalog: changed, Approvals: NewApprovals(nil)}
	comparer.check(ctx, Use{ApplicationID: "app-lab-checkout", EnvironmentID: "prod", Provider: "loki", Resource: "lab-checkout-other"})
	log := buffer.String()
	if !strings.Contains(log, `"state":"legacy_unregistered"`) || strings.Contains(log, "127.0.0.1") || strings.Contains(log, "namespace") {
		t.Fatalf("compare log: %s", log)
	}
	if result := comparer.Compare(ctx, use); !reflect.DeepEqual(result, Result{StateDiffers, []string{"scope.loki_labels"}}) {
		t.Fatalf("changed selector: %+v", result)
	}

	// A full queue drops reads instead of blocking the reader.
	for i := 0; i < compareQueue+10; i++ {
		comparer.ObserveLegacy(use)
	}
	if _, dropped := comparer.Counts(); dropped != 10 {
		t.Fatalf("dropped: %d", dropped)
	}
	var nilComparer *Comparer
	nilComparer.ObserveLegacy(use)

	// The importer refuses a database without migration 163.
	if _, err := db.Exec(ctx, `ALTER TABLE v3_connectors DROP COLUMN options`); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(ctx, db, input.Environments); !errors.Is(err, ErrSchema) {
		t.Fatalf("schema check: %v", err)
	}
}
