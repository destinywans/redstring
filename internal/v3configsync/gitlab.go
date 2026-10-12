package v3configsync

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Config is a server-owned binding to one configuration repository and target.
// TokenFile must contain a GitLab token and be a regular, non-symlink 0600 file.
type Config struct {
	BaseURL           string
	ProjectID         int64
	TargetBranch      string
	BranchPrefix      string
	TokenFile         string
	AllowLoopbackHTTP bool
	// CAFile optionally names a PEM CA bundle for a GitLab behind an
	// internal CA (PA-91). With it this client trusts only that bundle; the
	// host name is still verified. It must be an absolute path to a regular,
	// non-symlink file that neither group nor others can write, and only
	// with HTTPS.
	CAFile string
	// WebURL is the address GitLab's web links use (its external_url) when
	// it differs from BaseURL, as behind an API proxy (PA-98). Merge request
	// links must be on it. Empty means BaseURL.
	WebURL string
}

type MR struct {
	IID             int64     `json:"iid"`
	Title           string    `json:"title"`
	URL             string    `json:"web_url"`
	State           string    `json:"state"`
	SourceBranch    string    `json:"source_branch"`
	TargetBranch    string    `json:"target_branch"`
	SourceProjectID int64     `json:"source_project_id"`
	TargetProjectID int64     `json:"target_project_id"`
	MergeCommitSHA  string    `json:"merge_commit_sha"`
	SourceCommitSHA string    `json:"sha"`
	ChangesCount    string    `json:"changes_count"`
	MergedAt        time.Time `json:"merged_at"`
	Author          struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"author"`
}

// ExportError identifies a write step. MayHaveChanged means that retrying must
// first reconcile the same branch/path; the remote outcome may be unknown.
type ExportError struct {
	Stage          string
	MayHaveChanged bool
	Err            error
}

func (e *ExportError) Error() string { return fmt.Sprintf("GitLab export %s: %v", e.Stage, e.Err) }
func (e *ExportError) Unwrap() error { return e.Err }

var (
	ErrConflict  = errors.New("GitLab export identity conflicts with existing content")
	ErrUntrusted = errors.New("GitLab merge request provenance cannot be verified")
	// ErrMRLinkMismatch is a merge request whose web link is not on the
	// configured web address (PA-98). It is untrusted, and usually means
	// gitlab.web_url is missing or wrong.
	ErrMRLinkMismatch = fmt.Errorf("%w: merge request link is not on the configured GitLab web address", ErrUntrusted)
	// ErrMRNotPrepared is a merged MR whose changes_count GitLab has not
	// computed yet, as just after a merge. It is untrusted for now; reading
	// it again later can verify it.
	ErrMRNotPrepared = fmt.Errorf("%w: GitLab has not finished processing the merge request", ErrUntrusted)
	// ErrGitLabUnavailable is a read that failed in transit or that GitLab
	// refused as unauthorized, throttled or failing (401, 403, 429, 5xx):
	// nothing was learned about the object, so it is not untrusted (PA-103).
	ErrGitLabUnavailable = errors.New("GitLab unavailable")
	ErrListTooLarge      = errors.New("GitLab merge request list exceeds configured bound")
	branchRE             = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,127}$`)
	shaRE                = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
	pathPartRE           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

const promotionLabel = "aiops-config-promotion"
const notificationRouteDraftLabel = "aiops-notification-route-draft"
const inhibitionDraftLabel = "aiops-inhibition-draft"
const alertRuleDraftLabel = "aiops-alert-rule-draft"

type GitLabClient struct {
	config  Config
	base    *url.URL
	web     *url.URL // scheme and host of web links; base when not set
	token   string
	http    *http.Client
	mrLabel string
}

func (c *GitLabClient) ProjectID() int64     { return c.config.ProjectID }
func (c *GitLabClient) BranchPrefix() string { return c.config.BranchPrefix }
func (c *GitLabClient) TargetBranch() string { return c.config.TargetBranch }

// ForNotificationDrafts keeps the reviewed GitLab endpoint, project and
// credential while isolating route drafts by target, source prefix and label.
func (c *GitLabClient) ForNotificationDrafts(target, prefix string) (*GitLabClient, error) {
	if c == nil || !validBranch(target) || !validPrefix(prefix) {
		return nil, errors.New("invalid GitLab draft target or branch prefix")
	}
	copy := *c
	copy.config.TargetBranch = target
	copy.config.BranchPrefix = prefix
	copy.mrLabel = notificationRouteDraftLabel
	return &copy, nil
}

