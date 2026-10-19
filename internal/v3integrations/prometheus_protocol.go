package v3integrations

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

// PrometheusQueryError deliberately carries no endpoint, expression, credential,
// or upstream response text. Callers can safely expose its code.
type PrometheusQueryError struct{ Code string }

func (e *PrometheusQueryError) Error() string { return "prometheus query: " + e.Code }

func prometheusQueryError(code string) error { return &PrometheusQueryError{Code: code} }

func queryRegisteredPrometheus(ctx context.Context, rawEndpoint, authMethod, credential, expression string, window connectors.TimeWindow, client *http.Client) ([]connectors.MetricSeries, error) {
	base, err := validEndpoint(rawEndpoint)
	if err != nil {
		return nil, prometheusQueryError("invalid_endpoint")
	}
	if authMethod != "none" && authMethod != "bearer" {
		return nil, prometheusQueryError("invalid_auth")
	}
	if authMethod == "none" && credential != "" {
		return nil, prometheusQueryError("invalid_auth")
	}
	if authMethod == "bearer" {
		if credential == "" || len(credential) > 4096 {
			return nil, prometheusQueryError("invalid_auth")
		}
		for _, b := range []byte(credential) {
			if b <= ' ' || b >= 0x7f {
				return nil, prometheusQueryError("invalid_auth")
			}
		}
	}
	if strings.TrimSpace(expression) == "" || len(expression) > MaxPrometheusExpression {
		return nil, prometheusQueryError("invalid_query")
	}
	// Prometheus evaluates at millisecond resolution; the request and the
	// sample bounds below use the same millisecond window (PA-73).
	window.Start, window.End = window.Start.Truncate(time.Millisecond), window.End.Truncate(time.Millisecond)
	now := time.Now()
	duration := window.End.Sub(window.Start)
	if window.Start.IsZero() || window.End.IsZero() || duration <= 0 || duration > time.Hour || window.Start.Before(now.Add(-24*time.Hour)) || window.End.After(now.Add(time.Minute)) || window.StepSeconds < 15 || window.StepSeconds > 3600 || int64(duration/time.Second)/int64(window.StepSeconds)+1 > 500 {
		return nil, prometheusQueryError("invalid_window")
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v1/query_range"
	endpoint.RawPath = ""
	// A POST form keeps the reviewed expression out of URLs, proxy limits
	// and access logs (PA-43).
	form := url.Values{
		"query":   {expression},
		"start":   {window.Start.UTC().Format(time.RFC3339Nano)},
		"end":     {window.End.UTC().Format(time.RFC3339Nano)},
		"step":    {strconv.Itoa(window.StepSeconds)},
		"timeout": {"5s"},
		"limit":   {"100"},
	}.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), strings.NewReader(form))
	if err != nil {
		return nil, prometheusQueryError("invalid_endpoint")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if authMethod == "bearer" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	boundedClient := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if client != nil {
		boundedClient.Transport = client.Transport
	}
	response, err := boundedClient.Do(request)
	if err != nil {
		return nil, prometheusQueryError("connection_failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, prometheusQueryError("unauthorized")
	}
	if response.StatusCode >= 500 {
		return nil, prometheusQueryError("unavailable")
	}
	if response.StatusCode != http.StatusOK {
		return nil, prometheusQueryError("upstream")
	}
	const maxBody = 4 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, prometheusQueryError("connection_failed")
	}
	if len(body) > maxBody {
		return nil, prometheusQueryError("limit")
	}
	var envelope struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Infos    []string `json:"infos"`
		Data     struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Status != "success" || envelope.Data.ResultType != "matrix" {
		return nil, prometheusQueryError("protocol")
	}
	// A successful HTTP response may still be partial. Do not present a
	// warned or informational result as complete dashboard evidence.
	if len(envelope.Warnings) != 0 || len(envelope.Infos) != 0 {
		return nil, prometheusQueryError("partial")
	}
	if len(envelope.Data.Result) == 0 || string(envelope.Data.Result) == "null" {
		return nil, prometheusQueryError("protocol")
	}
	var result []struct {
		Metric     map[string]string   `json:"metric"`
		Values     [][]json.RawMessage `json:"values"`
		Histograms []json.RawMessage   `json:"histograms"`
	}
	if json.Unmarshal(envelope.Data.Result, &result) != nil {
		return nil, prometheusQueryError("protocol")
	}
	if len(result) > 100 {
		return nil, prometheusQueryError("limit")
	}
	if len(result) == 100 {
		// The request's limit=100 may have truncated a larger result.
		return nil, prometheusQueryError("partial")
	}
	series := make([]connectors.MetricSeries, 0, len(result))
	first := new(big.Rat).SetFrac(big.NewInt(window.Start.UnixNano()), big.NewInt(1e9))
	last := new(big.Rat).SetFrac(big.NewInt(window.End.UnixNano()), big.NewInt(1e9))
	for _, item := range result {
		if item.Metric == nil || item.Values == nil {
			return nil, prometheusQueryError("protocol")
		}
		if len(item.Metric) > 64 || len(item.Values) > 500 {
			return nil, prometheusQueryError("limit")
		}
		if len(item.Histograms) != 0 {
			return nil, prometheusQueryError("protocol")
		}
		for key, value := range item.Metric {
			if len(key) == 0 || len(key) > 128 || len(value) > 1024 || strings.ContainsAny(key, "\r\n\x00") || strings.ContainsAny(value, "\r\n\x00") {
				return nil, prometheusQueryError("limit")
			}
		}
		line := connectors.MetricSeries{Labels: item.Metric, Samples: make([]connectors.MetricSample, 0, len(item.Values))}
		for _, pair := range item.Values {
			var timestamp json.Number
			var value string
			if len(pair) != 2 || len(pair[0]) > 64 || len(pair[1]) > 128 ||
				json.Unmarshal(pair[0], &timestamp) != nil || json.Unmarshal(pair[1], &value) != nil {
				return nil, prometheusQueryError("protocol")
			}
			asFloat, tsErr := timestamp.Float64()
			parsed, err := strconv.ParseFloat(value, 64)
			// NaN is a bin without a value (a quantile over no traffic) and goes
			// to the callers as a gap, like the instant read; infinities and
			// malformed values are refused (PA-264).
			if tsErr != nil || math.IsNaN(asFloat) || math.IsInf(asFloat, 0) ||
				asFloat < float64(window.Start.Unix())-1 || asFloat > float64(window.End.Unix())+1 ||
				err != nil || math.IsInf(parsed, 0) {
				return nil, prometheusQueryError("protocol")
			}
			// The cheap float check above rules out pathological exponents before
			// exact decimal comparison with the requested nanosecond bounds.
			precise, valid := new(big.Rat).SetString(timestamp.String())
			if !valid || precise.Cmp(first) < 0 || precise.Cmp(last) > 0 {
				return nil, prometheusQueryError("protocol")
			}
			line.Samples = append(line.Samples, connectors.MetricSample{Timestamp: asFloat, Value: value})
		}
		series = append(series, line)
	}
	return series, nil
}

