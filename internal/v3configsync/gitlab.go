package v3configsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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
	ErrConflict     = errors.New("GitLab export identity conflicts with existing content")
	ErrUntrusted    = errors.New("GitLab merge request provenance cannot be verified")
	ErrListTooLarge = errors.New("GitLab merge request list exceeds configured bound")
	branchRE        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,127}$`)
	shaRE           = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
	pathPartRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

const promotionLabel = "aiops-config-promotion"

type GitLabClient struct {
	config Config
	base   *url.URL
	token  string
	http   *http.Client
}

func (c *GitLabClient) ProjectID() int64     { return c.config.ProjectID }
func (c *GitLabClient) BranchPrefix() string { return c.config.BranchPrefix }

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
	return &GitLabClient{config: config, base: u, token: token, http: &http.Client{
		Timeout: 8 * time.Second, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
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

func (c *GitLabClient) request(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, http.Header, error) {
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
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return 0, nil, nil, err
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
		return status, header, fmt.Errorf("GitLab status %d", status)
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return status, header, errors.New("invalid GitLab response")
	}
	return status, header, nil
}

func (c *GitLabClient) trustedMR(m MR, requireMerged bool) error {
	if m.IID <= 0 || m.SourceProjectID != c.config.ProjectID || m.TargetProjectID != c.config.ProjectID || m.TargetBranch != c.config.TargetBranch || !strings.HasPrefix(m.SourceBranch, c.config.BranchPrefix) || !validBranch(m.SourceBranch) {
		return ErrUntrusted
	}
	// /diffs may omit files when a GitLab diff limit is exceeded. GitLab's
	// independent changes_count must confirm one changed file before a
	// single returned diff can be treated as complete. Empty/asynchronous
	// or capped counts fail closed for a merged MR.
	if requireMerged && (m.State != "merged" || !shaRE.MatchString(m.MergeCommitSHA) || m.ChangesCount != "1" ||
		!shaRE.MatchString(m.SourceCommitSHA) || m.Author.ID <= 0 || m.MergedAt.IsZero()) {
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
	if err != nil || u.Scheme != c.base.Scheme || !strings.EqualFold(u.Host, c.base.Host) || u.User != nil || u.Path == "" {
		return ErrUntrusted
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
		return nil, status, fmt.Errorf("GitLab status %d", status)
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
	if list[0].SourceBranch != branch || c.trustedMR(list[0], false) != nil {
		return MR{}, false, ErrUntrusted
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
		body := map[string]string{"branch": branch, "content": string(payload), "commit_message": "Add validated AIOps configuration promotion"}
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
		"title": title, "labels": promotionLabel}
	status, _, err = c.call(ctx, http.MethodPost, c.projectPath()+"/merge_requests", nil, body, 201, &m)
	if err != nil {
		if status == 400 || status == 409 {
			return MR{}, ErrConflict
		}
		return MR{}, &ExportError{Stage: "merge_request", MayHaveChanged: true, Err: err}
	}
	if m.SourceBranch != branch || c.trustedMR(m, false) != nil {
		return MR{}, ErrUntrusted
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
	q := url.Values{"state": {"merged"}, "target_branch": {c.config.TargetBranch},
		"labels": {promotionLabel}, "per_page": {strconv.Itoa(limit)}, "page": {"1"}}
	var list []MR
	_, h, err := c.call(ctx, http.MethodGet, c.projectPath()+"/merge_requests", q, nil, 200, &list)
	if err != nil {
		return nil, err
	}
	// GitLab cannot filter by a branch prefix. A first page with more results
	// is not a complete configuration list, even if this page contains no
	// promotion branches. Fail instead of presenting a false empty list.
	if h.Get("X-Next-Page") != "" || len(list) > limit {
		return nil, ErrListTooLarge
	}
	result := make([]MR, 0, len(list))
	for _, m := range list {
		// Listing shows review candidates. GitLab may omit the asynchronous
		// changes_count from a list item; exact file verification happens on
		// the selected import through Merged and ReadMerged.
		if c.trustedMR(m, false) == nil && m.State == "merged" && shaRE.MatchString(m.MergeCommitSHA) {
			result = append(result, m)
		}
	}
	return result, nil
}

func (c *GitLabClient) ReadMerged(ctx context.Context, iid int64, path string) (MR, []byte, error) {
	if iid <= 0 || !validPath(path) {
		return MR{}, nil, errors.New("invalid GitLab import identity")
	}
	root := c.projectPath() + "/merge_requests/" + strconv.FormatInt(iid, 10)
	m, err := c.Merged(ctx, iid)
	if err != nil {
		return MR{}, nil, err
	}
	if err := c.verifyMergedReview(ctx, m); err != nil {
		return MR{}, nil, err
	}
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
		return MR{}, nil, err
	}
	// Export creates exactly one new file. Any extra or truncated diff fails closed.
	if h.Get("X-Next-Page") != "" || len(diffs) != 1 || diffs[0].NewPath != path || diffs[0].OldPath != path || !diffs[0].NewFile || diffs[0].DeletedFile || diffs[0].RenamedFile || diffs[0].TooLarge || diffs[0].Collapsed {
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
	if err != nil || h.Get("X-Next-Page") != "" || len(versions) == 0 || len(versions) > 100 {
		return ErrUntrusted
	}
	latest := versions[0]
	for _, v := range versions[1:] {
		if v.CreatedAt.After(latest.CreatedAt) {
			latest = v
		}
	}
	if latest.State != "collected" || latest.CreatedAt.IsZero() || !latest.CreatedAt.Before(m.MergedAt) || latest.HeadCommitSHA != m.SourceCommitSHA {
		return ErrUntrusted
	}
	var approvals struct {
		IID        int64  `json:"iid"`
		ProjectID  int64  `json:"project_id"`
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
	if err != nil || approvals.IID != m.IID || approvals.ProjectID != c.config.ProjectID || approvals.State != "merged" ||
		approvals.Approved == nil || !*approvals.Approved || len(approvals.ApprovedBy) == 0 || len(approvals.ApprovedBy) > 20 {
		return ErrUntrusted
	}
	reviewerVerified := false
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
	}
	if !reviewerVerified {
		return ErrUntrusted
	}
	return c.verifyProtectedTarget(ctx)
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
	if err != nil || branch.Name != c.config.TargetBranch || branch.Protected == nil || !*branch.Protected {
		return ErrUntrusted
	}
	var policy protectedBranchRule
	_, _, err = c.call(ctx, http.MethodGet, c.projectPath()+"/protected_branches/"+url.PathEscape(c.config.TargetBranch), nil, nil, 200, &policy)
	if err != nil || policy.Name != c.config.TargetBranch || !safeProtectedRule(policy) {
		return ErrUntrusted
	}
	exactFound := false
	for page := 1; page <= 5; page++ {
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var rules []protectedBranchRule
		_, h, err := c.call(ctx, http.MethodGet, c.projectPath()+"/protected_branches", q, nil, 200, &rules)
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
	if m.IID != iid || c.trustedMR(m, true) != nil {
		return MR{}, ErrUntrusted
	}
	return m, nil
}
