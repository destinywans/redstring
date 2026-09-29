package v3ruleassistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PreviewBinding is an operator-reviewed, immutable query for one application
// and environment. Query is never supplied by an HTTP caller.
type PreviewBinding struct {
	EnvironmentID    string
	ApplicationID    string
	Service          string
	QueryID          string
	Query            string
	Unit             string
	Source           string
	ApplicationLabel string
	EnvironmentLabel string
	ServiceLabel     string
}

type PreviewSample struct {
	At    time.Time
	Value float64
}

type PreviewSeries struct {
	Labels  map[string]string
	Samples []PreviewSample
}

type PreviewReader interface {
	Bindings(environmentID string) []PreviewBinding
	QueryRange(ctx context.Context, b PreviewBinding, start, end time.Time, step time.Duration) ([]PreviewSeries, error)
	QueryRangeDraft(ctx context.Context, b PreviewBinding, query string, start, end time.Time, step time.Duration) ([]PreviewSeries, error)
}

// PrometheusPreviewReader performs one bounded read against a configured,
// application-isolated HTTPS endpoint. The endpoint and expression are loaded
// together; a request cannot substitute either one.
type PrometheusPreviewReader struct {
	bindings map[string][]PreviewBinding
	sources  map[PreviewBinding]previewSource
	client   *http.Client
	active   chan struct{}
}

type previewSource struct {
	endpoint  string
	apiKeyEnv string
}

type previewConfig struct {
	Sources []struct {
		EnvironmentID string `json:"environment_id"`
		ApplicationID string `json:"application_id"`
		Service       string `json:"service"`
		QueryID       string `json:"query_id"`
		Query         string `json:"query"`
		Unit          string `json:"unit"`
		Source        string `json:"source"`
		Endpoint      string `json:"endpoint"`
		APIKeyEnv     string `json:"api_key_env"`
		IsolationMode string `json:"isolation_mode"`
		LabelKeys     struct {
			Application string `json:"application"`
			Environment string `json:"environment"`
			Service     string `json:"service"`
		} `json:"label_keys"`
	} `json:"sources"`
}

var previewID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var previewQueryID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
var previewEnvName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
var previewLabelName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

var errPreviewConfig = errors.New("invalid Prometheus preview configuration")
var errPreviewQuery = errors.New("invalid Prometheus preview request")
var errPreviewSource = errors.New("Prometheus preview source unavailable")
var errPreviewResponse = errors.New("invalid Prometheus preview response")

