package v3sourceregistry

import "testing"

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
	if err := validateConnection("confluence", "https://wiki.example.test", "vault:kv/readonly"); err != nil {
		t.Fatalf("rejected valid registration reference: %v", err)
	}
}

func TestRegistrationRequiresExplicitBoundedConfig(t *testing.T) {
	yes := true
	v := request{Name: "SRE wiki", Type: "confluence", URL: "https://wiki.example.test",
		SecretRef: "vault:kv/readonly", Spaces: []string{"SRE"}, Schedule: "manual", Chunk: "heading", InheritACL: &yes}
	if err := validateCreate(v); err != nil {
		t.Fatalf("valid registration: %v", err)
	}
	v.Spaces = []string{"SRE", "SRE"}
	if err := validateCreate(v); err == nil {
		t.Fatal("duplicate spaces accepted")
	}
	v.Spaces = []string{"SRE"}
	v.InheritACL = nil
	if err := validateCreate(v); err == nil {
		t.Fatal("missing ACL intent accepted")
	}
	v.InheritACL = &yes
	v.TeamID = "team-1"
	if err := validateCreate(v); err == nil {
		t.Fatal("team without application accepted")
	}
}
