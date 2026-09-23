package delivery

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type GitLabBinding struct {
	ApplicationID string   `json:"application_id"`
	EnvironmentID string   `json:"environment_id"`
	ProjectID     int64    `json:"project_id"`
	AllowedRefs   []string `json:"allowed_refs"`
	AllowActions  bool     `json:"allow_actions"`
	// Existing GitLab pipelines retain their own deployment/approval policy.
	// Enabling actions explicitly accepts this boundary; it is shown in the UI.
	ExternalGovernance bool     `json:"external_governance"`
	ManualJobs         []string `json:"manual_jobs,omitempty"`
}
type GitLabConfig struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	BaseURL          string          `json:"base_url"`
	TokenFile        string          `json:"token_file"`
	WebhookTokenFile string          `json:"webhook_token_file"`
	AllowHTTP        bool            `json:"allow_http"`
	Bindings         []GitLabBinding `json:"bindings"`
}
type GitLabClient struct {
	Config       GitLabConfig
	token        string
	webhookToken string
	http         *http.Client
}
type GitLabProject struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path_with_namespace"`
	WebURL        string `json:"web_url"`
	DefaultBranch string `json:"default_branch"`
}
type GitLabPipeline struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Source    string    `json:"source"`
	WebURL    string    `json:"web_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type GitLabJob struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Stage    string `json:"stage"`
	Status   string `json:"status"`
	WebURL   string `json:"web_url"`
	Pipeline struct {
		ID int64 `json:"id"`
	} `json:"pipeline"`
}
type Page[T any] struct {
	Items    []T `json:"items"`
	NextPage int `json:"next_page,omitempty"`
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func validEndpoint(raw string, allowHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && !(allowHTTP && u.Scheme == "http")) {
		return ErrInvalid
	}
	return nil
}
func NewGitLabClient(config GitLabConfig, token, webhookToken string) (*GitLabClient, error) {
	if err := validEndpoint(config.BaseURL, config.AllowHTTP); err != nil {
		return nil, err
	}
	if config.ID == "" || strings.TrimSpace(token) == "" || len(webhookToken) < 24 {
		return nil, ErrInvalid
	}
	seen := map[int64]bool{}
	for _, b := range config.Bindings {
		if b.ProjectID <= 0 || b.ApplicationID == "" || b.EnvironmentID == "" || seen[b.ProjectID] || b.AllowActions && !b.ExternalGovernance {
			return nil, ErrInvalid
		}
		seen[b.ProjectID] = true
	}
	return &GitLabClient{Config: config, token: strings.TrimSpace(token), webhookToken: strings.TrimSpace(webhookToken), http: newHTTPClient()}, nil
}
func (c *GitLabClient) Binding(project int64) (GitLabBinding, bool) {
	for _, b := range c.Config.Bindings {
		if b.ProjectID == project {
			return b, true
		}
	}
	return GitLabBinding{}, false
}
func (c *GitLabClient) AcceptWebhook(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(c.webhookToken)) == 1
}
func (c *GitLabClient) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Config.BaseURL, "/")+"/api/v4"+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitlab request unavailable: %w", ErrUnknownOutcome)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("gitlab HTTP %d", resp.StatusCode)
	}
	return resp, nil
}
func (c *GitLabClient) json(ctx context.Context, method, path string, body, out any) (int, error) {
	resp, err := c.request(ctx, method, path, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return 0, err
	}
	if len(raw) > 4<<20 {
		return 0, errors.New("gitlab response exceeds limit")
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return 0, errors.New("invalid gitlab response")
	}
	next, _ := strconv.Atoi(resp.Header.Get("X-Next-Page"))
	return next, nil
}
func projectPath(project int64) string { return "/projects/" + strconv.FormatInt(project, 10) }
func (c *GitLabClient) Project(ctx context.Context, project int64) (GitLabProject, error) {
	var out GitLabProject
	_, err := c.json(ctx, "GET", projectPath(project), nil, &out)
	return out, err
}
func (c *GitLabClient) Pipelines(ctx context.Context, project int64, page int) (Page[GitLabPipeline], error) {
	out := Page[GitLabPipeline]{Items: []GitLabPipeline{}}
	if page < 1 || page > 10000 {
		return out, ErrInvalid
	}
	next, err := c.json(ctx, "GET", projectPath(project)+"/pipelines?per_page=50&page="+strconv.Itoa(page), nil, &out.Items)
	out.NextPage = next
	return out, err
}
func (c *GitLabClient) Pipeline(ctx context.Context, project, id int64) (GitLabPipeline, error) {
	var out GitLabPipeline
	_, err := c.json(ctx, "GET", projectPath(project)+"/pipelines/"+strconv.FormatInt(id, 10), nil, &out)
	return out, err
}
func (c *GitLabClient) Jobs(ctx context.Context, project, pipeline int64, page int) (Page[GitLabJob], error) {
	out := Page[GitLabJob]{Items: []GitLabJob{}}
	if page < 1 || page > 10000 {
		return out, ErrInvalid
	}
	next, err := c.json(ctx, "GET", projectPath(project)+"/pipelines/"+strconv.FormatInt(pipeline, 10)+"/jobs?include_retried=true&per_page=100&page="+strconv.Itoa(page), nil, &out.Items)
	out.NextPage = next
	return out, err
}
func (c *GitLabClient) Job(ctx context.Context, project, id int64) (GitLabJob, error) {
	var out GitLabJob
	_, err := c.json(ctx, "GET", projectPath(project)+"/jobs/"+strconv.FormatInt(id, 10), nil, &out)
	return out, err
}
func (c *GitLabClient) Trigger(ctx context.Context, project int64, ref, operation string) (GitLabPipeline, error) {
	var out GitLabPipeline
	b, ok := c.Binding(project)
	if !ok || !b.AllowActions || !b.ExternalGovernance || !contains(b.AllowedRefs, ref) {
		return out, ErrUnsupported
	}
	_, err := c.json(ctx, "POST", projectPath(project)+"/pipeline", map[string]any{"ref": ref, "variables": []map[string]string{{"key": "AIOPS_RUN_ID", "value": operation, "variable_type": "env_var"}}}, &out)
	return out, err
}
func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
func (c *GitLabClient) Command(ctx context.Context, project, pipeline int64, kind string, job int64) (GitLabPipeline, error) {
	var out GitLabPipeline
	b, ok := c.Binding(project)
	if !ok || !b.AllowActions || !b.ExternalGovernance {
		return out, ErrUnsupported
	}
	current, err := c.Pipeline(ctx, project, pipeline)
	if err != nil {
		return out, err
	}
	if !contains(b.AllowedRefs, current.Ref) {
		return out, ErrUnsupported
	}
	if kind == "play" {
		v, e := c.Job(ctx, project, job)
		if e != nil {
			return out, e
		}
		if v.Pipeline.ID != pipeline || v.Status != "manual" || !contains(b.ManualJobs, v.Name) {
			return out, ErrUnsupported
		}
		var played GitLabJob
		_, err = c.json(ctx, "POST", projectPath(project)+"/jobs/"+strconv.FormatInt(job, 10)+"/play", map[string]any{}, &played)
		if err != nil {
			return out, err
		}
		return c.Pipeline(ctx, project, pipeline)
	}
	if kind != "cancel" && kind != "retry" {
		return out, ErrInvalid
	}
	_, err = c.json(ctx, "POST", projectPath(project)+"/pipelines/"+strconv.FormatInt(pipeline, 10)+"/"+kind, map[string]any{}, &out)
	return out, err
}

// RecoverTrigger never submits twice. If bounded lookup cannot prove an earlier
// POST's outcome, the command remains needs_attention for human reconciliation.
func (c *GitLabClient) RecoverTrigger(ctx context.Context, project int64, operation string) (GitLabPipeline, error) {
	page, err := c.Pipelines(ctx, project, 1)
	if err != nil {
		return GitLabPipeline{}, err
	}
	for _, p := range page.Items {
		if p.Source != "api" {
			continue
		}
		var vars []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		_, err = c.json(ctx, "GET", projectPath(project)+"/pipelines/"+strconv.FormatInt(p.ID, 10)+"/variables", nil, &vars)
		if err != nil {
			return GitLabPipeline{}, err
		}
		for _, v := range vars {
			if v.Key == "AIOPS_RUN_ID" && v.Value == operation {
				return c.Pipeline(ctx, project, p.ID)
			}
		}
	}
	return GitLabPipeline{}, ErrUnknownOutcome
}
func (c *GitLabClient) Trace(ctx context.Context, project, job, cursor int64) (LogChunk, error) {
	out := LogChunk{Source: "gitlab", NextCursor: cursor}
	if cursor < 0 || cursor > 16<<20 {
		return out, ErrInvalid
	}
	resp, err := c.request(ctx, "GET", projectPath(project)+"/jobs/"+strconv.FormatInt(job, 10)+"/trace", nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if cursor > 0 {
		n, e := io.CopyN(io.Discard, resp.Body, cursor)
		if e != nil && e != io.EOF {
			return out, e
		}
		if n < cursor {
			return out, fmt.Errorf("%w: log was truncated or replaced; restart cursor", ErrInvalid)
		}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return out, err
	}
	if len(b) > 65536 {
		b = b[:65536]
		out.Truncated = true
	}
	out.NextCursor += int64(len(b))
	out.Text = strings.ReplaceAll(string(b), c.token, "[REDACTED]")
	return out, nil
}
func gitlabStatus(s string) string {
	switch s {
	case "success":
		return "succeeded"
	case "failed":
		return "failed"
	case "canceled":
		return "cancelled"
	case "skipped":
		return "skipped"
	case "manual", "scheduled":
		return "waiting"
	case "running":
		return "running"
	default:
		return "pending"
	}
}
func (c *GitLabClient) Observe(ctx context.Context, project, pipeline int64) (Projection, error) {
	p, err := c.Pipeline(ctx, project, pipeline)
	if err != nil {
		return Projection{}, err
	}
	jobs, err := c.Jobs(ctx, project, pipeline, 1)
	if err != nil {
		return Projection{}, err
	}
	out := Projection{Status: gitlabStatus(p.Status), ExternalStatus: p.Status, ExternalID: strconv.FormatInt(p.ID, 10), CommitSHA: p.SHA, WebURL: p.WebURL, Nodes: []NodeResult{}}
	for _, j := range jobs.Items {
		out.Nodes = append(out.Nodes, NodeResult{ID: strconv.FormatInt(j.ID, 10), Name: j.Name, Status: gitlabStatus(j.Status)})
	}
	if jobs.NextPage > 0 {
		out.Message = "Job projection limited to first 100; use paginated jobs endpoint for full history."
	}
	return out, nil
}
