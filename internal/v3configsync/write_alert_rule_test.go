package v3configsync

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3rulemodel"
)

type alertRuleSourceStub struct {
	rules []v3rulemodel.ObservedRule
	err   error
}

func (s alertRuleSourceStub) List(_ context.Context, _ string) ([]v3rulemodel.ObservedRule, error) {
	return s.rules, s.err
}

func alertFixture() (v3rulemodel.ObservedRule, string, v3rulemodel.RuleGroupBinding) {
	bind := v3rulemodel.RuleGroupBinding{EnvironmentID: "test", ApplicationID: "app-a", File: "/rules/app.yaml", Group: "app"}
	rule := v3rulemodel.ObservedRule{GroupIdentity: bind.File + "\x00" + bind.Group, Group: bind.Group,
		Name: "HighErrors", Expression: "sum(rate(http_errors_total[5m])) > 10", Duration: 60,
		Labels:      map[string]string{"environment": "test", "application": "app-a", "severity": "critical"},
		Annotations: map[string]string{}}
	id := sha256.Sum256([]byte("test\x00app-a\x00" + rule.GroupIdentity + "\x00" + rule.Name))
	return rule, "prom-" + hex.EncodeToString(id[:12]), bind
}

func TestAlertRuleResolverCapturesExactBoundDefinition(t *testing.T) {
	rule, id, bind := alertFixture()
	resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{rule}}, []v3rulemodel.RuleGroupBinding{bind})
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
	second, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{changed}}, []v3rulemodel.RuleGroupBinding{bind})
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
	// A rule that cannot be promoted resolves to its application with the
	// reason, which the caller reports after authorization (PA-97).
	cases := []struct {
		name string
		rule v3rulemodel.ObservedRule
		want error
	}{
		{"wrong application label", func() v3rulemodel.ObservedRule {
			v := rule
			v.Labels = map[string]string{"environment": "test", "application": "other"}
			return v
		}(), store.ErrNotFound},
		{"unknown label", func() v3rulemodel.ObservedRule {
			v := rule
			v.Labels = map[string]string{"environment": "test", "application": "app-a", "secret": "x"}
			return v
		}(), errAlertRuleLabels},
		{"unknown severity", func() v3rulemodel.ObservedRule {
			v := rule
			v.Labels = map[string]string{"environment": "test", "application": "app-a", "severity": "P9"}
			return v
		}(), errAlertRuleLabels},
		{"credential matcher", func() v3rulemodel.ObservedRule {
			v := rule
			v.Expression = `http_requests_total{token="private"} > 0`
			return v
		}(), errAlertRuleExpression},
		{"fractional duration", func() v3rulemodel.ObservedRule { v := rule; v.Duration = 1.5; return v }(), errAlertRuleShape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{tc.rule}}, []v3rulemodel.RuleGroupBinding{bind})
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := resolver.Resolve(t.Context(), "test", id)
			if tc.want == store.ErrNotFound {
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
				return
			}
			if err != nil || candidate.ApplicationID != "app-a" || !errors.Is(candidate.Refused, tc.want) {
				t.Fatalf("candidate = %+v, %v; want refused %v", candidate, err, tc.want)
			}
		})
	}
	resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{rule, rule}}, []v3rulemodel.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(t.Context(), "test", id); !errors.Is(err, errUntrusted) {
		t.Fatalf("duplicate provider identity = %v", err)
	}
	resolver, err = NewAlertRuleResolver(alertRuleSourceStub{err: errors.New("down")}, []v3rulemodel.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(t.Context(), "test", id); !errors.Is(err, errUnavailable) {
		t.Fatalf("provider failure = %v", err)
	}
}

