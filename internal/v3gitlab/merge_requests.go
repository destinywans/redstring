// Package v3gitlab provides bounded, read-only GitLab evidence for V3
// configuration promotion. It cannot create commits, branches or merge requests.
package v3gitlab

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

var (
	ErrInvalidConfig     = errors.New("invalid GitLab configuration")
	ErrInvalidIID        = errors.New("invalid merge request IID")
	ErrCredential        = errors.New("GitLab credential unavailable")
	ErrUnauthorized      = errors.New("GitLab authentication failed")
	ErrForbidden         = errors.New("GitLab access forbidden")
	ErrNotFound          = errors.New("GitLab merge request not found")
	ErrRateLimited       = errors.New("GitLab rate limited")
	ErrTimeout           = errors.New("GitLab request timed out")
	ErrRedirect          = errors.New("GitLab redirected merge request request")
	ErrUpstream          = errors.New("GitLab upstream error")
	ErrTransport         = errors.New("GitLab transport error")
	ErrResponseTooLarge  = errors.New("GitLab response too large")
	ErrMalformedResponse = errors.New("invalid GitLab merge request response")
	ErrUnexpectedTarget  = errors.New("GitLab merge request target differs from configured branch")
)

const maxResponseBytes = 1 << 20
const maxTokenBytes = 4096

// Config is server-owned. No request or persisted user input can change the
// GitLab origin, project, target branch, or token path for an observation.
type Config struct {
	BaseURL      string
	ProjectID    int64
	TargetBranch string
	TokenFile    string
	// AllowHTTP permits only loopback HTTP, for an explicitly configured lab.
	AllowHTTP bool
}

type Client struct {
	endpoint     string
	projectID    int64
	targetBranch string
	tokenFile    string
	http         *http.Client
}

func New(config Config) (*Client, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" ||
		(u.Scheme != "https" && !(config.AllowHTTP && u.Scheme == "http" && loopback(u.Hostname()))) ||
		config.ProjectID <= 0 || !validRef(config.TargetBranch) ||
		!filepath.IsAbs(config.TokenFile) || strings.TrimSpace(config.TokenFile) != config.TokenFile {
		return nil, ErrInvalidConfig
	}
	return &Client{
		endpoint:     strings.TrimRight(u.String(), "/"),
		projectID:    config.ProjectID,
		targetBranch: config.TargetBranch,
		tokenFile:    config.TokenFile,
		http: &http.Client{Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validRef(ref string) bool {
	if ref == "" || len(ref) > 200 || strings.TrimSpace(ref) != ref ||
		strings.HasPrefix(ref, "-") || strings.HasSuffix(ref, ".") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.Contains(ref, "//") {
		return false
	}
	for _, ch := range ref {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' ||
			ch >= '0' && ch <= '9' || ch == '/' || ch == '_' || ch == '-' || ch == '.') {
			return false
		}
	}
	return true
}

// MergeRequest is a verified projection of GitLab's current MR record.
// Merged is derived only from GitLab's state, never from a requested action.
type MergeRequest struct {
	IID             int64      `json:"iid"`
	ProjectID       int64      `json:"project_id"`
	TargetProjectID int64      `json:"target_project_id"`
	State           string     `json:"state"`
	Merged          bool       `json:"merged"`
	SourceBranch    string     `json:"source_branch"`
	TargetBranch    string     `json:"target_branch"`
	SourceSHA       string     `json:"sha"`
	MergeCommitSHA  string     `json:"merge_commit_sha"`
	SquashCommitSHA string     `json:"squash_commit_sha"`
	MergedAt        *time.Time `json:"merged_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func tokenFromFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", ErrCredential
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxTokenBytes || info.Mode().Perm()&0077 != 0 {
		return "", ErrCredential
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxTokenBytes+1))
	if err != nil || len(raw) > maxTokenBytes {
		return "", ErrCredential
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || len(token) > maxTokenBytes || strings.ContainsAny(token, "\r\n\x00\t ") {
		return "", ErrCredential
	}
	return token, nil
}

// MergeRequest fetches one IID from the configured project. It never follows
// GitLab redirects, so a Location header cannot forward the private token.
func (c *Client) MergeRequest(ctx context.Context, iid int64) (MergeRequest, error) {
	if c == nil || c.http == nil {
		return MergeRequest{}, ErrInvalidConfig
	}
	if iid <= 0 {
		return MergeRequest{}, ErrInvalidIID
	}
	token, err := tokenFromFile(c.tokenFile)
	if err != nil {
		return MergeRequest{}, err
	}
	path := c.endpoint + "/api/v4/projects/" + strconv.FormatInt(c.projectID, 10) +
		"/merge_requests/" + strconv.FormatInt(iid, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return MergeRequest{}, ErrInvalidConfig
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if timeout(err) {
			return MergeRequest{}, errors.Join(ErrTimeout, err)
		}
		return MergeRequest{}, errors.Join(ErrTransport, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return MergeRequest{}, ErrUnauthorized
		case http.StatusForbidden:
			return MergeRequest{}, ErrForbidden
		case http.StatusNotFound:
			return MergeRequest{}, ErrNotFound
		case http.StatusTooManyRequests:
			return MergeRequest{}, ErrRateLimited
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return MergeRequest{}, ErrTimeout
		default:
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				return MergeRequest{}, ErrRedirect
			}
			return MergeRequest{}, &UpstreamStatusError{StatusCode: resp.StatusCode}
		}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if timeout(err) {
			return MergeRequest{}, errors.Join(ErrTimeout, err)
		}
		return MergeRequest{}, errors.Join(ErrTransport, err)
	}
	if len(raw) > maxResponseBytes {
		return MergeRequest{}, ErrResponseTooLarge
	}
	var result MergeRequest
	if err := json.Unmarshal(raw, &result); err != nil ||
		result.IID != iid || result.ProjectID != c.projectID || result.TargetProjectID != c.projectID ||
		result.SourceBranch == "" || result.TargetBranch == "" ||
		!validState(result.State) || len(result.SourceBranch) > 200 {
		return MergeRequest{}, ErrMalformedResponse
	}
	if result.TargetBranch != c.targetBranch {
		return MergeRequest{}, ErrUnexpectedTarget
	}
	result.Merged = result.State == "merged"
	return result, nil
}

func timeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func validState(state string) bool {
	return state == "opened" || state == "closed" || state == "locked" || state == "merged"
}

// UpstreamStatusError preserves status without including response body, URL,
// request headers, or credentials in logs or caller-visible errors.
type UpstreamStatusError struct{ StatusCode int }

func (e *UpstreamStatusError) Error() string {
	return "GitLab upstream HTTP " + strconv.Itoa(e.StatusCode)
}
func (e *UpstreamStatusError) Unwrap() error { return ErrUpstream }
