package v3alertmanager

import (
	"context"
	"errors"
	"testing"

	"github.com/destinywans/redstring/internal/core"
)

func TestSilenceProposalBindingFingerprint(t *testing.T) {
	base := Endpoint{URL: "https://am.example.test/prefix/", BearerTokenFile: "/private/am-token"}
	want, err := silenceProposalBindingFingerprint(base, "prod", "environment", "api", "app-1")
	if err != nil || len(want) != 64 {
		t.Fatalf("fingerprint=%q error=%v", want, err)
	}
	again, err := silenceProposalBindingFingerprint(base, "prod", "environment", "api", "app-1")
	if err != nil || again != want {
		t.Fatalf("fingerprint not deterministic: %q %v", again, err)
	}
	normalized := base
	normalized.URL = "https://am.example.test/prefix"
	got, err := silenceProposalBindingFingerprint(normalized, "prod", "environment", "api", "app-1")
	if err != nil || got != want {
		t.Fatalf("trailing slash should match request normalization: %q %v", got, err)
	}
	changes := []struct {
		name                             string
		ep                               Endpoint
		envID, envLabel, appLabel, appID string
	}{
		{"endpoint", Endpoint{URL: "https://other.example.test/prefix/", BearerTokenFile: base.BearerTokenFile}, "prod", "environment", "api", "app-1"},
		{"token path", Endpoint{URL: base.URL, BearerTokenFile: "/private/other-token"}, "prod", "environment", "api", "app-1"},
		{"allow http", Endpoint{URL: base.URL, BearerTokenFile: base.BearerTokenFile, AllowHTTP: true}, "prod", "environment", "api", "app-1"},
		{"environment", base, "stage", "environment", "api", "app-1"},
		{"environment label", base, "prod", "cluster", "api", "app-1"},
		{"application label", base, "prod", "environment", "worker", "app-1"},
		{"application id", base, "prod", "environment", "api", "app-2"},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			got, err := silenceProposalBindingFingerprint(tc.ep, tc.envID, tc.envLabel, tc.appLabel, tc.appID)
			if err != nil || got == want {
				t.Fatalf("configuration change not reflected: %q %v", got, err)
			}
		})
	}
}

func TestSilenceProposalBindingFingerprintRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name                             string
		ep                               Endpoint
		envID, envLabel, appLabel, appID string
	}{
		{"missing app", Endpoint{URL: "https://am.example.test"}, "prod", "environment", "", ""},
		{"missing endpoint", Endpoint{}, "prod", "environment", "api", "app-1"},
		{"relative token", Endpoint{URL: "https://am.example.test", BearerTokenFile: "token"}, "prod", "environment", "api", "app-1"},
		{"insecure endpoint", Endpoint{URL: "http://am.example.test"}, "prod", "environment", "api", "app-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := silenceProposalBindingFingerprint(tc.ep, tc.envID, tc.envLabel, tc.appLabel, tc.appID)
			if !errors.Is(err, ErrSilenceCreateInvalid) {
				t.Fatalf("error=%v, want invalid", err)
			}
		})
	}
}

func TestCanonicalSilenceMatchersRequiresExactApplicationForProposal(t *testing.T) {
	for _, input := range [][]Matcher{
		{{Name: "severity", Value: "critical", IsEqual: true}},
		{{Name: "application", Value: "api.*", IsEqual: true, IsRegex: true}},
		{{Name: "application", Value: "api", IsEqual: false}},
	} {
		_, app, err := canonicalSilenceMatchers(input, "environment", "prod")
		if err == nil && app != "" {
			t.Fatalf("unexpected application scope %q from %#v", app, input)
		}
	}
	matchers, app, err := canonicalSilenceMatchers([]Matcher{
		{Name: "severity", Value: "critical", IsEqual: true},
		{Name: "application", Value: "api", IsEqual: true},
	}, "environment", "prod")
	if err != nil || app != "api" || len(matchers) != 3 || matchers[0].Name != "application" || matchers[1].Name != "environment" {
		t.Fatalf("canonical matchers=%#v application=%q error=%v", matchers, app, err)
	}
}

func TestRevalidateCreateProposalRejectsWrongActorOrSchemaBeforeLookup(t *testing.T) {
	l := &DatabaseSilenceLedger{}
	for _, action := range []ProposedSilenceCreate{
		{ActorID: "other", SchemaVersion: ProposedSilenceCreateVersion, Kind: ProposedSilenceCreateKind, DurationSeconds: 1800},
		{ActorID: "actor", SchemaVersion: 99, Kind: ProposedSilenceCreateKind, DurationSeconds: 1800},
		{ActorID: "actor", SchemaVersion: ProposedSilenceCreateVersion, Kind: "other", DurationSeconds: 1800},
	} {
		if err := l.RevalidateCreateProposalWithTx(context.Background(), nil, core.User{ID: "actor"}, action, nil); !errors.Is(err, ErrSilenceCreateInvalid) {
			t.Fatalf("action=%#v error=%v", action, err)
		}
	}
}
