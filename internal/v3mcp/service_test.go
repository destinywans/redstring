package v3mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/aiextensions"
)

func TestSummaryNeverExposesEndpointOrCredential(t *testing.T) {
	v := registration{
		ID: "mcp-1", ApplicationID: "app-1", Application: "Orders", EnvironmentID: "test",
		Record: aiextensions.Record{
			ID: "mcp-1", ApplicationID: "app-1", Kind: "mcp", Title: "Orders MCP",
			Enabled: true, UpdatedAt: time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC),
			MCP: &aiextensions.MCPConfig{
				Endpoint: "https://private.example.test/mcp", CredentialRef: "secret-ref",
				Bindings: []aiextensions.Binding{{Name: "get_orders", ReadOnlyConfirmed: true}},
			},
		},
	}
	raw, err := json.Marshal(summary(v))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private.example.test", "secret-ref"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("registration summary exposed %q: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), `"status":"pending"`) || !strings.Contains(string(raw), "连接状态未验证") && !strings.Contains(string(raw), "待验证") {
		t.Fatalf("unverified registration was reported as healthy: %s", raw)
	}
}

func TestBindableMCPRequiresActiveReviewedReadOnlyRegistration(t *testing.T) {
	valid := aiextensions.Record{
		ID: "mcp-1", ApplicationID: "app-1", Kind: "mcp",
		Title: "Orders MCP", Maintainer: "platform", Enabled: true, Version: 1,
		MCP: &aiextensions.MCPConfig{
			Endpoint: "https://mcp.example.invalid/tools",
			Bindings: []aiextensions.Binding{{Name: "orders", Tool: "read_orders", ReadOnlyConfirmed: true, Arguments: map[string]any{}}},
		},
	}
	if !bindableMCP(valid) {
		t.Fatal("reviewed registration must be bindable")
	}
	for _, tc := range []struct {
		name string
		edit func(*aiextensions.Record)
	}{
		{"disabled", func(r *aiextensions.Record) { r.Enabled = false }},
		{"no reviewed tools", func(r *aiextensions.Record) { r.MCP.Bindings = nil }},
		{"unreviewed tool", func(r *aiextensions.Record) { r.MCP.Bindings[0].ReadOnlyConfirmed = false }},
		{"credential in URL", func(r *aiextensions.Record) { r.MCP.Endpoint = "https://secret@example.invalid/tools" }},
		{"unsupported remote HTTP", func(r *aiextensions.Record) { r.MCP.Endpoint = "http://mcp.example.invalid/tools" }},
		{"missing arguments", func(r *aiextensions.Record) { r.MCP.Bindings[0].Arguments = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			mcp := *valid.MCP
			mcp.Bindings = append([]aiextensions.Binding(nil), valid.MCP.Bindings...)
			r.MCP = &mcp
			tc.edit(&r)
			if bindableMCP(r) {
				t.Fatal("invalid registration must not be bound")
			}
		})
	}
}
