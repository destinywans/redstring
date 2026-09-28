package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func alertmanagerTestInstance(t *testing.T) v3Instance {
	t.Helper()
	var instance v3Instance
	if err := json.Unmarshal([]byte(`{"environments":[{"id":"test"},{"id":"staging"}]}`), &instance); err != nil {
		t.Fatal(err)
	}
	return instance
}

func TestV3AlertmanagerConfigRequiresTrustedEnvironmentLabel(t *testing.T) {
	instance := alertmanagerTestInstance(t)
	for _, tc := range []struct {
		name string
		body string
	}{
		{"unknown environment", `{"environments":{"prod":{"url":"https://example.invalid","environment_label":"environment"}}}`},
		{"no label", `{"environments":{"test":{"url":"https://example.invalid"}}}`},
		{"unsafe label", `{"environments":{"test":{"url":"https://example.invalid","environment_label":"environment|prod"}}}`},
		{"plain HTTP", `{"environments":{"test":{"url":"http://example.invalid","environment_label":"environment"}}}`},
		{"credential in URL", `{"environments":{"test":{"url":"https://secret@example.invalid","environment_label":"environment"}}}`},
		{"extra field", `{"environments":{"test":{"url":"https://example.invalid","environment_label":"environment","secret":"leak"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "alertmanager.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadV3AlertmanagerSource(path, instance); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestV3AlertmanagerConfigAllowsExplicitLocalReadSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alertmanager.json")
	data := []byte(`{"environments":{"test":{"url":"http://127.0.0.1:19093","allow_http":true,"environment_label":"environment"}}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client, labels, err := loadV3AlertmanagerSource(path, alertmanagerTestInstance(t))
	if err != nil || client == nil || labels["test"] != "environment" || labels["staging"] != "" {
		t.Fatalf("source=%v labels=%+v err=%v", client, labels, err)
	}
	client, labels, err = loadV3AlertmanagerSource("", alertmanagerTestInstance(t))
	if err != nil || client != nil || labels != nil {
		t.Fatalf("unconfigured source=%v labels=%+v err=%v", client, labels, err)
	}
}

func TestV3AlertmanagerProductionRequiresHTTPS(t *testing.T) {
	instance := alertmanagerTestInstance(t)
	instance.Tier = "production"
	path := filepath.Join(t.TempDir(), "alertmanager.json")
	for _, body := range []string{
		`{"environments":{"test":{"url":"http://127.0.0.1:19093","allow_http":true,"environment_label":"environment"}}}`,
		`{"environments":{"test":{"url":"https://example.invalid","allow_http":true,"environment_label":"environment"}}}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadV3AlertmanagerSource(path, instance); err == nil {
			t.Fatal("production accepted insecure Alertmanager source")
		}
	}
}
