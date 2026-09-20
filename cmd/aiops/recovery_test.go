package main

import (
	"context"
	"encoding/json"
	"github.com/wans/aiops-canvas/internal/connectors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBusinessRecoverySamplesRejectMissingStaleAndNonFinite(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, value := range []string{"NaN", "+Inf", "-1", "not-number"} {
		v := connectors.MetricEvidence{Series: []connectors.MetricSeries{{Samples: []connectors.MetricSample{{Timestamp: 1000, Value: value}}}}}
		if _, e := latestMetricValue(v, now); e == nil {
			t.Errorf("accepted %s", value)
		}
	}
	for _, v := range []connectors.MetricEvidence{{}, {Empty: true}, {LimitReached: true}, {Series: []connectors.MetricSeries{{Samples: []connectors.MetricSample{{Timestamp: 900, Value: "1"}}}}}} {
		if _, e := latestMetricValue(v, now); e == nil {
			t.Error("accepted incomplete sample")
		}
	}
	v := connectors.MetricEvidence{Series: []connectors.MetricSeries{{Samples: []connectors.MetricSample{{Timestamp: 1000, Value: "2"}}}, {Samples: []connectors.MetricSample{{Timestamp: 1000, Value: "3"}}}}}
	if n, e := latestMetricValue(v, now); e != nil || n != 5 {
		t.Fatalf("aggregate=%v err=%v", n, e)
	}
}

func TestBusinessRecoveryRequiresRawCountersAndRejectsNoNewTraffic(t *testing.T) {
	// An old rate-only registration cannot silently become a recovery verifier.
	var p BusinessVerification
	if e := json.Unmarshal([]byte(`{"telemetry_application":"app","request_rate_query_id":"positive_long_window_rate","error_rate_query_id":"zero_rate","probe_url":"https://example.invalid/health"}`), &p); e != nil {
		t.Fatal(e)
	}
	if e := validateBusinessVerification(p); e == nil {
		t.Fatal("rate-only configuration accepted")
	}
	baseline := businessCounters{requests: 100, errors: 0}
	for _, current := range []businessCounters{{100, 0}, {99, 0}, {110, 1}} {
		if e := validateBusinessCounterWindow(baseline, current); e == nil {
			t.Fatalf("invalid actual traffic accepted: %+v", current)
		}
	}
	if e := validateBusinessCounterWindow(baseline, businessCounters{110, 0}); e != nil {
		t.Fatal(e)
	}
}

func TestRecoveryRejectsStaleSourceDespiteFreshQueryEvaluation(t *testing.T) {
	now := time.Now().UTC()
	age := "300"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := "100"
		if r.URL.Query().Get("query") == "errors_total" {
			value = "0"
		}
		if r.URL.Query().Get("query") == "source_age" {
			value = age
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]string{}, "values": [][]any{{float64(now.Unix()), value}}}}}})
	}))
	defer server.Close()
	telemetry, e := connectors.NewTelemetry(connectors.TelemetryConfig{Prometheus: connectors.TelemetryEndpoint{URL: server.URL, AllowHTTP: true}, Applications: map[string]connectors.ApplicationTelemetry{"app": {PrometheusQueries: map[string]string{"requests": "requests_total", "errors": "errors_total", "age": "source_age"}}}})
	if e != nil {
		t.Fatal(e)
	}
	recovery, e := newBusinessRecovery(nil, telemetry, BusinessVerification{TelemetryApplication: "app", RequestCounterQueryID: "requests", ErrorCounterQueryID: "errors", SampleAgeQueryID: "age", ProbeURL: "https://example.invalid/health"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = recovery.counters(context.Background(), now); e == nil {
		t.Fatal("fresh evaluation timestamp masked stale source")
	}
	age = "1"
	v, e := recovery.counters(context.Background(), now)
	if e != nil || v.requests != 100 || v.errors != 0 {
		t.Fatalf("fresh raw counters rejected: %+v %v", v, e)
	}
}
