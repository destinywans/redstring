// Package v3alertmanager reads environment-bound Alertmanager silences and
// inhibition configuration. It never changes Alertmanager state.
package v3alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxResponseBytes = 2 << 20
const maxRecords = 500

var ErrNotConfigured = errors.New("Alertmanager is not configured for this environment")

// Endpoint is trusted server-side configuration, never supplied by a request.
// URL may include Alertmanager's configured route prefix, but not credentials,
// query parameters or a fragment.
type Endpoint struct {
	URL             string
	BearerTokenFile string
	AllowHTTP       bool // Explicit opt-in for a trusted local/test endpoint.
}

type Client struct {
	endpoints map[string]Endpoint
	http      *http.Client
}

func New(endpoints map[string]Endpoint, client *http.Client) (*Client, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("no environment-bound Alertmanager endpoint")
	}
	copyEndpoints := make(map[string]Endpoint, len(endpoints))
	for env, ep := range endpoints {
		if env == "" || env == "all" || strings.TrimSpace(env) != env || strings.TrimSpace(ep.URL) != ep.URL {
			return nil, errors.New("invalid Alertmanager environment or URL")
		}
		u, err := url.Parse(ep.URL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "https" && !(ep.AllowHTTP && u.Scheme == "http")) {
			return nil, errors.New("Alertmanager URL requires HTTPS without credentials, query or fragment")
		}
		if ep.BearerTokenFile != "" && !filepath.IsAbs(ep.BearerTokenFile) {
			return nil, errors.New("Alertmanager token file must be absolute")
		}
		// HTTP with a bearer token is only permitted against a literal loopback
		// address in an explicitly configured local test. A DNS name (including
		// localhost) could resolve outside the host and disclose the token.
		if u.Scheme == "http" && ep.BearerTokenFile != "" {
			ip := net.ParseIP(u.Hostname())
			if ip == nil || !ip.IsLoopback() {
				return nil, errors.New("HTTP Alertmanager bearer tokens require a loopback IP")
			}
		}
		copyEndpoints[env] = ep
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	clientCopy := *client
	if clientCopy.Timeout <= 0 || clientCopy.Timeout > 30*time.Second {
		clientCopy.Timeout = 15 * time.Second
	}
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{endpoints: copyEndpoints, http: &clientCopy}, nil
}

type Matcher struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	IsRegex bool   `json:"isRegex"`
	IsEqual bool   `json:"isEqual"`
}