// ForInhibitionDrafts isolates review-only inhibition proposals from both
// importable promotions and notification route drafts.
func (c *GitLabClient) ForInhibitionDrafts(target, prefix string) (*GitLabClient, error) {
	if c == nil || !validBranch(target) || !validPrefix(prefix) {
		return nil, errors.New("invalid GitLab inhibition draft target or branch prefix")
	}
	copy := *c
	copy.config.TargetBranch = target
	copy.config.BranchPrefix = prefix
	copy.mrLabel = inhibitionDraftLabel
	return &copy, nil
}

// ForAlertRuleDrafts exports review-only JSON records. Its target, source
// namespace and label are separate from importable configuration promotions.
func (c *GitLabClient) ForAlertRuleDrafts(target, prefix string) (*GitLabClient, error) {
	if c == nil || !validBranch(target) || prefix != "aiops-alert-rule-drafts/" ||
		target == c.config.TargetBranch || strings.HasPrefix(prefix, c.config.BranchPrefix) ||
		strings.HasPrefix(c.config.BranchPrefix, prefix) {
		return nil, errors.New("invalid GitLab alert rule draft target or branch prefix")
	}
	copy := *c
	copy.config.TargetBranch = target
	copy.config.BranchPrefix = prefix
	copy.mrLabel = alertRuleDraftLabel
	return &copy, nil
}

func NewGitLabClient(config Config) (*GitLabClient, error) {
	u, err := url.Parse(config.BaseURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid GitLab base URL")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !config.AllowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return nil, errors.New("GitLab requires HTTPS except explicit loopback test")
		}
	}
	if config.ProjectID <= 0 || !validBranch(config.TargetBranch) || !validPrefix(config.BranchPrefix) {
		return nil, errors.New("invalid GitLab project, target branch or promotion branch prefix")
	}
	info, err := os.Lstat(config.TokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("GitLab token file must be regular and mode 0600")
	}
	if info.Size() < 1 || info.Size() > 16384 {
		return nil, errors.New("invalid GitLab token file size")
	}
	f, err := os.Open(config.TokenFile)
	if err != nil {
		return nil, errors.New("cannot read GitLab token file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, errors.New("GitLab token file changed during opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(b) > 16384 {
		return nil, errors.New("cannot read GitLab token file")
	}
	token := strings.TrimSpace(string(b))
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("invalid GitLab token")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if config.CAFile != "" {
		if u.Scheme != "https" {
			return nil, errors.New("GitLab CA file is only for an HTTPS GitLab")
		}
		pool, err := gitLabCAPool(config.CAFile)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	web := u
	if config.WebURL != "" {
		web, err = url.Parse(config.WebURL)
		if err != nil || web == nil || (web.Scheme != "https" && web.Scheme != "http") || web.Host == "" || web.User != nil ||
			web.RawQuery != "" || web.Fragment != "" || (web.Path != "" && web.Path != "/") {
			return nil, errors.New("invalid GitLab web URL")
		}
	}
	return &GitLabClient{config: config, base: u, web: web, token: token, mrLabel: promotionLabel, http: &http.Client{
		Timeout: 8 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

// gitLabCAPool reads a CA bundle that only its owner can change.
func gitLabCAPool(path string) (*x509.CertPool, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n") {
		return nil, errors.New("GitLab CA file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() < 1 || info.Size() > 1<<20 {
		return nil, errors.New("GitLab CA file must be a regular file of at most 1 MiB that neither group nor others can write")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read GitLab CA file")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("GitLab CA file changed during opening")
	}
	pem, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(pem) > 1<<20 {
		return nil, errors.New("cannot read GitLab CA file")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("GitLab CA file holds no PEM certificate")
	}
	return pool, nil
}

func validBranch(s string) bool {
	return branchRE.MatchString(s) && !strings.Contains(s, "..") && !strings.Contains(s, "//") && !strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".")
}

func validPrefix(s string) bool {
	return strings.HasSuffix(s, "/") && len(s) < 80 && validBranch(strings.TrimSuffix(s, "/"))
}

func validPath(s string) bool {
	if len(s) < 6 || len(s) > 240 || !strings.HasSuffix(s, ".json") {
		return false
	}
	parts := strings.Split(s, "/")
	for _, part := range parts {
		if part == "." || part == ".." || !pathPartRE.MatchString(part) {
			return false
		}
	}
	return true
}

func (c *GitLabClient) projectPath() string {
	return "/api/v4/projects/" + strconv.FormatInt(c.config.ProjectID, 10)
}

// gitLabRetryDelay is the pause before the one retry of a failed read.
var gitLabRetryDelay = 300 * time.Millisecond

// request reads or writes once; a GET that failed in transit or at a
// gateway (502, 503, 504) is tried once more, since a proxy in front of
// GitLab can drop a new connection now and then (PA-103). Writes are never
// repeated here: their callers reconcile instead.
func (c *GitLabClient) request(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, http.Header, error) {
	status, data, header, err := c.requestOnce(ctx, method, path, query, body)
	transient := (err != nil && errors.Is(err, ErrGitLabUnavailable)) || status == 502 || status == 503 || status == 504
	if method != http.MethodGet || !transient || ctx.Err() != nil {
		return status, data, header, err
	}
	timer := time.NewTimer(gitLabRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return status, data, header, err
	case <-timer.C:
	}
	return c.requestOnce(ctx, method, path, query, body)
}

func (c *GitLabClient) requestOnce(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, http.Header, error) {
	u, err := url.Parse(strings.TrimRight(c.base.String(), "/") + path)
	if err != nil {
		return 0, nil, nil, err
	}
	u.RawQuery = query.Encode()
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		input = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), input)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %w", ErrGitLabUnavailable, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %w", ErrGitLabUnavailable, err)
	}
	if len(b) > 1024*1024 {
		return 0, nil, nil, errors.New("GitLab response exceeds limit")
	}
	return resp.StatusCode, b, resp.Header, nil
}

