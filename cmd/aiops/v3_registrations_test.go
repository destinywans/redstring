package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3rollouts"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// PA-19 P3 stage 2 step 7: GET /integrations and GET /connector-registrations
// project the registry. A capability needs a ready connector and a target
// with a catalog scope; targets without a scope, of archived services or of
// another kind are not registrations.
func TestRegistrationsProjectTheRegistry(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := context.Background()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_registrations_" + store.ID()
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
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	if db, err = pgxpool.New(ctx, dsn+separator+"search_path="+url.QueryEscape(schema+",public")); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	tools := func(name string, on bool) string {
		raw, _ := json.Marshal([]map[string]any{{"name": name, "desc": "", "enabled": on}})
		return string(raw)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,key,name,archived) VALUES('app','checkout','Checkout',false),('app2','orders','Orders',false),('app3','old','Old',true)`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev'),('app2-dev','app2','dev','Dev'),('app3-dev','app3','dev','Dev')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev'),('app2','dev'),('app3','dev')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('prom','dev','prometheus','Prometheus','observe','http://127.0.0.1:19090','none','',true,'` + tools("query_metrics", true) + `'),
 ('loki-1','dev','loki','Loki','observe','http://127.0.0.1:13100','none','',false,'` + tools("search_logs", false) + `'),
 ('tempo-1','dev','tempo','Tempo','observe','http://127.0.0.1:13200','none','',true,'` + tools("query_traces", true) + `'),
 ('k8s','dev','kubernetes','Lab cluster','infra','https://127.0.0.1:6443','bearer','vault://kv/lab/k8s#token',true,'` + tools("read_workloads", true) + `'),
 ('gitlab-1','dev','gitlab','GitLab','delivery','https://gitlab.example.invalid','bearer','vault://kv/gitlab#token',true,'[]')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('t-metrics','app','dev','prom','metric_resource','checkout',$1),
 ('t-metrics-2','app2','dev','prom','metric_resource','orders',$1),
 ('t-metrics-3','app3','dev','prom','metric_resource','old',$1),
 ('t-logs','app','dev','loki-1','log_stream','checkout',$1),
 ('t-traces','app','dev','tempo-1','trace_service','checkout',$1),
 ('t-workload','app','dev','k8s','workload','checkout-api',$1),
 ('t-code','app','dev','gitlab-1','project','42',$1)`, adminID); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"queries":[
 {"connector_id":"prom","application_id":"app","environment_id":"dev","resource_id":"checkout","query_id":"request_rate","expression":"sum(rate(x[5m]))"},
 {"connector_id":"prom","application_id":"app3","environment_id":"dev","resource_id":"old","query_id":"request_rate","expression":"sum(rate(z[5m]))"}],
 "scopes":[
 {"connector_id":"loki-1","application_id":"app","environment_id":"dev","resource_id":"checkout","provider":"loki","loki_labels":{"app":"checkout"}},
 {"connector_id":"tempo-1","application_id":"app","environment_id":"dev","resource_id":"checkout","provider":"tempo","tempo_service":"checkout","tempo_attributes":{"deployment.environment":"dev"}}],
 "workloads":[{"connector_id":"k8s","application_id":"app","environment_id":"dev","resource_id":"checkout-api","namespace":"aiops-lab","deployment":"checkout-api"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resolver := &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals([]v3integrations.CredentialBinding{{
		Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "k8s", Provider: "kubernetes", Endpoint: "https://127.0.0.1:6443",
		CredentialRef: "vault://kv/lab/k8s#token"}})}
	projection := v3Registrations{DB: db, Registry: resolver, Environments: []string{"dev"},
		Workloads: &v3rollouts.Service{DB: db, Tier: "nonprod", Registry: resolver}}

	integrations, err := projection.Integrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range integrations {
		got[item.ID] = item.Provider + " " + item.Status + " " + strings.Join(item.Capabilities, ",") + " " + strings.Join(item.TargetApplicationIDs, ",")
	}
	want := map[string]string{
		"prom":    "prometheus configured_unverified metrics.read app",
		"loki-1":  "loki not_configured  app",
		"tempo-1": "tempo configured_unverified traces.read app",
		// Readable, but no running executor: no rollback capabilities.
		"k8s": "kubernetes configured_unverified deployment.read app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("integrations:\n%v\n%v", got, want)
	}

	registrations, err := projection.Registrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := []string{}
	for _, item := range registrations {
		row := item["id"].(string) + " " + item["kind"].(string) + " " + item["resource"].(string) + " " + item["environment_id"].(string)
		if item["kind"] == "kubernetes" {
			row += " " + item["namespace"].(string) + "/" + item["deployment"].(string)
		}
		if item["rollback_available"] != false {
			row += " rollback"
		}
		rows = append(rows, row)
	}
	if !reflect.DeepEqual(rows, []string{"k8s kubernetes checkout-api dev aiops-lab/checkout-api", "loki-1 loki checkout dev",
		"prom prometheus checkout dev", "tempo-1 tempo checkout dev"}) {
		t.Fatalf("registrations: %v", rows)
	}
}
