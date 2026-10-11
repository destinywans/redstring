package v3telemetry

import (
	"encoding/json"
	"strings"
	"testing"
)

// D11: a GitLab scope binds a code project to a deploy environment and its
// branches; every field is checked and the file round-trips.
func TestCatalogGitLabScopes(t *testing.T) {
	valid := `{"queries":[],"gitlab":[{"connector_id":"gitlab-1","application_id":"app","environment_id":"prod",
 "project_id":"4","deploy_environment":"aiops-lab","refs":["main","release/1.x"]}]}`
	catalog, err := ParseCatalog([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	scope, ok := catalog.GitLabScopeFor("gitlab-1", "app", "prod", "4")
	if !ok || scope.DeployEnvironment != "aiops-lab" || strings.Join(scope.Refs, ",") != "main,release/1.x" {
		t.Fatalf("scope: %+v %v", scope, ok)
	}
	if _, ok := catalog.GitLabScopeFor("gitlab-1", "app", "test", "4"); ok {
		t.Fatal("scope crossed environments")
	}
	again, err := json.Marshal(catalog.File())
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseCatalog(again)
	if err != nil || len(reparsed.GitLab) != 1 || len(catalog.Clone().GitLab) != 1 {
		t.Fatalf("round trip: %s %v", again, err)
	}
	for name, entry := range map[string]string{
		"project zero":     `"project_id":"0","deploy_environment":"aiops-lab","refs":["main"]`,
		"project text":     `"project_id":"group/app","deploy_environment":"aiops-lab","refs":["main"]`,
		"no environment":   `"project_id":"4","deploy_environment":"","refs":["main"]`,
		"no refs":          `"project_id":"4","deploy_environment":"aiops-lab","refs":[]`,
		"duplicate ref":    `"project_id":"4","deploy_environment":"aiops-lab","refs":["main","main"]`,
		"revision syntax":  `"project_id":"4","deploy_environment":"aiops-lab","refs":["main~1"]`,
		"unknown field":    `"project_id":"4","deploy_environment":"aiops-lab","refs":["main"],"token":"x"`,
		"newline in value": `"project_id":"4","deploy_environment":"aiops\nlab","refs":["main"]`,
	} {
		raw := `{"queries":[],"gitlab":[{"connector_id":"gitlab-1","application_id":"app","environment_id":"prod",` + entry + `}]}`
		if _, err := ParseCatalog([]byte(raw)); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	duplicate := `{"queries":[],"gitlab":[{"connector_id":"g","application_id":"a","environment_id":"e","project_id":"4","deploy_environment":"x","refs":["main"]},
 {"connector_id":"g","application_id":"a","environment_id":"e","project_id":"4","deploy_environment":"y","refs":["main"]}]}`
	if _, err := ParseCatalog([]byte(duplicate)); err == nil {
		t.Fatal("duplicate scope accepted")
	}
}
