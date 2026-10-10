package v3telemetry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseCatalogAcceptsQueriesAndProviderScopes(t *testing.T) {
	file := `{"queries":[{"connector_id":"c1","application_id":"app","environment_id":"prod","resource_id":"lab-checkout","query_id":"request_rate","expression":"sum(rate(x[5m]))"}],
 "scopes":[
  {"connector_id":"c2","application_id":"app","environment_id":"prod","resource_id":"lab-checkout","provider":"loki","loki_labels":{"app":"checkout"}},
  {"connector_id":"c3","application_id":"app","environment_id":"prod","resource_id":"lab-checkout","provider":"elasticsearch","elasticsearch_index":"lab-logs","elasticsearch_filters":{"service.name":"checkout"}},
  {"connector_id":"c4","application_id":"app","environment_id":"prod","resource_id":"lab-checkout","provider":"tempo","tempo_service":"checkout","tempo_attributes":{"deployment.environment":"lab"},"tempo_safe_operations":["GET /orders"]}]}`
	catalog, err := ParseCatalog([]byte(file))
	if err != nil || len(catalog.Queries) != 1 || len(catalog.Scopes) != 3 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	if scope := catalog.Scopes[ScopeKey{"c2", "app", "prod", "lab-checkout"}]; scope.Provider != "loki" || scope.LokiLabels["app"] != "checkout" {
		t.Fatalf("loki scope: %+v", scope)
	}
	// The file shape round-trips, so a generated catalog parses to the same.
	raw, _ := json.Marshal(catalog.File())
	again, err := ParseCatalog(raw)
	if err != nil || !reflect.DeepEqual(again, catalog) || strings.Contains(string(raw), "prometheus_queries") {
		t.Fatalf("round trip: %s %v", raw, err)
	}
	// The existing queries-only file is still a valid catalog.
	if old, err := ParseCatalog([]byte(`{"queries":[]}`)); err != nil || len(old.Queries) != 0 || len(old.Scopes) != 0 {
		t.Fatalf("queries-only file: %v", err)
	}
}

func TestParseCatalogRejectsMixedOrUnsafeScopes(t *testing.T) {
	scope := func(fields string) string {
		return `{"scopes":[{"connector_id":"c","application_id":"app","environment_id":"prod","resource_id":"r",` + fields + `}]}`
	}
	for name, file := range map[string]string{
		"unknown field":         scope(`"provider":"loki","loki_labels":{"app":"a"},"url":"http://x"`),
		"promql in a scope":     scope(`"provider":"loki","loki_labels":{"app":"a"},"prometheus_queries":{"q":"up"}`),
		"other provider fields": scope(`"provider":"loki","loki_labels":{"app":"a"},"tempo_service":"a"`),
		"empty loki selector":   scope(`"provider":"loki"`),
		"bad label name":        scope(`"provider":"loki","loki_labels":{"a-b":"x"}`),
		"keyword filter":        scope(`"provider":"elasticsearch","elasticsearch_index":"i","elasticsearch_filters":{"service.keyword":"x"}`),
		"tempo without scope":   scope(`"provider":"tempo","tempo_service":"a"`),
		"prometheus scope":      scope(`"provider":"prometheus"`),
		"bad resource id":       `{"scopes":[{"connector_id":"c","application_id":"app","environment_id":"prod","resource_id":"../x","provider":"loki","loki_labels":{"app":"a"}}]}`,
		"duplicate scope": `{"scopes":[{"connector_id":"c","application_id":"app","environment_id":"prod","resource_id":"r","provider":"loki","loki_labels":{"app":"a"}},
 {"connector_id":"c","application_id":"app","environment_id":"prod","resource_id":"r","provider":"loki","loki_labels":{"app":"b"}}]}`,
		"long expression": `{"queries":[{"connector_id":"c","application_id":"app","environment_id":"prod","resource_id":"r","query_id":"q","expression":"` + strings.Repeat("x", 8193) + `"}]}`,
		"trailing data":   `{"queries":[]} {}`,
	} {
		if _, err := ParseCatalog([]byte(file)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestParseMode(t *testing.T) {
	for value, want := range map[string]Mode{"": ModeLegacy, "legacy": ModeLegacy, "compare": ModeCompare} {
		if mode, err := ParseMode(value); err != nil || mode != want {
			t.Fatalf("%q: %v %v", value, mode, err)
		}
	}
	for _, value := range []string{"unified", "Compare", "on"} {
		if _, err := ParseMode(value); err == nil {
			t.Fatalf("%q accepted", value)
		}
	}
}
