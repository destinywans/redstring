package v3telemetry

import (
	"strings"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// The fixture mirrors the production acceptance lab (2026-10-09): Prometheus,
// Loki and Tempo on loopback; the integration center already registered
// Prometheus connector c9ea436d7e4f3b8be53212ce3e197292 with metric_resource
// targets lab-checkout-api and lab-order-api and a reviewed "dashboard"
// query each. Bindings still point at the telemetry file's resources.
const labPrometheus = "c9ea436d7e4f3b8be53212ce3e197292"

func labTelemetry() connectors.TelemetryConfig {
	return connectors.TelemetryConfig{
		Prometheus:    connectors.TelemetryEndpoint{URL: "http://127.0.0.1:19090", AllowHTTP: true},
		Loki:          connectors.TelemetryEndpoint{URL: "http://127.0.0.1:13100/", AllowHTTP: true},
		Tempo:         connectors.TelemetryEndpoint{URL: "http://127.0.0.1:13200", AllowHTTP: true},
		Elasticsearch: connectors.TelemetryEndpoint{URL: "https://es.example.invalid", Username: "reader", PasswordFile: "/run/secrets/es-password"},
		Applications: map[string]connectors.ApplicationTelemetry{
			"lab-checkout": {
				PrometheusQueries: map[string]string{
					"request_rate": `sum(rate(lab_http_requests_total{application="checkout"}[5m]))`,
					"error_ratio":  `sum(rate(lab_http_requests_total{application="checkout",status=~"5.."}[5m])) / sum(rate(lab_http_requests_total{application="checkout"}[5m]))`,
				},
				LokiLabels:      map[string]string{"namespace": "lab", "app": "checkout"},
				TempoService:    "checkout",
				TempoAttributes: map[string]string{"deployment.environment": "lab"},
			},
			"lab-order": {
				PrometheusQueries:    map[string]string{"request_rate": `sum(rate(lab_http_requests_total{application="order"}[5m]))`},
				LokiLabels:           map[string]string{"namespace": "lab", "app": "order"},
				ElasticsearchIndex:   "lab-order-logs",
				ElasticsearchFilters: map[string]string{"service.name": "order"},
				TempoService:         "order",
				TempoAttributes:      map[string]string{"deployment.environment": "lab"},
			},
			"lab-payments": {
				// Longer than the catalog's 2048-character expression limit.
				PrometheusQueries: map[string]string{"dashboard": "sum(" + strings.Repeat(`rate(lab_http_requests_total{application="payments"}[5m]) + `, 40) + "vector(0))"},
			},
			"lab-search": {
				ElasticsearchIndex:   "lab-search-logs",
				ElasticsearchFilters: map[string]string{"service.name": "search"},
			},
		},
	}
}

func labInput() ImportInput {
	catalog := Catalog{Queries: map[v3integrations.PrometheusQueryKey]string{}, Scopes: map[ScopeKey]Scope{}}
	for app, resource := range map[string]string{"app-lab-checkout": "lab-checkout-api", "app-lab-order": "lab-order-api"} {
		catalog.Queries[v3integrations.PrometheusQueryKey{ConnectorID: labPrometheus, ApplicationID: app, EnvironmentID: "prod",
			ResourceID: resource, QueryID: "dashboard"}] = `label_replace(sum(rate(lab_http_requests_total{application="` + app + `"}[100s])),"panel","requests_total","","")`
	}
	return ImportInput{Tier: "production", Environments: []string{"prod"}, Telemetry: labTelemetry(), Catalog: catalog,
		Approvals: NewApprovals(nil)}
}

func labState() State {
	binding := func(app, kind, resource string) LegacyBinding {
		connector := kind
		if kind == "kubernetes" {
			connector = "lab-kubernetes"
		}
		return LegacyBinding{ApplicationID: app, EnvironmentID: "prod", Kind: kind, ConnectorID: connector, Resource: resource}
	}
	return State{
		Bindings: []LegacyBinding{
			binding("app-lab-checkout", "prometheus", "lab-checkout"),
			binding("app-lab-checkout", "loki", "lab-checkout"),
			binding("app-lab-checkout", "tempo", "lab-checkout"),
			binding("app-lab-checkout", "kubernetes", "deployment"),
			binding("app-lab-order", "prometheus", "lab-order"),
			binding("app-lab-order", "loki", "lab-order"),
			binding("app-lab-order", "elasticsearch", "lab-order"),
			binding("app-lab-order", "tempo", "lab-order"),
			binding("app-lab-payments", "prometheus", "lab-payments"),
			binding("app-lab-payments", "loki", "lab-unregistered"),
			binding("app-lab-payments", "tempo", "lab-order"),
			binding("app-lab-search", "elasticsearch", "lab-search"),
			binding("app-lab-search", "gitlab", "group/search"),
		},
		Connectors: []ExistingConnector{{ID: labPrometheus, EnvironmentID: "prod", Provider: "prometheus",
			Endpoint: "http://127.0.0.1:19090", AuthMethod: "none", OptionsValid: true}},
		Targets: []ExistingTarget{
			{ID: "target-checkout-metrics", ApplicationID: "app-lab-checkout", EnvironmentID: "prod", ConnectorID: labPrometheus,
				Provider: "prometheus", TargetKind: "metric_resource", ExternalID: "lab-checkout-api"},
			{ID: "target-order-metrics", ApplicationID: "app-lab-order", EnvironmentID: "prod", ConnectorID: labPrometheus,
				Provider: "prometheus", TargetKind: "metric_resource", ExternalID: "lab-order-api"},
		},
	}
}
