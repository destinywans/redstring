package v3catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestServiceEvidenceUsesOnlyBoundedProfile(t *testing.T) {
	row := serviceEvidenceRow{appID: "app-1", key: "checkout", name: "Checkout", description: "Order checkout", environment: "test", teamID: "team-1", team: "Orders", tier: "T1", owner: "SRE"}
	got := row.evidence()
	if got.ID != recordID("app-1", "test") || got.Key != "checkout" || got.Team != "Orders" || got.Tier != "T1" || got.Owner != "SRE" || got.Health != "unavailable" {
		t.Fatalf("unexpected service evidence: %#v", got)
	}
}

func TestServiceEvidenceAuthorizationUsesPersistedApplicationThenTeam(t *testing.T) {
	user := core.User{ID: "user-1"}
	row := serviceEvidenceRow{appID: "app-1", environment: "test", teamID: "team-1"}
	var decisions []rbac.V3Decision
	s := Service{Access: catalogAccess(func(d rbac.V3Decision) error {
		decisions = append(decisions, d)
		if d.ApplicationID != "" {
			return store.ErrForbidden
		}
		return nil
	})}
	if err := s.authorizeServiceEvidence(context.Background(), user, row); err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 || decisions[0].Module != "services" || decisions[0].Action != "view" || decisions[0].ApplicationID != "app-1" || decisions[0].EnvironmentID != "test" || decisions[1].TeamID != "team-1" || decisions[1].ApplicationID != "" {
		t.Fatalf("unsafe authorization target: %#v", decisions)
	}
	decisions = nil
	row.teamID = ""
	if err := s.authorizeServiceEvidence(context.Background(), user, row); !errors.Is(err, store.ErrNotFound) || len(decisions) != 1 {
		t.Fatalf("hidden service should be indistinguishable from absent: err=%v decisions=%#v", err, decisions)
	}
}

func TestServiceEvidenceAuthorizationFailsClosed(t *testing.T) {
	user := core.User{ID: "user-1"}
	row := serviceEvidenceRow{appID: "app-1", environment: "test", teamID: "team-1"}
	broken := errors.New("policy database unavailable")
	s := Service{Access: catalogAccess(func(rbac.V3Decision) error { return broken })}
	if err := s.authorizeServiceEvidence(context.Background(), user, row); !errors.Is(err, broken) {
		t.Fatalf("policy failure was hidden: %v", err)
	}
	user.Disabled = true
	if err := s.authorizeServiceEvidence(context.Background(), user, row); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabled user was accepted: %v", err)
	}
}
