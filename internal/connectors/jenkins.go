package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Jenkins is a read-only, server-configured view of a fixed set of jobs. The
// caller selects an alias, never an upstream URL or an unconfigured job path.
type Jenkins struct {
	baseURL   string
	jobs      map[string]string
	username  string
	tokenFile string
	http      *http.Client
}

type JenkinsConfig struct {
	BaseURL string
	// Jobs maps application-owned aliases to Jenkins full names. A full name
	// may contain folder segments separated by '/'.
	Jobs map[string]string
	// AuthMode is "", "basic", or "bearer". Basic uses Username and a Jenkins
	// API token from TokenFile. Bearer uses only TokenFile.
	AuthMode  string
	Username  string
	TokenFile string
	// AllowHTTP permits only a literal loopback IP for local tests.
	AllowHTTP bool
}

var (
	ErrJenkinsConfig           = errors.New("invalid Jenkins configuration")
	ErrJenkinsJob              = errors.New("Jenkins job is not configured")
	ErrJenkinsLimit            = errors.New("invalid Jenkins build limit")
	ErrJenkinsCredential       = errors.New("Jenkins credential unavailable")
	ErrJenkinsUnauthorized     = errors.New("Jenkins authentication failed")
	ErrJenkinsForbidden        = errors.New("Jenkins access forbidden")
	ErrJenkinsNotFound         = errors.New("Jenkins job not found")
	ErrJenkinsRateLimited      = errors.New("Jenkins rate limited")
	ErrJenkinsTimeout          = errors.New("Jenkins request timed out")
	ErrJenkinsRedirect         = errors.New("Jenkins redirected request")
	ErrJenkinsUpstream         = errors.New("Jenkins upstream error")
	ErrJenkinsTransport        = errors.New("Jenkins transport error")
	ErrJenkinsResponseTooLarge = errors.New("Jenkins response too large")
	ErrJenkinsResponse         = errors.New("invalid Jenkins response")
)

const (
	jenkinsMaxBuilds        = 50
	jenkinsMaxResponseBytes = 1 << 20
	jenkinsMaxTokenBytes    = 4096
)

// NewJenkins rejects URL credentials, redirects, unscoped jobs, and insecure
// credential files. Its configuration must come from a trusted server source.
func NewJenkins(config JenkinsConfig) (*Jenkins, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.Opaque != "" || u.RawPath != "" ||
		(u.Scheme != "https" && !(config.AllowHTTP && u.Scheme == "http" && jenkinsLoopbackIP(u.Hostname()))) ||
		!jenkinsValidPrefix(u.Path) || len(config.Jobs) == 0 || len(config.Jobs) > 100 {
		return nil, ErrJenkinsConfig
	}
	if config.AuthMode != "" && config.AuthMode != "basic" && config.AuthMode != "bearer" {
		return nil, ErrJenkinsConfig
	}
	if config.AuthMode == "" && (config.Username != "" || config.TokenFile != "") ||
		config.AuthMode == "basic" && (!jenkinsValidUsername(config.Username) || !filepath.IsAbs(config.TokenFile)) ||
		config.AuthMode == "bearer" && (config.Username != "" || !filepath.IsAbs(config.TokenFile)) {
		return nil, ErrJenkinsConfig
	}
	jobs := make(map[string]string, len(config.Jobs))
	for alias, fullName := range config.Jobs {
		if !jenkinsValidSegment(alias) || !jenkinsValidJobName(fullName) {
			return nil, ErrJenkinsConfig
		}
		jobs[alias] = fullName
	}
	return &Jenkins{
		baseURL: strings.TrimRight(u.String(), "/"), jobs: jobs,
		username: config.Username, tokenFile: config.TokenFile,
		http: &http.Client{Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func jenkinsLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func jenkinsValidPrefix(path string) bool {
	if path == "" || path == "/" {
		return true
	}
	if strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if !jenkinsValidSegment(segment) {
			return false
		}
	}
	return true
}

func jenkinsValidJobName(name string) bool {
	if name == "" || len(name) > 512 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if !jenkinsValidSegment(segment) {
			return false
		}
	}
	return true
}

func jenkinsValidSegment(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, ch := range value {
		if ch < 0x20 || ch == 0x7f || ch == '/' || ch == '\\' || ch == '?' || ch == '#' || ch == '%' {
			return false
		}
	}
	return true
}

func jenkinsValidUsername(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, ":\r\n\x00")
}

// JenkinsBuild contains only stable status fields. Jenkins URLs, descriptions,
// parameters, console output, actions, and causes are never returned.
type JenkinsBuild struct {
	Number      int64  `json:"number"`
	Result      string `json:"result,omitempty"`
	Building    bool   `json:"building"`
	TimestampMS int64  `json:"timestamp_ms"`
	DurationMS  int64  `json:"duration_ms"`
}

type JenkinsJobBuilds struct {
	Alias  string         `json:"alias"`
	Builds []JenkinsBuild `json:"builds"`
}

