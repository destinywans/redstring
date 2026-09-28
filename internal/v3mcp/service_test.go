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
	for _, tc := range []struct {
		language, trust, caps, last string
	}{
		{"zh-CN", "V3 待审核", "MCP 能力未验证", "V3 调用记录未接入"},
		{"en-US", "V3 review pending", "MCP capabilities unverified", "V3 call history unavailable"},
	} {
		raw, err := json.Marshal(summary(v, tc.language))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"private.example.test", "secret-ref"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("registration summary exposed %q: %s", secret, raw)
			}
		}
		var got struct {
			Status string `json:"status"`
			Desc   string `json:"desc"`
			Trust  struct {
				Label string `json:"label"`
				Tone  string `json:"tone"`
			} `json:"trust"`
			Apps []any  `json:"apps"`
			Caps string `json:"caps"`
			Last string `json:"last"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status != "pending" || !strings.Contains(got.Desc, "Orders") || !strings.Contains(got.Desc, "test") ||
			got.Trust.Tone != "warn" || got.Trust.Label != tc.trust || got.Apps == nil || len(got.Apps) != 0 || got.Caps != tc.caps || got.Last != tc.last {
			t.Fatalf("registration appeared reviewed, called or Agent-authorized: %s", raw)
		}
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
