package rbac

import "testing"

func TestV3DecisionRequiresExplicitScopeAndKnownAction(t *testing.T) {
	valid := V3Decision{Module: "resources", Action: "view", EnvironmentID: "dev", ApplicationID: "app-1"}
	if !validV3Decision(valid) {
		t.Fatal("valid decision rejected")
	}
	for _, decision := range []V3Decision{
		{Module: "resources", Action: "view", EnvironmentID: ""},
		{Module: "resources", Action: "view", EnvironmentID: "all"},
		{Module: "resources", Action: "investigate", EnvironmentID: "dev"},
		{Module: "resources;drop", Action: "view", EnvironmentID: "dev"},
		{Module: "resources", Action: "view", EnvironmentID: "dev", ApplicationID: "app-1", TeamID: "team-1"},
	} {
		if validV3Decision(decision) {
			t.Fatalf("invalid decision accepted: %+v", decision)
		}
	}
}
