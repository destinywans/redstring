package v3sourceregistry

import (
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/store"
)

func externalFixture(kind string) (registration, ExternalReadResult) {
	spaces, id, version := []string{"12345"}, "98765", "1"
	sourceURL := "https://wiki.example.test/wiki"
	if kind == "notion" {
		spaces, id, version = nil, "01234567-89ab-cdef-0123-456789abcdef", "2026-09-29T12:34:56Z"
		sourceURL = "https://www.notion.so/notes-0123456789abcdef0123456789abcdef"
	}
	source := registration{Type: kind, ApplicationID: "app", EnvironmentID: "dev",
		Schedule: "manual", InheritACL: true, Spaces: spaces,
		URL:                sourceURL,
		BindingFingerprint: strings.Repeat("a", 64), BindingSnapshot: []byte(`{"version":1}`)}
	body := "# Bounded page\n"
	result := ExternalReadResult{Complete: true, Documents: []ExternalDocument{{
		Observation: Observation{ExternalID: id, ContentHash: store.Hash(body),
			EnvironmentID: "dev", ApplicationID: "app", ACLVerified: true},
		Title: "Page", Body: body, SourceVersion: version,
	}}}
	return source, result
}

func TestExternalContentRequiresCompletePinnedScopedBodies(t *testing.T) {
	for _, kind := range []string{"confluence", "notion"} {
		t.Run(kind, func(t *testing.T) {
			source, result := externalFixture(kind)
			if !validExternalContent(source, result) {
				t.Fatal("valid bounded source rejected")
			}
			for _, mutate := range []struct {
				name string
				edit func(*registration, *ExternalReadResult)
			}{
				{"partial", func(_ *registration, r *ExternalReadResult) { r.Complete = false }},
				{"body hash", func(_ *registration, r *ExternalReadResult) { r.Documents[0].Body = "changed" }},
				{"access", func(_ *registration, r *ExternalReadResult) { r.Documents[0].Observation.ACLVerified = false }},
				{"team", func(_ *registration, r *ExternalReadResult) { r.Documents[0].Observation.TeamID = "other" }},
				{"unbound", func(s *registration, _ *ExternalReadResult) { s.BindingFingerprint = "" }},
				{"too large", func(_ *registration, r *ExternalReadResult) {
					r.Documents[0].Body = strings.Repeat("x", maxExternalBodyBytes+1)
				}},
				{"duplicate", func(_ *registration, r *ExternalReadResult) { r.Documents = append(r.Documents, r.Documents[0]) }},
				{"bad version", func(_ *registration, r *ExternalReadResult) { r.Documents[0].SourceVersion = "0" }},
			} {
				t.Run(mutate.name, func(t *testing.T) {
					copySource := source
					copyResult := ExternalReadResult{Complete: result.Complete,
						Documents: append([]ExternalDocument(nil), result.Documents...)}
					mutate.edit(&copySource, &copyResult)
					if validExternalContent(copySource, copyResult) {
						t.Fatal("invalid source content accepted")
					}
				})
			}
		})
	}
}

func TestExternalVersionCanonicalAndMonotonicInputs(t *testing.T) {
	for _, tc := range []struct {
		kind, value string
		ok          bool
	}{
		{"confluence", "1", true}, {"confluence", "01", false},
		{"confluence", "18446744073709551616", false},
		{"notion", "2026-09-29T12:34:56.123Z", true},
		{"notion", "2026-09-29T12:34:56.000Z", true},
		{"notion", "2026-09-29T12:34:56+00:00", true},
		{"notion", "2026-09-29", false},
	} {
		_, _, ok := externalVersionTime(tc.kind, tc.value)
		if ok != tc.ok {
			t.Errorf("%s %q: got %t want %t", tc.kind, tc.value, ok, tc.ok)
		}
	}
}