func (c *GitLabClient) call(ctx context.Context, method, path string, query url.Values, body any, expected int, out any) (int, http.Header, error) {
	status, data, header, err := c.request(ctx, method, path, query, body)
	if err != nil {
		return 0, nil, err
	}
	if status != expected {
		return status, header, &StatusError{Status: status}
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return status, header, errors.New("invalid GitLab response")
	}
	return status, header, nil
}

// StatusError is a GitLab answer with another status than expected. A
// 401, 403, 429 or 5xx is ErrGitLabUnavailable.
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("GitLab status %d", e.Status) }

func (e *StatusError) Unwrap() error {
	if e.Status == 401 || e.Status == 403 || e.Status == 429 || e.Status >= 500 {
		return ErrGitLabUnavailable
	}
	return nil
}

// MergedCheckError says which check of a merged MR failed (PA-103): the
// step, GitLab's status when a request failed, and the cause, which is
// ErrGitLabUnavailable, ErrMRNotPrepared or ErrUntrusted. It carries no
// token or response body.
type MergedCheckError struct {
	IID    int64
	Step   string
	Status int
	Err    error
}

func (e *MergedCheckError) Error() string {
	return fmt.Sprintf("merged MR !%d %s check: %v", e.IID, e.Step, e.Err)
}

func (e *MergedCheckError) Unwrap() error { return e.Err }

// checkStep names the step of a failed merged MR check. A failure that is
// not GitLab being unavailable or the MR not being ready is untrusted.
func checkStep(step string, err error) error {
	var check *MergedCheckError
	if errors.As(err, &check) {
		return err
	}
	out := &MergedCheckError{Step: step, Err: err}
	var status *StatusError
	if errors.As(err, &status) {
		out.Status = status.Status
	}
	if !errors.Is(err, ErrGitLabUnavailable) && !errors.Is(err, ErrUntrusted) {
		out.Err = fmt.Errorf("%w: %w", ErrUntrusted, err)
	}
	return out
}

func (c *GitLabClient) trustedMR(m MR, requireMerged bool) error {
	if m.IID <= 0 || m.SourceProjectID != c.config.ProjectID || m.TargetProjectID != c.config.ProjectID || m.TargetBranch != c.config.TargetBranch || !strings.HasPrefix(m.SourceBranch, c.config.BranchPrefix) || !validBranch(m.SourceBranch) {
		return ErrUntrusted
	}
	// /diffs may omit files when a GitLab diff limit is exceeded. GitLab's
	// independent changes_count must confirm one changed file before a
	// single returned diff can be treated as complete. Empty/asynchronous
	// or capped counts fail closed for a merged MR.
	if requireMerged && (m.State != "merged" || !shaRE.MatchString(m.MergeCommitSHA) ||
		!shaRE.MatchString(m.SourceCommitSHA) || m.Author.ID <= 0 || m.MergedAt.IsZero()) {
		return ErrUntrusted
	}
	if requireMerged && m.ChangesCount == "" {
		return ErrMRNotPrepared
	}
	if requireMerged && m.ChangesCount != "1" {
		return ErrUntrusted
	}
	if m.State != "merged" && m.State != "opened" {
		return ErrUntrusted
	}
	if strings.TrimSpace(m.Author.Name) == "" || len(m.Author.Name) > 120 || strings.IndexFunc(m.Author.Name, unicode.IsControl) >= 0 {
		return ErrUntrusted
	}
	if strings.TrimSpace(m.Title) == "" || len(m.Title) > 180 || strings.IndexFunc(m.Title, unicode.IsControl) >= 0 {
		return ErrUntrusted
	}
	u, err := url.Parse(m.URL)
	if err != nil || u.User != nil || u.Fragment != "" || u.Path == "" {
		return ErrUntrusted
	}
	if u.Scheme != c.web.Scheme || !strings.EqualFold(u.Host, c.web.Host) {
		return ErrMRLinkMismatch
	}
	return nil
}

