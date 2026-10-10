package main

import "testing"

func TestLegacyOnlyConfigIsNotReadOnV3(t *testing.T) {
	t.Setenv("AIOPS_TELEMETRY_CONFIG", "/private/telemetry.json")
	if got := legacyOnlyConfig(false, "AIOPS_TELEMETRY_CONFIG", "registry"); got != "/private/telemetry.json" {
		t.Fatalf("legacy mode: %q", got)
	}
	if got := legacyOnlyConfig(true, "AIOPS_TELEMETRY_CONFIG", "registry"); got != "" {
		t.Fatalf("V3 instance read %q", got)
	}
	t.Setenv("AIOPS_TELEMETRY_CONFIG", "")
	if got := legacyOnlyConfig(true, "AIOPS_TELEMETRY_CONFIG", "registry"); got != "" {
		t.Fatalf("unset: %q", got)
	}
}
