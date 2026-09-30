package v3sourceregistry

import (
	"strings"
	"testing"
)

func TestConnectionValidationRejectsCredentialsAndUnprovenSchemes(t *testing.T) {
	for _, tc := range []struct{ name, kind, url, ref string }{
		{"plaintext", "git", "https://git.example.test/repo", "password"},
		{"url credentials", "git", "https://user:pass@git.example.test/repo", "vault:kv/readonly"},
		{"query credential", "git", "https://git.example.test/repo?token=x", "vault:kv/readonly"},
		{"insecure URL", "web", "http://example.test", "vault:kv/readonly"},
		{"vault traversal", "web", "https://example.test", "vault:kv/../secret"},
		{"unknown source", "custom", "https://example.test", "vault:kv/readonly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateConnection(tc.kind, tc.url, tc.ref); err == nil {
				t.Fatal("accepted invalid source connection")
			}
		})
	}
	if err := validateConnection("confluence", "https://wiki.example.test/wiki", "vault://kv/readonly#token"); err != nil {
		t.Fatalf("rejected valid registration reference: %v", err)
	}
	if err := validateConnection("web", "https://docs.example.test/guide.md", ""); err != nil {
		t.Fatalf("rejected public Web source without a credential: %v", err)
	}
	if err := validateConnection("git", "https://git.example.test/repo", ""); err == nil {
		t.Fatal("Git registration accepted without a Vault reference")
	}
	for _, address := range []string{
		"https://docs.example.test/guide.md?", "https://docs.example.test/guide.md#",
		"https://docs.example.test:8443/guide.md", "https://docs.example.test./guide.md",
		"https://docs.example.test/" + strings.Repeat("a", MaxPublicWebURLBytes),
	} {
		if err := validateConnection("web", address, ""); err == nil {
			t.Errorf("accepted a public Web URL the reader cannot use: %q", address)
		}
	}
}

func TestRegistrationRequiresExplicitBoundedConfig(t *testing.T) {
	yes := true
	v := request{Name: "SRE wiki", Type: "confluence", URL: "https://wiki.example.test/wiki",
		SecretRef: "vault://kv/readonly#token", Spaces: []string{"12345"}, Schedule: "manual", Chunk: "heading", InheritACL: &yes,
		ApplicationID: "app"}
	if err := validateCreate(v); err != nil {
		t.Fatalf("valid registration: %v", err)
	}
	v.Spaces = []string{"12345", "12345"}
	if err := validateCreate(v); err == nil {
		t.Fatal("duplicate spaces accepted")
	}
	v.Spaces = []string{"12345"}
	v.InheritACL = nil
	if err := validateCreate(v); err == nil {
		t.Fatal("missing ACL intent accepted")
	}
	v.InheritACL = &yes
	v.ApplicationID = ""
	v.TeamID = "team-1"
	if err := validateCreate(v); err == nil {
		t.Fatal("team without application accepted")
	}
	v = request{Name: "Public guide", Type: "web", URL: "https://docs.example.test/guide.md",
		Schedule: "manual", Chunk: "fixed", InheritACL: &yes}
	if err := validateCreate(v); err != nil {
		t.Fatalf("rejected narrow public Web registration: %v", err)
	}
	v.Spaces = []string{"SRE"}
	if err := validateCreate(v); err == nil {
		t.Fatal("public single-page Web source accepted a space")
	}
	v.Spaces = nil
	v.Schedule = "6h"
	if err := validateCreate(v); err == nil {
		t.Fatal("public Web source claimed an unconfigured scheduler")
	}
	v.Schedule = "manual"
	no := false
	v.InheritACL = &no
	if err := validateCreate(v); err == nil {
		t.Fatal("public Web source without its verified public ACL accepted")
	}
	v = request{Name: "Notion page", Type: "notion", URL: "https://www.notion.so/notes-0123456789abcdef0123456789abcdef",
		SecretRef: "vault://kv/notion#token", Schedule: "manual", Chunk: "fixed", InheritACL: &yes,
		ApplicationID: "app"}
	if err := validateCreate(v); err != nil {
		t.Fatalf("valid Notion page: %v", err)
	}
	v.Spaces = []string{"123"}
	if err := validateCreate(v); err == nil {
		t.Fatal("Notion page accepted extra space scope")
	}
}