func (c *GitLabClient) file(ctx context.Context, path, ref string) ([]byte, int, error) {
	q := url.Values{"ref": {ref}}
	status, data, _, err := c.request(ctx, http.MethodGet, c.projectPath()+"/repository/files/"+url.PathEscape(path)+"/raw", q, nil)
	if err != nil {
		return nil, 0, err
	}
	if status != 200 {
		return nil, status, &StatusError{Status: status}
	}
	if len(data) > 256*1024 {
		return nil, 0, errors.New("GitLab configuration file exceeds limit")
	}
	return data, status, nil
}

func (c *GitLabClient) findMR(ctx context.Context, branch string) (MR, bool, error) {
	q := url.Values{"source_branch": {branch}, "target_branch": {c.config.TargetBranch}, "state": {"all"}, "per_page": {"20"}, "page": {"1"}}
	var list []MR
	_, h, err := c.call(ctx, http.MethodGet, c.projectPath()+"/merge_requests", q, nil, 200, &list)
	if err != nil {
		return MR{}, false, err
	}
	if h.Get("X-Next-Page") != "" || len(list) > 1 {
		return MR{}, false, ErrConflict
	}
	if len(list) == 0 {
		return MR{}, false, nil
	}
	if list[0].SourceBranch != branch {
		return MR{}, false, ErrUntrusted
	}
	if err := c.trustedMR(list[0], false); err != nil {
		return MR{}, false, err
	}
	return list[0], true, nil
}

// Export reconciles the same branch, path and payload before each write.
// Callers persist that identity. Lease expiry can overlap calls: a competing
// GitLab write may return ErrConflict, and retrying this identity reconciles
// the accepted result. This is not a remote uniqueness guarantee for all time.
func (c *GitLabClient) Export(ctx context.Context, branch, path string, payload []byte, title string) (MR, error) {
	if !validBranch(branch) || !strings.HasPrefix(branch, c.config.BranchPrefix) || !validPath(path) || len(payload) == 0 || len(payload) > 256*1024 || !json.Valid(payload) || len(title) == 0 || len(title) > 180 {
		return MR{}, errors.New("invalid GitLab export input")
	}
	if m, found, err := c.findMR(ctx, branch); err != nil {
		return MR{}, err
	} else if found {
		ref := m.SourceCommitSHA
		if m.State == "merged" {
			ref = m.MergeCommitSHA
		}
		if !shaRE.MatchString(ref) {
			return MR{}, ErrUntrusted
		}
		data, _, err := c.file(ctx, path, ref)
		if err != nil {
			return MR{}, err
		}
		if !bytes.Equal(data, payload) {
			return MR{}, ErrConflict
		}
		return m, nil
	}
	branchPath := c.projectPath() + "/repository/branches/" + url.PathEscape(branch)
	status, _, err := c.call(ctx, http.MethodGet, branchPath, nil, nil, 200, nil)
	if err != nil && status != 404 {
		return MR{}, err
	}
	if status == 404 {
		q := url.Values{"branch": {branch}, "ref": {c.config.TargetBranch}}
		status, _, err = c.call(ctx, http.MethodPost, c.projectPath()+"/repository/branches", q, nil, 201, nil)
		if err != nil {
			if status == 400 || status == 409 {
				return MR{}, ErrConflict
			}
			return MR{}, &ExportError{Stage: "branch", MayHaveChanged: true, Err: err}
		}
	}
	data, fileStatus, err := c.file(ctx, path, branch)
	if err != nil && fileStatus != 404 {
		return MR{}, &ExportError{Stage: "read_file", MayHaveChanged: true, Err: err}
	}
	if fileStatus == 200 && !bytes.Equal(data, payload) {
		return MR{}, ErrConflict
	}
	if fileStatus == 404 {
		message := "Add validated AIOps configuration promotion"
		if c.mrLabel == notificationRouteDraftLabel {
			message = "Add reviewed AIOps notification route draft"
		} else if c.mrLabel == inhibitionDraftLabel {
			message = "Add reviewed AIOps inhibition rule draft"
		} else if c.mrLabel == alertRuleDraftLabel {
			message = "Add reviewed AIOps alert rule draft"
		}
		body := map[string]string{"branch": branch, "content": string(payload), "commit_message": message}
		status, _, err = c.call(ctx, http.MethodPost, c.projectPath()+"/repository/files/"+url.PathEscape(path), nil, body, 201, nil)
		if err != nil {
			if status == 400 || status == 409 {
				return MR{}, ErrConflict
			}
			return MR{}, &ExportError{Stage: "file", MayHaveChanged: true, Err: err}
		}
	}
	if m, found, err := c.findMR(ctx, branch); err != nil {
		return MR{}, err
	} else if found {
		return c.reconcileMR(ctx, m, path, payload)
	}
	var m MR
	body := map[string]string{"source_branch": branch, "target_branch": c.config.TargetBranch,
		"title": title, "labels": c.mrLabel}
	status, _, err = c.call(ctx, http.MethodPost, c.projectPath()+"/merge_requests", nil, body, 201, &m)
	if err != nil {
		if status == 400 || status == 409 {
			return MR{}, ErrConflict
		}
		return MR{}, &ExportError{Stage: "merge_request", MayHaveChanged: true, Err: err}
	}
	if m.SourceBranch != branch {
		return MR{}, ErrUntrusted
	}
	if err := c.trustedMR(m, false); err != nil {
		return MR{}, err
	}
	return m, nil
}

