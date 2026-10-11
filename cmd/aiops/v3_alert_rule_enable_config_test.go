package main

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wans/aiops-canvas/internal/v3alertrules"
)

// D11: a file_pull binding reads its write token and private CA from Vault on
// every request and needs no Argo CD settings.
func TestV3AlertRuleEnableConfigFilePull(t *testing.T) {
	const rules = "groups:\n- name: g\n  rules:\n  - alert: Existing\n    expr: up == 0\n"
	head := strings.Repeat("a", 40)
	var tokens atomic.Value
	gitlab := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens.Store(r.Header.Get("PRIVATE-TOKEN"))
		root := "/api/v4/projects/6"
		rule := map[string]any{"name": "main", "allow_force_push": false,
			"push_access_levels": []any{map[string]any{"access_level": 0}}, "merge_access_levels": []any{map[string]any{"access_level": 40}}}
		switch r.URL.Path {
		case root + "/repository/branches/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "protected": true, "commit": map[string]any{"id": head}})
		case root + "/protected_branches/main":
			_ = json.NewEncoder(w).Encode(rule)
		case root + "/protected_branches":
			w.Header().Set("X-Page", "1")
			w.Header().Set("X-Per-Page", "100")
			_ = json.NewEncoder(w).Encode([]any{rule})
		case root + "/repository/files/rules/test/app.yml/raw":
			_, _ = w.Write([]byte(rules))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gitlab.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: gitlab.Certificate().Raw}))
	var vaultReads atomic.Int32
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vaultReads.Add(1)
		if r.Header.Get("X-Vault-Token") != "process-token" || r.URL.Path != "/v1/aiops/data/lab/gitlab/rules-write" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]any{"token": "write-token", "ca": ca}}})
	}))
	defer vault.Close()
	t.Setenv("AIOPS_VAULT_URL", vault.URL)
	t.Setenv("AIOPS_VAULT_TOKEN", "process-token")

	var instance v3Instance
	instance.Environments = append(instance.Environments, struct {
		ID       string   `json:"id"`
		Label    string   `json:"label"`
		Kind     string   `json:"kind"`
		Tone     string   `json:"tone"`
		Clusters []string `json:"clusters"`
		Note     string   `json:"note,omitempty"`
	}{ID: "test"})
	source, err := v3alertrules.NewPrometheusSource(map[string]v3alertrules.PrometheusEndpoint{"test": {URL: "https://prometheus.example.invalid"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ruleFile := "/etc/prometheus-gitops/current/rules/test/app.yml"
	// D32: the executor takes the application label from the rule source.
	ruleBindings := []v3alertrules.RuleGroupBinding{{EnvironmentID: "test", ApplicationID: "app", File: ruleFile, Group: "g", ApplicationLabel: "checkout-api"}}
	write := func(config map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(config)
		path := filepath.Join(t.TempDir(), "enable.json")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	binding := map[string]any{"environment_id": "test", "application_id": "app", "group": "g", "provider_file": ruleFile,
		"project_id": 6, "target_branch": "main", "yaml_path": "rules/test/app.yml", "load": "file_pull", "readback_timeout_seconds": 300,
		"project_path": "aiops-lab/prometheus-rules", "connector_id": "gitlab-test"}
	config := map[string]any{"gitlab_url": gitlab.URL, "gitlab_token_ref": "vault://aiops/lab/gitlab/rules-write#token",
		"gitlab_ca_field": "ca", "gitlab_web_url": "http://127.0.0.1:28080", "bindings": []any{binding}}
	applier, environments, err := loadV3AlertRuleEnableApplier(write(config), instance, source, ruleBindings)
	if err != nil || applier == nil || !environments["test"] {
		t.Fatalf("file_pull configuration: %v", err)
	}
	if vaultReads.Load() != 0 {
		t.Fatal("startup read Vault")
	}
	intent := v3alertrules.AlertRuleEnableIntent{EnvironmentID: "test", ApplicationID: "app",
		ProviderFileIdentityDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(ruleFile))),
		Spec: v3alertrules.DraftRuleSpec{ApplicationID: "app", EnvironmentID: "test", Group: "g", Name: "New", Expr: "up == 1", For: "5m",
			Mode: "draft", Labels: map[string]string{"application": "checkout-api", "environment": "test", "severity": "P2"}}}
	plan, err := applier.Plan(t.Context(), intent)
	if err != nil || plan.Load != "file_pull" || !plan.ExecutorMerges() || plan.ReadbackTimeoutSeconds != 300 || plan.BaseRevision != head {
		t.Fatalf("plan through the Vault token and CA: %+v %v", plan, err)
	}
	if tokens.Load() != "write-token" || vaultReads.Load() < 2 {
		t.Fatalf("token %v, Vault reads %d", tokens.Load(), vaultReads.Load())
	}

	for name, mutate := range map[string]func(map[string]any, map[string]any){
		"both token kinds":          func(c, _ map[string]any) { c["gitlab_token_file"] = "/run/secrets/gitlab" },
		"no token":                  func(c, _ map[string]any) { delete(c, "gitlab_token_ref") },
		"plain token reference":     func(c, _ map[string]any) { c["gitlab_token_ref"] = "write-token" },
		"argo binding without argo": func(_, b map[string]any) { b["load"] = "argo_cd" },
		"argo settings unused": func(c, _ map[string]any) {
			c["argo_url"], c["argo_token_file"] = "https://argo.example.invalid", "/run/secrets/argo"
		},
		"unknown load":     func(_, b map[string]any) { b["load"] = "push" },
		"short readback":   func(_, b map[string]any) { b["readback_timeout_seconds"] = 10 },
		"bad project path": func(_, b map[string]any) { b["project_path"] = "../rules" },
		"http endpoint":    func(c, _ map[string]any) { c["gitlab_url"] = "http://127.0.0.1:28080" },
	} {
		c, b := map[string]any{}, map[string]any{}
		for k, v := range config {
			c[k] = v
		}
		for k, v := range binding {
			b[k] = v
		}
		c["bindings"] = []any{b}
		mutate(c, b)
		if _, _, err := loadV3AlertRuleEnableApplier(write(c), instance, source, ruleBindings); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	t.Setenv("AIOPS_VAULT_TOKEN", "")
	if _, _, err := loadV3AlertRuleEnableApplier(write(config), instance, source, ruleBindings); err == nil {
		t.Error("a Vault reference without process Vault settings was accepted")
	}
}
