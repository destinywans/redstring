package connectors

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// PA-19 P3 stage 2 step 0: a registry-built client takes its credential per
// request, reports 401/403, and sends Prometheus and Loki queries as POST
// forms; Tempo stays GET. A file registration is unchanged.
func TestTelemetryRuntimeEndpoint(t *testing.T) {
	var unauthorized atomic.Int32
	var status atomic.Int32
	status.Store(200)
	seen := make(chan *http.Request, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		seen <- r
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer server.Close()
	credential := func(context.Context) (string, error) { return "secret-value", nil }
	build := func(kind, username string) *Telemetry {
		t.Helper()
		client, err := NewTelemetry(TelemetryConfig{
			Prometheus: TelemetryEndpoint{URL: server.URL, AllowHTTP: true, Credential: credential, CredentialKind: kind, Username: username,
				Unauthorized: func() { unauthorized.Add(1) }, PostQueries: true},
			Applications: map[string]ApplicationTelemetry{"svc": {PrometheusQueries: map[string]string{"rate": "sum(rate(x[5m]))"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}
	window := TimeWindow{Start: time.Now().Add(-10 * time.Minute), End: time.Now(), StepSeconds: 60}
	for kind, want := range map[string]string{"bearer": "Bearer secret-value", "api_key": "ApiKey secret-value"} {
		if _, err := build(kind, "").QueryMetrics(context.Background(), "svc", "rate", window); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		r := <-seen
		if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.PostForm.Get("query") != "sum(rate(x[5m]))" ||
			r.Header.Get("Authorization") != want || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatalf("%s request: %s %q auth=%q", kind, r.Method, r.URL.RawQuery, r.Header.Get("Authorization"))
		}
	}
	if _, err := build("basic", "elastic").QueryMetrics(context.Background(), "svc", "rate", window); err != nil {
		t.Fatal(err)
	}
	if r := <-seen; func() bool { u, p, ok := r.BasicAuth(); return !ok || u != "elastic" || p != "secret-value" }() {
		t.Fatal("basic auth not sent")
	}
	status.Store(401)
	if _, err := build("bearer", "").QueryMetrics(context.Background(), "svc", "rate", window); err == nil || unauthorized.Load() != 1 {
		t.Fatalf("401 not reported: %v (%d)", err, unauthorized.Load())
	}
	<-seen

	// A credential that cannot be read fails the request before it is sent.
	failing, err := NewTelemetry(TelemetryConfig{
		Prometheus: TelemetryEndpoint{URL: server.URL, AllowHTTP: true, CredentialKind: "bearer", PostQueries: true,
			Credential: func(context.Context) (string, error) { return "", errors.New("vault down") }},
		Applications: map[string]ApplicationTelemetry{"svc": {PrometheusQueries: map[string]string{"rate": "x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var telemetryErr *TelemetryError
	if _, err := failing.QueryMetrics(context.Background(), "svc", "rate", window); !errors.As(err, &telemetryErr) || telemetryErr.Kind != "credentials" {
		t.Fatalf("credential failure: %v", err)
	}
	if len(seen) != 0 {
		t.Fatal("request sent without a credential")
	}

	// Invalid combinations are rejected; runtime settings never serialize.
	for name, endpoint := range map[string]TelemetryEndpoint{
		"hook and file":     {URL: server.URL, AllowHTTP: true, Credential: credential, CredentialKind: "bearer", BearerTokenFile: "/tmp/x"},
		"unknown kind":      {URL: server.URL, AllowHTTP: true, Credential: credential, CredentialKind: "digest"},
		"basic no username": {URL: server.URL, AllowHTTP: true, Credential: credential, CredentialKind: "basic"},
		"kind without hook": {URL: server.URL, AllowHTTP: true, CredentialKind: "bearer"},
	} {
		if _, err := NewTelemetry(TelemetryConfig{Prometheus: endpoint,
			Applications: map[string]ApplicationTelemetry{"svc": {PrometheusQueries: map[string]string{"rate": "x"}}}}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if raw := build("bearer", "").Registration(); raw.Prometheus.Credential != nil || raw.Prometheus.PostQueries {
		t.Fatal("registration copied runtime settings")
	}
}

// Tempo searches stay GET even on a registry-built client.
func TestTelemetryRuntimeTempoStaysGet(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/search") {
			t.Errorf("tempo request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"traces":[],"metrics":{"completedJobs":1,"totalJobs":1}}`))
	}))
	defer server.Close()
	client, err := NewTelemetry(TelemetryConfig{Tempo: TelemetryEndpoint{URL: server.URL, AllowHTTP: true, PostQueries: true},
		Applications: map[string]ApplicationTelemetry{"svc": {TempoService: "svc", TempoAttributes: map[string]string{"deployment.environment": "lab"}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.QueryTraces(context.Background(), "svc", TimeWindow{Start: time.Now().Add(-10 * time.Minute), End: time.Now()}, 5)
	if requests.Load() == 0 {
		t.Fatal("no Tempo request was sent")
	}
}
