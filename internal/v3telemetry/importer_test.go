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
				Provider: c.Provider, Endpoint: c.Endpoint, AuthMethod: c.AuthMethod, TenantID: c.TenantID, Username: c.Username,
				OptionsValid: true})
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
	if kind == WorkloadTargetKind {
		return WorkloadProvider
	}
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
	input.Workloads = nil                                          // only the Loki credential here
	input.Telemetry.Elasticsearch = connectors.TelemetryEndpoint{} // ES basic: TestBuildPlanImportsBasicAuth
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

// PA-19 P3 stage 2 step 3 (decision 3): Elasticsearch with a username and
// password file imports as basic auth. The username goes into the connector
// options; the password becomes a credential approval. A registered
// connector with another username is a conflict, not a reuse.
func TestBuildPlanImportsBasicAuth(t *testing.T) {
	input, state := labInput(), labState()
	input.Workloads = nil
	plan := BuildPlan(input, state)
	var es PlannedConnector
	for _, c := range plan.Connectors {
		if c.Provider == "elasticsearch" {
			es = c
		}
	}
	var binding *PlannedCredential
	for i, b := range plan.CredentialBindings {
		if b.ConnectorID == es.ID {
			binding = &plan.CredentialBindings[i]
		}
	}
	if es.Action != actionCreate || es.AuthMethod != "basic" || es.Username != "reader" || binding == nil || binding.AuthMethod != "basic" {
		t.Fatalf("basic plan: %+v %+v", es, plan.CredentialBindings)
	}
	for _, issue := range plan.Conflicts {
		for _, reason := range issue.Reasons {
			if reason == "auth_method_unsupported" {
				t.Fatalf("basic auth still unsupported: %+v", issue)
			}
		}
	}
	state.Connectors = append(state.Connectors, ExistingConnector{ID: "es-registered", EnvironmentID: "prod", Provider: "elasticsearch",
		Endpoint: "https://es.example.invalid", AuthMethod: "basic", Username: "someone-else", OptionsValid: true})
	found := false
	for _, issue := range BuildPlan(input, state).Conflicts {
		found = found || (issue.Kind == "elasticsearch" && issue.Reasons[0] == "existing_connector_username_differs")
	}
	if !found {
		t.Fatal("username conflict missing")
	}
	// A username the options cannot hold stays a conflict.
	input.Telemetry.Elasticsearch.Username = "re:ader"
	found = false
	for _, issue := range BuildPlan(input, labState()).Conflicts {
		for _, reason := range issue.Reasons {
			found = found || (issue.Kind == "elasticsearch" && reason == "basic_username_invalid")
		}
	}
	if !found {
		t.Fatal("a username with a colon was imported")
	}
}

// PA-19 P3: a legacy Kubernetes registration becomes a read-only connector, a
// workload target and a catalog scope with rollback off until reviewed.
func TestBuildPlanImportsWorkloads(t *testing.T) {
	plan := BuildPlan(labInput(), labState())
	var workload *PlannedTarget
	for i, target := range plan.Targets {
		if target.TargetKind == WorkloadTargetKind {
			workload = &plan.Targets[i]
		}
	}
	key := ScopeKey{ConnectorID: workload.ConnectorID, ApplicationID: "app-lab-checkout", EnvironmentID: "prod", ResourceID: "checkout-api"}
	catalogRaw, _ := json.Marshal(plan.Catalog)
	catalog, err := ParseCatalog(catalogRaw)
	if err != nil || catalog.Workloads[key].Deployment != "checkout-api" || catalog.Workloads[key].Rollback != nil ||
		len(plan.Reviews) != 1 || plan.Reviews[0].Reasons[0] != "rollback_policy_requires_review" {
		t.Fatalf("workload plan: %+v %+v %v", catalog.Workloads, plan.Reviews, err)
	}

	conflicts := func(input ImportInput, state State) []string {
		var reasons []string
		for _, issue := range BuildPlan(input, state).Conflicts {
			if issue.Kind == WorkloadProvider && issue.ApplicationID == "app-lab-checkout" {
				reasons = append(reasons, issue.Reasons...)
			}
		}
		return reasons
	}
	input, state := labInput(), labState()
	input.Workloads["lab-kubernetes"] = LegacyWorkload{Endpoint: "http://10.0.0.1:6443", Namespace: "Lab", Deployment: "checkout-api"}
	if got := conflicts(input, state); len(got) != 3 || got[0] != "endpoint_not_allowed" || got[1] != "auth_method_unsupported" ||
		got[2] != "workload_name_invalid" {
		t.Fatalf("invalid registration: %v", got)
	}
	// The reviewed catalog names another Deployment for this key.
	input = labInput()
	input.Catalog.Workloads = map[ScopeKey]WorkloadScope{key: {Namespace: "aiops-lab", Deployment: "order-api"}}
	if got := conflicts(input, state); len(got) != 1 || got[0] != "scope_conflict" {
		t.Fatalf("scope conflict: %v", got)
	}
	// The service already reads a workload through another connector.
	input = labInput()
	state.Targets = append(state.Targets, ExistingTarget{ID: "t-other", ApplicationID: "app-lab-checkout", EnvironmentID: "prod",
		ConnectorID: "other-cluster", Provider: WorkloadProvider, TargetKind: WorkloadTargetKind, ExternalID: "checkout-api"})
	if got := conflicts(input, state); len(got) != 1 || got[0] != "existing_target_other_source" {
		t.Fatalf("other source: %v", got)
	}
}
