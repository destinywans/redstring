package v3mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/aiextensions"
)

func TestRegistrationConfigurationRejectsUnsafeOrInlineCredentials(t *testing.T) {
	valid := registrationRequest{ApplicationID: "app", Title: "App MCP", Maintainer: "platform", Endpoint: "https://mcp.example.invalid/mcp", CredentialRef: "private-ref"}
	r, ok := valid.record()
	if !ok || len(r.MCP.Bindings) != 0 || !validMCPRegistration(r, false) || bindableMCP(r) {
		t.Fatal("new registration must support discovery without implying reviewed bindings")
	}
	for _, tc := range []struct {
		name string
		edit func(*registrationRequest)
	}{
		{"userinfo", func(r *registrationRequest) { r.Endpoint = "https://token@mcp.example.invalid/mcp" }},
		{"query", func(r *registrationRequest) { r.Endpoint += "?token=secret" }},
		{"empty query", func(r *registrationRequest) { r.Endpoint += "?" }},
		{"fragment", func(r *registrationRequest) { r.Endpoint += "#secret" }},
		{"empty fragment", func(r *registrationRequest) { r.Endpoint += "#" }},
		{"remote http", func(r *registrationRequest) { r.Endpoint = "http://example.invalid/mcp"; r.AllowHTTP = true }},
		{"loopback requires opt-in", func(r *registrationRequest) { r.Endpoint = "http://127.0.0.1:8000/mcp" }},
		{"credential traversal", func(r *registrationRequest) { r.CredentialRef = "../secret" }},
		{"credential dot", func(r *registrationRequest) { r.CredentialRef = ".." }},
		{"missing host", func(r *registrationRequest) { r.Endpoint = "https://:443/mcp" }},
		{"backslash", func(r *registrationRequest) { r.Endpoint = "https://example.invalid/mcp\\x" }},
		{"space", func(r *registrationRequest) { r.Title = " App MCP" }},
		{"long maintainer", func(r *registrationRequest) { r.Maintainer = strings.Repeat("a", 161) }},
		{"blank title", func(r *registrationRequest) { r.Title = "" }},
		{"bad app", func(r *registrationRequest) { r.ApplicationID = "../app" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.edit(&in)
			if _, ok := in.record(); ok {
				t.Fatal("unsafe registration accepted")
			}
		})
	}
	valid.Endpoint = "http://127.0.0.1:8000/mcp"
	valid.AllowHTTP = true
	if _, ok := valid.record(); !ok {
		t.Fatal("explicit local HTTP transport declaration rejected")
	}
}

func TestV3RegistrationDiscoveryKeepsDeploymentAdmission(t *testing.T) {
	r, ok := (registrationRequest{ApplicationID: "app", Title: "App MCP", Maintainer: "platform", Endpoint: "https://mcp.example.invalid/mcp"}).record()
	if !ok {
		t.Fatal("invalid fixture")
	}
	r.ID = "mcp-1"
	newRaw, _ := json.Marshal(v3RegistrationRecord{Record: r, RegistrationSource: "v3"})
	v := registration{ID: r.ID, ApplicationID: r.ApplicationID, EnvironmentID: "dev", Version: 1, Record: r, raw: newRaw}
	d := discoveryRegistration{registration: v, version: 1}
	if err := d.validateDiscoveryRegistration(); err != nil {
		t.Fatal("new zero-binding registration cannot be discovered", err)
	}
	service := New(nil, nil)
	if service.registrationEndpointAllowed(context.Background(), v) {
		t.Fatal("unconfigured deployment allowed new endpoint")
	}
	calls := 0
	service.AuthorizeRegistrationEndpoint = func(_ context.Context, app, env string, config aiextensions.MCPConfig) error {
		calls++
		if app != "app" || env != "dev" || config.Endpoint != r.MCP.Endpoint {
			return errors.New("not admitted")
		}
		return nil
	}
	if !service.registrationEndpointAllowed(context.Background(), v) || calls != 1 {
		t.Fatal("exact tuple not sent to deployment admission")
	}
	v.EnvironmentID = "prod"
	if service.registrationEndpointAllowed(context.Background(), v) {
		t.Fatal("deployment approval carried across environments")
	}
	legacyRaw, _ := json.Marshal(r)
	d.raw = legacyRaw
	if d.validateDiscoveryRegistration() == nil {
		t.Fatal("legacy registration without operator-confirmed bindings became discoverable")
	}
	var raw map[string]any
	if err := json.Unmarshal(newRaw, &raw); err != nil {
		t.Fatal(err)
	}
	raw["registration_source"] = "unknown"
	d.raw, _ = json.Marshal(raw)
	if d.validateDiscoveryRegistration() == nil {
		t.Fatal("unknown provenance accepted")
	}
}
