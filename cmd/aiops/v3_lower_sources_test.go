package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadV3LowerSourcesRequiresExplicitValidProductionConfig(t *testing.T) {
	if _, err := loadV3LowerSources("", "production"); err != nil {
		t.Fatalf("empty production configuration: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sources.json")
	valid := `{"environments":[{"id":"staging","label":{"zh":"预发","en":"Staging"}}],` +
		`"sources":[{"id":"staging-prometheus","brand":"prometheus","environment_id":"staging",` +
		`"name":{"zh":"预发指标","en":"Staging metrics"},"purpose":{"zh":"对比","en":"Compare"},` +
		`"endpoint":"https://prometheus.example.invalid","vault_ref":"vault://secret/aiops/prometheus#token"}]}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadV3LowerSources(path, "production"); err != nil {
		t.Fatalf("valid production configuration: %v", err)
	}
	if _, err := loadV3LowerSources(path, "nonprod"); err == nil {
		t.Fatal("non-production instance accepted lower-environment source")
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"environment_id":"staging"`, `"environment_id":"prod"`, 1),
		strings.Replace(valid, `"vault_ref":"vault://secret/aiops/prometheus#token"`, `"vault_ref":"raw-secret"`, 1),
		valid + valid,
		strings.Repeat("x", (64<<10)+1),
	} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadV3LowerSources(path, "production"); err == nil {
			t.Fatal("invalid lower-source configuration accepted")
		}
	}
}
