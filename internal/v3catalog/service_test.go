package v3catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestValidateItemsRejectsAmbiguousOrInvalidRegistration(t *testing.T) {
	cases := []struct {
		name  string
		items []registerItem
	}{
		{"empty", nil},
		{"duplicate names", []registerItem{{ID: "api"}, {ID: "API"}}},
		{"invalid namespace", []registerItem{{ID: "api", Namespace: "prod/checkout"}}},
		{"resource without namespace", []registerItem{{ID: "api", ResourceID: "resource-1"}}},
		{"invalid resource identity", []registerItem{{ID: "api", Namespace: "test", ResourceID: "resource\n1"}}},
		{"invalid tier", []registerItem{{ID: "api", Tier: "critical"}}},
		{"invalid slo", []registerItem{{ID: "api", SLO: "101"}}},
		{"invalid p99", []registerItem{{ID: "api", P99: "0"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateItems(tc.items); err == nil {
				t.Fatal("invalid registration accepted")
			}
		})
	}
	got, err := validateItems([]registerItem{{ID: " checkout-api ", Namespace: " trade ", Tier: "T1", SLO: "99.9", P99: "300"}})
	if err != nil || got[0].ID != "checkout-api" || got[0].Namespace != "trade" {
		t.Fatalf("valid registration rejected: %#v, %v", got, err)
	}
}

func TestMappingKindUsesPersistedBindingFamilies(t *testing.T) {
	for provider, want := range map[string]string{
		"prometheus": "metrics", "victoriametrics": "metrics", "loki": "logs",
		"elasticsearch": "logs", "tempo": "traces", "kubernetes": "workload",
		"gitlab": "code", "argocd": "deploy", "grafana": "dash", "unknown": "",
	} {
		if got := mappingKind(provider); got != want {
			t.Errorf("%s: got %q, want %q", provider, got, want)
		}
	}
}

func TestCatalogProjectionDoesNotInventOperationalState(t *testing.T) {
	row := catalogRow{appID: "app-1", key: "checkout", name: "Checkout", env: "test", tier: "T1", target: "99.900", bindings: []byte(`[{"kind":"prometheus","resource":"job=checkout"}]`)}
	item, err := projectCatalog(row)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status.State != "unavailable" || item.Health.State != "unavailable" || item.SLO.Availability.State != "unavailable" || item.SLO.BudgetLeft.State != "unavailable" || item.Release.State != "unavailable" || item.Alerts.State != "unavailable" {
		t.Fatalf("invented operational state: %#v", item)
	}
	if item.SLO.TargetPercent.Value != "99.900" || item.SLO.TargetPercent.State != "configured" {
		t.Fatalf("lost configured target: %#v", item.SLO)
	}
	if len(item.Connectors) != 1 || item.Connectors[0].State != "configured" {
		t.Fatalf("binding was treated as live health: %#v", item.Connectors)
	}
	if item.Team.State != "unavailable" || item.Oncall.State != "unavailable" {
		t.Fatalf("missing ownership was invented: %#v", item)
	}
	if app, env, ok := parseRecordID(item.ID); !ok || app != row.appID || env != row.env {
		t.Fatalf("service ID lost environment scope: %q", item.ID)
	}
}

func TestCatalogRecordIDRejectsMalformedOrAmbiguousValues(t *testing.T) {
	for _, id := range []string{"", "bad*", "YXBw", recordID("a\x00b", "test"), recordID("app", "test") + "="} {
		if _, _, ok := parseRecordID(id); ok {
			t.Errorf("accepted malformed ID %q", id)
		}
	}
}

type catalogAccess func(rbac.V3Decision) error

func (f catalogAccess) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	return f(d)
}

func (f catalogAccess) AuthorizeWithTx(_ context.Context, _ pgx.Tx, _ core.User, d rbac.V3Decision) error {
	return f(d)
}

