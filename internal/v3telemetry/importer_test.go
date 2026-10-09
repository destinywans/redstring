package v3telemetry

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

var update = flag.Bool("update", false, "rewrite golden files")

// The lab plan is reviewed as a golden file: deterministic IDs, the merged
// catalog and every deferred or conflicting binding with its reason.
func TestBuildPlanForTheLab(t *testing.T) {
	plan := BuildPlan(labInput(), labState())
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	golden := filepath.Join("testdata", "lab_plan.golden.json")
	if *update {
		if err := os.WriteFile(golden, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) {
		t.Fatalf("plan differs from %s; review and rerun with -update:\n%s", golden, raw)
	}
	// The same inputs always give the same digest, in any input order.
	state := labState()
	for i, j := 0, len(state.Bindings)-1; i < j; i, j = i+1, j-1 {
		state.Bindings[i], state.Bindings[j] = state.Bindings[j], state.Bindings[i]
	}
	if again := BuildPlan(labInput(), state); again.Digest != plan.Digest {
		t.Fatal("digest depends on binding order")
	}
	// The generated catalog is a valid catalog file.
	catalogRaw, _ := json.Marshal(plan.Catalog)
	if _, err := ParseCatalog(catalogRaw); err != nil {
		t.Fatalf("generated catalog: %v", err)
	}
}

func TestBuildPlanIsIdempotentOnceApplied(t *testing.T) {
	input, state := labInput(), labState()
	plan := BuildPlan(input, state)
	// Simulate apply plus installing the generated catalog.
	for _, c := range plan.Connectors {
		if c.Action == actionCreate {
			state.Connectors = append(state.Connectors, ExistingConnector{ID: c.ID, EnvironmentID: c.EnvironmentID,
				Provider: c.Provider, Endpoint: c.Endpoint, AuthMethod: c.AuthMethod, TenantID: c.TenantID, OptionsValid: true})
		}
	}
	for _, target := range plan.Targets {
		if target.Action == actionCreate {
			state.Targets = append(state.Targets, ExistingTarget{ID: target.ID, ApplicationID: target.ApplicationID,
				EnvironmentID: target.EnvironmentID, ConnectorID: target.ConnectorID, Provider: providerOfKind(target.TargetKind),
				TargetKind: target.TargetKind, ExternalID: target.ExternalID})
		}
	}
	raw, _ := json.Marshal(plan.Catalog)
	if input.Catalog, _ = ParseCatalog(raw); input.Catalog.Queries == nil {
		t.Fatal("catalog")
	}
	again := BuildPlan(input, state)
	if again.Summary.ConnectorsCreated != 0 || again.Summary.TargetsCreated != 0 || again.Summary.CatalogAdded != 0 ||
		again.Summary.Imported != plan.Summary.Imported || len(again.Conflicts) != len(plan.Conflicts) {
		t.Fatalf("second plan: %+v (first %+v)", again.Summary, plan.Summary)
	}
}

func providerOfKind(kind string) string {
	for provider, info := range telemetryProviders {
		if info.targetKind == kind {
			return provider
		}
	}
	return ""
}

// A credentialed endpoint becomes a disabled connector plus a credential-use
// approval the operator must add; an existing approval is reported.
func TestBuildPlanListsCredentialApprovals(t *testing.T) {
	input, state := labInput(), labState()
	input.Telemetry.Loki = connectors.TelemetryEndpoint{URL: "https://loki.example.invalid", BearerTokenFile: "/run/secrets/loki", TenantID: "lab"}
	plan := BuildPlan(input, state)
	var loki PlannedConnector
	for _, c := range plan.Connectors {
		if c.Provider == "loki" {
			loki = c
		}
	}
	if loki.Action != actionCreate || loki.AuthMethod != "bearer" || loki.TenantID != "lab" || len(plan.CredentialBindings) != 1 ||
		plan.CredentialBindings[0].ConnectorID != loki.ID || plan.CredentialBindings[0].Approved {
		t.Fatalf("credential plan: %+v %+v", loki, plan.CredentialBindings)
	}
	input.Approvals = NewApprovals([]v3integrations.CredentialBinding{{Tier: "production", EnvironmentID: "prod", ConnectorID: loki.ID,
		Provider: "loki", Endpoint: "https://loki.example.invalid/", CredentialRef: "vault://secret/loki#token"}})
	if again := BuildPlan(input, state); !again.CredentialBindings[0].Approved || again.Digest != plan.Digest {
		t.Fatalf("approval not seen or digest changed: %+v", again.CredentialBindings)
	}
	// A registered connector with another tenant is a conflict, not a reuse.
	state.Connectors = append(state.Connectors, ExistingConnector{ID: "loki-registered", EnvironmentID: "prod", Provider: "loki",
		Endpoint: "https://loki.example.invalid", AuthMethod: "bearer", TenantID: "other", OptionsValid: true})
	conflicted := BuildPlan(input, state)
	found := false
	for _, issue := range conflicted.Conflicts {
		found = found || (issue.Kind == "loki" && issue.Reasons[0] == "existing_connector_tenant_differs")
	}
	if !found {
		t.Fatalf("tenant conflict missing: %+v", conflicted.Conflicts)
	}
}
