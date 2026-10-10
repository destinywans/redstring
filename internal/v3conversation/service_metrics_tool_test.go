package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type metricToolTelemetry struct {
	calls    int
	resource string
	queryID  string
	window   connectors.TimeWindow
	result   connectors.MetricEvidence
	err      error
}

func (m *metricToolTelemetry) QueryMetrics(_ context.Context, resource, queryID string, window connectors.TimeWindow) (connectors.MetricEvidence, error) {
	m.calls++
	m.resource, m.queryID, m.window = resource, queryID, window
	return m.result, m.err
}

// The tool reads the service's registered Prometheus target (here through a
// stub client), fails closed and keeps the provider configuration out of the
// evidence.
func TestServiceMetricsToolFailsClosedAndRedactsProviderConfiguration(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		CREATE TABLE applications (id text PRIMARY KEY,key text NOT NULL,owner_team_id text,archived boolean NOT NULL DEFAULT false);
		CREATE TABLE v3_application_environments (application_id text NOT NULL,environment_id text NOT NULL);
		CREATE TABLE application_environments (application_id text NOT NULL,key text NOT NULL,archived boolean NOT NULL DEFAULT false,bindings jsonb NOT NULL DEFAULT '[]');
		CREATE TABLE v3_connectors (id text PRIMARY KEY,environment_id text NOT NULL,provider text NOT NULL,endpoint text NOT NULL,
		 auth_method text NOT NULL,credential_ref text NOT NULL DEFAULT '',enabled boolean NOT NULL,tools jsonb NOT NULL,options jsonb NOT NULL DEFAULT '{}');
		CREATE TABLE v3_connector_targets (id text PRIMARY KEY,application_id text NOT NULL,environment_id text NOT NULL,connector_id text NOT NULL,
		 target_kind text NOT NULL,external_id text NOT NULL);
		INSERT INTO applications(id,key) VALUES('app-1','checkout');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-1','test');
		INSERT INTO application_environments(application_id,key) VALUES('app-1','test');
		INSERT INTO v3_connectors(id,environment_id,provider,endpoint,auth_method,enabled,tools)
		 VALUES('prom-1','test','prometheus','http://127.0.0.1:9090','none',true,'[{"name":"query_metrics","enabled":true}]');`)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"queries":[{"connector_id":"prom-1","application_id":"app-1","environment_id":"test",
 "resource_id":"registered-checkout","query_id":"request_rate","expression":"sum(rate(http_requests_total[5m]))"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	memberActive := true
	serviceVisible := true
	identityCalls, catalogCalls := 0, 0
	provider := &metricToolTelemetry{result: connectors.MetricEvidence{
		Source: connectors.TelemetrySource{Provider: "prometheus", ApplicationID: "registered-checkout", QueryID: "request_rate", Query: "secret-promql", URL: "https://secret.metrics.internal", CapturedAt: time.Now().UTC()},
		Series: []connectors.MetricSeries{{Labels: map[string]string{"job": "checkout", "sensitive": "secret-label"}, Samples: []connectors.MetricSample{{Timestamp: 100, Value: "2.5"}}}},
	}}
	tool, ok := NewServiceMetricsTool(ServiceMetricsToolOptions{
		DB: db, OwnerID: "owner", EnvironmentID: "test",
		Registry: &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)},
		Clients:  &v3telemetry.Clients{Tier: "nonprod"},
		clientFor: func(_ string, source v3telemetry.Source) (metricsReader, string, error) {
			return provider, source.ResourceID, nil
		},
		CurrentUser: func(_ context.Context, owner string) (core.User, error) {
			identityCalls++
			if owner != "owner" || !memberActive {
				return core.User{}, errors.New("disabled")
			}
			return core.User{ID: owner}, nil
		},
		LookupService: func(_ context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			catalogCalls++
			if !serviceVisible || user.ID != "owner" || env != "test" || key != "checkout" {
				return v3catalog.ServiceEvidence{}, errors.New("hidden")
			}
			return v3catalog.ServiceEvidence{ID: "app-1", Key: key, Environment: env}, nil
		},
	})
	if !ok || tool.Name != "read_service_metrics" {
		t.Fatalf("tool not registered: %#v", tool)
	}
	read := func(input string) error {
		_, err := tool.Read(ctx, input)
		return err
	}
	for _, input := range []string{
		`{"service":"checkout","query_id":"request_rate","url":"http://evil"}`,
		`{"service":"checkout","query_id":"up"}`,
		`{"service":"checkout","query_id":"rate(http_requests_total[5m])"}`,
		`{"service":"other","query_id":"request_rate"}`,
		`{"service":"checkout","query_id":"request_rate"} {}`,
	} {
		if err := read(input); err == nil {
			t.Fatalf("invalid or hidden target reached provider: %s", input)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("provider called before authorized binding: %d", provider.calls)
	}
	input := `{"service":"checkout","query_id":"request_rate"}`
	memberActive = false
	if err := read(input); err == nil {
		t.Fatal("revoked account reached provider")
	}
	memberActive = true
	serviceVisible = false
	if err := read(input); err == nil {
		t.Fatal("hidden service reached provider")
	}
	serviceVisible = true
	if err := read(input); err == nil {
		t.Fatal("missing registered target reached provider")
	}
	// A legacy binding is not a source.
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"prometheus","resource":"registered-checkout"}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	if err := read(input); err == nil {
		t.Fatal("legacy binding reached provider")
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets VALUES('t-metrics','app-1','test','prom-1','metric_resource','registered-checkout')`); err != nil {
		t.Fatal(err)
	}
	evidence, err := tool.Read(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || provider.resource != "registered-checkout" || provider.queryID != "request_rate" || provider.window.End.Sub(provider.window.Start) != 15*time.Minute || provider.window.StepSeconds != 30 {
		t.Fatalf("unscoped provider call: %+v", provider)
	}
	if evidence.TargetID != "app-1" || evidence.Source != "prometheus" || !strings.Contains(evidence.Content, `"status":"observed"`) || strings.Contains(evidence.Content, "secret-") {
		t.Fatalf("unsafe or missing metric evidence: %#v", evidence)
	}
	var body metricToolEvidence
	if err := json.Unmarshal([]byte(evidence.Content), &body); err != nil || len(body.Series) != 1 || body.Series[0].Samples[0].Value != "2.5" {
		t.Fatalf("metric sample missing: %#v %v", body, err)
	}
	provider.result.Source.Warnings = []string{"secret-provider-warning"}
	partial, err := tool.Read(ctx, input)
	if err != nil || !strings.Contains(partial.Content, `"status":"partial"`) || !strings.Contains(partial.Content, `"incomplete":true`) || strings.Contains(partial.Content, "secret-") {
		t.Fatalf("partial provider result was hidden or leaked details: %#v %v", partial, err)
	}
	provider.result.Source.Warnings = nil
	if identityCalls < 4 || catalogCalls < 3 {
		t.Fatalf("identity/catalog not rechecked on each valid read: %d/%d", identityCalls, catalogCalls)
	}
	provider.result.Empty = true
	provider.result.Series = nil
	empty, err := tool.Read(ctx, input)
	if err != nil || !strings.Contains(empty.Content, `"status":"no_samples"`) {
		t.Fatalf("empty query was presented as evidence of health: %#v %v", empty, err)
	}
	provider.result.Source.Warnings = []string{"partial upstream response"}
	partialEmpty, err := tool.Read(ctx, input)
	if err != nil || !strings.Contains(partialEmpty.Content, `"status":"partial"`) {
		t.Fatalf("warning with no samples was reported as complete: %#v %v", partialEmpty, err)
	}
	provider.result.Source.Warnings = nil
	provider.result.LimitReached = true
	limitedEmpty, err := tool.Read(ctx, input)
	if err != nil || !strings.Contains(limitedEmpty.Content, `"status":"partial"`) {
		t.Fatalf("limited result with no samples was reported as complete: %#v %v", limitedEmpty, err)
	}
	provider.result.LimitReached = false
	provider.result.Series = []connectors.MetricSeries{{Samples: []connectors.MetricSample{{Timestamp: 100, Value: "2.5"}}}}
	provider.result.Empty = false
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets VALUES('t-metrics-2','app-1','test','prom-1','metric_resource','other')`); err != nil {
		t.Fatal(err)
	}
	if err := read(input); err == nil || provider.calls != 5 {
		t.Fatalf("ambiguous binding queried provider: calls=%d err=%v", provider.calls, err)
	}
}

func TestServiceMetricsToolRejectsMissingDependencies(t *testing.T) {
	if _, ok := NewServiceMetricsTool(ServiceMetricsToolOptions{OwnerID: "owner", EnvironmentID: "all"}); ok {
		t.Fatal("all-environments scope exposed tool")
	}
	if _, ok := NewServiceMetricsTool(ServiceMetricsToolOptions{
		DB: isolatedConversationDB(t), OwnerID: "owner", EnvironmentID: "test",
		CurrentUser: func(context.Context, string) (core.User, error) { return core.User{}, nil },
		LookupService: func(context.Context, core.User, string, string) (v3catalog.ServiceEvidence, error) {
			return v3catalog.ServiceEvidence{}, nil
		},
	}); ok {
		t.Fatal("a tool without a registry was registered")
	}
}