func TestCatalogAuthorizationUsesPersistedTeamOnlyAfterApplicationDenial(t *testing.T) {
	decisions := []rbac.V3Decision{}
	s := Service{Access: catalogAccess(func(d rbac.V3Decision) error {
		decisions = append(decisions, d)
		if d.ApplicationID != "" {
			return store.ErrForbidden
		}
		if d.TeamID == "team-1" && d.EnvironmentID == "test" {
			return nil
		}
		return errors.New("unexpected target")
	})}
	allowed, err := s.canView(context.Background(), core.User{}, catalogRow{appID: "app-1", env: "test", teamID: "team-1"})
	if err != nil || !allowed || len(decisions) != 2 || decisions[0].ApplicationID != "app-1" || decisions[1].TeamID != "team-1" {
		t.Fatalf("team-scoped access failed: allowed=%v err=%v decisions=%#v", allowed, err, decisions)
	}
	decisions = nil
	allowed, err = s.canView(context.Background(), core.User{}, catalogRow{appID: "app-2", env: "test"})
	if err != nil || allowed || len(decisions) != 1 {
		t.Fatalf("unowned application leaked: allowed=%v err=%v decisions=%#v", allowed, err, decisions)
	}
}

func TestExistingServiceLinkRequiresAuthorizationOnExistingIdentity(t *testing.T) {
	decisions := []rbac.V3Decision{}
	s := Service{InstanceEnvironmentIDs: []string{"staging", "production"}, Access: catalogAccess(func(d rbac.V3Decision) error {
		decisions = append(decisions, d)
		if d.EnvironmentID == "staging" && d.TeamID == "team-1" && d.Action == "change" {
			return nil
		}
		return store.ErrForbidden
	})}
	user := core.User{ID: "user-1"}
	if err := s.authorizeExistingGrants(context.Background(), nil, user, "app-1", "team-1", []string{"staging"}); err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 || decisions[0].ApplicationID != "app-1" || decisions[1].TeamID != "team-1" || decisions[1].EnvironmentID != "staging" {
		t.Fatalf("existing identity was not checked before link: %#v", decisions)
	}
	decisions = nil
	if err := s.authorizeExistingGrants(context.Background(), nil, user, "app-2", "", []string{"staging"}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("unrelated application was linkable: %v", err)
	}
	if len(decisions) != 1 || decisions[0].ApplicationID != "app-2" {
		t.Fatalf("unexpected scope for unrelated application: %#v", decisions)
	}
	decisions = nil
	if err := s.authorizeExistingGrants(context.Background(), nil, user, "legacy", "", nil); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("unmapped legacy application was linkable: %v", err)
	}
	if len(decisions) != 0 {
		t.Fatalf("unmapped identity triggered a grant check: %#v", decisions)
	}
}

func TestExistingServiceCannotUseOtherInstanceEnvironmentAsIdentityEvidence(t *testing.T) {
	checks := 0
	s := New(nil, catalogAccess(func(rbac.V3Decision) error {
		checks++
		return nil // even a global grant must not cross the configured boundary
	}), "staging", "test")
	user := core.User{ID: "user-1"}
	if err := s.authorizeExistingGrants(context.Background(), nil, user, "app-prod", "", []string{"prod"}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("prod-only identity was reusable in nonprod instance: %v", err)
	}
	if checks != 0 {
		t.Fatalf("outside-instance grant was consulted %d times", checks)
	}
	if err := s.authorizeExistingGrants(context.Background(), nil, user, "app-test", "", []string{"prod", "test"}); err != nil {
		t.Fatalf("in-instance identity was denied: %v", err)
	}
	if checks != 1 {
		t.Fatalf("expected exactly one in-instance grant check, got %d", checks)
	}
	if err := New(nil, s.Access).authorizeExistingGrants(context.Background(), nil, user, "app-test", "", []string{"test"}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("missing instance configuration did not fail closed: %v", err)
	}
}