type Silence struct {
	ID     string `json:"id"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
	Matchers    []Matcher         `json:"matchers"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	CreatedBy   string            `json:"createdBy"`
	Comment     string            `json:"comment"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Alert contains only the fields needed to count currently silenced alerts.
// The complete upstream alert body can include unrelated annotations and is
// deliberately not returned by this adapter.
type Alert struct {
	Labels map[string]string `json:"labels"`
	Status struct {
		SilencedBy []string `json:"silencedBy"`
	} `json:"status"`
}

func (c *Client) ListAlerts(ctx context.Context, environment string) ([]Alert, error) {
	body, err := c.get(ctx, environment, "/api/v2/alerts")
	if err != nil {
		return nil, err
	}
	var alerts []Alert
	if err := decodeOne(body, &alerts); err != nil {
		return nil, fmt.Errorf("invalid Alertmanager alerts: %w", err)
	}
	// The shared upstream can contain many other environments. The 2 MiB
	// response bound is enforced by get; the service applies its 500-record
	// bound only after it has selected records visible in this environment.
	for _, alert := range alerts {
		if len(alert.Labels) == 0 || len(alert.Status.SilencedBy) > 100 {
			return nil, errors.New("invalid Alertmanager alert record")
		}
	}
	if alerts == nil {
		alerts = []Alert{}
	}
	return alerts, nil
}

// ListSilences returns the exact upstream status, without calculating a
// different state from the local clock or implying application ownership.
func (c *Client) ListSilences(ctx context.Context, environment string) ([]Silence, error) {
	body, err := c.get(ctx, environment, "/api/v2/silences")
	if err != nil {
		return nil, err
	}
	var silences []Silence
	if err := decodeOne(body, &silences); err != nil {
		return nil, fmt.Errorf("invalid Alertmanager silences: %w", err)
	}
	// Do not reject 501 shared-upstream records before environment filtering.
	// get has already bounded the complete provider response to 2 MiB.
	for _, silence := range silences {
		if silence.ID == "" || silence.StartsAt.IsZero() || silence.EndsAt.IsZero() || silence.UpdatedAt.IsZero() ||
			(silence.Status.State != "active" && silence.Status.State != "pending" && silence.Status.State != "expired") ||
			len(silence.Matchers) == 0 || len(silence.Matchers) > 100 {
			return nil, errors.New("invalid Alertmanager silence record")
		}
		for _, matcher := range silence.Matchers {
			if matcher.Name == "" {
				return nil, errors.New("invalid Alertmanager silence matcher")
			}
		}
	}
	if silences == nil {
		silences = []Silence{}
	}
	return silences, nil
}

// InhibitionRule represents configured matchers only. Alertmanager does not
// expose per-rule hit counts or an enabled/disabled flag through its status API.
type InhibitionRule struct {
	SourceMatchers []string          `yaml:"source_matchers"`
	TargetMatchers []string          `yaml:"target_matchers"`
	Equal          []string          `yaml:"equal"`
	SourceMatch    map[string]string `yaml:"source_match"`
	TargetMatch    map[string]string `yaml:"target_match"`
	SourceMatchRE  map[string]string `yaml:"source_match_re"`
	TargetMatchRE  map[string]string `yaml:"target_match_re"`
}

// ListInhibitionRules extracts only inhibition matchers from /status. It never
// returns config.original because that document can contain receiver secrets.
func (c *Client) ListInhibitionRules(ctx context.Context, environment string) ([]InhibitionRule, error) {
	body, err := c.get(ctx, environment, "/api/v2/status")
	if err != nil {
		return nil, err
	}
	var status struct {
		Config struct {
			Original string `json:"original"`
		} `json:"config"`
	}
	if err := decodeOne(body, &status); err != nil || status.Config.Original == "" {
		return nil, errors.New("invalid Alertmanager status configuration")
	}
	if len(status.Config.Original) > maxResponseBytes {
		return nil, errors.New("Alertmanager configuration exceeds limit")
	}
	var config struct {
		InhibitRules []InhibitionRule `yaml:"inhibit_rules"`
	}
	if err := yaml.Unmarshal([]byte(status.Config.Original), &config); err != nil {
		return nil, errors.New("invalid Alertmanager inhibition configuration")
	}
	if len(config.InhibitRules) > maxRecords {
		return nil, errors.New("Alertmanager inhibition rule count exceeds limit")
	}
	for _, rule := range config.InhibitRules {
		if len(rule.SourceMatchers)+len(rule.TargetMatchers)+len(rule.Equal)+
			len(rule.SourceMatch)+len(rule.TargetMatch)+len(rule.SourceMatchRE)+len(rule.TargetMatchRE) > 100 {
			return nil, errors.New("Alertmanager inhibition matcher count exceeds limit")
		}
	}
	if config.InhibitRules == nil {
		config.InhibitRules = []InhibitionRule{}
	}
	return config.InhibitRules, nil
}

func (c *Client) get(ctx context.Context, environment, path string) ([]byte, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	ep, ok := c.endpoints[environment]
	if !ok {
		return nil, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(ep.URL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if ep.BearerTokenFile != "" {
		file, err := os.Open(ep.BearerTokenFile)
		if err != nil {
			return nil, errors.New("Alertmanager token unavailable")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<10 {
			return nil, errors.New("invalid Alertmanager token file")
		}
		token, err := io.ReadAll(io.LimitReader(file, 16<<10+1))
		if err != nil || len(token) > 16<<10 || len(token) == 0 {
			return nil, errors.New("invalid Alertmanager token")
		}
		value := strings.TrimSpace(string(token))
		if value == "" || strings.ContainsAny(value, "\r\n\x00\t ") {
			return nil, errors.New("invalid Alertmanager token")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Alertmanager request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Alertmanager HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Alertmanager response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("Alertmanager response exceeds 2 MiB")
	}
	return body, nil
}

func decodeOne(body []byte, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("extra JSON value")
	}
	return nil
}