func (c *GitLabClient) reconcileMR(ctx context.Context, m MR, path string, payload []byte) (MR, error) {
	ref := m.SourceCommitSHA
	if m.State == "merged" {
		ref = m.MergeCommitSHA
	}
	if !shaRE.MatchString(ref) {
		return MR{}, ErrUntrusted
	}
	data, _, err := c.file(ctx, path, ref)
	if err != nil {
		return MR{}, err
	}
	if !bytes.Equal(data, payload) {
		return MR{}, ErrConflict
	}
	return m, nil
}

func (c *GitLabClient) ListMerged(ctx context.Context, limit int) ([]MR, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid GitLab merge request limit")
	}
	// Labels can be removed after merge. Query every MR on the bounded target
	// branch, then select only our generated promotion branches locally.
	q := url.Values{"scope": {"all"}, "state": {"merged"}, "target_branch": {c.config.TargetBranch},
		"per_page": {strconv.Itoa(limit)}, "page": {"1"}}
	var list []MR
	_, h, err := c.call(ctx, http.MethodGet, c.projectPath()+"/merge_requests", q, nil, 200, &list)
	if err != nil {
		return nil, err
	}
	// GitLab cannot filter by a branch prefix. An incomplete first page, even
	// when filled by unrelated MRs, cannot establish a complete promotion list.
	// A full page without pagination metadata is ambiguous and also fails.
	if h.Get("X-Next-Page") != "" || len(list) >= limit {
		return nil, ErrListTooLarge
	}
	result := make([]MR, 0, len(list))
	for _, m := range list {
		// Listing shows review candidates. GitLab may omit the asynchronous
		// changes_count from a list item; exact file verification happens on
		// the selected import through Merged and ReadMerged.
		if !strings.HasPrefix(m.SourceBranch, c.config.BranchPrefix) {
			continue // An unrelated branch is not a promotion candidate.
		}
		id := strings.TrimPrefix(m.SourceBranch, c.config.BranchPrefix)
		if err := c.trustedMR(m, false); err != nil {
			return nil, err
		}
		if !promotionIDRE.MatchString(id) || m.State != "merged" || !shaRE.MatchString(m.MergeCommitSHA) {
			return nil, ErrUntrusted
		}
		result = append(result, m)
	}
	return result, nil
}

func (c *GitLabClient) ReadMerged(ctx context.Context, iid int64, path string) (MR, []byte, error) {
	if iid <= 0 || !validPath(path) {
		return MR{}, nil, errors.New("invalid GitLab import identity")
	}
	m, err := c.Merged(ctx, iid)
	if err != nil {
		return MR{}, nil, err
	}
	if err := c.verifyMergedReview(ctx, m); err != nil {
		return MR{}, nil, err
	}
	diffPath, err := c.mergedDiffPath(ctx, m)
	if err != nil {
		return MR{}, nil, err
	}
	if diffPath != path {
		return MR{}, nil, ErrUntrusted
	}
	data, _, err := c.file(ctx, path, m.MergeCommitSHA)
	if err != nil {
		return MR{}, nil, err
	}
	if !json.Valid(data) {
		return MR{}, nil, ErrUntrusted
	}
	return m, data, nil
}

