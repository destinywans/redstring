package connectors

import (
	"strings"
	"testing"
)

// PA-50/51: only IDs leave the registration, never expressions.
func TestMetricQueryIDsAndLogRegistration(t *testing.T) {
	client, err := NewTelemetry(TelemetryConfig{
		Prometheus: TelemetryEndpoint{URL: "http://127.0.0.1:19090", AllowHTTP: true},
		Loki:       TelemetryEndpoint{URL: "http://127.0.0.1:13100", AllowHTTP: true},
		Applications: map[string]ApplicationTelemetry{
			"lab-checkout": {PrometheusQueries: map[string]string{"request_rate": "sum(rate(x[5m]))", "node_cpu_busy_ratio": "max(lab:vm_node_cpu_busy:ratio1m)"},
				LokiLabels: map[string]string{"app": "checkout"}},
			"lab-order": {PrometheusQueries: map[string]string{"request_rate": "sum(rate(y[5m]))"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := client.MetricQueryIDs("lab-checkout"); strings.Join(ids, ",") != "node_cpu_busy_ratio,request_rate" {
		t.Fatalf("ids: %v", ids)
	}
	if ids := client.MetricQueryIDs("missing"); ids != nil {
		t.Fatalf("unknown resource: %v", ids)
	}
	if !client.LogsRegistered("loki", "lab-checkout") || client.LogsRegistered("loki", "lab-order") ||
		client.LogsRegistered("elasticsearch", "lab-checkout") {
		t.Fatal("log registration")
	}
	var none *Telemetry
	if none.MetricQueryIDs("lab-checkout") != nil || none.LogsRegistered("loki", "lab-checkout") {
		t.Fatal("nil telemetry")
	}
}
