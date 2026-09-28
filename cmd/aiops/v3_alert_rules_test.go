package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestV3AlertRuleConfigRejectsUnboundEnvironment(t *testing.T) {
	instance := v3Instance{}
	if err := json.Unmarshal([]byte(`{"environments":[{"id":"test"}]}`), &instance); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rules.json")
	data := []byte(`{"environments":{"test":{"url":"https://example.invalid"}},"bindings":[{"environment_id":"prod","application_id":"checkout","file":"/rules.yml","group":"checkout"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadV3AlertRuleSource(path, instance); err == nil {
		t.Fatal("cross-environment rule group binding accepted")
	}
}

func TestV3AlertRuleConfigBindsExactGroup(t *testing.T) {
	instance := v3Instance{}
	if err := json.Unmarshal([]byte(`{"environments":[{"id":"test"}]}`), &instance); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rules.json")
	data := []byte(`{"environments":{"test":{"url":"https://example.invalid"}},"bindings":[{"environment_id":"test","application_id":"checkout","file":"/rules.yml","group":"checkout"}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	source, bindings, err := loadV3AlertRuleSource(path, instance)
	if err != nil || source == nil || len(bindings) != 1 || bindings[0].ApplicationID != "checkout" || bindings[0].File != "/rules.yml" {
		t.Fatalf("source=%v bindings=%+v error=%v", source, bindings, err)
	}
}
