package v3alertrules

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusSourceReadsActualAlertingRules(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/rules" || r.URL.Query().Get("type") != "alert" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"groups":[{"file":"/etc/prometheus/rules/checkout.yml","name":"app-rules","rules":[{"type":"alerting","name":"HighErrors","query":"rate(errors_total[5m]) > 1","state":"pending","duration":300,"labels":{"environment":"test","application":"checkout"},"annotations":{"summary":"High errors"},"lastEvaluation":"2026-09-28T08:00:00Z","evaluationTime":0.02},{"type":"recording","name":"record","query":"up"}]}]}}`))
	}))
	defer upstream.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: upstream.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := source.List(context.Background(), "test")
	if err != nil || len(rules) != 1 || rules[0].Name != "HighErrors" || rules[0].Duration != 300 || rules[0].State != "pending" || rules[0].GroupIdentity != "/etc/prometheus/rules/checkout.yml\x00app-rules" {
		t.Fatalf("rules=%+v err=%v", rules, err)
	}
	if _, err := source.List(context.Background(), "prod"); err == nil {
		t.Fatal("unregistered environment was allowed")
	}
}

func TestPrometheusSourceRejectsUnsafeConfigurationAndMalformedResults(t *testing.T) {
	for _, endpoint := range []PrometheusEndpoint{{URL: "http://example.com"}, {URL: "https://user:pass@example.com"}, {URL: "https://example.com/?query=up"}} {
		if _, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": endpoint}, nil); err == nil {
			t.Fatalf("accepted endpoint %+v", endpoint)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"groups":[{"file":"/etc/prometheus/rules/bad.yml","name":"g","rules":[{"type":"alerting","name":"bad","query":"","state":"inactive"}]}]}}`))
	}))
	defer upstream.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: upstream.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.List(context.Background(), "test"); err == nil || !strings.Contains(err.Error(), "invalid Prometheus") {
		t.Fatalf("malformed rule accepted: %v", err)
	}
}
