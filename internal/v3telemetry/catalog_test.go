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

// PA-19 P4: the client limits and the Tempo search attestation live in the
// catalog, which a V3 instance reads instead of the telemetry file.
func TestCatalogLimitsAndTempoAttestation(t *testing.T) {
	catalog, err := ParseCatalog([]byte(`{"queries":[],"limits":{"timeout_seconds":30,"max_window_seconds":7200,"max_lookback_seconds":1209600},
 "tempo_search_fail_closed":["tempo-a"]}`))
	if err != nil || catalog.Limits != (Limits{TimeoutSeconds: 30, MaxWindowSeconds: 7200, MaxLookbackSeconds: 1209600}) ||
		len(catalog.TempoSearchFailClosed) != 1 || !catalog.TempoSearchFailClosed["tempo-a"] {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	if config := catalog.Limits.Config(); config.MaxWindowSeconds != 7200 || config.TempoSearchFailClosed {
		t.Fatalf("client limits: %+v", config)
	}
	raw, _ := json.Marshal(catalog.File())
	if again, err := ParseCatalog(raw); err != nil || again.Limits != catalog.Limits || !again.TempoSearchFailClosed["tempo-a"] {
		t.Fatalf("round trip: %s %+v %v", raw, again, err)
	}
	clone := catalog.Clone()
	clone.TempoSearchFailClosed["tempo-b"] = true
	if clone.Limits != catalog.Limits || catalog.TempoSearchFailClosed["tempo-b"] {
		t.Fatalf("clone: %+v %+v", clone, catalog)
	}
	if empty, err := ParseCatalog([]byte(`{"queries":[]}`)); err != nil || empty.Limits != (Limits{}) || len(empty.TempoSearchFailClosed) != 0 {
		t.Fatalf("a catalog without limits keeps the defaults: %+v %v", empty, err)
	}
	if raw, _ := json.Marshal(Catalog{}.File()); strings.Contains(string(raw), "limits") || strings.Contains(string(raw), "fail_closed") {
		t.Fatalf("an empty catalog writes no limits: %s", raw)
	}
	for name, raw := range map[string]string{
		"lookback shorter than the window": `{"queries":[],"limits":{"max_window_seconds":7200,"max_lookback_seconds":3600}}`,
		"timeout too long":                 `{"queries":[],"limits":{"timeout_seconds":61}}`,
		"unknown limit":                    `{"queries":[],"limits":{"max_spans":10}}`,
		"empty connector":                  `{"queries":[],"tempo_search_fail_closed":[""]}`,
		"repeated connector":               `{"queries":[],"tempo_search_fail_closed":["tempo-a","tempo-a"]}`,
	} {
		if _, err := ParseCatalog([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
