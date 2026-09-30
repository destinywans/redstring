package v3integrations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type countingCredentialResolver struct {
	calls int
}

func (resolver *countingCredentialResolver) Resolve(_ context.Context, _ string) (string, error) {
	resolver.calls++
	return "secret", nil
}

func testCredentialBinding() CredentialBinding {
	return CredentialBinding{
		Tier: "nonprod", EnvironmentID: "env-a", ConnectorID: "connector-a",
		Provider: "gitlab", Endpoint: "https://gitlab.example.test", CredentialRef: "vault://ops/connectors/gitlab#token",
	}
}

func TestParseCredentialBindings(t *testing.T) {
	bindings, err := ParseCredentialBindings("  ")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("empty configuration: bindings=%v err=%v", bindings, err)
	}

	raw := `[{"tier":"nonprod","environment_id":"env-a","connector_id":"connector-a","provider":"gitlab","endpoint":"https://gitlab.example.test///","credential_ref":"vault://ops/connectors/gitlab#token"}]`
	bindings, err = ParseCredentialBindings(raw)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("valid configuration: bindings=%v err=%v", bindings, err)
	}
	if bindings[0].Endpoint != "https://gitlab.example.test" {
		t.Fatalf("endpoint was not canonicalized: %q", bindings[0].Endpoint)
	}

	bad := map[string]string{
		"malformed":           `[{`,
		"not array":           `{}`,
		"null":                `null`,
		"unknown field":       strings.Replace(raw, `"tier":"nonprod"`, `"tier":"nonprod","extra":true`, 1),
		"duplicate field":     strings.Replace(raw, `"tier":"nonprod"`, `"tier":"production","tier":"nonprod"`, 1),
		"trailing data":       raw + ` []`,
		"invalid tier":        strings.Replace(raw, `"nonprod"`, `"staging"`, 1),
		"missing connector":   strings.Replace(raw, `"connector-a"`, `""`, 1),
		"missing environment": strings.Replace(raw, `"env-a"`, `""`, 1),
		"unknown provider":    strings.Replace(raw, `"gitlab"`, `"unknown"`, 1),
		"invalid endpoint":    strings.Replace(raw, `"https://gitlab.example.test///"`, `"http://gitlab.example.test"`, 1),
		"invalid reference":   strings.Replace(raw, `"vault://ops/connectors/gitlab#token"`, `"plain-secret"`, 1),
		"duplicate":           strings.TrimSuffix(raw, "]") + `,` + strings.TrimPrefix(raw, "["),
		"canonical duplicate": strings.TrimSuffix(raw, "]") + `,` + strings.TrimPrefix(strings.Replace(raw, `"https://gitlab.example.test///"`, `"https://gitlab.example.test"`, 1), "["),
	}
	for name, input := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCredentialBindings(input); err == nil {
				t.Fatal("expected invalid credential bindings to be rejected")
			}
		})
	}

	rows := make([]string, maxCredentialBindings+1)
	for i := range rows {
		rows[i] = strings.Replace(raw[1:len(raw)-1], `"connector-a"`, fmt.Sprintf(`"connector-%d"`, i), 1)
	}
	if _, err := ParseCredentialBindings("[" + strings.Join(rows, ",") + "]"); err == nil {
		t.Fatal("expected oversized binding list to be rejected")
	}
}

func TestBoundResolverRequiresExactConnectorUse(t *testing.T) {
	binding := testCredentialBinding()
	inner := &countingCredentialResolver{}
	resolver := NewBoundResolver(inner, []CredentialBinding{binding})
	use := CredentialUse{
		Tier: binding.Tier, EnvironmentID: binding.EnvironmentID,
		ConnectorID: binding.ConnectorID, Provider: binding.Provider, Endpoint: binding.Endpoint + "/",
	}
	if !resolver.CredentialAllowed(use, binding.CredentialRef) {
		t.Fatal("expected exact connector configuration to be allowed")
	}
	if value, err := resolver.Resolve(WithCredentialUse(context.Background(), use), binding.CredentialRef); err != nil || value != "secret" || inner.calls != 1 {
		t.Fatalf("expected exact binding to resolve: value=%q err=%v calls=%d", value, err, inner.calls)
	}

	cases := map[string]func(*CredentialUse){
		"tier":         func(use *CredentialUse) { use.Tier = "production" },
		"environment":  func(use *CredentialUse) { use.EnvironmentID = "env-b" },
		"connector id": func(use *CredentialUse) { use.ConnectorID = "connector-b" },
		"provider":     func(use *CredentialUse) { use.Provider = "prometheus" },
		"endpoint":     func(use *CredentialUse) { use.Endpoint = "https://other.example.test" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			wrong := use
			change(&wrong)
			if resolver.CredentialAllowed(wrong, binding.CredentialRef) {
				t.Fatal("different connector configuration was allowed")
			}
			if _, err := resolver.Resolve(WithCredentialUse(context.Background(), wrong), binding.CredentialRef); !errors.Is(err, ErrCredentialUnavailable) {
				t.Fatalf("expected authorization refusal, got %v", err)
			}
		})
	}
	if _, err := resolver.Resolve(context.Background(), binding.CredentialRef); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("missing context: %v", err)
	}
	if _, err := resolver.Resolve(WithCredentialUse(context.Background(), use), "vault://ops/connectors/other#token"); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("different reference: %v", err)
	}
	if resolver.CredentialAllowed(use, "vault://ops/connectors/other#token") {
		t.Fatal("different reference was allowed")
	}
	if inner.calls != 1 {
		t.Fatalf("denied attempts reached the inner resolver: %d calls", inner.calls)
	}
}

func TestBoundResolverIgnoresInvalidDirectBindings(t *testing.T) {
	binding := testCredentialBinding()
	binding.ConnectorID = ""
	inner := &countingCredentialResolver{}
	resolver := NewBoundResolver(inner, []CredentialBinding{binding})
	use := CredentialUse{Tier: binding.Tier, EnvironmentID: binding.EnvironmentID, Provider: binding.Provider, Endpoint: binding.Endpoint}
	if _, err := resolver.Resolve(WithCredentialUse(context.Background(), use), binding.CredentialRef); !errors.Is(err, ErrCredentialUnavailable) || inner.calls != 0 {
		t.Fatalf("invalid direct binding was accepted: err=%v calls=%d", err, inner.calls)
	}
}

func TestKnowledgeSourceCredentialProvidersRequireExactBoundUse(t *testing.T) {
	for _, provider := range []string{"confluence", "notion"} {
		t.Run(provider, func(t *testing.T) {
			binding := testCredentialBinding()
			binding.Provider = provider
			inner := &countingCredentialResolver{}
			resolver := NewBoundResolver(inner, []CredentialBinding{binding})
			use := CredentialUse{Tier: binding.Tier, EnvironmentID: binding.EnvironmentID,
				ConnectorID: binding.ConnectorID, Provider: provider, Endpoint: binding.Endpoint}
			if !resolver.CredentialAllowed(use, binding.CredentialRef) {
				t.Fatal("exact source binding was rejected")
			}
			if _, err := resolver.Resolve(WithCredentialUse(context.Background(), use), binding.CredentialRef); err != nil || inner.calls != 1 {
				t.Fatalf("exact source binding did not resolve: err=%v calls=%d", err, inner.calls)
			}
			use.ConnectorID = "other"
			if _, err := resolver.Resolve(WithCredentialUse(context.Background(), use), binding.CredentialRef); !errors.Is(err, ErrCredentialUnavailable) || inner.calls != 1 {
				t.Fatalf("different source binding reached Vault: err=%v calls=%d", err, inner.calls)
			}
		})
	}
}
