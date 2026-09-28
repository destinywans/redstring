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

// Harbor reads only repositories explicitly mapped by trusted server configuration.
// Callers choose an alias, never an upstream URL, project, or repository path.
type Harbor struct {
	baseURL      string
	repositories map[string]HarborRepositoryRef
	authMode     string
	username     string
	tokenFile    string
	http         *http.Client
}

type HarborRepositoryRef struct {
	Project    string
	Repository string // Relative to Project; may contain nested path segments.
}

type HarborConfig struct {
	BaseURL      string
	Repositories map[string]HarborRepositoryRef
	// AuthMode is "", "basic", or "bearer". Credentials are read from TokenFile
	// for each request, so rotation does not require recreating the adapter.
	AuthMode  string
	Username  string
	TokenFile string
	// AllowHTTP permits plain HTTP only to a literal loopback IP for local tests.
	AllowHTTP bool
}

var (
	ErrHarborConfig           = errors.New("invalid Harbor configuration")
	ErrHarborRepository       = errors.New("Harbor repository is not configured")
	ErrHarborLimit            = errors.New("invalid Harbor artifact limit")
	ErrHarborCredential       = errors.New("Harbor credential unavailable")
	ErrHarborUnauthorized     = errors.New("Harbor authentication failed")
	ErrHarborForbidden        = errors.New("Harbor access forbidden")
	ErrHarborNotFound         = errors.New("Harbor repository not found")
	ErrHarborRateLimited      = errors.New("Harbor rate limited")
	ErrHarborTimeout          = errors.New("Harbor request timed out")
	ErrHarborRedirect         = errors.New("Harbor redirected request")
	ErrHarborUpstream         = errors.New("Harbor upstream error")
	ErrHarborTransport        = errors.New("Harbor transport error")
	ErrHarborResponseTooLarge = errors.New("Harbor response too large")
	ErrHarborResponse         = errors.New("invalid Harbor response")
)

const (
	harborMaxArtifacts     = 50
	harborMaxResponseBytes = 1 << 20
	harborMaxTokenBytes    = 4096
)

