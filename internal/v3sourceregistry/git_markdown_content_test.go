package v3sourceregistry

import (
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/store"
)

func TestGitMarkdownContentRequiresPinnedCompleteScopedBodies(t *testing.T) {
	source := registration{ID: "source", EnvironmentID: "dev", ApplicationID: "app",
		Type: "git_markdown", Schedule: "manual", InheritACL: true}
	body := "# Recovery\n\nCheck the deployment.\n"
	result := GitMarkdownReadResult{CommitSHA: strings.Repeat("a", 40), Complete: true,
		Files: []GitMarkdownFile{{Path: "runbooks/recovery.md", Body: body,
			Observation: Observation{ExternalID: "runbooks/recovery.md", ContentHash: store.Hash(body),
				EnvironmentID: "dev", ApplicationID: "app", ACLVerified: true}}}}
	if !validGitMarkdownContent(source, result) {
		t.Fatal("valid pinned scoped Markdown rejected")
	}
	for _, variant := range []struct {
		name   string
		mutate func(*GitMarkdownReadResult)
	}{
		{"unverified ACL", func(v *GitMarkdownReadResult) { v.Files[0].Observation.ACLVerified = false }},
		{"body mismatch", func(v *GitMarkdownReadResult) { v.Files[0].Body += "changed" }},
		{"partial tree", func(v *GitMarkdownReadResult) { v.Complete = false }},
		{"unfixed ref", func(v *GitMarkdownReadResult) { v.CommitSHA = "main" }},
		{"traversal", func(v *GitMarkdownReadResult) {
			v.Files[0].Path = "../secret.md"
			v.Files[0].Observation.ExternalID = "../secret.md"
		}},
		{"duplicate path", func(v *GitMarkdownReadResult) { v.Files = append(v.Files, v.Files[0]) }},
		{"oversized body", func(v *GitMarkdownReadResult) { v.Files[0].Body = strings.Repeat("x", maxGitMarkdownFileBytes+1) }},
	} {
		t.Run(variant.name, func(t *testing.T) {
			copy := result
			copy.Files = append([]GitMarkdownFile(nil), result.Files...)
			variant.mutate(&copy)
			if validGitMarkdownContent(source, copy) {
				t.Fatal("invalid Git Markdown evidence accepted")
			}
		})
	}
}

func TestGitMarkdownConnectionRequiresResolvableVaultReference(t *testing.T) {
	url := "https://gitlab.example.invalid/group/runbooks"
	if err := validateConnection("git_markdown", url, "vault://ops/gitlab#token"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"vault:ops/gitlab", "vault://ops/../gitlab#token", "vault://ops//gitlab#token", "plain"} {
		if err := validateConnection("git_markdown", url, ref); err == nil {
			t.Fatalf("invalid credential reference accepted: %q", ref)
		}
	}
}
