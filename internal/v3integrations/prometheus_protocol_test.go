package v3integrations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

func prometheusTestWindow() connectors.TimeWindow {
	end := time.Now().Add(-time.Minute).Truncate(time.Second)
	return connectors.TimeWindow{Start: end.Add(-time.Minute), End: end, StepSeconds: 15}
}

func prometheusCode(err error) string {
	var queryErr *PrometheusQueryError
	if errors.As(err, &queryErr) {
		return queryErr.Code
	}
	return ""
}

func TestQueryRegisteredPrometheusBearerAndEmpty(t *testing.T) {
	window := prometheusTestWindow()
	const token = "private-test-token"
	const expression = "sum(rate(http_requests_total[5m]))"
	respondEmpty := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// PA-43: a POST form; the expression never appears in the URL.
		if err := r.ParseForm(); err != nil || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
			r.URL.RawQuery != "" || r.URL.Path != "/prom/api/v1/query_range" || r.Header.Get("Authorization") != "Bearer "+token ||
			r.PostForm.Get("query") != expression || r.PostForm.Get("limit") != "100" || r.PostForm.Get("timeout") != "5s" || r.PostForm.Get("step") != "15" {
			t.Errorf("unexpected bounded query request: method=%s path=%s query=%q", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if respondEmpty {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"api"},"values":[[%d,"2.5"]]}]}}`, window.Start.Unix())
	}))
	defer server.Close()
	series, err := queryRegisteredPrometheus(context.Background(), server.URL+"/prom", "bearer", token, expression, window, nil)
	if err != nil || len(series) != 1 || series[0].Labels["job"] != "api" || len(series[0].Samples) != 1 || series[0].Samples[0].Value != "2.5" {
		t.Fatalf("series=%+v error=%v", series, err)
	}
	respondEmpty = true
	series, err = queryRegisteredPrometheus(context.Background(), server.URL+"/prom", "bearer", token, expression, window, nil)
	if err != nil || len(series) != 0 {
		t.Fatalf("empty series=%+v error=%v", series, err)
	}
}

func TestQueryRegisteredPrometheusRejectsRedirectUpstreamAndBounds(t *testing.T) {
	window := prometheusTestWindow()
	redirected := false
	status := http.StatusFound
	body := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/private" {
			redirected = true
			return
		}
		if status == http.StatusFound {
			http.Redirect(w, r, "/private", status)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	query := func(w connectors.TimeWindow, credential string) error {
		_, err := queryRegisteredPrometheus(context.Background(), server.URL, "bearer", credential, "up", w, &http.Client{})
		return err
	}
	if code := prometheusCode(query(window, "secret")); code != "upstream" || redirected {
		t.Fatalf("redirect code=%q followed=%t", code, redirected)
	}
	status = http.StatusUnauthorized
	body = "secret in upstream error"
	if err := query(window, "secret"); prometheusCode(err) != "unauthorized" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unauthorized error=%v", err)
	}
	status = http.StatusServiceUnavailable
	if code := prometheusCode(query(window, "secret")); code != "unavailable" {
		t.Fatalf("unavailable code=%q", code)
	}
	if code := prometheusCode(query(window, "bad\r\nheader")); code != "invalid_auth" {
		t.Fatalf("bearer code=%q", code)
	}
	tooWide := window
	tooWide.Start = tooWide.End.Add(-2 * time.Hour)
	if code := prometheusCode(query(tooWide, "secret")); code != "invalid_window" {
		t.Fatalf("window code=%q", code)
	}
	status = http.StatusOK
	body = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	if code := prometheusCode(query(window, "secret")); code != "protocol" {
		t.Fatalf("result type code=%q", code)
	}
	body = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"api"},"values":[[0,"1"]]}]}}`
	if code := prometheusCode(query(window, "secret")); code != "protocol" {
		t.Fatalf("out of window code=%q", code)
	}
	body = fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[%d.9995,"1"]]}]}}`, window.Start.Unix()-1)
	if code := prometheusCode(query(window, "secret")); code != "protocol" {
		t.Fatalf("fractionally out of window code=%q", code)
	}
	body = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[1e1000000,"1"]]}]}}`
	if code := prometheusCode(query(window, "secret")); code != "protocol" {
		t.Fatalf("huge timestamp exponent code=%q", code)
	}
	body = `{"status":"success","data":{"resultType":"matrix","result":null}}`
	if code := prometheusCode(query(window, "secret")); code != "protocol" {
		t.Fatalf("null matrix code=%q", code)
	}
	body = `{"status":"success","warnings":["some series were skipped"],"data":{"resultType":"matrix","result":[]}}`
	if code := prometheusCode(query(window, "secret")); code != "partial" {
		t.Fatalf("warned matrix code=%q", code)
	}
	body = `{"status":"success","infos":["partial result"],"data":{"resultType":"matrix","result":[]}}`
	if code := prometheusCode(query(window, "secret")); code != "partial" {
		t.Fatalf("informational matrix code=%q", code)
	}
	body = `{"status":"success","data":{"resultType":"matrix","result":[` + strings.Repeat(`{},`, 99) + `{}` + `]}}`
	if code := prometheusCode(query(window, "secret")); code != "partial" {
		t.Fatalf("limit-bound matrix code=%q", code)
	}
	body = `{"status":"success","data":{"resultType":"matrix","result":[]}}` + strings.Repeat(" ", 4<<20)
	if code := prometheusCode(query(window, "secret")); code != "limit" {
		t.Fatalf("body limit code=%q", code)
	}
}

