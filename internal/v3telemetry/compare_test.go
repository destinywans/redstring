package v3telemetry

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/connectors"
)

func TestLegacySourceReadsTheFileRegistration(t *testing.T) {
	cfg := labTelemetry()
	loki, ok := LegacySource(cfg, "loki", "lab-checkout")
	if !ok || loki.Endpoint != "http://127.0.0.1:13100" || loki.AuthMethod != "none" || loki.Scope.LokiLabels["app"] != "checkout" ||
		loki.Scope.TempoService != "" || loki.Queries != nil {
		t.Fatalf("loki: %+v", loki)
	}
	if es, ok := LegacySource(cfg, "elasticsearch", "lab-search"); !ok || es.AuthMethod != "basic" {
		t.Fatalf("elasticsearch: %+v", es)
	}
	if prom, ok := LegacySource(cfg, "prometheus", "lab-checkout"); !ok || len(prom.Queries) != 2 {
		t.Fatalf("prometheus: %+v", prom)
	}
	for _, missing := range [][2]string{{"loki", "lab-payments"}, {"loki", "nope"}, {"kubernetes", "lab-checkout"}} {
		if _, ok := LegacySource(cfg, missing[0], missing[1]); ok {
			t.Fatalf("%v registered", missing)
		}
	}
}

func TestCompareStates(t *testing.T) {
	cfg := labTelemetry()
	loki, _ := LegacySource(cfg, "loki", "lab-checkout")
	prom, _ := LegacySource(cfg, "prometheus", "lab-checkout")
	same := Source{Provider: "loki", Endpoint: "http://127.0.0.1:13100/", AuthMethod: "none",
		Scope: connectors.ApplicationTelemetry{LokiLabels: map[string]string{"app": "checkout", "namespace": "lab"}}}
	with := func(change func(*Source)) Source {
		s := same
		s.Scope.LokiLabels = map[string]string{"app": "checkout", "namespace": "lab"}
		change(&s)
		return s
	}
	promSource := Source{Provider: "prometheus", Endpoint: "http://127.0.0.1:19090", AuthMethod: "none",
		Queries: map[string]string{"request_rate": prom.Queries["request_rate"], "error_ratio": "changed", "dashboard": "extra is fine"}}
	for name, tc := range map[string]struct {
		legacy   Legacy
		legacyOK bool
		source   Source
		err      error
		want     Result
	}{
		"match":       {loki, true, same, nil, Result{State: StateMatch}},
		"not ready":   {loki, true, with(func(s *Source) { s.NotReady = []string{"connector_disabled", "tool_disabled"} }), nil, Result{StateMatchNotReady, []string{"connector_disabled", "tool_disabled"}}},
		"endpoint":    {loki, true, with(func(s *Source) { s.Endpoint = "http://127.0.0.1:3100" }), nil, Result{StateDiffers, []string{"endpoint"}}},
		"selector":    {loki, true, with(func(s *Source) { s.Scope.LokiLabels["app"] = "order" }), nil, Result{StateDiffers, []string{"scope.loki_labels"}}},
		"no scope":    {loki, true, with(func(s *Source) { s.Scope = connectors.ApplicationTelemetry{}; s.NotReady = []string{"scope_missing"} }), nil, Result{StateDiffers, []string{"scope_missing"}}},
		"provider":    {loki, true, with(func(s *Source) { s.Provider = "elasticsearch" }), nil, Result{StateDiffers, []string{"provider"}}},
		"tenant":      {loki, true, with(func(s *Source) { s.TenantID = "lab" }), nil, Result{StateDiffers, []string{"tenant_id"}}},
		"queries":     {prom, true, promSource, nil, Result{StateDiffers, []string{"query_changed:error_ratio"}}},
		"missing":     {loki, true, Source{}, ErrNotConfigured, Result{State: StateMissing}},
		"ambiguous":   {loki, true, Source{}, ErrAmbiguous, Result{State: StateAmbiguous}},
		"storage":     {loki, true, Source{}, errors.New("db down"), Result{State: StateUnavailable}},
		"legacy only": {Legacy{}, false, same, nil, Result{State: StateLegacyUnregistered}},
	} {
		if got := Compare(tc.legacy, tc.legacyOK, tc.source, tc.err); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v want %+v", name, got, tc.want)
		}
	}
	delete(promSource.Queries, "request_rate")
	if got := Compare(prom, true, promSource, nil); !reflect.DeepEqual(got.Reasons, []string{"query_changed:error_ratio", "query_missing:request_rate"}) {
		t.Fatalf("query reasons: %+v", got)
	}
}

func TestCompareWorkloadStates(t *testing.T) {
	legacy := LegacyWorkload{Endpoint: "https://127.0.0.1:16443/", Namespace: "aiops-lab", Deployment: "checkout-api", Token: true}
	unified := Workload{Endpoint: "https://127.0.0.1:16443", AuthMethod: "bearer",
		Scope: WorkloadScope{Namespace: "aiops-lab", Deployment: "checkout-api"}}
	moved := unified
	moved.Scope.Deployment = "checkout"
	unready := unified
	unready.NotReady = []string{"credential_missing"}
	unscoped := unified
	unscoped.NotReady, unscoped.Scope = []string{"scope_missing"}, WorkloadScope{}
	for _, c := range []struct {
		name       string
		registered bool
		unified    Workload
		err        error
		want       string
		reasons    []string
	}{
		{"match", true, unified, nil, StateMatch, nil},
		{"not ready", true, unready, nil, StateMatchNotReady, []string{"credential_missing"}},
		{"moved", true, moved, nil, StateDiffers, []string{"scope.deployment"}},
		{"unscoped", true, unscoped, nil, StateDiffers, []string{"scope_missing"}},
		{"missing", true, Workload{}, ErrNotConfigured, StateMissing, nil},
		{"unregistered", false, unified, nil, StateLegacyUnregistered, nil},
	} {
		got := compareWorkload(legacy, c.registered, c.unified, c.err)
		if got.State != c.want || strings.Join(got.Reasons, ",") != strings.Join(c.reasons, ",") {
			t.Fatalf("%s: %+v", c.name, got)
		}
	}
}
