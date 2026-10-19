package backend_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type componentFixture struct {
	Owner       string `json:"owner"`
	Work        string `json:"work"`
	FixturePID  int    `json:"fixture_pid"`
	Credentials struct {
		VaultToken string `json:"vault_token"`
	} `json:"credentials"`
	HostConfig struct {
		Application string `json:"application_id"`
		Environment string `json:"environment_id"`
		Service     string `json:"service"`
		Prometheus  string `json:"prometheus_url"`
		Loki        string `json:"loki_url"`
		Tempo       string `json:"tempo_url"`
		Vault       string `json:"vault_url"`
		OTLP        string `json:"otlp_http_url"`
	} `json:"host_config"`
	Services []struct{ Name, Container, URL, Mode string } `json:"services"`
}

func privateComponentFile(t *testing.T, path string) []byte {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("component fixture and ownership token require regular 0600 files")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("generated component file unavailable")
	}
	return raw
}

// Verify process and every real target's port ownership before any test data is
// sent. Only the disposable generated fixture is read, never product .env.
func readComponentFixture(t *testing.T) (componentFixture, string) {
	t.Helper()
	f := readFixture(t, false)
	dir := filepath.Dir(os.Getenv("AIOPS_TESTLAB_BACKEND_FIXTURE"))
	var c componentFixture
	if json.Unmarshal(privateComponentFile(t, filepath.Join(dir, "component-environment.json")), &c) != nil {
		t.Fatal("invalid generated component fixture")
	}
	if c.Owner != f.Owner || !regexp.MustCompile(`^testlab-[a-z0-9]+$`).MatchString(c.Owner) || filepath.Clean(c.Work) != filepath.Join(dir, "environments", c.Owner) || c.FixturePID < 1 {
		t.Fatal("component owner or work path does not match backend fixture")
	}
	if c.HostConfig.Application != "testlab" || c.HostConfig.Environment != "test" || c.HostConfig.Service != "testlab-api" || c.Credentials.VaultToken == "" {
		t.Fatal("dedicated component application/environment required")
	}
	wanted := map[string]string{"prometheus": c.HostConfig.Prometheus, "loki": c.HostConfig.Loki, "tempo": c.HostConfig.Tempo, "vault": c.HostConfig.Vault, "otel": strings.TrimSuffix(c.HostConfig.OTLP, "/v1/traces")}
	producer := ""
	for _, s := range c.Services {
		if s.Name == "fixtures" {
			producer = s.URL
			continue
		}
		target, ok := wanted[s.Name]
		if !ok {
			continue
		}
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || target != s.URL || s.Mode != "real_local" || s.Container != "aiops-"+c.Owner+"-"+s.Name {
			t.Fatal("component target must be the exact owned loopback origin")
		}
		raw, err := exec.CommandContext(t.Context(), "docker", "inspect", s.Container).Output()
		var states []struct {
			Config          struct{ Labels map[string]string }
			State           struct{ Running bool }
			NetworkSettings struct {
				Ports map[string][]struct{ HostIP, HostPort string }
			}
		}
		if err != nil || json.Unmarshal(raw, &states) != nil || len(states) != 1 || !states[0].State.Running || states[0].Config.Labels["aiops.testlab.owner"] != c.Owner {
			t.Fatal("real component lacks live exact ownership label")
		}
		bound := false
		for _, bindings := range states[0].NetworkSettings.Ports {
			for _, b := range bindings {
				if b.HostPort == u.Port() && b.HostIP == "127.0.0.1" {
					bound = true
				}
			}
		}
		if !bound {
			t.Fatal("component loopback port is not owned by its labelled container")
		}
		delete(wanted, s.Name)
	}
	if len(wanted) != 0 || producer != "http://127.0.0.1:18578" {
		t.Fatal("all five real targets and the dedicated signal producer required")
	}
	token := strings.TrimSpace(string(privateComponentFile(t, filepath.Join(c.Work, "fixture-owner-token"))))
	challenge := store.ID()
	raw := componentRequest(t, http.MethodGet, producer+"/health?challenge="+challenge, nil, nil, 200)
	var health struct {
		Owner, Proof, Mode string
		PID                int `json:"pid"`
	}
	if json.Unmarshal(raw, &health) != nil {
		t.Fatal("fixture ownership response invalid")
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(fmt.Sprintf("%s|%d|%s", challenge, c.FixturePID, c.Owner)))
	proof, err := hex.DecodeString(health.Proof)
	if err != nil || !hmac.Equal(proof, mac.Sum(nil)) || health.Owner != c.Owner || health.PID != c.FixturePID || health.Mode != "owned_testlab" {
		t.Fatal("controlled producer HMAC ownership proof failed")
	}
	return c, producer
}

