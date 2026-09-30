package connectors

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"
)

// This opt-in diagnostic must not read a previously saved credential/config
// or contact a developer's old environment during the ordinary unit suite.
func TestLocalReadcheck(t *testing.T) {
	path := os.Getenv("AIOPS_TESTLAB_TELEMETRY_CONFIG")
	if path == "" {
		t.Skip("AIOPS_TESTLAB_TELEMETRY_CONFIG required for explicit local live telemetry")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read explicitly supplied telemetry config:", err)
	}
	var config TelemetryConfig
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal("decode telemetry config:", err)
	}
	for _, endpoint := range []TelemetryEndpoint{config.Prometheus, config.Elasticsearch, config.Tempo, config.Loki} {
		if endpoint.URL == "" {
			continue
		}
		u, err := url.Parse(endpoint.URL)
		if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
			t.Fatal("local readcheck accepts only explicit loopback endpoints")
		}
	}
	telemetry, err := NewTelemetry(config)
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC().Truncate(time.Millisecond)
	result, err := telemetry.QueryLogsOptions(context.Background(), "lab-order-api", "loki", "", "", false, TimeWindow{Start: end.Add(-30 * time.Minute), End: end}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = telemetry.lokiSummary(context.Background(), result.Source); err != nil {
		t.Fatal(err)
	}
}