func TestNewAlertRuleResolverRejectsDuplicateOrUntrustedBindings(t *testing.T) {
	_, _, bind := alertFixture()
	for _, bindings := range [][]v3rulemodel.RuleGroupBinding{
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
	snapshot, err := alertSnapshot(rule, "test", "app-a", "app-a", identity)
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
	// An import cannot know the source's D32 label: any object key passes,
	// a different one does not when the label is known.
	mutated = snapshot
	mutated.Labels = map[string]string{"environment": "test", "application": "checkout-api", "severity": "critical"}
	if !sanitizedAlertSnapshot("test", "", id, resign(mutated)) || sanitizedAlertSnapshot("test", "app-a", id, resign(mutated)) {
		t.Fatal("import label check")
	}
}

func resign(snapshot readAlertSnapshot) readAlertSnapshot {
	snapshot.Version = ""
	raw, _ := json.Marshal(snapshot)
	version := sha256.Sum256(raw)
	snapshot.Version = "sha256:" + hex.EncodeToString(version[:])
	return snapshot
}

// PA-97: rules the product writes are promoted. Their "application" label is
// the D32 key, their prom- ID is the rule list's, their severity is P1-P4,
// their summary annotation is left out, and their expression matches the
// application label in quotes.
func TestAlertRuleResolverPromotesProductRules(t *testing.T) {
	bind := v3rulemodel.RuleGroupBinding{EnvironmentID: "test", ApplicationID: "0a5cb011", File: "rules/test/checkout-api.yml",
		Group: "redstring-test-checkout-api", ApplicationLabel: "checkout-api"}
	rule := v3rulemodel.ObservedRule{GroupIdentity: bind.File + "\x00" + bind.Group, Group: bind.Group,
		Name: "RedstringAcceptance", Expression: `sum(rate(http_requests_total{application="checkout-api",code=~"5.."}[5m])) > 1`,
		Duration: 60, Labels: map[string]string{"environment": "test", "application": "checkout-api", "severity": "P3"},
		Annotations: map[string]string{"summary": "checkout-api 5xx", "description": "placeholder"}}
	identity := sha256.Sum256([]byte("test\x00" + bind.ApplicationID + "\x00" + rule.GroupIdentity + "\x00" + rule.Name))
	id := "prom-" + hex.EncodeToString(identity[:12])
	resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{rule}}, []v3rulemodel.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := resolver.Resolve(t.Context(), "test", id)
	if err != nil || candidate.Refused != nil || candidate.ApplicationID != bind.ApplicationID ||
		candidate.Snapshot.Labels["application"] != "checkout-api" || candidate.Snapshot.Labels["severity"] != "P3" ||
		len(candidate.Snapshot.Annotations) != 0 || candidate.Snapshot.Expression != rule.Expression ||
		!sanitizedAlertSnapshot("test", "", id, candidate.Snapshot) {
		t.Fatalf("product rule: %+v %v", candidate, err)
	}
	// The application ID as label is not this binding's rule any more.
	rule.Labels = map[string]string{"environment": "test", "application": bind.ApplicationID, "severity": "P3"}
	resolver, _ = NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{rule}}, []v3rulemodel.RuleGroupBinding{bind})
	if _, err = resolver.Resolve(t.Context(), "test", id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("application ID label: %v", err)
	}
}

func TestSafeAlertExpression(t *testing.T) {
	for expr, want := range map[string]bool{
		`up == 0`: true,
		// The rule alert_rule_loop wrote on nonprod.
		`sum by (cluster) (rate(lab_http_requests_total{application="checkout-api"}[2m])) > 0`:              true,
		`sum(rate(http_requests_total{application="checkout-api",environment="test"}[5m])) by (code) > 0.5`: true,
		`up{job!="node-exporter", instance=~"10.0.0.1:9100"} == 0`:                                          true,
		`up{application='checkout-api'} == 0`:                                                               true,
		`up{application=""} == 0`:                                                                           true,
		`up{token="private"} > 0`:                                                                           false,
		`up{Authorization="x"} > 0`:                                                                         false,
		`up{job=~"a|b"} == 0`:                                                                               false,
		`up{job="has space"} == 0`:                                                                          false,
		`up{job="x\"y"} == 0`:                                                                               false,
		`label_replace(up, "dst", "$1", "src", "(.*)")`:                                                     false,
		`up{job="x"}{y="z"}`:                                                                                true,
		`up{job={x="y"}}`:                                                                                   false,
		`up{job="unterminated} == 0`:                                                                        false,
		`up > 0 # "comment"`:                                                                                false,
	} {
		if got := safeAlertExpression(expr); got != want {
			t.Errorf("%s: %v, want %v", expr, got, want)
		}
	}
}

