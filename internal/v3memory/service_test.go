package v3memory

import (
	"strings"
	"testing"
)

func TestCandidateRequiresBoundedExactSourceSelection(t *testing.T) {
	base := CandidateInput{DocumentID: "doc-1", EnvironmentID: "dev", ExactExcerpt: "The service recovered after rollback."}
	if !validCandidate(base) {
		t.Fatal("valid exact excerpt rejected")
	}
	cases := []CandidateInput{
		{DocumentID: "", EnvironmentID: base.EnvironmentID, ExactExcerpt: base.ExactExcerpt},
		{DocumentID: base.DocumentID, EnvironmentID: "all", ExactExcerpt: base.ExactExcerpt},
		{DocumentID: base.DocumentID, EnvironmentID: base.EnvironmentID, ExactExcerpt: ""},
		{DocumentID: base.DocumentID, EnvironmentID: base.EnvironmentID, ExactExcerpt: " leading space"},
		{DocumentID: base.DocumentID, EnvironmentID: base.EnvironmentID, ExactExcerpt: strings.Repeat("x", 4001)},
	}
	for _, in := range cases {
		if validCandidate(in) {
			t.Fatalf("invalid candidate accepted: document=%q environment=%q excerpt length=%d", in.DocumentID, in.EnvironmentID, len(in.ExactExcerpt))
		}
	}
}

func TestFactTransitionsAreManualAndTerminal(t *testing.T) {
	for _, tc := range []struct {
		from, action, to string
		ok               bool
	}{
		{"candidate", "confirm", "confirmed", true},
		{"candidate", "reject", "rejected", true},
		{"candidate", "expire", "expired", true},
		{"confirmed", "expire", "expired", true},
		{"confirmed", "reject", "rejected", false},
		{"rejected", "confirm", "confirmed", false},
		{"expired", "confirm", "confirmed", false},
		{"candidate", "delete", "", false},
	} {
		got, ok := transition(tc.from, tc.action)
		if got != tc.to || ok != tc.ok {
			t.Fatalf("%s + %s: got %q, %t; want %q, %t", tc.from, tc.action, got, ok, tc.to, tc.ok)
		}
	}
}

func TestScopeMembershipRequiresExactEnvironment(t *testing.T) {
	if !has([]string{"dev", "stage"}, "dev") || has([]string{"dev", "stage"}, "prod") {
		t.Fatal("environment membership changed")
	}
}