// ListJenkinsBuilds reads at most 50 recent builds from one configured job.
// A zero limit uses the default of 20. It checks Jenkins's fullName to avoid
// accepting a response from a different folder or reverse-proxy route.
func (c *Jenkins) ListJenkinsBuilds(ctx context.Context, alias string, limit int) (JenkinsJobBuilds, error) {
	if c == nil || c.http == nil {
		return JenkinsJobBuilds{}, ErrJenkinsConfig
	}
	fullName, ok := c.jobs[alias]
	if !ok {
		return JenkinsJobBuilds{}, ErrJenkinsJob
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > jenkinsMaxBuilds {
		return JenkinsJobBuilds{}, ErrJenkinsLimit
	}
	path := c.baseURL
	for _, segment := range strings.Split(fullName, "/") {
		path += "/job/" + url.PathEscape(segment)
	}
	query := url.Values{"tree": {"fullName,builds[number,result,building,timestamp,duration]{0," + strconv.Itoa(limit) + "}"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path+"/api/json?"+query.Encode(), nil)
	if err != nil {
		return JenkinsJobBuilds{}, ErrJenkinsConfig
	}
	req.Header.Set("Accept", "application/json")
	if c.tokenFile != "" {
		token, err := jenkinsReadToken(c.tokenFile)
		if err != nil {
			return JenkinsJobBuilds{}, err
		}
		if c.username != "" {
			req.SetBasicAuth(c.username, token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if jenkinsTimeout(err) {
			return JenkinsJobBuilds{}, ErrJenkinsTimeout
		}
		return JenkinsJobBuilds{}, ErrJenkinsTransport
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return JenkinsJobBuilds{}, ErrJenkinsUnauthorized
		case http.StatusForbidden:
			return JenkinsJobBuilds{}, ErrJenkinsForbidden
		case http.StatusNotFound:
			return JenkinsJobBuilds{}, ErrJenkinsNotFound
		case http.StatusTooManyRequests:
			return JenkinsJobBuilds{}, ErrJenkinsRateLimited
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return JenkinsJobBuilds{}, ErrJenkinsTimeout
		default:
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				return JenkinsJobBuilds{}, ErrJenkinsRedirect
			}
			return JenkinsJobBuilds{}, ErrJenkinsUpstream
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jenkinsMaxResponseBytes+1))
	if err != nil {
		if jenkinsTimeout(err) {
			return JenkinsJobBuilds{}, ErrJenkinsTimeout
		}
		return JenkinsJobBuilds{}, ErrJenkinsTransport
	}
	if len(body) > jenkinsMaxResponseBytes {
		return JenkinsJobBuilds{}, ErrJenkinsResponseTooLarge
	}
	var upstream struct {
		FullName string `json:"fullName"`
		Builds   []struct {
			Number      *int64          `json:"number"`
			Result      json.RawMessage `json:"result"`
			Building    *bool           `json:"building"`
			TimestampMS *int64          `json:"timestamp"`
			DurationMS  *int64          `json:"duration"`
		} `json:"builds"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil || upstream.FullName != fullName || len(upstream.Builds) > limit {
		return JenkinsJobBuilds{}, ErrJenkinsResponse
	}
	seen := make(map[int64]struct{}, len(upstream.Builds))
	builds := make([]JenkinsBuild, 0, len(upstream.Builds))
	for _, build := range upstream.Builds {
		if build.Number == nil || build.Building == nil || build.TimestampMS == nil || build.DurationMS == nil || len(build.Result) == 0 ||
			*build.Number < 1 || *build.TimestampMS <= 0 || *build.TimestampMS > time.Now().Add(5*time.Minute).UnixMilli() || *build.DurationMS < 0 {
			return JenkinsJobBuilds{}, ErrJenkinsResponse
		}
		result := ""
		if strings.TrimSpace(string(build.Result)) != "null" {
			if err := json.Unmarshal(build.Result, &result); err != nil {
				return JenkinsJobBuilds{}, ErrJenkinsResponse
			}
		}
		if !jenkinsValidResult(result, *build.Building) {
			return JenkinsJobBuilds{}, ErrJenkinsResponse
		}
		if _, duplicate := seen[*build.Number]; duplicate {
			return JenkinsJobBuilds{}, ErrJenkinsResponse
		}
		seen[*build.Number] = struct{}{}
		builds = append(builds, JenkinsBuild{
			Number: *build.Number, Result: result, Building: *build.Building,
			TimestampMS: *build.TimestampMS, DurationMS: *build.DurationMS,
		})
	}
	return JenkinsJobBuilds{Alias: alias, Builds: builds}, nil
}

func jenkinsValidResult(result string, building bool) bool {
	if building {
		return result == ""
	}
	switch result {
	case "SUCCESS", "UNSTABLE", "FAILURE", "NOT_BUILT", "ABORTED":
		return true
	default:
		return false
	}
}

func jenkinsReadToken(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrJenkinsCredential
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > jenkinsMaxTokenBytes {
		return "", ErrJenkinsCredential
	}
	raw, err := io.ReadAll(io.LimitReader(file, jenkinsMaxTokenBytes+1))
	if err != nil || len(raw) > jenkinsMaxTokenBytes {
		return "", ErrJenkinsCredential
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\x00\t ") {
		return "", ErrJenkinsCredential
	}
	return token, nil
}

func jenkinsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
