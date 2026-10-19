package main

import (
	"context"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3knowledgesources"
	"github.com/destinywans/redstring/internal/v3sourceregistry"
)

func TestConfiguredV3SourceRegistryRequiresExplicitPublicWebOptIn(t *testing.T) {
	st := &store.Store{}
	policy := &rbac.V3Policy{Store: st}
	for _, raw := range []string{"", "false"} {
		registry, err := configuredV3SourceRegistry(raw, st, policy)
		if err != nil || registry == nil || len(registry.Readers) != 1 {
			t.Fatalf("flag %q must leave only the local upload reader available: registry=%+v err=%v", raw, registry, err)
		}
	}
	registry, err := configuredV3SourceRegistry("true", st, policy)
	if err != nil || registry == nil || len(registry.Readers) != 2 {
		t.Fatalf("explicit opt-in must install the Web and upload readers: registry=%+v err=%v", registry, err)
	}
	reader, ok := registry.Readers["web"].(v3knowledgesources.PublicWebObservationReader)
	if !ok || reader.Fetcher.Resolver != nil || reader.Fetcher.DialContext != nil || reader.Fetcher.RootCAs != nil {
		t.Fatalf("opt-in must use only the default public Web reader: %+v", registry.Readers)
	}
	if _, probes := registry.Readers["web"].(v3sourceregistry.ConnectionTester); !probes {
		t.Fatal("opt-in public Web reader must support a bounded read-only connection probe")
	}
	for _, raw := range []string{"TRUE", " true", "true ", "1", "enabled", " false "} {
		registry, err := configuredV3SourceRegistry(raw, st, policy)
		if registry != nil || err == nil || !strings.Contains(err.Error(), "AIOPS_V3_PUBLIC_WEB_READER_ENABLED") {
			t.Fatalf("invalid flag %q must fail closed: registry=%+v err=%v", raw, registry, err)
		}
	}
}

func TestInvalidPublicWebReaderFlagFailsBeforeV3Configuration(t *testing.T) {
	t.Setenv("AIOPS_V3_PUBLIC_WEB_READER_ENABLED", "yes")
	err := configureV3(context.Background(), &store.Store{}, nil, nil, nil, &v3Instance{}, httpapi.V3InstanceConfig{}, operations.Config{})
	if err == nil || !strings.Contains(err.Error(), "AIOPS_V3_PUBLIC_WEB_READER_ENABLED") {
		t.Fatalf("invalid flag reached V3 configuration side effects: %v", err)
	}
}

func TestInvalidExternalSourceBindingsFailBeforeV3Configuration(t *testing.T) {
	t.Setenv("AIOPS_V3_EXTERNAL_SOURCE_BINDINGS", `[{"type":"notion","credential_ref":"plaintext"}]`)
	err := configureV3(context.Background(), &store.Store{}, nil, nil, nil, &v3Instance{}, httpapi.V3InstanceConfig{}, operations.Config{})
	if err == nil || !strings.Contains(err.Error(), "configure V3 Confluence/Notion bindings") {
		t.Fatalf("invalid source binding reached V3 configuration side effects: %v", err)
	}
}

func TestConfiguredV3SourceRegistryInstallsOnlyBoundGitMarkdownReader(t *testing.T) {
	st := &store.Store{}
	policy := &rbac.V3Policy{Store: st}
	binding := v3knowledgesources.GitMarkdownBinding{
		SourceURL: "https://gitlab.example.invalid/team/runbooks", Endpoint: "https://gitlab.example.invalid",
		ProjectID: 7, Branch: "main", PathPrefix: "docs", CredentialRef: "vault://ops/gitlab#token",
		Tier: "nonprod", EnvironmentID: "dev", ApplicationID: "app", ConnectorID: "runbooks",
	}
	registry, err := configuredV3SourceRegistryWithGit("", st, policy, []v3knowledgesources.GitMarkdownBinding{binding})
	if err != nil || registry == nil || len(registry.Readers) != 2 {
		t.Fatalf("configured Git reader missing: registry=%+v err=%v", registry, err)
	}
	if _, ok := registry.Readers["git_markdown"].(*v3knowledgesources.GitMarkdownReader); !ok {
		t.Fatalf("unexpected configured Git reader: %T", registry.Readers["git_markdown"])
	}
	if _, ok := registry.Readers["web"]; ok {
		t.Fatal("Git binding unexpectedly enabled the public Web reader")
	}
	binding.CredentialRef = "vault:ops/gitlab"
	if registry, err := configuredV3SourceRegistryWithGit("", st, policy, []v3knowledgesources.GitMarkdownBinding{binding}); registry != nil || err == nil {
		t.Fatalf("legacy credential reference enabled Git reader: registry=%+v err=%v", registry, err)
	}
}

