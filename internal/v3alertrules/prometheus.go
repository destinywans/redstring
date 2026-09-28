package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
				Name  string `json:"name"`
				File  string `json:"file"`
				Rules []struct {
					Type           string            `json:"type"`
					Name           string            `json:"name"`
					Query          string            `json:"query"`
					State          string            `json:"state"`
					Duration       float64           `json:"duration"`
					Labels         map[string]string `json:"labels"`
					Annotations    map[string]string `json:"annotations"`
					LastEvaluation time.Time         `json:"lastEvaluation"`
					EvaluationTime float64           `json:"evaluationTime"`
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
			result = append(result, ObservedRule{GroupIdentity: group.File + "\x00" + group.Name, Group: group.Name, Name: rule.Name, Expression: rule.Query,
				State: rule.State, Duration: rule.Duration, Labels: rule.Labels, Annotations: rule.Annotations,
				LastEvaluation: rule.LastEvaluation, EvaluationTime: rule.EvaluationTime})
		}
	}
	return result, nil
}