// NewPrometheusPreviewReader accepts only exact DNS hosts supplied separately
// from the config. The operator must verify that each endpoint itself exposes
// only the bound application and environment; label checks are defense in depth.
func NewPrometheusPreviewReader(configJSON []byte, allowedHosts []string) (*PrometheusPreviewReader, error) {
	if len(configJSON) == 0 || len(configJSON) > 128<<10 || len(allowedHosts) == 0 || len(allowedHosts) > 64 {
		return nil, errPreviewConfig
	}
	hosts := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		if !validPreviewHost(host) {
			return nil, errPreviewConfig
		}
		hosts[host] = struct{}{}
	}
	var cfg previewConfig
	decoder := json.NewDecoder(strings.NewReader(string(configJSON)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || len(cfg.Sources) == 0 || len(cfg.Sources) > 64 {
		return nil, errPreviewConfig
	}
	r := &PrometheusPreviewReader{bindings: make(map[string][]PreviewBinding), sources: make(map[PreviewBinding]previewSource), active: make(chan struct{}, 2)}
	endpointOwner := make(map[string]string)
	queryOwner := make(map[string]struct{})
	for _, item := range cfg.Sources {
		b := PreviewBinding{EnvironmentID: item.EnvironmentID, ApplicationID: item.ApplicationID,
			Service: item.Service, QueryID: item.QueryID, Query: item.Query, Unit: item.Unit, Source: item.Source,
			ApplicationLabel: item.LabelKeys.Application, EnvironmentLabel: item.LabelKeys.Environment,
			ServiceLabel: item.LabelKeys.Service}
		if !previewID.MatchString(b.EnvironmentID) || !previewID.MatchString(b.ApplicationID) ||
			!previewID.MatchString(b.Service) || !previewQueryID.MatchString(b.QueryID) ||
			!previewID.MatchString(b.Source) || b.Unit == "" || len(b.Unit) > 32 ||
			strings.TrimSpace(b.Unit) != b.Unit || strings.ContainsAny(b.Unit, "\r\n\x00") ||
			b.Query == "" || len(b.Query) > 2048 || strings.TrimSpace(b.Query) != b.Query ||
			strings.ContainsAny(b.Query, "\r\n\x00") || item.IsolationMode != "application_endpoint" ||
			(item.APIKeyEnv != "" && !previewEnvName.MatchString(item.APIKeyEnv)) ||
			!previewLabelName.MatchString(item.LabelKeys.Application) ||
			!previewLabelName.MatchString(item.LabelKeys.Environment) ||
			!previewLabelName.MatchString(item.LabelKeys.Service) ||
			item.LabelKeys.Application == item.LabelKeys.Environment ||
			item.LabelKeys.Application == item.LabelKeys.Service ||
			item.LabelKeys.Environment == item.LabelKeys.Service {
			return nil, errPreviewConfig
		}
		u, err := url.Parse(item.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			u.Opaque != "" || u.RawPath != "" || u.ForceQuery || !validPreviewPath(u.Path) || u.Port() != "" && u.Port() != "443" {
			return nil, errPreviewConfig
		}
		if _, ok := hosts[u.Hostname()]; !ok || !validPreviewHost(u.Hostname()) {
			return nil, errPreviewConfig
		}
		owner := b.EnvironmentID + "\x00" + b.ApplicationID
		if prior, exists := endpointOwner[item.Endpoint]; exists && prior != owner {
			return nil, errPreviewConfig
		}
		endpointOwner[item.Endpoint] = owner
		if _, duplicate := r.sources[b]; duplicate {
			return nil, errPreviewConfig
		}
		key := b.EnvironmentID + "\x00" + b.ApplicationID + "\x00" + b.Service + "\x00" + b.QueryID
		if _, duplicate := queryOwner[key]; duplicate {
			return nil, errPreviewConfig
		}
		queryOwner[key] = struct{}{}
		r.sources[b] = previewSource{endpoint: strings.TrimRight(item.Endpoint, "/"), apiKeyEnv: item.APIKeyEnv}
		r.bindings[b.EnvironmentID] = append(r.bindings[b.EnvironmentID], b)
	}
	r.client = &http.Client{Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second, DisableCompression: true, DisableKeepAlives: true}}
	return r, nil
}

func validPreviewHost(host string) bool {
	if host == "" || host != strings.ToLower(host) || strings.HasSuffix(host, ".") ||
		strings.ContainsAny(host, "/:@* \\\r\n\x00") || net.ParseIP(host) != nil ||
		host == "localhost" || !strings.Contains(host, ".") || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validPreviewPath(path string) bool {
	if path == "" || path == "/" {
		return true
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?%#\\\r\n\x00") {
		return false
	}
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func (r *PrometheusPreviewReader) Bindings(environmentID string) []PreviewBinding {
	if r == nil {
		return nil
	}
	return append([]PreviewBinding(nil), r.bindings[environmentID]...)
}

// QueryRange performs exactly one GET. An empty matrix remains empty; callers
// must report it as no observed data, not as a successful zero-valued history.
func (r *PrometheusPreviewReader) QueryRange(ctx context.Context, b PreviewBinding, start, end time.Time, step time.Duration) ([]PreviewSeries, error) {
	return r.queryRange(ctx, b, b.Query, start, end, step)
}

// QueryRangeDraft accepts a new expression only after the package-level
// reviewed-scope PromQL validator approves it for this exact known binding.
// It uses the same endpoint, credential, range, and response checks as the
// seed-expression read.
func (r *PrometheusPreviewReader) QueryRangeDraft(ctx context.Context, b PreviewBinding, query string, start, end time.Time, step time.Duration) ([]PreviewSeries, error) {
	if r == nil {
		return nil, errPreviewSource
	}
	if _, exists := r.sources[b]; !exists {
		return nil, errPreviewSource
	}
	if err := validateDraftPromQL(b, query); err != nil {
		return nil, errPreviewQuery
	}
	return r.queryRange(ctx, b, query, start, end, step)
}

func (r *PrometheusPreviewReader) queryRange(ctx context.Context, b PreviewBinding, query string, start, end time.Time, step time.Duration) ([]PreviewSeries, error) {
	if r == nil {
		return nil, errPreviewSource
	}
	source, exists := r.sources[b]
	if !exists {
		return nil, errPreviewSource
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) || end.Sub(start) > 7*24*time.Hour ||
		step < 5*time.Minute || step > 24*time.Hour || step%time.Second != 0 ||
		end.Sub(start)%step != 0 || end.Sub(start)/step+1 > 2017 {
		return nil, errPreviewQuery
	}
	select {
	case r.active <- struct{}{}:
		defer func() { <-r.active }()
	default:
		return nil, errPreviewSource
	}
	start, end = start.UTC(), end.UTC()
	u, err := url.Parse(source.endpoint + "/api/v1/query_range")
	if err != nil {
		return nil, errPreviewSource
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatFloat(float64(start.UnixNano())/1e9, 'f', 3, 64))
	q.Set("end", strconv.FormatFloat(float64(end.UnixNano())/1e9, 'f', 3, 64))
	q.Set("step", strconv.FormatInt(int64(step/time.Second), 10))
	q.Set("limit", "2") // detect another series rather than silently truncating to one
	q.Set("timeout", "15s")
	u.RawQuery = q.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errPreviewSource
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if source.apiKeyEnv != "" {
		key := os.Getenv(source.apiKeyEnv)
		if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
			return nil, errPreviewSource
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, errPreviewSource
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errPreviewSource
	}
	contents, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(contents) > 2<<20 {
		return nil, errPreviewResponse
	}
	return decodePreviewMatrix(contents, b, start, end, step)
}

func decodePreviewMatrix(contents []byte, b PreviewBinding, start, end time.Time, step time.Duration) ([]PreviewSeries, error) {
	var envelope struct {
		Status   string          `json:"status"`
		Warnings json.RawMessage `json:"warnings"`
		Infos    json.RawMessage `json:"infos"`
		Data     *struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric     map[string]string   `json:"metric"`
				Values     [][]json.RawMessage `json:"values"`
				Histograms json.RawMessage     `json:"histograms"`
			} `json:"result"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Status != "success" ||
		envelope.Data == nil || envelope.Data.ResultType != "matrix" || envelope.Data.Result == nil ||
		len(envelope.Data.Result) > 1 || !emptyPreviewMessages(envelope.Warnings) || !emptyPreviewMessages(envelope.Infos) {
		return nil, errPreviewResponse
	}
	if len(envelope.Data.Result) == 0 {
		return []PreviewSeries{}, nil
	}
	row := envelope.Data.Result[0]
	if row.Metric == nil || len(row.Metric) > 16 || row.Metric[b.ApplicationLabel] != b.ApplicationID ||
		row.Metric[b.EnvironmentLabel] != b.EnvironmentID || row.Metric[b.ServiceLabel] != b.Service ||
		len(row.Histograms) > 0 || len(row.Values) != int(end.Sub(start)/step)+1 {
		return nil, errPreviewResponse
	}
	for key, value := range row.Metric {
		if !previewLabelName.MatchString(key) || len(value) > 256 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errPreviewResponse
		}
	}
	series := PreviewSeries{Labels: row.Metric, Samples: make([]PreviewSample, 0, len(row.Values))}
	for index, pair := range row.Values {
		if len(pair) != 2 {
			return nil, errPreviewResponse
		}
		seconds, err := strconv.ParseFloat(string(pair[0]), 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return nil, errPreviewResponse
		}
		whole, fraction := math.Modf(seconds)
		at := time.Unix(int64(whole), int64(math.Round(fraction*1e9))).UTC()
		grid := start.Add(time.Duration(index) * step)
		if drift := at.Sub(grid); drift < -50*time.Millisecond || drift > 50*time.Millisecond {
			return nil, errPreviewResponse
		}
		var numeric string
		if json.Unmarshal(pair[1], &numeric) != nil {
			return nil, errPreviewResponse
		}
		value, err := strconv.ParseFloat(numeric, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, errPreviewResponse
		}
		series.Samples = append(series.Samples, PreviewSample{At: grid, Value: value})
	}
	return []PreviewSeries{series}, nil
}

func emptyPreviewMessages(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var messages []string
	return json.Unmarshal(raw, &messages) == nil && messages != nil && len(messages) == 0
}
