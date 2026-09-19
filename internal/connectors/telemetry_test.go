package connectors

// These protocol cases are source-only until the user explicitly permits tests.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func telemetryFixture(t *testing.T, h http.HandlerFunc) (*Telemetry, TimeWindow) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c := TelemetryConfig{Prometheus: TelemetryEndpoint{URL: s.URL, AllowHTTP: true}, Loki: TelemetryEndpoint{URL: s.URL, AllowHTTP: true}, Applications: map[string]ApplicationTelemetry{"demo": {PrometheusQueries: map[string]string{"requests": `sum(rate(checkout_requests_total{application="demo"}[1m]))`}, LokiLabels: map[string]string{"application": "demo", "namespace": "aiops-demo"}}}}
	out, e := NewTelemetry(c)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC().Truncate(time.Second)
	out.now = func() time.Time { return now }
	return out, TimeWindow{Start: now.Add(-time.Minute), End: now, StepSeconds: 15}
}
func TestTelemetryMetricsUsesOnlyRegisteredQuery(t *testing.T) {
	var got string
	var start string
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("query")
		start = r.URL.Query().Get("start")
		if r.Method != "GET" || r.URL.Path != "/api/v1/query_range" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[]},"warnings":["partial backend"]}`)
	})
	out, e := tm.QueryMetrics(context.Background(), "demo", "requests", w)
	if e != nil {
		t.Fatal(e)
	}
	if got != tm.config.Applications["demo"].PrometheusQueries["requests"] || start != w.Start.Format(time.RFC3339Nano) || !out.Empty || out.Source.HTTPStatus != 200 || len(out.Source.Warnings) != 1 {
		t.Fatalf("unexpected query or evidence: %s %+v", got, out)
	}
	got = ""
	_, e = tm.QueryMetrics(context.Background(), "demo", `up{application=~".*"}`, w)
	if e == nil || got != "" {
		t.Fatal("arbitrary PromQL reached endpoint")
	}
	_, e = tm.QueryMetrics(context.Background(), "other", "requests", w)
	if e == nil || got != "" {
		t.Fatal("cross-application query reached endpoint")
	}
}
func TestTelemetryLokiEscapesAndEnforcesExactScope(t *testing.T) {
	var query string
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("query")
		if r.URL.Path != "/loki/api/v1/query_range" || r.URL.Query().Get("direction") != "forward" {
			t.Errorf("invalid Loki request: %s", r.URL)
		}
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	})
	scope := `demo"} |= "escape`
	tm.config.Applications["demo"].LokiLabels["application"] = scope
	out, e := tm.QueryLogs(context.Background(), "demo", w, 100)
	if e != nil {
		t.Fatal(e)
	}
	if query != `{application="demo\"} |= \"escape",namespace="aiops-demo"}` || !out.Empty {
		t.Fatalf("unsafe selector %q", query)
	}
}
func TestTelemetryRejectsBadWindowBeforeNetwork(t *testing.T) {
	calls := 0
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	cases := []TimeWindow{{Start: w.End, End: w.Start}, {Start: w.Start, End: w.Start}, {Start: w.Start.Add(-24 * time.Hour), End: w.End}, {Start: w.Start, End: w.End.Add(time.Hour)}, {Start: w.Start, End: w.End, StepSeconds: 1}}
	for _, item := range cases {
		if _, e := tm.QueryMetrics(context.Background(), "demo", "requests", item); e == nil {
			t.Errorf("accepted invalid window %+v", item)
		}
	}
	if _, e := tm.QueryLogs(context.Background(), "demo", w, 5001); e == nil {
		t.Fatal("accepted unbounded log request")
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached backend %d times", calls)
	}
}
func TestTelemetryEmptyResultIsNotHealthy(t *testing.T) {
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		kind := "matrix"
		if strings.HasPrefix(r.URL.Path, "/loki") {
			kind = "streams"
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":%q,"result":[]}}`, kind)
	})
	m, e := tm.QueryMetrics(context.Background(), "demo", "requests", w)
	if e != nil || !m.Empty || len(m.Series) != 0 {
		t.Fatalf("%+v %v", m, e)
	}
	l, e := tm.QueryLogs(context.Background(), "demo", w, 10)
	if e != nil || !l.Empty || len(l.Entries) != 0 {
		t.Fatalf("%+v %v", l, e)
	}
}
func TestTelemetryTimeoutPreservesSource(t *testing.T) {
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out, e := tm.QueryMetrics(ctx, "demo", "requests", w)
	if !errors.Is(e, context.DeadlineExceeded) || out.Source.Failure == nil || out.Source.Failure.Kind != "timeout" || out.Source.QueryID != "requests" {
		t.Fatalf("%+v %v", out, e)
	}
}
func TestTelemetryStatusAndUpstreamFailure(t *testing.T) {
	status := 503
	body := `{"status":"error","errorType":"timeout","error":"query exhausted budget"}`
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, body) })
	out, e := tm.QueryMetrics(context.Background(), "demo", "requests", w)
	if e == nil || out.Source.HTTPStatus != 503 || !out.Source.Failure.Retryable {
		t.Fatalf("%+v %v", out, e)
	}
	status = 200
	out, e = tm.QueryMetrics(context.Background(), "demo", "requests", w)
	if e == nil || out.Source.Failure.Kind != "upstream" || !strings.Contains(e.Error(), "query exhausted budget") {
		t.Fatalf("%+v %v", out, e)
	}
}
func TestTelemetryCredentialsNeverEnterSourceAndNoRedirect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if e := os.WriteFile(path, []byte("private-token"), 0600); e != nil {
		t.Fatal(e)
	}
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("missing auth")
		}
		w.Header().Set("Location", "http://invalid.example/steal")
		w.WriteHeader(302)
	})
	tm.prometheus.config.BearerTokenFile = path
	out, e := tm.QueryMetrics(context.Background(), "demo", "requests", w)
	b, _ := json.Marshal(out)
	if e == nil || out.Source.HTTPStatus != 302 || strings.Contains(string(b), "private-token") || strings.Contains(string(b), path) {
		t.Fatalf("credential leak or followed redirect: %s %v", b, e)
	}
}
func TestTelemetryRejectsOutOfScopeLokiResponse(t *testing.T) {
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"application":"other","namespace":"aiops-demo"},"values":[]}]}}`)
	})
	out, e := tm.QueryLogs(context.Background(), "demo", w, 100)
	if e == nil || out.Source.Failure.Kind != "scope" {
		t.Fatalf("%+v %v", out, e)
	}
}
func TestTelemetryParsesActualProtocolShapes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tm, w := telemetryFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/loki") {
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"application":"demo","namespace":"aiops-demo"},"values":[["%d","checkout status=200"]]}]}}`, now.Add(-30*time.Second).UnixNano())
			return
		}
		fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"application":"demo"},"values":[[%d,"1.25"]]}]}}`, now.Add(-30*time.Second).Unix())
	})
	m, e := tm.QueryMetrics(context.Background(), "demo", "requests", w)
	if e != nil || m.Empty || len(m.Series) != 1 || m.Series[0].Samples[0].Value != "1.25" {
		t.Fatalf("%+v %v", m, e)
	}
	l, e := tm.QueryLogs(context.Background(), "demo", w, 1)
	if e != nil || l.Empty || len(l.Entries) != 1 || !l.LimitReached {
		t.Fatalf("%+v %v", l, e)
	}
}
func TestTelemetryConfigurationIsCopied(t *testing.T) {
	c := TelemetryConfig{Prometheus: TelemetryEndpoint{URL: "https://metrics.example"}, Applications: map[string]ApplicationTelemetry{"demo": {PrometheusQueries: map[string]string{"requests": "registered"}}}}
	tm, e := NewTelemetry(c)
	if e != nil {
		t.Fatal(e)
	}
	c.Applications["demo"].PrometheusQueries["requests"] = "unscoped"
	if tm.config.Applications["demo"].PrometheusQueries["requests"] != "registered" {
		t.Fatal("caller expanded live query scope")
	}
}
