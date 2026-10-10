package v3telemetry

import (
	"context"
	"reflect"
	"testing"
)

// Stage 2 step 8: the startup summary counts targets by kind and connectors
// by provider, lists the connectors the readers cannot read yet, and names
// the switched readers without any target.
func TestRegistrySummary(t *testing.T) {
	db := isolatedRegistryDB(t)
	ctx := context.Background()
	var admin string
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE email='admin@example.invalid'`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,key,name) VALUES('app','checkout','Checkout')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-prod','app','prod','Prod')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','prod')`,
		`INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('prom','prod','prometheus','Prometheus','observe','http://127.0.0.1:19090','none','',true,'[{"name":"query_metrics","enabled":true}]'),
 ('loki','prod','loki','Loki','observe','http://127.0.0.1:13100','none','',false,'[{"name":"search_logs","enabled":false}]'),
 ('gitlab','prod','gitlab','GitLab','delivery','https://gitlab.example.invalid','bearer','vault://kv/gitlab#token',true,'[]')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by) VALUES
 ('t1','app','prod','prom','metric_resource','checkout',$1),('t2','app','prod','loki','log_stream','checkout',$1),
 ('t3','app','prod','gitlab','project','42',$1)`, admin); err != nil {
		t.Fatal(err)
	}
	catalog, err := ParseCatalog([]byte(`{"queries":[{"connector_id":"prom","application_id":"app","environment_id":"prod",
 "resource_id":"checkout","query_id":"request_rate","expression":"sum(rate(x[5m]))"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{Tier: "production", Catalog: catalog, Approvals: NewApprovals(nil)}
	summary, err := resolver.Summarize(ctx, db, []string{"prod"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summary.Targets, map[string]int{"metric_resource": 1, "log_stream": 1, "project": 1}) ||
		!reflect.DeepEqual(summary.Connectors, map[string]int{"prometheus": 1, "loki": 1, "gitlab": 1}) || summary.CatalogQueries != 1 ||
		len(summary.NotReady) != 1 || summary.NotReady[0].ID != "loki" ||
		!reflect.DeepEqual(summary.NotReady[0].Reasons, []string{"connector_disabled", "tool_disabled"}) {
		t.Fatalf("summary: %+v", summary)
	}
	if empty := summary.EmptyReaders(); !reflect.DeepEqual(empty, []string{"traces"}) {
		t.Fatalf("readers without targets: %v", empty)
	}
}
