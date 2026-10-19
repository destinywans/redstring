package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/destinywans/redstring/internal/aiextensions"
)

func TestV3MCPRegistrationBindingsRequireExactDeploymentAdmission(t *testing.T) {
	var instance v3Instance
	if err := json.Unmarshal([]byte(`{"tier":"non-production","domain":"aiops.example.invalid","environments":[{"id":"dev"}]}`), &instance); err != nil {
		t.Fatal(err)
	}
	if admit, err := loadV3MCPRegistrationBindings("", instance); err != nil || admit != nil {
		t.Fatal("missing configuration must disable registration")
	}
	config := v3MCPRegistrationFile{Tier: instance.Tier, InstanceID: instance.Domain, Bindings: []v3MCPRegistrationBinding{{
		ApplicationID: "orders", EnvironmentID: "dev", Endpoint: "https://mcp.example.invalid/read", CredentialRef: "orders-reader",
	}}}
	path := filepath.Join(t.TempDir(), "bindings.json")
	write := func() {
		t.Helper()
		data, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	admit, err := loadV3MCPRegistrationBindings(path, instance)
	if err != nil {
		t.Fatal(err)
	}
	transport := aiextensions.MCPConfig{Endpoint: config.Bindings[0].Endpoint, CredentialRef: "orders-reader"}
	if err := admit(t.Context(), "orders", "dev", transport); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		app, env, endpoint, credential string
		http                           bool
	}{
		{"other", "dev", transport.Endpoint, transport.CredentialRef, false},
		{"orders", "prod", transport.Endpoint, transport.CredentialRef, false},
		{"orders", "dev", transport.Endpoint + "/other", transport.CredentialRef, false},
		{"orders", "dev", transport.Endpoint, "other-token", false},
		{"orders", "dev", transport.Endpoint, transport.CredentialRef, true},
	} {
		if err := admit(t.Context(), tc.app, tc.env, aiextensions.MCPConfig{Endpoint: tc.endpoint, CredentialRef: tc.credential, AllowHTTP: tc.http}); err == nil {
			t.Fatalf("binding mismatch accepted: %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := admit(ctx, "orders", "dev", transport); err == nil {
		t.Fatal("canceled operation admitted")
	}
	for _, endpoint := range []string{"http://remote.invalid/mcp", "https://user:password@mcp.invalid/", "https://mcp.invalid/?token=x", "https://mcp.invalid/#x"} {
		config.Bindings[0].Endpoint = endpoint
		config.Bindings[0].AllowHTTP = true
		write()
		if _, err := loadV3MCPRegistrationBindings(path, instance); err == nil {
			t.Fatalf("unsafe endpoint configured: %s", endpoint)
		}
	}
	config.Bindings[0].Endpoint = "http://127.0.0.1:19001/mcp"
	write()
	if _, err := loadV3MCPRegistrationBindings(path, instance); err != nil {
		t.Fatalf("explicit local lab binding: %v", err)
	}
	config.InstanceID = "different-instance"
	write()
	if _, err := loadV3MCPRegistrationBindings(path, instance); err == nil {
		t.Fatal("wrong instance accepted")
	}
	config.InstanceID = instance.Domain
	write()
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadV3MCPRegistrationBindings(path, instance); err == nil {
		t.Fatal("public configuration accepted")
	}
}