// ReadMergedManifest derives the exact file from the complete, reviewed MR
// diff. Its path and promotion ID must agree with the owned branch; callers
// cannot supply a path or read mutable branch HEAD.
// ReadMergedManifest verifies a merged promotion MR and reads its one
// file. A failure is a *MergedCheckError naming the MR and the step.
func (c *GitLabClient) ReadMergedManifest(ctx context.Context, iid int64) (MR, string, []byte, error) {
	m, path, data, err := c.readMergedManifest(ctx, iid)
	if err != nil {
		err = checkStep("merge_request", err)
		var check *MergedCheckError
		if errors.As(err, &check) {
			check.IID = iid
		}
		return MR{}, "", nil, err
	}
	return m, path, data, nil
}

func (c *GitLabClient) readMergedManifest(ctx context.Context, iid int64) (MR, string, []byte, error) {
	m, err := c.Merged(ctx, iid)
	if err != nil {
		return MR{}, "", nil, checkStep("merge_request", err)
	}
	if err := c.verifyMergedReview(ctx, m); err != nil {
		return MR{}, "", nil, err
	}
	path, err := c.mergedDiffPath(ctx, m)
	if err != nil {
		return MR{}, "", nil, checkStep("diff", err)
	}
	id := strings.TrimPrefix(m.SourceBranch, c.config.BranchPrefix)
	if !promotionIDRE.MatchString(id) || !validPromotionManifestPath(path, id) {
		return MR{}, "", nil, checkStep("diff", ErrUntrusted)
	}
	data, _, err := c.file(ctx, path, m.MergeCommitSHA)
	if err != nil {
		return MR{}, "", nil, checkStep("file", err)
	}
	if !json.Valid(data) {
		return MR{}, "", nil, checkStep("file", ErrUntrusted)
	}
	return m, path, data, nil
}

func validPromotionManifestPath(path, id string) bool {
	if !promotionIDRE.MatchString(id) {
		return false
	}
	for _, dir := range []string{"templates", "runbooks", "alert-rules", "agent-versions"} {
		if path == "ops-config/"+dir+"/"+id+".json" {
			return true
		}
	}
	return false
}

func (c *GitLabClient) mergedDiffPath(ctx context.Context, m MR) (string, error) {
	root := c.projectPath() + "/merge_requests/" + strconv.FormatInt(m.IID, 10)
	q := url.Values{"per_page": {"100"}, "page": {"1"}}
	var diffs []struct {
		NewPath     string `json:"new_path"`
		OldPath     string `json:"old_path"`
		NewFile     bool   `json:"new_file"`
		DeletedFile bool   `json:"deleted_file"`
		RenamedFile bool   `json:"renamed_file"`
		TooLarge    bool   `json:"too_large"`
		Collapsed   bool   `json:"collapsed"`
	}
	_, h, err := c.call(ctx, http.MethodGet, root+"/diffs", q, nil, 200, &diffs)
	if err != nil {
		return "", err
	}
	// Export creates exactly one new file. Any extra or truncated diff fails closed.
	if h.Get("X-Next-Page") != "" || len(diffs) != 1 || diffs[0].NewPath == "" || diffs[0].NewPath != diffs[0].OldPath || !diffs[0].NewFile || diffs[0].DeletedFile || diffs[0].RenamedFile || diffs[0].TooLarge || diffs[0].Collapsed {
		return "", ErrUntrusted
	}
	return diffs[0].NewPath, nil
}

