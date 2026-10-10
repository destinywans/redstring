package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type metricsVault struct{ empty atomic.Bool }

func (v *metricsVault) Resolve(_ context.Context, reference string) (string, error) {
	if v.empty.Load() || reference != "vault://kv/lab/prometheus#token" {
		return "", v3integrations.ErrCredentialUnavailable
	}
	return "prom-token", nil
}

// PA-19 P3 stage 2 step 5: with the ai_metrics reader switched,
// read_service_metrics reads the service's one metric_resource target with
// its connector's client and the catalog's query IDs, and list_services
// answers its metrics and logs columns from the registry. The evidence is
// the same as through the legacy binding; legacy bindings are no source.
func TestMetricsToolsReadTheRegistryWhenSwitched(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	var mu sync.Mutex
	var auth, methods []string
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth, methods = append(auth, r.Header.Get("Authorization")), append(methods, r.Method)
		mu.Unlock()
		if r.URL.Path != "/api/v1/query_range" || r.FormValue("query") != `sum(rate(http_errors_total{app="checkout"}[5m]))` {
			t.Errorf("prometheus request %s %s %q", r.Method, r.URL.Path, r.FormValue("query"))
		}
		start, _ := time.Parse(time.RFC3339Nano, r.FormValue("start"))
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix",
			"result": []any{map[string]any{"metric": map[string]string{"instance": "10.0.0.7:9090"},
				"values": []any{[]any{start.Unix(), "0.02"}, []any{start.Unix() + 30, "0.03"}}}}}})
	}))
	defer prometheus.Close()
	tools := `[{"name":"query_metrics","enabled":true},{"name":"search_logs","enabled":true}]`
	if _, err := db.Exec(ctx, `
		CREATE TABLE applications (id text PRIMARY KEY,key text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE v3_application_environments (application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE application_environments (application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false,bindings jsonb NOT NULL DEFAULT '[]');
		CREATE TABLE v3_connectors (id text PRIMARY KEY,environment_id text NOT NULL,provider text NOT NULL,endpoint text NOT NULL,
		 auth_method text NOT NULL,credential_ref text NOT NULL DEFAULT '',enabled boolean NOT NULL,tools jsonb NOT NULL,options jsonb NOT NULL DEFAULT '{}');
		CREATE TABLE v3_connector_targets (id text PRIMARY KEY,application_id text NOT NULL,environment_id text NOT NULL,connector_id text NOT NULL,
		 target_kind text NOT NULL,external_id text NOT NULL);
		INSERT INTO applications(id,key) VALUES('app-checkout','checkout'),('app-orders','orders');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-checkout','test'),('app-orders','test');
		INSERT INTO application_environments(application_id,key,bindings) VALUES
		 ('app-checkout','test','[{"kind":"prometheus","resource":"checkout"},{"kind":"loki","resource":"checkout"}]'),
		 ('app-orders','test','[{"kind":"prometheus","resource":"orders"}]');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,endpoint,auth_method,credential_ref,enabled,tools) VALUES
 ('prom-1','test','prometheus',$1,'bearer','vault://kv/lab/prometheus#token',true,$2),
 ('loki-1','test','loki','http://127.0.0.1:3100','none','',true,$2)`, prometheus.URL, tools); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets VALUES('t-metrics','app-checkout','test','prom-1','metric_resource','checkout'),
 ('t-logs','app-checkout','test','loki-1','log_stream','checkout')`); err != nil {
		t.Fatal(err)
	}
	const expression = `sum(rate(http_errors_total{app="checkout"}[5m]))`
	raw, _ := json.Marshal(map[string]any{
		"queries": []any{
			map[string]any{"connector_id": "prom-1", "application_id": "app-checkout", "environment_id": "test", "resource_id": "checkout",
				"query_id": "error_rate", "expression": expression},
			map[string]any{"connector_id": "prom-1", "application_id": "app-checkout", "environment_id": "test", "resource_id": "checkout",
				"query_id": "request_rate", "expression": `sum(rate(http_requests_total{app="checkout"}[5m]))`},
			// The metrics page's composite query is not offered to the model.
			map[string]any{"connector_id": "prom-1", "application_id": "app-checkout", "environment_id": "test", "resource_id": "checkout",
				"query_id": "dashboard", "expression": `label_replace(vector(1),"panel","p","","")`},
		},
		"scopes": []any{map[string]any{"connector_id": "loki-1", "application_id": "app-checkout", "environment_id": "test",
			"resource_id": "checkout", "provider": "loki", "loki_labels": map[string]string{"app": "checkout"}}},
	})
	catalog, err := v3telemetry.ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := connectors.NewTelemetry(connectors.TelemetryConfig{
		Prometheus: connectors.TelemetryEndpoint{URL: prometheus.URL, AllowHTTP: true},
		Loki:       connectors.TelemetryEndpoint{URL: "http://127.0.0.1:3100", AllowHTTP: true},
		Applications: map[string]connectors.ApplicationTelemetry{
			"checkout": {PrometheusQueries: map[string]string{"error_rate": expression, "request_rate": `sum(rate(http_requests_total{app="checkout"}[5m]))`},
				LokiLabels: map[string]string{"app": "checkout"}},
			"orders": {PrometheusQueries: map[string]string{"request_rate": `sum(rate(http_requests_total{app="orders"}[5m]))`}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals([]v3integrations.CredentialBinding{{
		Tier: "nonprod", EnvironmentID: "test", ConnectorID: "prom-1", Provider: "prometheus", Endpoint: prometheus.URL,
		CredentialRef: "vault://kv/lab/prometheus#token"}})}
	vault := &metricsVault{}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	clients := &v3telemetry.Clients{Tier: "nonprod", Credentials: vault, Limits: legacy.Registration(),
		Now: func() time.Time { return time.Unix(0, clock.Load()) }}
	owner := func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil }
	lookup := func(_ context.Context, _ core.User, env, key string) (v3catalog.ServiceEvidence, error) {
		app := map[string]string{"checkout": "app-checkout", "orders": "app-orders"}[key]
		if app == "" {
			return v3catalog.ServiceEvidence{}, store.ErrNotFound
		}
		return v3catalog.ServiceEvidence{ID: app, Key: key, Name: key, Environment: env}, nil
	}
	listed := func(_ context.Context, _ core.User, env string, _ int) ([]v3catalog.ServiceEvidence, bool, error) {
		return []v3catalog.ServiceEvidence{{ID: "app-checkout", Key: "checkout", Name: "checkout", Environment: env},
			{ID: "app-orders", Key: "orders", Name: "orders", Environment: env}}, false, nil
	}
	toolsFor := func(unified bool) (inference.EvidenceTool, inference.EvidenceTool) {
		t.Helper()
		metricsOptions := ServiceMetricsToolOptions{DB: db, OwnerID: "owner", EnvironmentID: "test", CurrentUser: owner,
			LookupService: lookup, Unified: unified, Registry: registry, Clients: clients}
		listOptions := ServiceListToolOptions{DB: db, OwnerID: "owner", EnvironmentID: "test", CurrentUser: owner,
			ListServices: listed, Telemetry: legacy, Unified: unified, Registry: registry}
		if !unified {
			metricsOptions.Telemetry = legacy
		}
		metrics, ok := NewServiceMetricsTool(metricsOptions)
		list, listOK := NewServiceListTool(listOptions)
		if !ok || !listOK {
			t.Fatalf("tools not registered (unified %v)", unified)
		}
		return metrics, list
	}
	type sample struct {
		Status string `json:"status"`
		Series []struct {
			Samples []struct{ Value string } `json:"samples"`
		} `json:"series"`
	}
	readMetrics := func(tool inference.EvidenceTool, service, query string) (inference.Evidence, sample, error) {
		raw, _ := json.Marshal(map[string]string{"service": service, "query_id": query})
		evidence, err := tool.Read(ctx, string(raw))
		var body sample
		if err == nil {
			_ = json.Unmarshal([]byte(evidence.Content), &body)
		}
		return evidence, body, err
	}
	readList := func(tool inference.EvidenceTool) []serviceListItem {
		t.Helper()
		evidence, err := tool.Read(ctx, "{}")
		var body serviceListEvidence
		if err != nil || json.Unmarshal([]byte(evidence.Content), &body) != nil {
			t.Fatalf("list: %v", err)
		}
		return body.Services
	}

	// Baseline through the legacy bindings and file registration.
	legacyMetrics, legacyList := toolsFor(false)
	beforeEvidence, before, err := readMetrics(legacyMetrics, "checkout", "error_rate")
	if err != nil || before.Status != "observed" {
		t.Fatalf("legacy read: %+v %v", before, err)
	}
	beforeList := readList(legacyList)

	// Switched: the same evidence and list, read with the Vault credential.
	metrics, list := toolsFor(true)
	afterEvidence, after, err := readMetrics(metrics, "checkout", "error_rate")
	if err != nil || !reflect.DeepEqual(after, before) || afterEvidence.Title != beforeEvidence.Title ||
		afterEvidence.Source != beforeEvidence.Source || afterEvidence.TargetID != beforeEvidence.TargetID ||
		strings.Contains(afterEvidence.Content, "10.0.0.7") || strings.Contains(afterEvidence.Content, "http_errors_total") {
		t.Fatalf("switched read:\n%+v %+v\n%+v %+v %v", beforeEvidence, before, afterEvidence, after, err)
	}
	mu.Lock()
	if auth[len(auth)-1] != "Bearer prom-token" || methods[len(methods)-1] != http.MethodPost {
		t.Fatalf("registered read: %v %v", auth, methods)
	}
	mu.Unlock()
	afterList := readList(list)
	if afterList[0].Metrics != "configured" || !reflect.DeepEqual(afterList[0].MetricQueries, beforeList[0].MetricQueries) ||
		afterList[0].Logs != beforeList[0].Logs || !reflect.DeepEqual(afterList[0].MetricQueries, []string{"error_rate", "request_rate"}) {
		t.Fatalf("list differs:\n%+v\n%+v", beforeList, afterList)
	}
	// orders has only a legacy binding: no source once switched.
	if beforeList[1].Metrics != "configured" || afterList[1].Metrics != "not_configured" || len(afterList[1].MetricQueries) != 0 {
		t.Fatalf("legacy-only service: %+v / %+v", beforeList[1], afterList[1])
	}
	expect := func(service, query, code, message string) {
		t.Helper()
		_, _, err := readMetrics(metrics, service, query)
		if inference.ToolErrorCode(err) != code || !strings.Contains(err.Error(), message) {
			t.Fatalf("%s/%s: want %s %q, got %v", service, query, code, message, err)
		}
	}
	expect("orders", "request_rate", inference.ToolNotConfigured, "no metrics source is registered")
	expect("checkout", "pod_restarts", inference.ToolNotConfigured, "configured: error_rate, request_rate")
	expect("checkout", "dashboard", inference.ToolNotConfigured, "configured: error_rate, request_rate")

	// Not ready and ambiguous are said by both tools.
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=false WHERE id='prom-1'`); err != nil {
		t.Fatal(err)
	}
	expect("checkout", "error_rate", inference.ToolNotConfigured, "not ready (connector_disabled)")
	if items := readList(list); items[0].Metrics != "not_ready" || len(items[0].MetricQueries) != 0 || items[0].Logs != "configured" {
		t.Fatalf("not ready in the list: %+v", items[0])
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET enabled=true WHERE id='prom-1';
 INSERT INTO v3_connector_targets VALUES('t-metrics-2','app-checkout','test','prom-1','metric_resource','checkout-2')`); err != nil {
		t.Fatal(err)
	}
	expect("checkout", "error_rate", inference.ToolNotConfigured, "more than one registered metrics source")
	if items := readList(list); items[0].Metrics != "ambiguous" {
		t.Fatalf("ambiguous in the list: %+v", items[0])
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='t-metrics-2'`); err != nil {
		t.Fatal(err)
	}

	// An empty Vault is a retryable outage once the cached token expires.
	vault.empty.Store(true)
	clock.Add(int64(61 * time.Second))
	expect("checkout", "error_rate", inference.ToolSourceUnavailable, "Prometheus query failed")
	vault.empty.Store(false)
	if _, _, err := readMetrics(metrics, "checkout", "error_rate"); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewServiceMetricsTool(ServiceMetricsToolOptions{DB: db, OwnerID: "owner", EnvironmentID: "test", CurrentUser: owner,
		LookupService: lookup, Unified: true}); ok {
		t.Fatal("switched tool registered without a registry")
	}
}