func componentRequest(t *testing.T, method, endpoint string, body any, headers map[string]string, status int) []byte {
	t.Helper()
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal("component seed encoding failed")
		}
		input = bytes.NewReader(raw)
	}
	ctx := t.Context()
	if ctx.Err() != nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, method, endpoint, input)
	if err != nil {
		t.Fatal("component request invalid")
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	w, err := client.Do(r)
	if err != nil {
		t.Fatal("owned component transport failed")
	}
	defer w.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(w.Body, 1<<20))
	if err != nil || w.StatusCode != status {
		t.Fatalf("owned component status=%d expected=%d", w.StatusCode, status)
	}
	return raw
}

func nativeTelemetry(t *testing.T, c componentFixture, expression string) *connectors.Telemetry {
	t.Helper()
	app := connectors.ApplicationTelemetry{PrometheusQueries: map[string]string{"requests": expression}, LokiLabels: map[string]string{"application": c.HostConfig.Application, "environment": c.HostConfig.Environment, "testlab_owner": c.Owner}, LokiPatternField: "message_template", LokiCompleteLookbackSeconds: 86400, TempoService: c.HostConfig.Service, TempoAttributes: map[string]string{"application": c.HostConfig.Application, "environment": c.HostConfig.Environment}, TempoSafeOperations: []string{"GET /native-testlab"}}
	other := app
	other.LokiLabels = map[string]string{"application": c.HostConfig.Application, "environment": "staging", "testlab_owner": c.Owner}
	other.TempoAttributes = map[string]string{"application": c.HostConfig.Application, "environment": "staging"}
	endpoint := func(u string) connectors.TelemetryEndpoint {
		return connectors.TelemetryEndpoint{URL: u, AllowHTTP: true}
	}
	p, err := connectors.NewTelemetry(connectors.TelemetryConfig{Prometheus: endpoint(c.HostConfig.Prometheus), Loki: endpoint(c.HostConfig.Loki), Tempo: endpoint(c.HostConfig.Tempo), Applications: map[string]connectors.ApplicationTelemetry{"native-test": app, "native-staging": other}})
	if err != nil {
		t.Fatal("native telemetry configuration rejected")
	}
	return p
}

func componentWindow() connectors.TimeWindow {
	end := time.Now().UTC()
	return connectors.TimeWindow{Start: end.Add(-2 * time.Minute), End: end, StepSeconds: 5}
}

func pollNative(t *testing.T, fn func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	for {
		if fn() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("native adapter did not return this run's exact collected data within 45 seconds")
		case <-time.After(time.Second):
		}
	}
}

