package connectors

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TelemetryConfig is trusted server configuration. Models can choose a registered
// query ID, but cannot change a query expression, endpoint, credential or scope.
type TelemetryConfig struct {
	Prometheus          TelemetryEndpoint               `json:"prometheus"`
	Elasticsearch       TelemetryEndpoint               `json:"elasticsearch"`
	Tempo               TelemetryEndpoint               `json:"tempo"`
	Loki                TelemetryEndpoint               `json:"loki"`
	Applications        map[string]ApplicationTelemetry `json:"applications"`
	TimeoutSeconds      int                             `json:"timeout_seconds"`
	MaxWindowSeconds    int                             `json:"max_window_seconds"`
	MaxLookbackSeconds  int                             `json:"max_lookback_seconds"`
	MinStepSeconds      int                             `json:"min_step_seconds"`
	MaxSamplesPerSeries int                             `json:"max_samples_per_series"`
	MaxLogEntries       int                             `json:"max_log_entries"`
}
type TelemetryEndpoint struct {
	URL             string `json:"url"`
	BearerTokenFile string `json:"bearer_token_file,omitempty"`
	Username        string `json:"username,omitempty"`
	PasswordFile    string `json:"password_file,omitempty"`
	CAFile          string `json:"ca_file,omitempty"`
	TenantID        string `json:"tenant_id,omitempty"`
	AllowHTTP       bool   `json:"allow_http,omitempty"`
}
type ApplicationTelemetry struct {
	// Every expression is reviewed and registered for this application by an admin.
	// Arbitrary PromQL is intentionally absent from the public request interface.
	PrometheusQueries     map[string]string `json:"prometheus_queries"`
	LokiLabels            map[string]string `json:"loki_labels"`
	ElasticsearchIndex    string            `json:"elasticsearch_index"`
	ElasticsearchFilters  map[string]string `json:"elasticsearch_filters"`
	ElasticsearchPrefixes map[string]string `json:"elasticsearch_prefixes"`
	TempoService          string            `json:"tempo_service"`
	TempoAttributes       map[string]string `json:"tempo_attributes"`
}
type TimeWindow struct {
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	StepSeconds int       `json:"step_seconds"`
}
type TelemetrySource struct {
	Provider      string          `json:"provider"`
	URL           string          `json:"url"`
	ApplicationID string          `json:"application_id"`
	QueryID       string          `json:"query_id,omitempty"`
	Query         string          `json:"query"`
	Window        TimeWindow      `json:"window"`
	CapturedAt    time.Time       `json:"captured_at"`
	HTTPStatus    int             `json:"http_status"`
	Warnings      []string        `json:"warnings,omitempty"`
	Failure       *TelemetryError `json:"failure,omitempty"`
}
type TelemetryError struct {
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Retryable  bool   `json:"retryable"`
	cause      error
}

func (e *TelemetryError) Error() string { return e.Kind + ": " + e.Message }
func (e *TelemetryError) Unwrap() error { return e.cause }

