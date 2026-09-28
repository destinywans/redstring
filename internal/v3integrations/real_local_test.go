package v3integrations

import (
	"context"
	"os"
	"testing"
)

// TestRealLocalConnectorProbes is an opt-in, read-only smoke check for local
// providers. It never registers a connector or writes to a remote system.
func TestRealLocalConnectorProbes(t *testing.T) {
	targets := []struct {
		provider string
		env      string
	}{
		{"prometheus", "AIOPS_TEST_PROMETHEUS_URL"},
		{"loki", "AIOPS_TEST_LOKI_URL"},
		{"tempo", "AIOPS_TEST_TEMPO_URL"},
		{"grafana", "AIOPS_TEST_GRAFANA_URL"},
	}
	configured := 0
	for _, target := range targets {
		endpoint := os.Getenv(target.env)
		if endpoint == "" {
			continue
		}
		configured++
		t.Run(target.provider, func(t *testing.T) {
			result, err := (Prober{}).Check(context.Background(), target.provider, endpoint, "none", "")
			if err != nil || !result.OK {
				t.Fatalf("local %s read-only probe failed: reason=%s err=%v", target.provider, result.ErrorCode, err)
			}
		})
	}
	if configured == 0 {
		t.Skip("set AIOPS_TEST_*_URL variables to smoke-check local read-only providers")
	}
}
