package v3alertrules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const enableGitOpsMaxResponse = 512 << 10

type enableGitOpsHTTP struct {
	base   *url.URL
	token  string
	client *http.Client
	gitlab bool
}

func newEnableGitOpsHTTP(rawURL, tokenFile string, allowLoopbackHTTP, gitlab bool) (enableGitOpsHTTP, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" ||
		(u.Scheme != "https" && !(allowLoopbackHTTP && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return enableGitOpsHTTP{}, errors.New("invalid GitOps endpoint")
	}
	if tokenFile == "" || strings.ContainsAny(tokenFile, "\r\n") {
		return enableGitOpsHTTP{}, errors.New("invalid GitOps token file")
	}
	info, err := os.Lstat(tokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return enableGitOpsHTTP{}, errors.New("GitOps token file must be regular 0600")
	}
	f, err := os.Open(tokenFile)
	if err != nil {
		return enableGitOpsHTTP{}, errors.New("GitOps token unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return enableGitOpsHTTP{}, errors.New("GitOps token file changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(raw) > 16384 {
		return enableGitOpsHTTP{}, errors.New("GitOps token unavailable")
	}
	token := strings.TrimSpace(string(raw))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return enableGitOpsHTTP{}, errors.New("invalid GitOps token")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return enableGitOpsHTTP{base: u, token: token, gitlab: gitlab, client: &http.Client{
		Timeout: 10 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// request never returns an upstream body on failure. The client does not send
// credentials through redirects or ambient proxy settings.
func (c enableGitOpsHTTP) request(ctx context.Context, method, endpoint string, query url.Values, body any) (int, []byte, http.Header, error) {
	if !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") {
		return 0, nil, nil, errors.New("invalid GitOps path")
	}
	// Parse the escaped API path as URL text. Assigning it to URL.Path would
	// escape the percent signs a second time for GitLab file/branch segments.
	u, err := url.Parse(strings.TrimRight(c.base.String(), "/") + endpoint)
	if err != nil || u.Host != c.base.Host || u.Scheme != c.base.Scheme || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return 0, nil, nil, errors.New("invalid GitOps request target")
	}
	u.RawQuery = query.Encode()
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), payload)
	if err != nil {
		return 0, nil, nil, err
	}
	if c.gitlab {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("GitOps transport: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return 0, nil, nil, errors.New("GitOps redirect refused")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("GitOps upstream HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, enableGitOpsMaxResponse+1))
	if err != nil || len(raw) > enableGitOpsMaxResponse {
		return 0, nil, nil, errors.New("GitOps response unavailable or too large")
	}
	return resp.StatusCode, raw, resp.Header, nil
}

func (c enableGitOpsHTTP) json(ctx context.Context, method, endpoint string, query url.Values, body any, expected int, out any) (http.Header, error) {
	status, raw, header, err := c.request(ctx, method, endpoint, query, body)
	if err != nil {
		return nil, err
	}
	if status != expected {
		return nil, fmt.Errorf("GitOps upstream HTTP %d", status)
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return nil, errors.New("invalid GitOps response")
	}
	return header, nil
}

func (c enableGitOpsHTTP) projectPath(projectID int64) string {
	return "/api/v4/projects/" + strconv.FormatInt(projectID, 10)
}