// PA-97: why a rule cannot be promoted is said only to a caller who may
// manage the rule's application; anyone else gets the authorization error.
func TestAlertRuleRefusalAfterAuthorization(t *testing.T) {
	s := isolatedConfigStore(t)
	if _, err := s.DB.Exec(t.Context(), `INSERT INTO v3_application_environments(application_id,environment_id)
 VALUES('app-a','test') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	rule, id, bind := alertFixture()
	rule.Expression = `up{token="private"} == 0`
	resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{rule}}, []v3rulemodel.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	access := &agentPromotionAccess{deny: true}
	service := &Service{DB: s.DB, Access: access, AlertRules: resolver, SourceInstanceID: "aiops-test", SigningKey: testSigningKey()}
	if _, _, err = service.reserveKindExport(t.Context(), core.User{ID: "owner"}, "alert_rule", "test", id); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("unauthorized caller: %v", err)
	}
	access.deny = false
	if _, _, err = service.reserveKindExport(t.Context(), core.User{ID: "owner"}, "alert_rule", "test", id); !errors.Is(err, errAlertRuleExpression) {
		t.Fatalf("authorized caller: %v", err)
	}
	var reserved int
	if err = s.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_config_exports`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("a refused rule reached the ledger: %d %v", reserved, err)
	}
}

// PA-100: a kind export carries the application key in a schema 3 manifest;
// an earlier format-1 record of the same source is history, not a replay,
// and an application without a key cannot be exported.
func TestKindExportCarriesApplicationKey(t *testing.T) {
	s := isolatedConfigStore(t)
	ctx := t.Context()
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_application_environments(application_id,environment_id)
 VALUES('app-a','test') ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	rule, id, bind := alertFixture()
	resolver, err := NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{rule}}, []v3rulemodel.RuleGroupBinding{bind})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{DB: s.DB, Access: &agentPromotionAccess{}, AlertRules: resolver, Repository: &fakeRepository{},
		SourceInstanceID: "aiops-test", SigningKey: testSigningKey()}
	first, _, err := service.reserveKindExport(ctx, core.User{ID: "owner"}, "alert_rule", "test", id)
	if err != nil {
		t.Fatal(err)
	}
	var m readManifestV2
	if err = json.Unmarshal(first.Payload, &m); err != nil || m.Schema != readManifestKeyed || m.ApplicationKey != "checkout-api" ||
		m.ApplicationID != "app-a" {
		t.Fatalf("manifest: %+v %v", m, err)
	}
	if _, err = m.verify(first.ID, map[string]ed25519.PublicKey{"aiops-test": testSigningKey().Public().(ed25519.PublicKey)}); err != nil {
		t.Fatalf("keyed manifest does not verify: %v", err)
	}
	// The same source exported before PA-100.
	if _, err = s.DB.Exec(ctx, `UPDATE v3_config_exports SET export_format=1,state='exported',mr_iid=3,mr_url='https://gitlab.example/mr/3',lease_until=NULL
 WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	again, _, err := service.reserveKindExport(ctx, core.User{ID: "owner"}, "alert_rule", "test", id)
	if err != nil || again.ID == first.ID || again.State == "exported" {
		t.Fatalf("re-export replayed the old record: %+v %v", again, err)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE applications SET key='' WHERE id='app-a'`); err != nil {
		t.Fatal(err)
	}
	other := rule
	other.Expression = "sum(rate(http_errors_total[5m])) > 20"
	service.AlertRules, _ = NewAlertRuleResolver(alertRuleSourceStub{rules: []v3rulemodel.ObservedRule{other}}, []v3rulemodel.RuleGroupBinding{bind})
	if _, _, err = service.reserveKindExport(ctx, core.User{ID: "owner"}, "alert_rule", "test", id); !errors.Is(err, errApplicationKeyMissing) {
		t.Fatalf("application without a key: %v", err)
	}
}
