package v3alertrules

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrometheusQueryRangeUsesConfiguredEnvironmentAndExactQuery(t *testing.T) {
	start := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)
	expression := "sum by (application) (rate(errors_total{job=\"web+api\"}[5m])) > 0"
	tokenPath := filepath.Join(t.TempDir(), "prometheus-token")
	if err := os.WriteFile(tokenPath, []byte("test-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var testCalls, otherCalls atomic.Int32
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testCalls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/test/api/v1/query_range" || r.URL.Query().Get("query") != expression || r.URL.Query().Get("start") != fmt.Sprint(start.Unix()) || r.URL.Query().Get("end") != fmt.Sprint(end.Unix()) || r.URL.Query().Get("step") != "30" || r.URL.Query().Get("limit") != "101" || r.URL.Query().Get("timeout") != "15s" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("unexpected test request: %s auth=%q", r.URL, r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"application":"z","__name__":"errors"},"values":[[%d,"0"],[%d,"1"]]},{"metric":{"application":"a"},"values":[[%d,"2"]]}]}}`, start.Unix(), end.Unix(), start.Add(time.Minute).Unix())
	}))
	defer testServer.Close()
	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherCalls.Add(1)
		if r.URL.Path != "/other/api/v1/query_range" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected other request: %s auth=%q", r.URL, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer otherServer.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{
		"test":  {URL: testServer.URL + "/test", AllowHTTP: true, BearerTokenFile: tokenPath},
		"other": {URL: otherServer.URL + "/other", AllowHTTP: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	series, err := source.QueryRange(context.Background(), "test", expression, start, end, 30*time.Second)
	if err != nil || len(series) != 2 || series[0].Labels["application"] != "z" || len(series[0].Samples) != 2 || !series[0].Samples[1].Equal(end) || series[1].Labels["application"] != "a" || len(series[1].Samples) != 1 || !series[1].Samples[0].Equal(start.Add(time.Minute)) {
		t.Fatalf("series=%+v err=%v", series, err)
	}
	series, err = source.QueryRange(context.Background(), "other", expression, start, end, time.Minute)
	if err != nil || len(series) != 0 {
		t.Fatalf("empty matrix series=%+v err=%v", series, err)
	}
	if _, err := source.QueryRange(context.Background(), "unknown", expression, start, end, time.Minute); err == nil || testCalls.Load() != 1 || otherCalls.Load() != 1 {
		t.Fatalf("unconfigured environment allowed or extra call: %v calls=%d/%d", err, testCalls.Load(), otherCalls.Load())
	}
}

