package main

import (
	"os"
	"path/filepath"
	"testing"
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