func TestConfiguredV3SourceRegistryInstallsExactExternalReaders(t *testing.T) {
	st := &store.Store{}
	policy := &rbac.V3Policy{Store: st}
	bindings := []v3knowledgesources.ExternalBinding{
		{Type: "confluence", SourceURL: "https://docs.example.invalid/wiki",
			Endpoint: "https://api.atlassian.com/ex/confluence/11111111-1111-4111-8111-111111111111/wiki/api/v2",
			SpaceID:  "42", CredentialRef: "vault://ops/confluence#token", Tier: "nonprod",
			EnvironmentID: "dev", ApplicationID: "app", ConnectorID: "confluence-runbooks"},
		{Type: "notion", SourceURL: "https://www.notion.so/aaaaaaaaaaaa4aaa8aaaaaaaaaaaaaaa",
			Endpoint: "https://api.notion.com/v1", PageID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			CredentialRef: "vault://ops/notion#token", Tier: "nonprod",
			EnvironmentID: "dev", ApplicationID: "app", ConnectorID: "notion-runbooks"},
	}
	registry, err := configuredV3SourceRegistryWithSources("", st, policy, nil, bindings)
	if err != nil || registry == nil || len(registry.Readers) != 3 {
		t.Fatalf("exact external readers missing: registry=%+v err=%v", registry, err)
	}
	for _, kind := range []string{"confluence", "notion"} {
		if _, ok := registry.Readers[kind].(*v3knowledgesources.ExternalReader); !ok {
			t.Fatalf("%s installed wrong reader: %T", kind, registry.Readers[kind])
		}
	}
	bindings[0].CredentialRef = "vault:ops/confluence"
	if registry, err := configuredV3SourceRegistryWithSources("", st, policy, nil, bindings); registry != nil || err == nil {
		t.Fatalf("legacy Confluence reference enabled reader: registry=%+v err=%v", registry, err)
	}
}

func TestConfiguredV3SourceRegistryInstallsExactFeishuAndLocalUploadReaders(t *testing.T) {
	st := &store.Store{}
	policy := &rbac.V3Policy{Store: st}
	binding := v3knowledgesources.FeishuBinding{
		SourceURL: "https://tenant.feishu.cn/docx/AbCdEf1234567890",
		Endpoint:  "https://open.feishu.cn", DocumentID: "AbCdEf1234567890",
		CredentialRef: "vault://ops/feishu#token", Tier: "nonprod",
		EnvironmentID: "dev", ApplicationID: "app", ConnectorID: "feishu-runbooks",
	}
	registry, err := configuredV3SourceRegistryWithAll("", st, policy, nil, nil,
		[]v3knowledgesources.FeishuBinding{binding})
	if err != nil || registry == nil || len(registry.Readers) != 2 {
		t.Fatalf("configured Feishu and upload readers missing: registry=%+v err=%v", registry, err)
	}
	if _, ok := registry.Readers["feishu"].(*v3knowledgesources.FeishuReader); !ok {
		t.Fatalf("unexpected Feishu reader: %T", registry.Readers["feishu"])
	}
	if _, ok := registry.Readers["upload"].(*v3knowledgesources.UploadReader); !ok {
		t.Fatalf("unexpected upload reader: %T", registry.Readers["upload"])
	}
	binding.Endpoint = "https://untrusted.example.invalid"
	if registry, err := configuredV3SourceRegistryWithAll("", st, policy, nil, nil,
		[]v3knowledgesources.FeishuBinding{binding}); registry != nil || err == nil {
		t.Fatalf("untrusted Feishu endpoint enabled a token reader: registry=%+v err=%v", registry, err)
	}
}
