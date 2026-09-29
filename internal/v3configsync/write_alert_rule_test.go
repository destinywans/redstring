package v3configsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertrules"
)

type alertRuleSourceStub struct {
	rules []v3alertrules.ObservedRule
	err   error
}

func (s alertRuleSourceStub) List(_ context.Context, _ string) ([]v3alertrules.ObservedRule, error) {
	return s.rules, s.err
}

func alertFixture() (v3alertrules.ObservedRule, string, v3alertrules.RuleGroupBinding) {
	bind := v3alertrules.RuleGroupBinding{EnvironmentID: "test", ApplicationID: "app-a", File: "/rules/app.yaml", Group: "app"}
	rule := v3alertrules.ObservedRule{GroupIdentity: bind.File + "\x00" + bind.Group, Group: bind.Group,
		Name: "HighErrors", Expression: "sum(rate(http_errors_total[5m])) > 10", Duration: 60,
		Labels:      map[string]string{"environment": "test", "application": "app-a", "severity": "critical"},
		Annotations: map[string]string{}}
	id := sha256.Sum256([]byte("test\x00app-a\x00" + rule.GroupIdentity + "\x00" + rule.Name))
	return rule, "prom-" + hex.EncodeToString(id[:12]), bind
}

func TestAlertRuleResolverCapturesExactBoundDefinition(t *testing.T) {
	rule, id, bind := alertFixture()
	resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3alertrules.ObservedRule{rule}}, []v3alertrules.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := resolver.Resolve(t.Context(), "test", id)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ApplicationID != "app-a" || candidate.Snapshot.Name != "HighErrors" ||
		candidate.Snapshot.Expression != rule.Expression || candidate.Snapshot.DurationSeconds != 60 ||
		!strings.HasPrefix(candidate.Snapshot.Version, "sha256:") || len(candidate.Snapshot.Version) != 71 ||
		candidate.Snapshot.SourceIdentityDigest == "" {
		t.Fatalf("unexpected captured definition: %+v", candidate)
	}
	changed := rule
	changed.Expression = "sum(rate(http_errors_total[5m])) > 20"
	second, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3alertrules.ObservedRule{changed}}, []v3alertrules.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	other, err := second.Resolve(t.Context(), "test", id)
	if err != nil || other.Snapshot.Version == candidate.Snapshot.Version {
		t.Fatalf("source configuration change must produce another version: %q, %v", other.Snapshot.Version, err)
	}
}

func TestAlertRuleResolverRejectsUnboundAmbiguousAndUnsanitizedSources(t *testing.T) {
	rule, id, bind := alertFixture()
	cases := []struct {
		name string
		rule v3alertrules.ObservedRule
		want error
	}{
		{"wrong application label", func() v3alertrules.ObservedRule {
			v := rule
			v.Labels = map[string]string{"environment": "test", "application": "other"}
			return v
		}(), store.ErrNotFound},
		{"unknown label", func() v3alertrules.ObservedRule {
			v := rule
			v.Labels = map[string]string{"environment": "test", "application": "app-a", "secret": "x"}
			return v
		}(), errInvalidManifest},
		{"quoted expression", func() v3alertrules.ObservedRule {
			v := rule
			v.Expression = `http_requests_total{token="private"} > 0`
			return v
		}(), errInvalidManifest},
		{"free text annotation", func() v3alertrules.ObservedRule {
			v := rule
			v.Annotations = map[string]string{"summary": "private"}
			return v
		}(), errInvalidManifest},
		{"fractional duration", func() v3alertrules.ObservedRule { v := rule; v.Duration = 1.5; return v }(), errInvalidManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3alertrules.ObservedRule{tc.rule}}, []v3alertrules.RuleGroupBinding{bind})
			if err != nil {
				t.Fatal(err)
			}
			_, err = resolver.Resolve(t.Context(), "test", id)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3alertrules.ObservedRule{rule, rule}}, []v3alertrules.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(t.Context(), "test", id); !errors.Is(err, errUntrusted) {
		t.Fatalf("duplicate provider identity = %v", err)
	}
	resolver, err = NewAlertRuleResolver(alertRuleSourceStub{err: errors.New("down")}, []v3alertrules.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(t.Context(), "test", id); !errors.Is(err, errUnavailable) {
		t.Fatalf("provider failure = %v", err)
	}
}

func TestNewAlertRuleResolverRejectsDuplicateOrUntrustedBindings(t *testing.T) {
	_, _, bind := alertFixture()
	for _, bindings := range [][]v3alertrules.RuleGroupBinding{
		{bind, bind},
		{{EnvironmentID: "all", ApplicationID: "app-a", File: bind.File, Group: bind.Group}},
		{{EnvironmentID: "test", ApplicationID: "app-a", File: bind.File, Group: ""}},
	} {
		if _, err := NewAlertRuleResolver(alertRuleSourceStub{}, bindings); !errors.Is(err, errInvalid) {
			t.Fatalf("binding %+v accepted: %v", bindings, err)
		}
	}
}

func TestSanitizedAlertSnapshotRecheckedForProduction(t *testing.T) {
	rule, id, _ := alertFixture()
	identity := sha256.Sum256([]byte("test\x00app-a\x00" + rule.GroupIdentity + "\x00" + rule.Name))
	snapshot, err := alertSnapshot(rule, "test", "app-a", identity)
	if err != nil || !sanitizedAlertSnapshot("test", "app-a", id, snapshot) {
		t.Fatalf("valid captured snapshot rejected: %v", err)
	}
	mutated := snapshot
	mutated.Expression = `up{token="private"}`
	if sanitizedAlertSnapshot("test", "app-a", id, mutated) {
		t.Fatal("quoted expression accepted")
	}
	mutated = snapshot
	mutated.Annotations = map[string]string{"summary": "private"}
	if sanitizedAlertSnapshot("test", "app-a", id, mutated) {
		t.Fatal("free text annotation accepted")
	}
	mutated = snapshot
	mutated.Version = "sha256:" + strings.Repeat("0", 64)
	if sanitizedAlertSnapshot("test", "app-a", id, mutated) {
		t.Fatal("unrelated version accepted")
	}
	if sanitizedAlertSnapshot("test", "app-a", "prom-"+strings.Repeat("0", 24), snapshot) {
		t.Fatal("unrelated source identity accepted")
	}
}