func NewHarbor(config HarborConfig) (*Harbor, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.Opaque != "" || u.RawPath != "" ||
		(u.Scheme != "https" && !(config.AllowHTTP && u.Scheme == "http" && harborLoopbackIP(u.Hostname()))) ||
		!harborValidPrefix(u.Path) || len(config.Repositories) == 0 || len(config.Repositories) > 100 {
		return nil, ErrHarborConfig
	}
	if config.AuthMode != "" && config.AuthMode != "basic" && config.AuthMode != "bearer" {
		return nil, ErrHarborConfig
	}
	if config.AuthMode == "" && (config.Username != "" || config.TokenFile != "") ||
		config.AuthMode == "basic" && (!harborValidUsername(config.Username) || !filepath.IsAbs(config.TokenFile)) ||
		config.AuthMode == "bearer" && (config.Username != "" || !filepath.IsAbs(config.TokenFile)) {
		return nil, ErrHarborConfig
	}
	repositories := make(map[string]HarborRepositoryRef, len(config.Repositories))
	for alias, ref := range config.Repositories {
		if !harborValidSegment(alias) || !harborValidSegment(ref.Project) || !harborValidRepository(ref.Repository) {
			return nil, ErrHarborConfig
		}
		repositories[alias] = ref
	}
	return &Harbor{
		baseURL: strings.TrimRight(u.String(), "/"), repositories: repositories,
		authMode: config.AuthMode, username: config.Username, tokenFile: config.TokenFile,
		http: &http.Client{Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func harborLoopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func harborValidPrefix(path string) bool {
	if path == "" || path == "/" {
		return true
	}
	if strings.HasSuffix(path, "/") || strings.Contains(path, "//") {
		return false
	}
	return harborValidRepository(strings.TrimPrefix(path, "/"))
}

func harborValidRepository(name string) bool {
	if name == "" || len(name) > 512 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if !harborValidSegment(segment) {
			return false
		}
	}
	return true
}

func harborValidSegment(value string) bool {
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

func harborValidUsername(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, ":\r\n\x00")
}

type HarborRepository struct {
	Alias         string `json:"alias"`
	Name          string `json:"name"`
	ArtifactCount int64  `json:"artifact_count"`
	PullCount     int64  `json:"pull_count"`
}

type HarborArtifact struct {
	Digest   string    `json:"digest"`
	Size     int64     `json:"size"`
	PushTime time.Time `json:"push_time"`
	Tags     []string  `json:"tags"`
}

type HarborArtifacts struct {
	Alias     string           `json:"alias"`
	Artifacts []HarborArtifact `json:"artifacts"`
	// Total is absent when Harbor does not send X-Total-Count; it is never
	// inferred from a single page.
	Total *int64 `json:"total,omitempty"`
}

// GetHarborRepository returns metadata for one configured repository.
func (c *Harbor) GetHarborRepository(ctx context.Context, alias string) (HarborRepository, error) {
	ref, err := c.repository(alias)
	if err != nil {
		return HarborRepository{}, err
	}
	body, _, err := c.get(ctx, c.repositoryURL(ref), nil)
	if err != nil {
		return HarborRepository{}, err
	}
	var upstream struct {
		Name          string `json:"name"`
		ArtifactCount *int64 `json:"artifact_count"`
		PullCount     *int64 `json:"pull_count"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil || upstream.Name != ref.Project+"/"+ref.Repository ||
		upstream.ArtifactCount == nil || *upstream.ArtifactCount < 0 || upstream.PullCount == nil || *upstream.PullCount < 0 {
		return HarborRepository{}, ErrHarborResponse
	}
	return HarborRepository{Alias: alias, Name: upstream.Name,
		ArtifactCount: *upstream.ArtifactCount, PullCount: *upstream.PullCount}, nil
}

// ListHarborArtifacts reads the first bounded page sorted by push time.
// Artifact existence and push time are evidence of a registry push, not proof
// of deployment or a production change.
func (c *Harbor) ListHarborArtifacts(ctx context.Context, alias string, limit int) (HarborArtifacts, error) {
	ref, err := c.repository(alias)
	if err != nil {
		return HarborArtifacts{}, err
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > harborMaxArtifacts {
		return HarborArtifacts{}, ErrHarborLimit
	}
	q := url.Values{"page": {"1"}, "page_size": {strconv.Itoa(limit)}, "sort": {"-push_time"}, "with_tag": {"true"}}
	body, header, err := c.get(ctx, c.repositoryURL(ref)+"/artifacts", q)
	if err != nil {
		return HarborArtifacts{}, err
	}
	var upstream []struct {
		Digest         string `json:"digest"`
		RepositoryName string `json:"repository_name"`
		Size           *int64 `json:"size"`
		PushTime       string `json:"push_time"`
		Tags           []struct {
			Name string `json:"name"`
		} `json:"tags"`
	}
	if err := json.Unmarshal(body, &upstream); err != nil || upstream == nil || len(upstream) > limit {
		return HarborArtifacts{}, ErrHarborResponse
	}
	result := HarborArtifacts{Alias: alias, Artifacts: make([]HarborArtifact, 0, len(upstream))}
	if raw := header.Get("X-Total-Count"); raw != "" {
		total, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || total < int64(len(upstream)) {
			return HarborArtifacts{}, ErrHarborResponse
		}
		result.Total = &total
	}
	seen := make(map[string]struct{}, len(upstream))
	latestPushTime := time.Now().Add(5 * time.Minute)
	earliestPushTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, item := range upstream {
		pushTime, timeErr := time.Parse(time.RFC3339Nano, item.PushTime)
		if !harborValidDigest(item.Digest) || item.Size == nil || *item.Size < 0 || timeErr != nil ||
			pushTime.Before(earliestPushTime) || pushTime.After(latestPushTime) ||
			item.RepositoryName != ref.Project+"/"+ref.Repository {
			return HarborArtifacts{}, ErrHarborResponse
		}
		if _, duplicate := seen[item.Digest]; duplicate {
			return HarborArtifacts{}, ErrHarborResponse
		}
		seen[item.Digest] = struct{}{}
		tags := make([]string, 0, len(item.Tags))
		for _, tag := range item.Tags {
			if tag.Name == "" || len(tag.Name) > 128 || strings.ContainsAny(tag.Name, "\r\n\x00") {
				return HarborArtifacts{}, ErrHarborResponse
			}
			tags = append(tags, tag.Name)
		}
		result.Artifacts = append(result.Artifacts, HarborArtifact{
			Digest: item.Digest, Size: *item.Size, PushTime: pushTime, Tags: tags,
		})
	}
	return result, nil
}

func harborValidDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, ch := range value[len("sha256:"):] {
		if ch < '0' || ch > '9' && ch < 'a' || ch > 'f' {
			return false
		}
	}
	return true
}

func (c *Harbor) repository(alias string) (HarborRepositoryRef, error) {
	if c == nil || c.http == nil {
		return HarborRepositoryRef{}, ErrHarborConfig
	}
	ref, ok := c.repositories[alias]
	if !ok {
		return HarborRepositoryRef{}, ErrHarborRepository
	}
	return ref, nil
}

func (c *Harbor) repositoryURL(ref HarborRepositoryRef) string {
	// Harbor's repository_name path parameter requires a slash to be encoded
	// twice (a/b -> a%252Fb), per the upstream V2 Swagger contract.
	return c.baseURL + "/api/v2.0/projects/" + url.PathEscape(ref.Project) +
		"/repositories/" + url.PathEscape(url.PathEscape(ref.Repository))
}

func (c *Harbor) get(ctx context.Context, path string, query url.Values) ([]byte, http.Header, error) {
	if query != nil {
		path += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, nil, ErrHarborConfig
	}
	req.Header.Set("Accept", "application/json")
	if c.tokenFile != "" {
		token, err := harborReadToken(c.tokenFile)
		if err != nil {
			return nil, nil, err
		}
		if c.authMode == "basic" {
			req.SetBasicAuth(c.username, token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if harborTimeout(err) {
			return nil, nil, ErrHarborTimeout
		}
		return nil, nil, ErrHarborTransport
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, nil, ErrHarborUnauthorized
		case http.StatusForbidden:
			return nil, nil, ErrHarborForbidden
		case http.StatusNotFound:
			return nil, nil, ErrHarborNotFound
		case http.StatusTooManyRequests:
			return nil, nil, ErrHarborRateLimited
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return nil, nil, ErrHarborTimeout
		default:
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				return nil, nil, ErrHarborRedirect
			}
			return nil, nil, ErrHarborUpstream
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, harborMaxResponseBytes+1))
	if err != nil {
		if harborTimeout(err) {
			return nil, nil, ErrHarborTimeout
		}
		return nil, nil, ErrHarborTransport
	}
	if len(body) > harborMaxResponseBytes {
		return nil, nil, ErrHarborResponseTooLarge
	}
	return body, resp.Header, nil
}

func harborReadToken(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", ErrHarborCredential
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > harborMaxTokenBytes {
		return "", ErrHarborCredential
	}
	raw, err := io.ReadAll(io.LimitReader(file, harborMaxTokenBytes+1))
	if err != nil || len(raw) > harborMaxTokenBytes {
		return "", ErrHarborCredential
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\x00\t ") {
		return "", ErrHarborCredential
	}
	return token, nil
}

func harborTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}
