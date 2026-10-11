package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PrometheusEndpoint is an explicitly bound, trusted server-side rule source.
// A shared Prometheus may be configured for several environments, but returned
// rules must still carry matching environment and application labels.
type PrometheusEndpoint struct {
	URL             string
	BearerTokenFile string
	AllowHTTP       bool // only for an explicitly trusted local/test endpoint
}

type PrometheusSource struct {
	endpoints map[string]PrometheusEndpoint
	client    *http.Client
}

// TrialSourceConflicts rejects reuse of any broad rule source, including one
// configured for another environment. Distinct values still need the
// operator's data-plane isolation review.
func (s *PrometheusSource) TrialSourceConflicts(endpoint TrialEndpoint) bool {
	if s == nil {
		return false
	}
	for _, current := range s.endpoints {
		if strings.TrimRight(current.URL, "/") == strings.TrimRight(endpoint.URL, "/") ||
			current.BearerTokenFile != "" && current.BearerTokenFile == endpoint.BearerTokenFile {
			return true
		}
	}
	return false
}

// RangeSeries contains the timestamps for which a Prometheus range query
// returned this label set. An alert expression fires whenever it has a vector
// element; its sample value need not be nonzero.
type RangeSeries struct {
	Labels  map[string]string
	Samples []time.Time
}

func NewPrometheusSource(endpoints map[string]PrometheusEndpoint, client *http.Client) (*PrometheusSource, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("no environment-bound Prometheus rule source")
	}
	copy := make(map[string]PrometheusEndpoint, len(endpoints))
	for env, endpoint := range endpoints {
		if env == "" || env == "all" || strings.TrimSpace(env) != env {
			return nil, errors.New("invalid Prometheus rule environment")
		}
		u, err := url.Parse(endpoint.URL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(endpoint.AllowHTTP && u.Scheme == "http")) {
			return nil, errors.New("Prometheus rule URL requires HTTPS without URL credentials/query/fragment")
		}
		if strings.ContainsAny(endpoint.BearerTokenFile, "\r\n") {
			return nil, errors.New("invalid Prometheus token path")
		}
		copy[env] = endpoint
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &PrometheusSource{endpoints: copy, client: &clientCopy}, nil
}

func (s *PrometheusSource) List(ctx context.Context, environment string) ([]ObservedRule, error) {
	if s == nil {
		return nil, errors.New("Prometheus rule source unavailable")
	}
	endpoint, ok := s.endpoints[environment]
	if !ok {
		return nil, errors.New("Prometheus rule source not configured for environment")
	}
	u := strings.TrimRight(endpoint.URL, "/") + "/api/v1/rules?type=alert"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if endpoint.BearerTokenFile != "" {
		b, err := os.ReadFile(endpoint.BearerTokenFile)
		if err != nil {
			return nil, fmt.Errorf("read Prometheus token: %w", err)
		}
		token := strings.TrimSpace(string(b))
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return nil, errors.New("invalid Prometheus token")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus rules HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			Groups []struct {
				Name     string          `json:"name"`
				File     string          `json:"file"`
				Interval json.RawMessage `json:"interval"`
				Rules    []struct {
					Type           string            `json:"type"`
					Name           string            `json:"name"`
					Query          string            `json:"query"`
					State          string            `json:"state"`
					Duration       float64           `json:"duration"`
					Labels         map[string]string `json:"labels"`
					Annotations    map[string]string `json:"annotations"`
					LastEvaluation time.Time         `json:"lastEvaluation"`
					EvaluationTime float64           `json:"evaluationTime"`
					Health         string            `json:"health"`
					LastError      string            `json:"lastError"`
				} `json:"rules"`
			} `json:"groups"`
		} `json:"data"`
	}
	limited := io.LimitReader(resp.Body, (2<<20)+1)
	contents, err := io.ReadAll(limited)
	if err != nil || len(contents) > 2<<20 {
		return nil, errors.New("Prometheus rule response exceeds 2 MiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	if err := decoder.Decode(&envelope); err != nil || envelope.Status != "success" {
		return nil, errors.New("invalid Prometheus rule response")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid trailing Prometheus rule response")
	}
	if len(envelope.Data.Groups) > 500 {
		return nil, errors.New("Prometheus rule group limit exceeded")
	}
	result := []ObservedRule{}
	for _, group := range envelope.Data.Groups {
		var interval time.Duration
		if len(group.Interval) != 0 {
			var seconds float64
			if err := json.Unmarshal(group.Interval, &seconds); err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 24*60*60 {
				return nil, errors.New("invalid Prometheus alert rule interval")
			}
			interval = time.Duration(seconds * float64(time.Second))
			if interval <= 0 {
				return nil, errors.New("invalid Prometheus alert rule interval")
			}
		}
		for _, rule := range group.Rules {
			if rule.Type != "alerting" {
				continue
			}
			if len(result) == 500 {
				return nil, errors.New("Prometheus alert rule limit exceeded")
			}
			if rule.Name == "" || group.Name == "" || group.File == "" || rule.Query == "" || rule.Duration < 0 || rule.EvaluationTime < 0 || (rule.State != "inactive" && rule.State != "pending" && rule.State != "firing") {
				return nil, errors.New("invalid Prometheus alert rule")
			}
			result = append(result, ObservedRule{GroupIdentity: group.File + "\x00" + group.Name, Group: group.Name, Name: rule.Name, Expression: rule.Query, EvaluationInterval: interval,
				State: rule.State, Duration: rule.Duration, Labels: rule.Labels, Annotations: rule.Annotations,
				LastEvaluation: rule.LastEvaluation, EvaluationTime: rule.EvaluationTime,
				Health: rule.Health, LastError: limitText(rule.LastError, 500)})
		}
	}
	return result, nil
}