type MetricSample struct {
	Timestamp float64 `json:"timestamp"`
	Value     string  `json:"value"`
}
type MetricSeries struct {
	Labels  map[string]string `json:"labels"`
	Samples []MetricSample    `json:"samples"`
}
type MetricEvidence struct {
	Source       TelemetrySource `json:"source"`
	Series       []MetricSeries  `json:"series"`
	Empty        bool            `json:"empty"`
	LimitReached bool            `json:"limit_reached"`
}
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Line      string            `json:"line"`
	Labels    map[string]string `json:"labels"`
	Level     string            `json:"level,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
}
type LogBucket struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int64     `json:"count"`
}
type LogSummary struct {
	Total         int64       `json:"total"`
	ErrorCount    *int64      `json:"error_count,omitempty"`
	BucketSeconds int         `json:"bucket_seconds"`
	Buckets       []LogBucket `json:"buckets"`
}
type LogPattern struct {
	Template string      `json:"template"`
	Count    int64       `json:"count"`
	Buckets  []LogBucket `json:"buckets"`
}
type LogPatternSummary struct {
	Patterns []LogPattern `json:"patterns"`
}
type LogEvidence struct {
	Summary        *LogSummary        `json:"summary,omitempty"`
	SummaryError   string             `json:"summary_error,omitempty"`
	PatternSummary *LogPatternSummary `json:"pattern_summary,omitempty"`
	PatternError   string             `json:"pattern_error,omitempty"`
	Source         TelemetrySource    `json:"source"`
	Entries        []LogEntry         `json:"entries"`
	Empty          bool               `json:"empty"`
	LimitReached   bool               `json:"limit_reached"`
}
type telemetryClient struct {
	config TelemetryEndpoint
	client *http.Client
}
type Telemetry struct {
	config        TelemetryConfig
	prometheus    *telemetryClient
	loki          *telemetryClient
	elasticsearch *telemetryClient
	tempo         *telemetryClient
	now           func() time.Time
}

var telemetryLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func LoadTelemetryConfig(path string) (TelemetryConfig, error) {
	var c TelemetryConfig
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func NewTelemetry(config TelemetryConfig) (*Telemetry, error) {
	// Deep-copy registration maps, so a caller cannot expand the scope after construction.
	b, e := json.Marshal(config)
	if e != nil {
		return nil, e
	}
	var c TelemetryConfig
	if e = json.Unmarshal(b, &c); e != nil {
		return nil, e
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 15
	}
	if c.MaxWindowSeconds == 0 {
		c.MaxWindowSeconds = 3600
	}
	if c.MaxLookbackSeconds == 0 {
		c.MaxLookbackSeconds = 7 * 86400
	}
	if c.MinStepSeconds == 0 {
		c.MinStepSeconds = 5
	}
	if c.MaxSamplesPerSeries == 0 {
		c.MaxSamplesPerSeries = 1000
	}
	if c.MaxLogEntries == 0 {
		c.MaxLogEntries = 1000
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 60 || c.MaxWindowSeconds < 1 || c.MaxWindowSeconds > 86400 || c.MaxLookbackSeconds < c.MaxWindowSeconds || c.MaxLookbackSeconds > 366*86400 || c.MinStepSeconds < 1 || c.MinStepSeconds > c.MaxWindowSeconds || c.MaxSamplesPerSeries < 2 || c.MaxSamplesPerSeries > 11000 || c.MaxLogEntries < 1 || c.MaxLogEntries > 5000 {
		return nil, errors.New("invalid telemetry time or result limits")
	}
	if len(c.Applications) == 0 {
		return nil, errors.New("at least one telemetry application must be registered")
	}
	for app, a := range c.Applications {
		if strings.TrimSpace(app) == "" {
			return nil, errors.New("empty telemetry application ID")
		}
		for id, q := range a.PrometheusQueries {
			if strings.TrimSpace(id) == "" || strings.TrimSpace(q) == "" || len(q) > 16384 {
				return nil, fmt.Errorf("invalid registered Prometheus query for %s", app)
			}
		}
		for key, value := range a.LokiLabels {
			if !telemetryLabelName.MatchString(key) || value == "" || len(value) > 1024 {
				return nil, fmt.Errorf("invalid Loki scope for %s", app)
			}
		}
	}
	t := &Telemetry{config: c, now: time.Now}
	if c.Prometheus.URL != "" {
		t.prometheus, e = newTelemetryClient(c.Prometheus, c.TimeoutSeconds)
		if e != nil {
			return nil, e
		}
	}
	if c.Loki.URL != "" {
		t.loki, e = newTelemetryClient(c.Loki, c.TimeoutSeconds)
		if e != nil {
			return nil, e
		}
	}
	if c.Elasticsearch.URL != "" {
		t.elasticsearch, e = newTelemetryClient(c.Elasticsearch, c.TimeoutSeconds)
		if e != nil {
			return nil, e
		}
	}
	if c.Tempo.URL != "" {
		t.tempo, e = newTelemetryClient(c.Tempo, c.TimeoutSeconds)
		if e != nil {
			return nil, e
		}
	}
	if e = validateExtendedTelemetry(c); e != nil {
		return nil, e
	}
	if t.prometheus == nil && t.loki == nil && t.elasticsearch == nil && t.tempo == nil {
		return nil, errors.New("no telemetry endpoint configured")
	}
	return t, nil
}
func newTelemetryClient(c TelemetryEndpoint, timeout int) (*telemetryClient, error) {
	u, e := url.Parse(c.URL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(c.AllowHTTP && u.Scheme == "http")) {
		return nil, errors.New("telemetry URL requires HTTPS without credentials/query/fragment; local HTTP must be explicitly enabled")
	}
	if (c.BearerTokenFile != "" && (c.Username != "" || c.PasswordFile != "")) || (c.Username == "") != (c.PasswordFile == "") {
		return nil, errors.New("configure exactly one complete telemetry authentication method")
	}
	if strings.ContainsAny(c.TenantID, "\r\n") || strings.Contains(c.TenantID, "|") {
		return nil, errors.New("invalid telemetry tenant ID")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, e := os.ReadFile(c.CAFile)
		if e != nil {
			return nil, e
		}
		pool, e := x509.SystemCertPool()
		if e != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid telemetry CA")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	return &telemetryClient{config: c, client: &http.Client{Transport: tr, Timeout: time.Duration(timeout) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (t *Telemetry) window(w TimeWindow, metrics bool) (TimeWindow, error) {
	now := t.now().UTC()
	if w.End.IsZero() {
		w.End = now
	}
	if w.Start.IsZero() {
		w.Start = w.End.Add(-min(15*time.Minute, time.Duration(t.config.MaxWindowSeconds)*time.Second))
	}
	w.Start = w.Start.UTC()
	w.End = w.End.UTC()
	if !w.Start.Before(w.End) || w.End.After(now.Add(5*time.Second)) || w.Start.Before(now.Add(-time.Duration(t.config.MaxLookbackSeconds)*time.Second)) || w.End.Sub(w.Start) > time.Duration(t.config.MaxWindowSeconds)*time.Second {
		return w, errors.New("telemetry window is empty, future, too old or exceeds configured scope")
	}
	if metrics {
		if w.StepSeconds == 0 {
			// Choose defaults inside configured bounds. Explicit caller values are
			// still rejected below rather than silently rewritten.
			intervals := time.Duration(t.config.MaxSamplesPerSeries-1) * time.Second
			stepForBudget := int((w.End.Sub(w.Start) + intervals - 1) / intervals)
			w.StepSeconds = min(t.config.MaxWindowSeconds, max(15, t.config.MinStepSeconds, stepForBudget))
		}
		if w.StepSeconds < t.config.MinStepSeconds || w.StepSeconds > t.config.MaxWindowSeconds || w.End.Sub(w.Start)/(time.Duration(w.StepSeconds)*time.Second)+1 > time.Duration(t.config.MaxSamplesPerSeries) {
			return w, errors.New("telemetry step exceeds configured resolution or sample limits")
		}
	} else {
		w.StepSeconds = 0
	}
	return w, nil
}
func failure(source *TelemetrySource, kind, message string, cause error) error {
	e := &TelemetryError{Kind: kind, Message: message, HTTPStatus: source.HTTPStatus, Retryable: kind == "timeout" || source.HTTPStatus == 429 || source.HTTPStatus >= 500, cause: cause}
	source.Failure = e
	return e
}
func (c *telemetryClient) get(ctx context.Context, path string, params url.Values, source *TelemetrySource) ([]byte, error) {
	return c.request(ctx, "GET", path+"?"+params.Encode(), nil, source)
}
func (c *telemetryClient) request(ctx context.Context, method, path string, body []byte, source *TelemetrySource) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.config.URL, "/")+path, bytes.NewReader(body))
	if e != nil {
		return nil, failure(source, "configuration", "invalid telemetry request", e)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.config.BearerTokenFile != "" {
		b, e := os.ReadFile(c.config.BearerTokenFile)
		if e != nil {
			return nil, failure(source, "credentials", "cannot read telemetry credentials", e)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(b)))
	}
	if c.config.PasswordFile != "" {
		b, e := os.ReadFile(c.config.PasswordFile)
		if e != nil {
			return nil, failure(source, "credentials", "cannot read telemetry credentials", e)
		}
		req.SetBasicAuth(c.config.Username, strings.TrimSpace(string(b)))
	}
	if c.config.TenantID != "" {
		req.Header.Set("X-Scope-OrgID", c.config.TenantID)
	}
	resp, e := c.client.Do(req)
	if e != nil {
		kind := "transport"
		if errors.Is(e, context.DeadlineExceeded) {
			kind = "timeout"
		}
		if errors.Is(e, context.Canceled) {
			kind = "cancelled"
		}
		return nil, failure(source, kind, "telemetry request did not complete", e)
	}
	defer resp.Body.Close()
	source.HTTPStatus = resp.StatusCode
	b, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil {
		return nil, failure(source, "transport", "cannot read telemetry response", e)
	}
	if len(b) > 4<<20 {
		return nil, failure(source, "limit", "telemetry response exceeds 4 MiB", nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("telemetry endpoint returned HTTP %d", resp.StatusCode)
		var upstream struct {
			ErrorType string `json:"errorType"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(b, &upstream) == nil && upstream.Error != "" {
			detail := upstream.ErrorType + ": " + upstream.Error
			if len(detail) > 512 {
				detail = detail[:512]
			}
			message += ": " + detail
		}
		return nil, failure(source, "http", message, nil)
	}
	return b, nil
}