// PA-73: the executor's instant query asks at a millisecond-normalized time,
// accepts only samples exactly at it, and passes NaN through for the caller.
func TestQueryRegisteredPrometheusInstant(t *testing.T) {
	at := time.Now().Add(-time.Minute).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	body := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Method != http.MethodPost || r.URL.RawQuery != "" || r.URL.Path != "/api/v1/query" ||
			r.PostForm.Get("time") != at.UTC().Truncate(time.Millisecond).Format(time.RFC3339Nano) {
			t.Errorf("unexpected instant request: %s %s %s", r.Method, r.URL.Path, r.PostForm.Get("time"))
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	ms := fmt.Sprintf("%d.%03d", at.Unix(), at.Nanosecond()/1e6)
	for _, c := range []struct {
		name, body, value, code string
	}{
		{"value", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[` + ms + `,"0.02"]}]}}`, "0.02", ""},
		{"no traffic", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[` + ms + `,"NaN"]}]}}`, "NaN", ""},
		{"other time", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[` + fmt.Sprint(at.Unix()-15) + `,"0.02"]}]}}`, "", "protocol"},
		{"range result", `{"status":"success","data":{"resultType":"matrix","result":[]}}`, "", "protocol"},
		{"warning", `{"status":"success","warnings":["partial"],"data":{"resultType":"vector","result":[]}}`, "", "partial"},
	} {
		body = c.body
		series, err := queryRegisteredPrometheusInstant(context.Background(), server.URL, "none", "", "ratio", at, nil)
		if prometheusCode(err) != c.code || (c.value != "" && (len(series) != 1 || series[0].Samples[0].Value != c.value)) {
			t.Fatalf("%s: series=%+v err=%v", c.name, series, err)
		}
	}
}

// PA-43/PA-73: an 8 KiB expression is accepted and a window with
// sub-millisecond bounds is sent and checked at millisecond resolution.
func TestQueryRegisteredPrometheusLongExpressionAndMillisecondWindow(t *testing.T) {
	end := time.Now().Add(-time.Minute).Truncate(time.Second).Add(987654321 * time.Nanosecond)
	window := connectors.TimeWindow{Start: end.Add(-time.Minute), End: end, StepSeconds: 15}
	expression := "sum(" + strings.Repeat("rate(x[5m])+", 600) + "vector(0))"
	if len(expression) <= 2048 || len(expression) > MaxPrometheusExpression {
		t.Fatalf("expression length %d", len(expression))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("query") != expression {
			t.Errorf("long expression not sent in the form")
		}
		start := window.Start.Truncate(time.Millisecond)
		if r.PostForm.Get("start") != start.UTC().Format(time.RFC3339Nano) {
			t.Errorf("start %s", r.PostForm.Get("start"))
		}
		// Prometheus answers from the millisecond start.
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{},"values":[[%d.%03d,"1"]]}]}}`,
			start.Unix(), start.Nanosecond()/1e6)
	}))
	defer server.Close()
	series, err := queryRegisteredPrometheus(context.Background(), server.URL, "none", "", expression, window, nil)
	if err != nil || len(series) != 1 {
		t.Fatalf("series=%+v err=%v", series, err)
	}
	if _, err := queryRegisteredPrometheus(context.Background(), server.URL, "none", "", strings.Repeat("x", MaxPrometheusExpression+1), window, nil); prometheusCode(err) != "invalid_query" {
		t.Fatalf("over the limit: %v", err)
	}
}

// PA-264: a bin without traffic gives NaN (a histogram quantile with no
// observations). A range read passes it through as a gap for the callers,
// like the instant read; only infinities and malformed values are refused.
func TestQueryRegisteredPrometheusRangeKeepsNaNGaps(t *testing.T) {
	window := prometheusTestWindow()
	value := "NaN"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"panel":"latency_p99_ms"},"values":[[%d,"%s"],[%d,"2.5"]]}]}}`,
			window.Start.Unix(), value, window.Start.Unix()+15)
	}))
	defer server.Close()
	series, err := queryRegisteredPrometheus(context.Background(), server.URL, "none", "", "q", window, nil)
	if err != nil || len(series) != 1 || len(series[0].Samples) != 2 || series[0].Samples[0].Value != "NaN" || series[0].Samples[1].Value != "2.5" {
		t.Fatalf("a NaN bin must stay a gap: series=%+v error=%v", series, err)
	}
	for _, bad := range []string{"+Inf", "-Inf", "not-a-number"} {
		value = bad
		if _, err := queryRegisteredPrometheus(context.Background(), server.URL, "none", "", "q", window, nil); prometheusCode(err) != "protocol" {
			t.Errorf("%s: want protocol error, got %v", bad, err)
		}
	}
}
