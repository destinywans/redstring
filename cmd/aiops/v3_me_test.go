package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
)

func TestConfigureV3MeEvidence(t *testing.T) {
	info := v3Instance{Tier: "nonprod", Domain: "aiops.example.test"}
	base, err := configureV3MeEvidence(info, "", "")
	if err != nil || base.MFAInstanceID != v3MFAInstanceID(info) || base.PasskeyRPID != info.Domain || base.Oncall != nil {
		t.Fatalf("unconfigured evidence: %+v, %v", base, err)
	}
	if _, err := configureV3MeEvidence(info, "https://oncall.example.test/current", ""); err == nil {
		t.Fatal("partial on-call configuration accepted")
	}
	tokenPath := filepath.Join(t.TempDir(), "oncall.token")
	if err := os.WriteFile(tokenPath, []byte("test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configureV3MeEvidence(info, "http://oncall.example.test/current", tokenPath); err == nil {
		t.Fatal("non-HTTPS on-call endpoint accepted")
	}
	configured, err := configureV3MeEvidence(info, "https://oncall.example.test/current", tokenPath)
	if err != nil || configured.Oncall == nil || configured.MFAInstanceID != base.MFAInstanceID {
		t.Fatalf("configured evidence: %+v, %v", configured, err)
	}
}

func TestConfigureV3RejectsPartialOncallBeforeRegisteringRoutes(t *testing.T) {
	t.Setenv("AIOPS_V3_ONCALL_CURRENT_URL", "https://oncall.example.test/current")
	t.Setenv("AIOPS_V3_ONCALL_TOKEN_FILE", "")
	err := configureV3(context.Background(), nil, nil, nil, nil, nil,
		&v3Instance{Tier: "nonprod", Domain: "aiops.example.test"}, httpapi.V3InstanceConfig{}, operations.Config{})
	if err == nil {
		t.Fatal("partial on-call configuration did not fail V3 startup")
	}
}