// verifyMergedReview is an independent import gate. The branch rules checked
// here are current policy only; historical protection at merge time still
// requires captured merge-time governance evidence.
func (c *GitLabClient) verifyMergedReview(ctx context.Context, m MR) error {
	root := c.projectPath() + "/merge_requests/" + strconv.FormatInt(m.IID, 10)
	var versions []struct {
		HeadCommitSHA string    `json:"head_commit_sha"`
		CreatedAt     time.Time `json:"created_at"`
		State         string    `json:"state"`
	}
	q := url.Values{"per_page": {"100"}, "page": {"1"}}
	_, h, err := c.call(ctx, http.MethodGet, root+"/versions", q, nil, 200, &versions)
	if err != nil {
		return checkStep("versions", err)
	}
	if h.Get("X-Next-Page") != "" || len(versions) == 0 || len(versions) > 100 {
		return checkStep("versions", ErrUntrusted)
	}
	latest := versions[0]
	for _, v := range versions[1:] {
		if v.CreatedAt.After(latest.CreatedAt) {
			latest = v
		}
	}
	if latest.State != "collected" || latest.CreatedAt.IsZero() || !latest.CreatedAt.Before(m.MergedAt) || latest.HeadCommitSHA != m.SourceCommitSHA {
		return checkStep("versions", ErrUntrusted)
	}
	var approvals struct {
		IID        *int64 `json:"iid"`
		ProjectID  *int64 `json:"project_id"`
		State      string `json:"state"`
		Approved   *bool  `json:"approved"`
		ApprovedBy []struct {
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
			ApprovedAt time.Time `json:"approved_at"`
		} `json:"approved_by"`
	}
	_, _, err = c.call(ctx, http.MethodGet, root+"/approvals", nil, nil, 200, &approvals)
	// GitLab EE may report approved=true with no applicable rules. Require both
	// satisfied current MR approval state and a distinct valid reviewer below.
	// GitLab CE's /approvals response has no MR/project/state fields. The
	// request path is built from the exact MR validated by Merged. Some
	// editions may include identifiers; reject them when inconsistent.
	if err != nil {
		return checkStep("approvals", err)
	}
	if (approvals.IID != nil && *approvals.IID != m.IID) ||
		(approvals.ProjectID != nil && *approvals.ProjectID != c.config.ProjectID) ||
		(approvals.State != "" && approvals.State != "merged") ||
		approvals.Approved == nil || !*approvals.Approved || len(approvals.ApprovedBy) == 0 || len(approvals.ApprovedBy) > 20 {
		return checkStep("approvals", ErrUntrusted)
	}
	reviewerVerified := false
	var approverErr error
	for _, a := range approvals.ApprovedBy {
		if a.User.ID <= 0 || a.User.ID == m.Author.ID || !a.ApprovedAt.After(latest.CreatedAt) || !a.ApprovedAt.Before(m.MergedAt) {
			continue
		}
		var user struct {
			ID  int64 `json:"id"`
			Bot *bool `json:"bot"`
		}
		_, _, err = c.call(ctx, http.MethodGet, "/api/v4/users/"+strconv.FormatInt(a.User.ID, 10), nil, nil, 200, &user)
		if err == nil && user.ID == a.User.ID && user.Bot != nil && !*user.Bot {
			reviewerVerified = true
			break
		}
		if errors.Is(err, ErrGitLabUnavailable) {
			approverErr = err
		}
	}
	if !reviewerVerified {
		// An approver that could not be read is not one that failed.
		if approverErr != nil {
			return checkStep("approver", approverErr)
		}
		return checkStep("approver", ErrUntrusted)
	}
	if err := c.verifyProtectedTarget(ctx); err != nil {
		return checkStep("protected_branch", err)
	}
	return nil
}

type branchAccessLevel struct {
	AccessLevel  *int   `json:"access_level"`
	UserID       *int64 `json:"user_id"`
	GroupID      *int64 `json:"group_id"`
	DeployKeyID  *int64 `json:"deploy_key_id"`
	MemberRoleID *int64 `json:"member_role_id"`
}

type protectedBranchRule struct {
	Name              string              `json:"name"`
	AllowForcePush    *bool               `json:"allow_force_push"`
	PushAccessLevels  []branchAccessLevel `json:"push_access_levels"`
	MergeAccessLevels []branchAccessLevel `json:"merge_access_levels"`
}

func safeProtectedRule(rule protectedBranchRule) bool {
	if rule.AllowForcePush == nil || *rule.AllowForcePush || len(rule.PushAccessLevels) != 1 || len(rule.MergeAccessLevels) == 0 {
		return false
	}
	push := rule.PushAccessLevels[0]
	if push.AccessLevel == nil || *push.AccessLevel != 0 || push.UserID != nil || push.GroupID != nil || push.DeployKeyID != nil || push.MemberRoleID != nil {
		return false
	}
	for _, merge := range rule.MergeAccessLevels {
		if merge.AccessLevel == nil || (*merge.AccessLevel != 30 && *merge.AccessLevel != 40 && *merge.AccessLevel != 60) ||
			merge.UserID != nil || merge.GroupID != nil || merge.DeployKeyID != nil || merge.MemberRoleID != nil {
			return false
		}
	}
	return true
}

// GitLab's documented protected-branch wildcard uses '*' across branch-name
// characters, including '/'. Unknown pattern syntax fails closed.
func branchPatternMatches(pattern, branch string) (bool, error) {
	if pattern == "" || len(pattern) > 255 || strings.ContainsAny(pattern, "?[]\\") {
		return false, ErrUntrusted
	}
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == branch, nil
	}
	if !strings.HasPrefix(branch, parts[0]) {
		return false, nil
	}
	rest := branch[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		index := strings.Index(rest, part)
		if index < 0 {
			return false, nil
		}
		rest = rest[index+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1]), nil
}