type telemetryEnvelope struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
	ErrorType string   `json:"errorType"`
	Error     string   `json:"error"`
	Warnings  []string `json:"warnings"`
	Infos     []string `json:"infos"`
}

func decodeTelemetry(b []byte, s *TelemetrySource) (telemetryEnvelope, error) {
	var v telemetryEnvelope
	if e := json.Unmarshal(b, &v); e != nil {
		return v, failure(s, "protocol", "invalid telemetry JSON", e)
	}
	s.Warnings = append(v.Warnings, v.Infos...)
	if v.Status != "success" {
		message := v.Error
		if len(message) > 512 {
			message = message[:512]
		}
		return v, failure(s, "upstream", v.ErrorType+": "+message, nil)
	}
	return v, nil
}
func (t *Telemetry) QueryMetrics(ctx context.Context, appID, queryID string, w TimeWindow) (MetricEvidence, error) {
	out := MetricEvidence{Source: TelemetrySource{Provider: "prometheus", ApplicationID: appID, QueryID: queryID, CapturedAt: t.now().UTC()}, Series: []MetricSeries{}, Empty: true}
	s := &out.Source
	if t.prometheus == nil {
		return out, failure(s, "configuration", "Prometheus is not configured", nil)
	}
	s.URL = strings.TrimRight(t.prometheus.config.URL, "/") + "/api/v1/query_range"
	a, ok := t.config.Applications[appID]
	if !ok {
		return out, failure(s, "scope", "application is not registered for telemetry", nil)
	}
	query, ok := a.PrometheusQueries[queryID]
	if !ok {
		return out, failure(s, "scope", "metric query ID is not registered for this application", nil)
	}
	s.Query = query
	var e error
	s.Window, e = t.window(w, true)
	if e != nil {
		return out, failure(s, "scope", e.Error(), e)
	}
	params := url.Values{"query": {query}, "start": {s.Window.Start.Format(time.RFC3339Nano)}, "end": {s.Window.End.Format(time.RFC3339Nano)}, "step": {strconv.Itoa(s.Window.StepSeconds)}, "timeout": {strconv.Itoa(t.config.TimeoutSeconds) + "s"}, "limit": {"100"}}
	b, e := t.prometheus.get(ctx, "/api/v1/query_range", params, s)
	if e != nil {
		return out, e
	}
	v, e := decodeTelemetry(b, s)
	if e != nil {
		return out, e
	}
	if v.Data.ResultType != "matrix" {
		return out, failure(s, "protocol", "Prometheus range result must be a matrix", nil)
	}
	var series []struct {
		Metric     map[string]string   `json:"metric"`
		Values     [][]json.RawMessage `json:"values"`
		Histograms []json.RawMessage   `json:"histograms"`
	}
	if e = json.Unmarshal(v.Data.Result, &series); e != nil {
		return out, failure(s, "protocol", "invalid Prometheus series", e)
	}
	if len(series) > 100 {
		return out, failure(s, "limit", "Prometheus returned more than 100 series", nil)
	}
	out.LimitReached = len(series) == 100
	for _, item := range series {
		if len(item.Histograms) > 0 {
			return out, failure(s, "unsupported", "native histogram queries need a registered scalar reduction expression", nil)
		}
		if len(item.Values) > t.config.MaxSamplesPerSeries {
			return out, failure(s, "limit", "Prometheus returned too many samples", nil)
		}
		line := MetricSeries{Labels: item.Metric, Samples: []MetricSample{}}
		for _, pair := range item.Values {
			var p MetricSample
			if len(pair) != 2 || json.Unmarshal(pair[0], &p.Timestamp) != nil || json.Unmarshal(pair[1], &p.Value) != nil {
				return out, failure(s, "protocol", "invalid Prometheus sample", nil)
			}
			if _, e := strconv.ParseFloat(p.Value, 64); e != nil {
				return out, failure(s, "protocol", "invalid Prometheus numeric value", e)
			}
			// Prometheus evaluation timestamps have millisecond precision.
			start := float64(s.Window.Start.UnixNano()) / 1e9
			end := float64(s.Window.End.UnixNano()) / 1e9
			if p.Timestamp < start-0.001 || p.Timestamp > end+0.001 {
				return out, failure(s, "scope", "Prometheus returned a sample outside requested time window", nil)
			}
			line.Samples = append(line.Samples, p)
		}
		if len(line.Samples) > 0 {
			out.Empty = false
		}
		out.Series = append(out.Series, line)
	}
	return out, nil
}
func lokiSelector(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Quote(labels[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
func (t *Telemetry) QueryLogs(ctx context.Context, appID string, w TimeWindow, limit int) (LogEvidence, error) {
	return t.queryLoki(ctx, appID, w, limit, "")
}
func (t *Telemetry) queryLoki(ctx context.Context, appID string, w TimeWindow, limit int, search string) (LogEvidence, error) {
	return t.queryLokiOptions(ctx, appID, w, limit, search, "")
}
func (t *Telemetry) queryLokiOptions(ctx context.Context, appID string, w TimeWindow, limit int, search, level string) (LogEvidence, error) {
	out := LogEvidence{Source: TelemetrySource{Provider: "loki", ApplicationID: appID, CapturedAt: t.now().UTC()}, Entries: []LogEntry{}, Empty: true}
	s := &out.Source
	if t.loki == nil {
		return out, failure(s, "configuration", "Loki is not configured", nil)
	}
	s.URL = strings.TrimRight(t.loki.config.URL, "/") + "/loki/api/v1/query_range"
	a, ok := t.config.Applications[appID]
	if !ok || len(a.LokiLabels) == 0 {
		return out, failure(s, "scope", "application has no registered Loki scope", nil)
	}
	s.Query = lokiSelector(a.LokiLabels)
	if search != "" {
		s.Query += " |= " + strconv.Quote(search)
	}
	if level != "" {
		s.Query += lokiLevelFilter(level)
	}
	var e error
	s.Window, e = t.window(w, false)
	if e != nil {
		return out, failure(s, "scope", e.Error(), e)
	}
	if limit == 0 {
		limit = min(100, t.config.MaxLogEntries)
	}
	if limit < 1 || limit > t.config.MaxLogEntries {
		return out, failure(s, "scope", "log limit exceeds configured maximum", nil)
	}
	// Select the most recent bounded sample, then sort it chronologically below
	// so readers can follow the selected events without receiving stale head entries.
	params := url.Values{"query": {s.Query}, "start": {strconv.FormatInt(s.Window.Start.UnixNano(), 10)}, "end": {strconv.FormatInt(s.Window.End.UnixNano(), 10)}, "limit": {strconv.Itoa(limit)}, "direction": {"backward"}}
	b, e := t.loki.get(ctx, "/loki/api/v1/query_range", params, s)
	if e != nil {
		return out, e
	}
	v, e := decodeTelemetry(b, s)
	if e != nil {
		return out, e
	}
	if v.Data.ResultType != "streams" {
		return out, failure(s, "protocol", "Loki log range result must contain streams", nil)
	}
	var streams []struct {
		Stream map[string]string `json:"stream"`
		Values [][]string        `json:"values"`
	}
	if e = json.Unmarshal(v.Data.Result, &streams); e != nil {
		return out, failure(s, "protocol", "invalid Loki streams", e)
	}
	for _, stream := range streams {
		for key, value := range a.LokiLabels {
			if stream.Stream[key] != value {
				return out, failure(s, "scope", "Loki returned labels outside registered application scope", nil)
			}
		}
		for _, pair := range stream.Values {
			if len(pair) < 2 {
				return out, failure(s, "protocol", "invalid Loki log entry", nil)
			}
			ns, e := strconv.ParseInt(pair[0], 10, 64)
			if e != nil {
				return out, failure(s, "protocol", "invalid Loki timestamp", e)
			}
			at := time.Unix(0, ns).UTC()
			if at.Before(s.Window.Start) || !at.Before(s.Window.End) {
				return out, failure(s, "scope", "Loki returned an entry outside requested time window", nil)
			}
			if len(out.Entries) >= limit {
				return out, failure(s, "limit", "Loki returned more entries than requested", nil)
			}
			out.Entries = append(out.Entries, enrichLogEntry(LogEntry{Timestamp: at, Line: pair[1], Labels: stream.Stream}))
		}
	}
	sort.SliceStable(out.Entries, func(i, j int) bool { return out.Entries[i].Timestamp.Before(out.Entries[j].Timestamp) })
	out.Empty = len(out.Entries) == 0
	out.LimitReached = len(out.Entries) == limit
	return out, nil
}