// queryRegisteredPrometheusInstant evaluates expression at one instant
// (/api/v1/query) for the rollback executor, which only judges the newest
// value: a range query also returns the earlier points, where a scrape gap
// (Pods being replaced) yields NaN and fails the whole read. The time is
// normalized to Prometheus' millisecond resolution and every returned sample
// must be exactly at it. NaN and ±Inf values are returned as they are; the
// caller decides what a missing value means.
func queryRegisteredPrometheusInstant(ctx context.Context, rawEndpoint, authMethod, credential, expression string, at time.Time, client *http.Client) ([]connectors.MetricSeries, error) {
	base, err := validEndpoint(rawEndpoint)
	if err != nil {
		return nil, prometheusQueryError("invalid_endpoint")
	}
	if (authMethod != "none" && authMethod != "bearer") || (authMethod == "none" && credential != "") {
		return nil, prometheusQueryError("invalid_auth")
	}
	if authMethod == "bearer" {
		if credential == "" || len(credential) > 4096 {
			return nil, prometheusQueryError("invalid_auth")
		}
		for _, b := range []byte(credential) {
			if b <= ' ' || b >= 0x7f {
				return nil, prometheusQueryError("invalid_auth")
			}
		}
	}
	if strings.TrimSpace(expression) == "" || len(expression) > MaxPrometheusExpression {
		return nil, prometheusQueryError("invalid_query")
	}
	at = at.UTC().Truncate(time.Millisecond)
	if now := time.Now(); at.IsZero() || at.Before(now.Add(-24*time.Hour)) || at.After(now.Add(time.Minute)) {
		return nil, prometheusQueryError("invalid_window")
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v1/query"
	endpoint.RawPath = ""
	form := url.Values{"query": {expression}, "time": {at.Format(time.RFC3339Nano)}, "timeout": {"5s"},
		"limit": {"100"}}.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), strings.NewReader(form))
	if err != nil {
		return nil, prometheusQueryError("invalid_endpoint")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if authMethod == "bearer" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	boundedClient := http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if client != nil {
		boundedClient.Transport = client.Transport
	}
	response, err := boundedClient.Do(request)
	if err != nil {
		return nil, prometheusQueryError("connection_failed")
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, prometheusQueryError("unauthorized")
	case response.StatusCode >= 500:
		return nil, prometheusQueryError("unavailable")
	case response.StatusCode != http.StatusOK:
		return nil, prometheusQueryError("upstream")
	}
	const maxBody = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return nil, prometheusQueryError("connection_failed")
	}
	if len(body) > maxBody {
		return nil, prometheusQueryError("limit")
	}
	var envelope struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Infos    []string `json:"infos"`
		Data     struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Status != "success" || envelope.Data.ResultType != "vector" {
		return nil, prometheusQueryError("protocol")
	}
	if len(envelope.Warnings) != 0 || len(envelope.Infos) != 0 {
		return nil, prometheusQueryError("partial")
	}
	if len(envelope.Data.Result) >= 100 {
		return nil, prometheusQueryError("partial")
	}
	want := new(big.Rat).SetFrac(big.NewInt(at.UnixMilli()), big.NewInt(1000))
	series := make([]connectors.MetricSeries, 0, len(envelope.Data.Result))
	for _, item := range envelope.Data.Result {
		if item.Metric == nil || len(item.Metric) > 64 || len(item.Value) != 2 || len(item.Value[0]) > 64 || len(item.Value[1]) > 128 {
			return nil, prometheusQueryError("protocol")
		}
		var timestamp json.Number
		var value string
		if json.Unmarshal(item.Value[0], &timestamp) != nil || json.Unmarshal(item.Value[1], &value) != nil {
			return nil, prometheusQueryError("protocol")
		}
		precise, valid := new(big.Rat).SetString(timestamp.String())
		asFloat, tsErr := timestamp.Float64()
		if _, err := strconv.ParseFloat(value, 64); err != nil || !valid || tsErr != nil || precise.Cmp(want) != 0 {
			return nil, prometheusQueryError("protocol")
		}
		for key, label := range item.Metric {
			if len(key) == 0 || len(key) > 128 || len(label) > 1024 || strings.ContainsAny(key, "\r\n\x00") || strings.ContainsAny(label, "\r\n\x00") {
				return nil, prometheusQueryError("limit")
			}
		}
		series = append(series, connectors.MetricSeries{Labels: item.Metric,
			Samples: []connectors.MetricSample{{Timestamp: asFloat, Value: value}}})
	}
	return series, nil
}