// QueryRange reads a bounded range from the Prometheus endpoint explicitly
// configured for environment. Callers must pass the server-owned rule query.
func (s *PrometheusSource) QueryRange(ctx context.Context, environment, expression string, start, end time.Time, step time.Duration) ([]RangeSeries, error) {
	if s == nil {
		return nil, errors.New("Prometheus rule source unavailable")
	}
	endpoint, ok := s.endpoints[environment]
	if !ok {
		return nil, errors.New("Prometheus rule source not configured for environment")
	}
	if strings.TrimSpace(expression) == "" || start.IsZero() || end.IsZero() || !start.Before(end) || end.Sub(start) > 12*time.Hour || step < 15*time.Second || step > 5*time.Minute {
		return nil, errors.New("invalid Prometheus range query")
	}
	u, err := url.Parse(strings.TrimRight(endpoint.URL, "/") + "/api/v1/query_range")
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("query", expression)
	query.Set("start", prometheusTimestamp(start))
	query.Set("end", prometheusTimestamp(end))
	query.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))
	query.Set("limit", "101") // one above our cap detects provider-side truncation
	query.Set("timeout", "15s")
	u.RawQuery = query.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if endpoint.BearerTokenFile != "" {
		b, err := os.ReadFile(endpoint.BearerTokenFile)
		if err != nil {
			return nil, fmt.Errorf("read Prometheus token: %w", err)
		}
		token := strings.TrimSpace(string(b))
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return nil, errors.New("invalid Prometheus token")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus range HTTP %d", resp.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(contents) > 2<<20 {
		return nil, errors.New("Prometheus range response exceeds 2 MiB")
	}
	var envelope struct {
		Status   string          `json:"status"`
		Warnings json.RawMessage `json:"warnings"`
		Infos    json.RawMessage `json:"infos"`
		Data     *struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	if err := decoder.Decode(&envelope); err != nil || decoder.Decode(new(any)) != io.EOF || envelope.Status != "success" || envelope.Data == nil || envelope.Data.ResultType != "matrix" {
		return nil, errors.New("invalid Prometheus range response")
	}
	if err := noPrometheusMessages(envelope.Warnings); err != nil {
		return nil, err
	}
	if err := noPrometheusMessages(envelope.Infos); err != nil {
		return nil, err
	}
	if len(envelope.Data.Result) == 0 || string(envelope.Data.Result) == "null" {
		return nil, errors.New("invalid Prometheus range result")
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(envelope.Data.Result, &rows); err != nil || rows == nil || len(rows) > 100 {
		return nil, errors.New("invalid Prometheus range result")
	}
	result := make([]RangeSeries, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, raw := range rows {
		var row struct {
			Metric     json.RawMessage `json:"metric"`
			Values     json.RawMessage `json:"values"`
			Histograms json.RawMessage `json:"histograms"`
		}
		if err := json.Unmarshal(raw, &row); err != nil || len(row.Metric) == 0 || string(row.Metric) == "null" || len(row.Values) == 0 || string(row.Values) == "null" {
			return nil, errors.New("invalid Prometheus range series")
		}
		var labels map[string]string
		var values []json.RawMessage
		if err := json.Unmarshal(row.Metric, &labels); err != nil || labels == nil {
			return nil, errors.New("invalid Prometheus range labels")
		}
		if err := json.Unmarshal(row.Values, &values); err != nil || values == nil || len(values) > 2000 {
			return nil, errors.New("invalid Prometheus range samples")
		}
		if len(row.Histograms) != 0 {
			var histograms []json.RawMessage
			if err := json.Unmarshal(row.Histograms, &histograms); err != nil || histograms == nil || len(histograms) != 0 {
				return nil, errors.New("unsupported Prometheus range histograms")
			}
		}
		// encoding/json sorts map keys, giving an unambiguous label-set key.
		key, _ := json.Marshal(labels)
		if _, duplicate := seen[string(key)]; duplicate {
			return nil, errors.New("duplicate Prometheus range label set")
		}
		seen[string(key)] = struct{}{}
		series := RangeSeries{Labels: labels, Samples: make([]time.Time, 0, len(values))}
		for _, value := range values {
			var pair []json.RawMessage
			if err := json.Unmarshal(value, &pair); err != nil || len(pair) != 2 {
				return nil, errors.New("invalid Prometheus range sample")
			}
			seconds, err := strconv.ParseFloat(string(pair[0]), 64)
			if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < float64(start.Unix())-1 || seconds > float64(end.Unix())+1 {
				return nil, errors.New("invalid Prometheus range timestamp")
			}
			sec, fraction := math.Modf(seconds)
			at := time.Unix(int64(sec), int64(math.Round(fraction*1e9))).UTC()
			if at.Before(start.Add(-50*time.Millisecond)) || at.After(end.Add(50*time.Millisecond)) || (len(series.Samples) > 0 && !at.After(series.Samples[len(series.Samples)-1])) {
				return nil, errors.New("invalid Prometheus range timestamp")
			}
			index := (at.Sub(start) + step/2) / step
			aligned := start.Add(index * step)
			if drift := at.Sub(aligned); drift < -50*time.Millisecond || drift > 50*time.Millisecond {
				return nil, errors.New("off-grid Prometheus range timestamp")
			}
			var numeric string
			if err := json.Unmarshal(pair[1], &numeric); err != nil {
				return nil, errors.New("invalid Prometheus range value")
			}
			n, err := strconv.ParseFloat(numeric, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, errors.New("invalid Prometheus range value")
			}
			series.Samples = append(series.Samples, at)
		}
		result = append(result, series)
	}
	// Keep output stable even if Prometheus returns the same series in a new order.
	sort.Slice(result, func(i, j int) bool {
		a, _ := json.Marshal(result[i].Labels)
		b, _ := json.Marshal(result[j].Labels)
		return string(a) < string(b)
	})
	return result, nil
}

func prometheusTimestamp(at time.Time) string {
	if at.Nanosecond() == 0 {
		return strconv.FormatInt(at.Unix(), 10)
	}
	return strconv.FormatInt(at.Unix(), 10) + "." + strings.TrimRight(fmt.Sprintf("%09d", at.Nanosecond()), "0")
}

func noPrometheusMessages(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var messages []string
	if err := json.Unmarshal(raw, &messages); err != nil || messages == nil || len(messages) != 0 {
		return errors.New("Prometheus range response contains warnings or infos")
	}
	return nil
}

// limitText keeps at most limit runes of provider text.
func limitText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