func (c *GitLabClient) verifyProtectedTarget(ctx context.Context) error {
	var branch struct {
		Name      string `json:"name"`
		Protected *bool  `json:"protected"`
	}
	_, _, err := c.call(ctx, http.MethodGet, c.projectPath()+"/repository/branches/"+url.PathEscape(c.config.TargetBranch), nil, nil, 200, &branch)
	if errors.Is(err, ErrGitLabUnavailable) {
		return err
	}
	if err != nil || branch.Name != c.config.TargetBranch || branch.Protected == nil || !*branch.Protected {
		return ErrUntrusted
	}
	var policy protectedBranchRule
	_, _, err = c.call(ctx, http.MethodGet, c.projectPath()+"/protected_branches/"+url.PathEscape(c.config.TargetBranch), nil, nil, 200, &policy)
	if errors.Is(err, ErrGitLabUnavailable) {
		return err
	}
	if err != nil || policy.Name != c.config.TargetBranch || !safeProtectedRule(policy) {
		return ErrUntrusted
	}
	exactFound := false
	for page := 1; page <= 5; page++ {
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var rules []protectedBranchRule
		_, h, err := c.call(ctx, http.MethodGet, c.projectPath()+"/protected_branches", q, nil, 200, &rules)
		if errors.Is(err, ErrGitLabUnavailable) {
			return err
		}
		if err != nil || len(rules) > 100 || h.Get("X-Page") != strconv.Itoa(page) || h.Get("X-Per-Page") != "100" {
			return ErrUntrusted
		}
		for _, rule := range rules {
			matches, err := branchPatternMatches(rule.Name, c.config.TargetBranch)
			if err != nil {
				return ErrUntrusted
			}
			if !matches {
				continue
			}
			if rule.Name == c.config.TargetBranch {
				exactFound = true
			}
			// GitLab applies the most permissive matching rule. Every matching
			// exact, wildcard and inherited rule must independently be safe.
			if !safeProtectedRule(rule) {
				return ErrUntrusted
			}
		}
		next := h.Get("X-Next-Page")
		if next == "" {
			if len(rules) == 100 {
				if page == 5 {
					return ErrUntrusted
				}
				continue
			}
			if !exactFound {
				return ErrUntrusted
			}
			return nil
		}
		if page == 5 || next != strconv.Itoa(page+1) || len(rules) == 0 {
			return ErrUntrusted
		}
	}
	return ErrUntrusted
}

// Merged retrieves one exact IID, so importing a selected item does not
// depend on its position in the bounded configuration list.
func (c *GitLabClient) Merged(ctx context.Context, iid int64) (MR, error) {
	if iid <= 0 {
		return MR{}, errors.New("invalid GitLab merge request IID")
	}
	root := c.projectPath() + "/merge_requests/" + strconv.FormatInt(iid, 10)
	var m MR
	_, _, err := c.call(ctx, http.MethodGet, root, nil, nil, 200, &m)
	if err != nil {
		return MR{}, err
	}
	if m.IID != iid {
		return MR{}, ErrUntrusted
	}
	if err := c.trustedMR(m, true); err != nil {
		return MR{}, err
	}
	return m, nil
}

// LookupPromotionMR reads one exact, owned MR for the exported ledger status.
// It does not assert approval, complete diff, or import readiness.
func (c *GitLabClient) LookupPromotionMR(ctx context.Context, iid int64) (MR, error) {
	if iid <= 0 {
		return MR{}, errors.New("invalid GitLab merge request IID")
	}
	root := c.projectPath() + "/merge_requests/" + strconv.FormatInt(iid, 10)
	var m MR
	_, _, err := c.call(ctx, http.MethodGet, root, nil, nil, 200, &m)
	if err != nil {
		return MR{}, err
	}
	id := strings.TrimPrefix(m.SourceBranch, c.config.BranchPrefix)
	if err := c.trustedMR(m, false); err != nil {
		return MR{}, err
	}
	if m.IID != iid || !strings.HasPrefix(m.SourceBranch, c.config.BranchPrefix) ||
		!promotionIDRE.MatchString(id) || (m.State == "merged" && !shaRE.MatchString(m.MergeCommitSHA)) {
		return MR{}, ErrUntrusted
	}
	u, _ := url.Parse(m.URL) // trustedMR already validated this URL and host.
	if u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/-/merge_requests/"+strconv.FormatInt(iid, 10)) {
		return MR{}, ErrUntrusted
	}
	return m, nil
}