func TestNativeRealComponents(t *testing.T) {
	c, producer := readComponentFixture(t)
	t.Run("PrometheusActualScrapeAndNumericSamples", func(t *testing.T) {
		var signal struct {
			Signals  int
			SignalID string `json:"signal_id"`
		}
		if json.Unmarshal(componentRequest(t, http.MethodPost, producer+"/signal", map[string]any{}, nil, 200), &signal) != nil || signal.Signals < 1 || !regexp.MustCompile(`^[a-f0-9]{24}$`).MatchString(signal.SignalID) {
			t.Fatal("controlled signal response invalid")
		}
		p := nativeTelemetry(t, c, `testlab_request_failures_total{application="testlab",environment="test",signal_id="`+signal.SignalID+`"}`)
		pollNative(t, func() bool {
			e, err := p.QueryMetrics(t.Context(), "native-test", "requests", componentWindow())
			if err != nil {
				t.Fatalf("product Prometheus adapter: %v", err)
			}
			if e.Source.Failure != nil {
				t.Fatalf("product Prometheus failure kind=%s", e.Source.Failure.Kind)
			}
			for _, s := range e.Series {
				if s.Labels["signal_id"] == signal.SignalID {
					if s.Labels["application"] != "testlab" || s.Labels["environment"] != "test" || e.Source.Provider != "prometheus" || e.Source.HTTPStatus != 200 {
						t.Fatal("metric source/scope mismatch")
					}
					for _, sample := range s.Samples {
						value, err := strconv.ParseFloat(sample.Value, 64)
						if err == nil && value == float64(signal.Signals) {
							return true
						}
					}
				}
			}
			return false
		})
		t.Log("real fixture workload → Prometheus scrape/storage → product Telemetry.QueryMetrics: exact current signal numeric value; no historical coverage claim")
	})
	t.Run("LokiExactLogAndEnvironmentIsolation", func(t *testing.T) {
		marker := "native-log-" + store.ID()
		stamp := time.Now().UnixNano()
		lineBytes, _ := json.Marshal(map[string]string{"level": "error", "message_template": "native test request failed", "message": marker, "trace_id": store.ID()})
		line := string(lineBytes)
		componentRequest(t, http.MethodPost, c.HostConfig.Loki+"/loki/api/v1/push", map[string]any{"streams": []any{map[string]any{"stream": map[string]string{"application": "testlab", "environment": "test", "service": c.HostConfig.Service, "testlab_owner": c.Owner}, "values": [][]string{{strconv.FormatInt(stamp, 10), line}}}}}, nil, 204)
		p := nativeTelemetry(t, c, `testlab_requests_total{application="testlab",environment="test"}`)
		pollNative(t, func() bool {
			e, err := p.QueryLogsOptions(t.Context(), "native-test", "loki", marker, "error", false, componentWindow(), 10)
			if err != nil {
				t.Fatalf("product Loki adapter: %v", err)
			}
			if e.Source.Failure != nil {
				t.Fatalf("product Loki failure kind=%s", e.Source.Failure.Kind)
			}
			for _, entry := range e.Entries {
				if entry.Line == line {
					if entry.Level != "error" || entry.Labels["environment"] != "test" || entry.TraceID == "" || e.Source.Provider != "loki" || e.Source.HTTPStatus != 200 {
						t.Fatal("log identity/source/enrichment mismatch")
					}
					return true
				}
			}
			return false
		})
		other, err := p.QueryLogsOptions(t.Context(), "native-staging", "loki", marker, "", false, componentWindow(), 10)
		if err != nil || other.Source.Failure != nil || len(other.Entries) != 0 {
			t.Fatal("test log leaked into registered staging scope")
		}
		end := time.Now().UTC().Truncate(time.Millisecond)
		e, err := p.QueryLogsWithPatterns(t.Context(), "native-test", "loki", marker, "error", connectors.TimeWindow{Start: end.Add(-30 * time.Minute), End: end}, 100)
		if err != nil || e.Source.Failure != nil || e.PatternError != "" || e.Summary == nil || e.Summary.Total != 1 {
			t.Fatalf("native complete Loki count failure: err=%v pattern=%s summary=%s entries=%d", err, e.PatternError, e.SummaryError, len(e.Entries))
		}
		t.Log("real Loki push/storage → product QueryLogsOptions: exact JSON line, level and trace identity; generated current sample only")
	})
	t.Run("OTelCollectorTempoExactSpanAndEnvironmentIsolation", func(t *testing.T) {
		traceID, spanID := store.ID(), store.ID()[:16]
		begin := time.Now().UnixNano()
		attr := func(k, v string) map[string]any {
			return map[string]any{"key": k, "value": map[string]string{"stringValue": v}}
		}
		span := map[string]any{"traceId": traceID, "spanId": spanID, "name": "GET /native-testlab", "kind": 2, "startTimeUnixNano": strconv.FormatInt(begin, 10), "endTimeUnixNano": strconv.FormatInt(begin+10000000, 10), "status": map[string]int{"code": 1}}
		payload := map[string]any{"resourceSpans": []any{map[string]any{"resource": map[string]any{"attributes": []any{attr("service.name", c.HostConfig.Service), attr("application", "testlab"), attr("environment", "test")}}, "scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "native-testlab"}, "spans": []any{span}}}}}}
		componentRequest(t, http.MethodPost, c.HostConfig.OTLP, payload, nil, 200)
		p := nativeTelemetry(t, c, `testlab_requests_total{application="testlab",environment="test"}`)
		pollNative(t, func() bool {
			results, err := p.QueryTracesBatchID(t.Context(), []string{"native-test", "native-staging"}, componentWindow(), 10, traceID)
			if err != nil {
				t.Fatalf("product Tempo adapter: %v", err)
			}
			e := results["native-test"]
			if e.Source.Failure != nil {
				if e.Source.Failure.HTTPStatus == 404 {
					return false
				}
				t.Fatalf("product Tempo failure kind=%s status=%d", e.Source.Failure.Kind, e.Source.Failure.HTTPStatus)
			}
			if len(results["native-staging"].Spans) != 0 {
				t.Fatal("trace span leaked into staging scope")
			}
			for _, s := range e.Spans {
				if s.TraceID == traceID && s.SpanID == spanID {
					if s.Service != c.HostConfig.Service || s.Name != "GET /native-testlab" || s.DurationMS != 10 || s.Error || e.Source.Provider != "tempo" || e.Source.HTTPStatus != 200 {
						t.Fatal("exact trace source/identity/duration mismatch")
					}
					return true
				}
			}
			return false
		})
		t.Log("real OTLP HTTP → OTel collector → Tempo storage → product QueryTracesBatchID: exact 10ms span; bounded selected scope, not complete chain/search attestation")
	})
	t.Run("VaultKV2NativeResolverAndFailClosed", func(t *testing.T) {
		path := "testlab-native-" + store.ID()
		marker := store.ID()
		headers := map[string]string{"X-Vault-Token": c.Credentials.VaultToken}
		componentRequest(t, http.MethodPost, c.HostConfig.Vault+"/v1/secret/data/"+path, map[string]any{"data": map[string]string{"token": marker}}, headers, 200)
		t.Cleanup(func() {
			componentRequest(t, http.MethodDelete, c.HostConfig.Vault+"/v1/secret/metadata/"+path, nil, headers, 204)
		})
		reference := "vault://secret/" + path + "#token"
		value, err := v3integrations.NewVaultResolver(c.HostConfig.Vault, c.Credentials.VaultToken).Resolve(t.Context(), reference)
		if err != nil || value != marker {
			t.Fatal("native Vault KV2 resolver failed exact field read")
		}
		if value, err := v3integrations.NewVaultResolver(c.HostConfig.Vault, "invalid-testlab-token").Resolve(t.Context(), reference); value != "" || !errors.Is(err, v3integrations.ErrCredentialUnavailable) {
			t.Fatal("native Vault resolver accepted invalid token")
		}
		if value, err := v3integrations.NewVaultResolver(c.HostConfig.Vault, c.Credentials.VaultToken).Resolve(t.Context(), "vault://secret/"+path+"#absent"); value != "" || !errors.Is(err, v3integrations.ErrCredentialUnavailable) {
			t.Fatal("native Vault resolver did not fail closed for absent field")
		}
		t.Log("real dev Vault KV2 → product credential resolver exact field; invalid token/missing field fail closed; no production Vault policy evidence")
	})
}

// This uses the real cmd/aiops HTTP path, persisted V3 bindings and the native
// provider adapters. The data is a current controlled workload, not production
// traffic or an invented historical population. No new /signal is needed.
func TestFormalBackendNativeHTTP(t *testing.T) {
	f := readFixture(t, true)
	c, _ := readComponentFixture(t)
	admin := authenticatedClient(t, f)
	db, err := pgxpool.New(t.Context(), f.DatabaseURL)
	if err != nil {
		t.Fatal("owned application database unavailable")
	}
	t.Cleanup(db.Close)
	limited := seedHuman(t, f, db, [][2]string{{"environment", "view"}, {"services", "view"}})
	a, b, env := f.BaseURLs[0], f.BaseURLs[1], f.Environment
	traceID, spanID := store.ID(), store.ID()[:16]
	marker := "formal-native-" + store.ID()
	stamp := time.Now().UnixNano()
	lineBytes, _ := json.Marshal(map[string]string{"level": "error", "message_template": "native test request failed", "message": marker, "trace_id": traceID})
	line := string(lineBytes)
	componentRequest(t, http.MethodPost, c.HostConfig.Loki+"/loki/api/v1/push", map[string]any{"streams": []any{map[string]any{"stream": map[string]string{"application": "testlab", "environment": "test", "service": c.HostConfig.Service, "testlab_owner": c.Owner}, "values": [][]string{{strconv.FormatInt(stamp, 10), line}}}}}, nil, 204)
	attr := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]string{"stringValue": v}}
	}
	span := map[string]any{"traceId": traceID, "spanId": spanID, "name": "GET /native-testlab", "kind": 2, "startTimeUnixNano": strconv.FormatInt(stamp, 10), "endTimeUnixNano": strconv.FormatInt(stamp+10000000, 10), "status": map[string]int{"code": 1}}
	componentRequest(t, http.MethodPost, c.HostConfig.OTLP, map[string]any{"resourceSpans": []any{map[string]any{"resource": map[string]any{"attributes": []any{attr("service.name", c.HostConfig.Service), attr("application", "testlab"), attr("environment", "test")}}, "scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "native-testlab-http"}, "spans": []any{span}}}}}}, nil, 200)

	t.Run("V3MetricSamplesMatchActualPrometheusWindow", func(t *testing.T) {
		path := "/api/v1/metrics/samples?service=testlab&query_id=request_rate"
		expect(t, request(t, httpClient(), a, "GET", path, env, nil, nil), 401)
		expect(t, request(t, limited, b, "GET", path, env, nil, nil), 403)
		var actual struct {
			Application, Environment, QueryID, Source, Status string
			Window                                            connectors.TimeWindow
			Samples                                           []connectors.MetricSample
		}
		pollNative(t, func() bool {
			r := request(t, admin, a, "GET", path, env, nil, nil)
			expect(t, r, 200)
			var body struct {
				Application    string `json:"application_id"`
				Environment    string `json:"environment_id"`
				QueryID        string `json:"query_id"`
				Source, Status string
				Window         connectors.TimeWindow
				Samples        []connectors.MetricSample
			}
			if json.Unmarshal(r.Raw, &body) != nil || body.Application != "testlab" || body.Environment != env || body.QueryID != "request_rate" || body.Source != "prometheus" {
				t.Fatal("formal metric source/scope provenance mismatch")
			}
			actual.Application, actual.Environment, actual.QueryID, actual.Source, actual.Status, actual.Window, actual.Samples = body.Application, body.Environment, body.QueryID, body.Source, body.Status, body.Window, body.Samples
			return body.Status == "observed" && len(body.Samples) > 0
		})
		p := nativeTelemetry(t, c, `sum(rate(testlab_requests_total{application="testlab",environment="test"}[1m]))`)
		e, err := p.QueryMetrics(t.Context(), "native-test", "requests", actual.Window)
		if err != nil || e.Source.Failure != nil || len(e.Series) != 1 || len(e.Series[0].Samples) != len(actual.Samples) {
			t.Fatal("formal metric population differs from real native adapter read")
		}
		for i, s := range actual.Samples {
			expected := e.Series[0].Samples[i]
			value, err := strconv.ParseFloat(s.Value, 64)
			want, wantErr := strconv.ParseFloat(expected.Value, 64)
			if err != nil || wantErr != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || s.Timestamp != expected.Timestamp || math.Abs(value-want) > 1e-9 {
				t.Fatal("formal metric sample differs from exact actual provider value/time")
			}
		}
		r := request(t, admin, b, "GET", path, env, nil, nil)
		expect(t, r, 200)
		if r.Data["application_id"] != "testlab" || r.Data["environment_id"] != env || r.Data["source"] != "prometheus" {
			t.Fatal("second instance lost metric registration")
		}
		expect(t, request(t, admin, a, "GET", path, f.OtherEnvironment, nil, nil), 503)
		expect(t, request(t, admin, b, "GET", "/api/v1/metrics/samples?service=testlab&query_id=arbitrary", env, nil, nil), 422)
		t.Log("formal V3 metric binding/query catalog → actual Prometheus: each rate sample matches native adapter at the exact returned window")
	})
	t.Run("LokiHTTPExactLogCountsPatternsAndScope", func(t *testing.T) {
		path := "/api/v1/logs?service=testlab&provider=loki&levels=error&q=" + url.QueryEscape(marker)
		expect(t, request(t, httpClient(), a, "GET", path, env, nil, nil), 401)
		expect(t, request(t, limited, b, "GET", path, env, nil, nil), 403)
		pollNative(t, func() bool {
			r := request(t, admin, a, "GET", path, env, nil, nil)
			if r.Status == 503 {
				p := nativeTelemetry(t, c, `sum(rate(testlab_requests_total{application="testlab",environment="test"}[1m]))`)
				end := time.Now().UTC().Truncate(time.Millisecond)
				result, err := p.QueryLogsWithPatterns(t.Context(), "native-test", "loki", marker, "error", connectors.TimeWindow{Start: end.Add(-30 * time.Minute), End: end}, 100)
				t.Logf("native Loki diagnosis: query_error=%v pattern_error=%s summary_error=%s entries=%d", err, result.PatternError, result.SummaryError, len(result.Entries))
			}
			expect(t, r, 200)
			items, ok := r.Data["items"].([]any)
			if !ok {
				t.Fatal("formal Loki items contract missing")
			}
			if len(items) == 0 {
				return false
			}
			if len(items) != 1 || r.Data["total"] != "1" || r.Data["errors"] != "1" {
				t.Fatal("formal Loki exact current-run counts mismatch")
			}
			item, ok := items[0].(map[string]any)
			if !ok || item["message"] != line || item["level"] != "ERROR" || item["trace"] != traceID || item["service"] != "testlab" {
				t.Fatal("formal Loki exact line/identity/scope mismatch")
			}
			patterns, ok := r.Data["patterns"].([]any)
			if !ok || len(patterns) != 1 {
				t.Fatal("formal Loki pattern population mismatch")
			}
			pattern, ok := patterns[0].(map[string]any)
			if !ok || pattern["template"] != "native test request failed" || pattern["count"] != "1" {
				t.Fatal("formal Loki reviewed template/count mismatch")
			}
			return true
		})
		expect(t, request(t, admin, b, "GET", path, env, nil, nil), 200)
		expect(t, request(t, admin, b, "GET", path, f.OtherEnvironment, nil, nil), 503)
		t.Log("formal Loki registered binding → actual provider: exact current-run line, trace identity, count and reviewed template; 24h configured query lookback, no historical data claim")
	})
	t.Run("TempoHTTPByIDExactSpanAndUnverifiedSearchFailClosed", func(t *testing.T) {
		path := "/api/v1/traces/" + traceID + "?service=testlab"
		expect(t, request(t, httpClient(), a, "GET", path, env, nil, nil), 401)
		// A service-filtered trace outside the viewer's scope deliberately
		// hides source existence with 404, matching the object-read contract.
		expect(t, request(t, limited, b, "GET", path, env, nil, nil), 404)
		pollNative(t, func() bool {
			r := request(t, admin, a, "GET", path, env, nil, nil)
			if r.Status == 404 {
				expect(t, r, 404)
				return false
			}
			expect(t, r, 200)
			if r.Data["trace_id"] != traceID || r.Data["op"] != "GET /native-testlab" || r.Data["duration_ms"] != float64(10) {
				t.Fatal("formal Tempo trace identity/operation/duration mismatch")
			}
			spans, ok := r.Data["spans"].([]any)
			if !ok || len(spans) != 1 {
				t.Fatal("formal Tempo span population mismatch")
			}
			actual, ok := spans[0].(map[string]any)
			if !ok || actual["id"] != spanID || actual["service"] != c.HostConfig.Service || actual["op"] != "GET /native-testlab" {
				t.Fatal("formal Tempo exact span identity/scope mismatch")
			}
			return true
		})
		expect(t, request(t, admin, b, "GET", path, env, nil, nil), 200)
		expect(t, request(t, admin, b, "GET", path, f.OtherEnvironment, nil, nil), 404)
		r := request(t, admin, a, "GET", "/api/v1/traces?service=testlab", env, nil, nil)
		expect(t, r, 503)
		e, ok := r.Data["error"].(map[string]any)
		if !ok || e["code"] != "not_configured" {
			t.Fatal("unverified Tempo search completeness did not fail closed")
		}
		t.Log("formal trace binding → OTel/Tempo exact current-run complete single-span by ID; search remains explicitly unavailable until live-store-lag fail-closed attestation")
	})
}