func TestPrometheusQueryRangeAcceptsFractionalEvaluationGrid(t *testing.T) {
	start := time.Date(2026, 9, 29, 8, 0, 0, 123456789, time.UTC)
	end := start.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("start"), fmt.Sprintf("%d.123456789", start.Unix()); got != want {
			t.Errorf("start=%q want %q", got, want)
		}
		// Prometheus JSON uses floating-point seconds and may round a few ns.
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[%.9f,"1"]]}]}}`, float64(start.Unix())+float64(start.Nanosecond())/1e9)
	}))
	defer server.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	series, err := source.QueryRange(context.Background(), "test", "up", start, end, time.Minute)
	if err != nil || len(series) != 1 || len(series[0].Samples) != 1 {
		t.Fatalf("fractional grid series=%+v err=%v", series, err)
	}
}

func TestPrometheusQueryRangeRejectsUnsafeResponsesAndInputs(t *testing.T) {
	start := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	point := fmt.Sprintf(`[%d,"1"]`, start.Unix())
	valid := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"app":"a"},"values":[` + point + `]}]}}`
	duplicate := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"app":"a"},"values":[` + point + `]},{"metric":{"app":"a"},"values":[` + point + `]}]}}`
	tests := []struct {
		name string
		body string
	}{
		{"warning", `{"status":"success","warnings":["partial data"],"data":{"resultType":"matrix","result":[]}}`},
		{"info", `{"status":"success","infos":["partial data"],"data":{"resultType":"matrix","result":[]}}`},
		{"wrong type", `{"status":"success","data":{"resultType":"vector","result":[]}}`},
		{"null result", `{"status":"success","data":{"resultType":"matrix","result":null}}`},
		{"null metric", `{"status":"success","data":{"resultType":"matrix","result":[{"metric":null,"values":[]}]}}`},
		{"null values", `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":null}]}}`},
		{"histogram omitted from float samples", `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[],"histograms":[[1,{}]]}]}}`},
		{"duplicate labels", duplicate},
		{"duplicate timestamp", `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` + point + `,` + point + `]}]}}`},
		{"out of range", fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[%d,"1"]]}]}}`, end.Add(time.Second).Unix())},
		{"off grid", fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[%d,"1"]]}]}}`, start.Add(30*time.Second).Unix())},
		{"nonfinite value", fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[%d,"NaN"]]}]}}`, start.Unix())},
		{"wrong value shape", fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[%d,1]]}]}}`, start.Unix())},
		{"trailing JSON", valid + `{}`},
		{"too many series", `{"status":"success","data":{"resultType":"matrix","result":[` + strings.TrimSuffix(strings.Repeat(`{"metric":{},"values":[]},`, 101), ",") + `]}}`},
		{"too many samples", `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[` + strings.TrimSuffix(strings.Repeat(point+",", 2001), ",") + `]}]}}`},
		{"body too large", valid + strings.Repeat(" ", 2<<20)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			defer server.Close()
			source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := source.QueryRange(context.Background(), "test", "up", start, end, time.Minute); err == nil {
				t.Fatalf("accepted invalid %s: %+v", tt.name, got)
			}
		})
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(valid))
	}))
	defer server.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		start, end time.Time
		step       time.Duration
	}{
		{start, start, time.Minute},
		{start, start.Add(12*time.Hour + time.Second), time.Minute},
		{start, end, 14 * time.Second},
		{start, end, 5*time.Minute + time.Second},
	} {
		if _, err := source.QueryRange(context.Background(), "test", "up", input.start, input.end, input.step); err == nil {
			t.Fatalf("accepted invalid range %+v", input)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid input reached provider %d times", requests.Load())
	}
}

func TestPrometheusQueryRangeDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { redirected.Add(1); w.WriteHeader(http.StatusOK) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	if _, err := source.QueryRange(context.Background(), "test", "up", start, start.Add(time.Minute), time.Minute); err == nil || redirected.Load() != 0 {
		t.Fatalf("redirect followed: err=%v calls=%d", err, redirected.Load())
	}
}

func TestPrometheusRuleGroupEvaluationInterval(t *testing.T) {
	for _, tt := range []struct {
		name     string
		interval string
		want     time.Duration
		invalid  bool
	}{
		{name: "explicit", interval: `,"interval":45`, want: 45 * time.Second},
		{name: "omitted legacy group", want: 0},
		{name: "explicit zero", interval: `,"interval":0`, invalid: true},
		{name: "explicit null", interval: `,"interval":null`, invalid: true},
		{name: "outside dry-run short", interval: `,"interval":14`, want: 14 * time.Second},
		{name: "outside dry-run long", interval: `,"interval":301`, want: 301 * time.Second},
		{name: "too long for list", interval: `,"interval":86401`, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"groups":[{"file":"rules.yml","name":"g"%s,"rules":[{"type":"alerting","name":"A","query":"up > 0","state":"inactive","duration":0}]}]}}`, tt.interval)
			}))
			defer server.Close()
			source, err := NewPrometheusSource(map[string]PrometheusEndpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			rules, err := source.List(context.Background(), "test")
			if tt.invalid {
				if err == nil {
					t.Fatalf("accepted invalid interval: %+v", rules)
				}
				return
			}
			if err != nil || len(rules) != 1 || rules[0].EvaluationInterval != tt.want {
				t.Fatalf("rules=%+v err=%v want interval=%v", rules, err, tt.want)
			}
		})
	}
}
