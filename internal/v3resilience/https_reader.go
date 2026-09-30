package v3resilience

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPSReader is a fixed, operator-configured aggregate producer. The server
// running at the configured endpoint is responsible for reading the five authoritative rule
// systems and positively attesting exact scope, revisions and capture times.
// User input can select only the already authorized application/environment;
// it cannot select a URL, query or credential.
type HTTPSReader struct {
	endpoint url.URL
	token    string
	client   *http.Client
}

var ErrInvalidResilienceSource = errors.New("invalid resilience source configuration")
var ErrResilienceSource = errors.New("resilience source unavailable")

func NewHTTPSReader(endpoint, token string, client *http.Client) (*HTTPSReader, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || len(token) > 4096 ||
		strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\x00") {
		return nil, ErrInvalidResilienceSource
	}
	if client == nil {
		client = &http.Client{}
	}
	bounded := *client
	if bounded.Timeout <= 0 || bounded.Timeout > 10*time.Second {
		bounded.Timeout = 10 * time.Second
	}
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	bounded.Jar = nil
	return &HTTPSReader{endpoint: *u, token: token, client: &bounded}, nil
}

func (h *HTTPSReader) ReadResilience(ctx context.Context, app, env string) (Observation, error) {
	var zero Observation
	if h == nil || h.client == nil || app == "" || env == "" {
		return zero, ErrResilienceSource
	}
	u := h.endpoint
	q := u.Query()
	q.Set("application_id", app)
	q.Set("environment_id", env)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return zero, ErrResilienceSource
	}
	req.Header.Set("Accept", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return zero, ErrResilienceSource
	}
	defer resp.Body.Close()
	const maxResponse = 512 << 10
	if resp.StatusCode != http.StatusOK || resp.ContentLength > maxResponse {
		return zero, ErrResilienceSource
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return zero, ErrResilienceSource
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	var observed Observation
	if err := d.Decode(&observed); err != nil {
		return zero, ErrResilienceSource
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return zero, ErrResilienceSource
	}
	return observed, nil
}
