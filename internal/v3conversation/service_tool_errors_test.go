package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3catalog"
	"github.com/destinywans/redstring/internal/v3telemetry"
)

// catalogTelemetry stands in for a registered connector's client.
type catalogTelemetry struct {
	result connectors.MetricEvidence
	err    error
	calls  int
}

func (c *catalogTelemetry) QueryMetrics(_ context.Context, resource, queryID string, _ connectors.TimeWindow) (connectors.MetricEvidence, error) {
	c.calls++
	result := c.result
	result.Source.ApplicationID, result.Source.QueryID = resource, queryID
	return result, c.err
}
func (c *catalogTelemetry) QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error) {
	return connectors.LogEvidence{}, errors.New("not used")
}

func toolCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a tool error")
	}
	return inference.ToolErrorCode(err)
}

// PA-50/51: every tool failure tells the model what happened (unknown key,
// missing source or query, source down) without revealing hidden services,
// and well-known infrastructure queries keep their reviewed labels.
func TestServiceToolsExplainFailuresAndListReadableServices(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `
		CREATE TABLE applications (id text PRIMARY KEY,key text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE v3_application_environments (application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE application_environments (application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false,bindings jsonb NOT NULL DEFAULT '[]');
		CREATE TABLE v3_connectors (id text PRIMARY KEY,environment_id text NOT NULL,provider text NOT NULL,endpoint text NOT NULL,
		 auth_method text NOT NULL,credential_ref text NOT NULL DEFAULT '',enabled boolean NOT NULL,tools jsonb NOT NULL,options jsonb NOT NULL DEFAULT '{}');
		CREATE TABLE v3_connector_targets (id text PRIMARY KEY,application_id text NOT NULL,environment_id text NOT NULL,connector_id text NOT NULL,
		 target_kind text NOT NULL,external_id text NOT NULL);
		INSERT INTO applications(id,key) VALUES('app-checkout','checkout'),('app-payments','payments'),('app-dual','dual');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-checkout','test'),('app-payments','test'),('app-dual','test');
		INSERT INTO application_environments(application_id,key) VALUES('app-checkout','test'),('app-payments','test'),('app-dual','test');
		INSERT INTO v3_connectors(id,environment_id,provider,endpoint,auth_method,enabled,tools) VALUES
		 ('prom-1','test','prometheus','http://127.0.0.1:9090','none',true,'[{"name":"query_metrics","enabled":true}]'),
		 ('loki-1','test','loki','http://127.0.0.1:3100','none',true,'[{"name":"search_logs","enabled":true}]');
		INSERT INTO v3_connector_targets VALUES
		 ('t-metrics','app-checkout','test','prom-1','metric_resource','registered-checkout'),
		 ('t-logs','app-checkout','test','loki-1','log_stream','checkout-logs'),
		 ('t-dual-a','app-dual','test','prom-1','metric_resource','a'),
		 ('t-dual-b','app-dual','test','prom-1','metric_resource','b');`); err != nil {
		t.Fatal(err)
	}
	query := func(id string) map[string]any {
		return map[string]any{"connector_id": "prom-1", "application_id": "app-checkout", "environment_id": "test",
			"resource_id": "registered-checkout", "query_id": id, "expression": "sum(" + id + ")"}
	}
	raw, _ := json.Marshal(map[string]any{"queries": []any{query("error_rate"), query("node_cpu_busy_ratio"), query("request_rate")},
		"scopes": []any{map[string]any{"connector_id": "loki-1", "application_id": "app-checkout", "environment_id": "test",
			"resource_id": "checkout-logs", "provider": "loki", "loki_labels": map[string]string{"app": "checkout"}}}})
	catalog, err := v3telemetry.ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	registry := &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)}
	clients := &v3telemetry.Clients{Tier: "nonprod"}
	telemetry := &catalogTelemetry{
		result: connectors.MetricEvidence{Source: connectors.TelemetrySource{Provider: "prometheus", CapturedAt: time.Now().UTC()},
			Series: []connectors.MetricSeries{
				{Labels: map[string]string{"node": "lab-vm-1", "instance": "10.0.0.7:9100", "job": "aiops-vm-nodes"}, Samples: []connectors.MetricSample{{Timestamp: 1791565135, Value: "0.93"}, {Timestamp: 1791565165.5, Value: "0.95"}}},
				{Labels: map[string]string{"node": "bad value with spaces"}, Samples: []connectors.MetricSample{{Timestamp: 100, Value: "0.12"}}},
			}}}
	visible := map[string]string{"checkout": "app-checkout", "payments": "app-payments", "dual": "app-dual"}
	lookup := func(_ context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
		if key == "catalog-down" {
			return v3catalog.ServiceEvidence{}, errors.New("database unavailable")
		}
		app, ok := visible[key]
		if !ok || user.ID != "owner" || env != "test" {
			return v3catalog.ServiceEvidence{}, store.ErrNotFound
		}
		return v3catalog.ServiceEvidence{ID: app, Key: key, Name: strings.ToUpper(key), Environment: env}, nil
	}
	owner := func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil }
	metrics, ok := NewServiceMetricsTool(ServiceMetricsToolOptions{DB: db, OwnerID: "owner", EnvironmentID: "test",
		CurrentUser: owner, LookupService: lookup, Registry: registry, Clients: clients,
		clientFor: func(_ string, source v3telemetry.Source) (metricsReader, string, error) {
			return telemetry, source.ResourceID, nil
		}})
	if !ok || !strings.Contains(metrics.Description, "node_cpu_busy_ratio") || !strings.Contains(metrics.Description, "list_services") {
		t.Fatalf("metrics tool: %v %q", ok, metrics.Description)
	}
	read := func(service, query string) (inference.Evidence, error) {
		raw, _ := json.Marshal(map[string]string{"service": service, "query_id": query})
		return metrics.Read(ctx, string(raw))
	}
	// A hidden and an absent key get the same answer.
	_, hidden := read("hidden-but-real", "request_rate")
	_, absent := read("no-such-service", "request_rate")
	if toolCode(t, hidden) != inference.ToolUnknownService || hidden.Error() != absent.Error() || !strings.Contains(hidden.Error(), "list_services") {
		t.Fatalf("unknown service: %v / %v", hidden, absent)
	}
	for name, tc := range map[string]struct {
		service, query, code, contains string
	}{
		"catalog down":      {"catalog-down", "request_rate", inference.ToolSourceUnavailable, "service catalog"},
		"no metrics source": {"payments", "request_rate", inference.ToolNotConfigured, "no metrics source"},
		"two sources":       {"dual", "request_rate", inference.ToolNotConfigured, "more than one"},
		"query not defined": {"checkout", "pod_restarts", inference.ToolNotConfigured, "configured: error_rate, node_cpu_busy_ratio, request_rate"},
		"not an ID":         {"checkout", "rate(x[5m])", inference.ToolInvalidInput, "expected JSON"},
	} {
		if _, err := read(tc.service, tc.query); toolCode(t, err) != tc.code || !strings.Contains(err.Error(), tc.contains) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if telemetry.calls != 0 {
		t.Fatal("a failed precheck reached Prometheus")
	}
	evidence, err := read("checkout", "node_cpu_busy_ratio")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Meaning string `json:"meaning"`
		Series  []struct {
			Labels map[string]string `json:"labels"`
		} `json:"series"`
	}
	if json.Unmarshal([]byte(evidence.Content), &body) != nil || body.Meaning == "" || len(body.Series) != 2 ||
		len(body.Series[0].Labels) != 1 || body.Series[0].Labels["node"] != "lab-vm-1" || body.Series[1].Labels["node"] != "unrecognized" ||
		strings.Contains(evidence.Content, "10.0.0.7") || strings.Contains(evidence.Content, "aiops-vm-nodes") {
		t.Fatalf("node labels: %s", evidence.Content)
	}
	// PA-53: sample times are RFC 3339 UTC like the log samples, never epoch.
	if !strings.Contains(evidence.Content, `{"time":"2026-10-09T16:58:55Z","value":"0.93"}`) ||
		!strings.Contains(evidence.Content, `"time":"2026-10-09T16:59:25.5Z"`) || strings.Contains(evidence.Content, "1791565135") ||
		strings.Contains(evidence.Content, `"timestamp"`) {
		t.Fatalf("sample times: %s", evidence.Content)
	}
	// Service-level queries keep no labels.
	if evidence, err = read("checkout", "request_rate"); err != nil || strings.Contains(evidence.Content, "lab-vm-1") {
		t.Fatalf("request_rate kept labels: %s %v", evidence.Content, err)
	}
	telemetry.err = &connectors.TelemetryError{Kind: "scope", Message: "metric query ID is not registered"}
	if _, err := read("checkout", "error_rate"); toolCode(t, err) != inference.ToolNotConfigured {
		t.Fatalf("scope error: %v", err)
	}
	telemetry.err = &connectors.TelemetryError{Kind: "http", Message: "502"}
	if _, err := read("checkout", "error_rate"); toolCode(t, err) != inference.ToolSourceUnavailable {
		t.Fatalf("provider failure: %v", err)
	}
	telemetry.err = nil

	// Logs: unknown key, no log source, no log permission, each distinct.
	logsAllowed := false
	logs, ok := NewServiceLogsTool(ServiceLogsToolOptions{Enabled: true, DB: db, OwnerID: "owner", EnvironmentID: "test",
		CurrentUser: owner, LookupService: lookup, Registry: registry, Clients: clients,
		clientFor: func(_ string, source v3telemetry.Source) (logToolReader, string, error) {
			return telemetry, source.ResourceID, nil
		},
		Access: logToolAccess(func(rbac.V3Decision) error {
			if logsAllowed {
				return nil
			}
			return store.ErrForbidden
		})})
	if !ok {
		t.Fatal("logs tool")
	}
	readLogs := func(service string) error {
		_, err := logs.Read(ctx, `{"service":"`+service+`","query_id":"recent"}`)
		return err
	}
	if toolCode(t, readLogs("no-such-service")) != inference.ToolUnknownService ||
		toolCode(t, readLogs("payments")) != inference.ToolNotConfigured ||
		toolCode(t, readLogs("checkout")) != inference.ToolNotPermitted {
		t.Fatal("log errors are not distinct")
	}
	if _, err := logs.Read(ctx, "checkout"); toolCode(t, err) != inference.ToolInvalidInput {
		t.Fatalf("plain text input: %v", err)
	}

	// list_services: only what the catalog lists for the owner, with sources.
	list, ok := NewServiceListTool(ServiceListToolOptions{DB: db, OwnerID: "owner", EnvironmentID: "test", CurrentUser: owner,
		Registry: registry,
		ListServices: func(_ context.Context, user core.User, env string, limit int) ([]v3catalog.ServiceEvidence, bool, error) {
			if user.ID != "owner" || env != "test" || limit != maxListedServices {
				t.Fatalf("list scope: %v %s %d", user, env, limit)
			}
			return []v3catalog.ServiceEvidence{{Key: "checkout", Name: "Checkout"}, {Key: "dual", Name: "Dual"}, {Key: "payments", Name: "Payments"}}, false, nil
		}})
	if !ok {
		t.Fatal("list tool")
	}
	listed, err := list.Read(ctx, "{}")
	if err != nil {
		t.Fatal(err)
	}
	var services struct {
		Services []serviceListItem `json:"services"`
	}
	if json.Unmarshal([]byte(listed.Content), &services) != nil || len(services.Services) != 3 {
		t.Fatalf("list: %s", listed.Content)
	}
	checkout, dual, payments := services.Services[0], services.Services[1], services.Services[2]
	if checkout.Metrics != "configured" || strings.Join(checkout.MetricQueries, ",") != "error_rate,node_cpu_busy_ratio,request_rate" ||
		checkout.Logs != "configured" || dual.Metrics != "ambiguous" || payments.Metrics != "not_configured" || payments.Logs != "not_configured" ||
		len(payments.MetricQueries) != 0 {
		t.Fatalf("listed sources: %+v", services.Services)
	}
	if _, err := list.Read(ctx, "checkout"); toolCode(t, err) != inference.ToolInvalidInput {
		t.Fatalf("list input: %v", err)
	}
}

// PA-53: the metric snapshot of /metrics/ask reaches the model with RFC 3339
// sample times; unparsable input is passed through unchanged.
func TestModelMetricSnapshotUsesRFC3339(t *testing.T) {
	got := modelMetricSnapshot(`{"view":"request_rate","samples":[{"timestamp":1791565135,"value":"5"}],"window":{"start":"2026-10-09T16:45:00Z"}}`)
	if !strings.Contains(got, `"samples":[{"time":"2026-10-09T16:58:55Z","value":"5"}]`) || strings.Contains(got, "1791565135") ||
		!strings.Contains(got, `"view":"request_rate"`) || !strings.Contains(got, "2026-10-09T16:45:00Z") {
		t.Fatalf("snapshot: %s", got)
	}
	if raw := `not json`; modelMetricSnapshot(raw) != raw {
		t.Fatal("unparsable snapshot changed")
	}
}
