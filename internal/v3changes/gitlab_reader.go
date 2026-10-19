package v3changes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
)

// D11: the GitLab changes of a service are read on demand from its confirmed
// code project (D20, v3_service_code_projects) within the reviewed catalog
// scope (deploy environment and branches). Nothing is stored: the GitLab
// deployment, pipeline and merge request lists are complete for a window,
// so an empty list is a complete answer, and an unreachable GitLab is an
// error, never an empty list.

// Errors of the change reader. Callers map them to responses.
var (
	ErrNotConfigured = errors.New("GitLab change source is not configured for this service")
	ErrNotVisible    = errors.New("change source not visible")
	ErrUnavailable   = errors.New("GitLab change source unavailable")
	ErrCredential    = errors.New("GitLab credential unavailable or rejected")
	ErrTooLarge      = errors.New("GitLab change window exceeds the read bound")
	ErrSourceChanged = errors.New("GitLab change source changed during reading")
	ErrNotFound      = errors.New("GitLab object not found")
)

// MaxChangeItems bounds each list of one read; more is ErrTooLarge.
const MaxChangeItems = 100

// MaxChangeWindow bounds the window of one read.
const MaxChangeWindow = 7*24*time.Hour + time.Minute

// Tool switches of the GitLab connector that each read needs.
const (
	ToolDeployments   = "list_deployments"
	ToolPipelines     = "list_pipelines"
	ToolMergeRequests = "list_merge_requests"
	ToolPipeline      = "get_pipeline"
	ToolCommit        = "get_commit"
	ToolCommitDiff    = "get_commit_diff"
)

// User is a GitLab username; display names and emails are never read.
type User struct {
	Username string `json:"username"`
}

// Project is the code project read.
type Project struct {
	ConnectorID       string   `json:"connector_id"`
	ProjectID         string   `json:"project_id"`
	Path              string   `json:"path"`
	WebURL            string   `json:"web_url"`
	DeployEnvironment string   `json:"deploy_environment"`
	Refs              []string `json:"refs"`
}

// Deployment is one GitLab deployment of the reviewed environment.
type Deployment struct {
	ID          int64      `json:"deployment_id"`
	IID         int64      `json:"iid"`
	Status      string     `json:"status"`
	Environment string     `json:"environment"`
	Ref         string     `json:"ref"`
	SHA         string     `json:"sha"`
	PipelineID  *int64     `json:"pipeline_id"`
	JobID       *int64     `json:"job_id"`
	JobName     string     `json:"job_name"`
	User        *User      `json:"user"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	WebURL      string     `json:"web_url"`
}

// At is when the deployment took effect: its finish, or its creation while
// it runs.
func (d Deployment) At() time.Time {
	if d.FinishedAt != nil {
		return *d.FinishedAt
	}
	return d.CreatedAt
}

// Pipeline is one pipeline of the list. The list has no duration, user or
// stages; the detail has.
type Pipeline struct {
	ID        int64     `json:"pipeline_id"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
}

// MergeRequestIID is the merge request of a merge request pipeline's ref.
func (p Pipeline) MergeRequestIID() (int64, bool) {
	match := mergeRequestRef.FindStringSubmatch(p.Ref)
	if match == nil {
		return 0, false
	}
	iid, err := strconv.ParseInt(match[1], 10, 64)
	return iid, err == nil && iid > 0
}

var mergeRequestRef = regexp.MustCompile(`^refs/merge-requests/([1-9][0-9]{0,18})/(head|merge|train)$`)

// MergeRequest is one merge request targeting a reviewed branch.
type MergeRequest struct {
	IID             int64      `json:"iid"`
	Title           string     `json:"title"`
	State           string     `json:"state"`
	SourceBranch    string     `json:"source_branch"`
	TargetBranch    string     `json:"target_branch"`
	Author          *User      `json:"author"`
	MergedBy        *User      `json:"merged_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	MergedAt        *time.Time `json:"merged_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	SHA             string     `json:"sha"`
	MergeCommitSHA  *string    `json:"merge_commit_sha"`
	SquashCommitSHA *string    `json:"squash_commit_sha"`
	WebURL          string     `json:"web_url"`
}

// LandedAs reports whether sha is the commit this merged merge request put on
// its target branch: the merge or squash commit, or its head when it was
// merged fast-forward.
func (m MergeRequest) LandedAs(sha string) bool {
	if m.State != "merged" || sha == "" {
		return false
	}
	if m.MergeCommitSHA != nil || m.SquashCommitSHA != nil {
		return (m.MergeCommitSHA != nil && *m.MergeCommitSHA == sha) || (m.SquashCommitSHA != nil && *m.SquashCommitSHA == sha)
	}
	return m.SHA == sha
}

// LandedMergeRequest is the merge request that landed as commit sha, when
// exactly one of mrs did.
func LandedMergeRequest(sha string, mrs []MergeRequest) *int64 {
	var found *int64
	for _, mr := range mrs {
		if mr.LandedAs(sha) {
			if found != nil {
				return nil
			}
			iid := mr.IID
			found = &iid
		}
	}
	return found
}

// PipelineMergeRequest is the merge request a pipeline belongs to, by the
// rules the change list, the pipeline detail and the changes evidence share
// (PA-95): a merge request pipeline names it in its ref; a pipeline of a
// branch outside the reviewed refs is the CI of the one merge request from
// that branch; any other pipeline, such as one on main, belongs to the one
// merge request that landed as its commit. mrs are merge requests into the
// reviewed refs; two candidates are none.
func PipelineMergeRequest(p Pipeline, reviewedRefs []string, mrs []MergeRequest) *int64 {
	if iid, ok := p.MergeRequestIID(); ok {
		return &iid
	}
	if !containsString(reviewedRefs, p.Ref) {
		var source *int64
		count := 0
		for _, mr := range mrs {
			if mr.SourceBranch == p.Ref {
				iid := mr.IID
				source, count = &iid, count+1
			}
		}
		if count == 1 {
			return source
		}
	}
	return LandedMergeRequest(p.SHA, mrs)
}

// ServiceChanges are the GitLab changes of one service environment in a
// window. A list is nil when its connector tool is off; Unavailable says so.
type ServiceChanges struct {
	ServiceID     string
	ApplicationID string
	EnvironmentID string
	Project       Project
	Since, Until  time.Time
	Deployments   []Deployment
	Pipelines     []Pipeline
	MergeRequests []MergeRequest
	Unavailable   map[string]string
	CapturedAt    time.Time
}

// source is a service's code project with its connector and reviewed scope.
type source struct {
	target
	scope v3telemetry.GitLabScope
}

// loadSource reads the confirmed code project of a service environment and
// authorizes the caller, in one read-only snapshot. It is called again after
// the upstream reads so a revoked grant or changed source returns nothing.
func (s *Service) loadSource(ctx context.Context, user core.User, app, env, serviceID string) (source, error) {
	var out source
	if s == nil || s.DB == nil || s.Access == nil {
		return out, ErrUnavailable
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, ErrUnavailable
	}
	defer tx.Rollback(ctx)
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived)`, app, env).Scan(&exists)
	if err != nil {
		return out, ErrUnavailable
	}
	if !exists {
		return out, ErrNotVisible
	}
	var team string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(owner_team_id,'') FROM applications WHERE id=$1`, app).Scan(&team); err != nil {
		return out, ErrUnavailable
	}
	if err = s.authorize(ctx, tx, user, target{ApplicationID: app, EnvironmentID: env, TeamID: team}); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			return out, ErrNotVisible
		}
		return out, ErrUnavailable
	}
	err = tx.QueryRow(ctx, `SELECT t.id,t.connector_id,t.external_id,c.endpoint,c.auth_method,c.credential_ref,c.tools
 FROM v3_service_code_projects p
 JOIN v3_connector_targets t ON t.id=p.target_id AND t.application_id=p.application_id AND t.environment_id=p.environment_id
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id AND c.enabled
 WHERE p.application_id=$1 AND p.environment_id=$2 AND t.target_kind='project' AND c.provider='gitlab'`, app, env).
		Scan(&out.BindingID, &out.ConnectorID, &out.ProjectID, &out.Endpoint, &out.AuthMethod, &out.CredentialRef, &out.Tools)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotConfigured
	}
	if err != nil {
		return out, ErrUnavailable
	}
	out.ServiceID, out.ApplicationID, out.EnvironmentID, out.TeamID = serviceID, app, env, team
	if err = tx.Commit(ctx); err != nil {
		return out, ErrUnavailable
	}
	if s.Catalog == nil {
		return out, ErrNotConfigured
	}
	scope, ok := s.Catalog().GitLabScopeFor(out.ConnectorID, app, env, out.ProjectID)
	if !ok || !validProject(out.ProjectID) {
		return out, ErrNotConfigured
	}
	out.scope = scope
	return out, nil
}

func validProject(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func (t target) toolEnabled(name string) bool {
	var tools []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if json.Unmarshal(t.Tools, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		if tool.Name == name && tool.Enabled {
			return true
		}
	}
	return false
}

func sameSource(a, b source) bool {
	return a.BindingID == b.BindingID && a.ConnectorID == b.ConnectorID && a.ProjectID == b.ProjectID &&
		a.Endpoint == b.Endpoint && a.AuthMethod == b.AuthMethod && a.CredentialRef == b.CredentialRef &&
		string(a.Tools) == string(b.Tools) && a.scope.DeployEnvironment == b.scope.DeployEnvironment &&
		strings.Join(a.scope.Refs, "\x00") == strings.Join(b.scope.Refs, "\x00")
}

// get reads one GitLab API path into out with the connector's read
// credential: 5 s, no redirects, at most 1 MiB. It returns the response
// headers for paging.
func (s *Service) get(ctx context.Context, t target, path string, query url.Values, out any) (http.Header, error) {
	base, ok := safeEndpoint(t.Endpoint)
	if !ok || (t.AuthMethod != "pat" && t.AuthMethod != "oauth") || s.Resolver == nil || t.CredentialRef == "" {
		return nil, ErrNotConfigured
	}
	token, err := s.resolveTargetCredential(ctx, t)
	if err != nil || token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, ErrCredential
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/api/v4/projects/" + t.ProjectID + path
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	if t.AuthMethod == "pat" {
		req.Header.Set("PRIVATE-TOKEN", token)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	client, err := s.httpClient(ctx, t, base, s.requestTimeout())
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, ErrCredential
	case response.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case response.StatusCode != http.StatusOK:
		return nil, ErrUnavailable
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(content) > 1<<20 {
		return nil, ErrUnavailable
	}
	if err := json.Unmarshal(content, out); err != nil {
		return nil, ErrUnavailable
	}
	return response.Header, nil
}

// requestTimeout bounds one GitLab request: the catalog's
// limits.timeout_seconds, 10 s by default.
func (s *Service) requestTimeout() time.Duration {
	if s.Catalog != nil {
		if seconds := s.Catalog().Limits.TimeoutSeconds; seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return 10 * time.Second
}

// list reads one bounded page; another page means the window is too large.
func (s *Service) list(ctx context.Context, t target, path string, query url.Values, out any) error {
	query.Set("per_page", strconv.Itoa(MaxChangeItems))
	query.Set("page", "1")
	header, err := s.get(ctx, t, path, query, out)
	if err != nil {
		return err
	}
	if next := header.Get("X-Next-Page"); next != "" {
		return ErrTooLarge
	}
	return nil
}

// cleanText keeps user-written GitLab text as data: no control characters,
// at most limit runes.
func cleanText(value string, limit int) string {
	var b strings.Builder
	count := 0
	for _, r := range value {
		if unicode.IsControl(r) {
			r = ' '
		}
		if count == limit {
			break
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

var (
	gitSHA      = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	gitLabUser  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,255}$`)
	earliestAny = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
)

func validTime(at, now time.Time) bool {
	return !at.Before(earliestAny) && !at.After(now.Add(5*time.Minute))
}

func validUser(u *User) bool { return u == nil || gitLabUser.MatchString(u.Username) }

func validWebURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && len(raw) <= 2048
}

type gitLabProject struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
}

type gitLabDeployment struct {
	ID          int64      `json:"id"`
	IID         int64      `json:"iid"`
	Ref         string     `json:"ref"`
	SHA         string     `json:"sha"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	User        *User      `json:"user"`
	Environment struct {
		Name string `json:"name"`
	} `json:"environment"`
	Deployable *struct {
		ID         int64      `json:"id"`
		Name       string     `json:"name"`
		WebURL     string     `json:"web_url"`
		FinishedAt *time.Time `json:"finished_at"`
		Pipeline   *struct {
			ID int64 `json:"id"`
		} `json:"pipeline"`
	} `json:"deployable"`
}

type gitLabPipeline struct {
	ID        int64     `json:"id"`
	ProjectID int64     `json:"project_id"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	WebURL    string    `json:"web_url"`
}

type gitLabMergeRequest struct {
	IID             int64      `json:"iid"`
	ProjectID       int64      `json:"project_id"`
	Title           string     `json:"title"`
	State           string     `json:"state"`
	SourceBranch    string     `json:"source_branch"`
	TargetBranch    string     `json:"target_branch"`
	Author          *User      `json:"author"`
	MergedBy        *User      `json:"merged_by"`
	MergeUser       *User      `json:"merge_user"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	MergedAt        *time.Time `json:"merged_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	SHA             string     `json:"sha"`
	MergeCommitSHA  *string    `json:"merge_commit_sha"`
	SquashCommitSHA *string    `json:"squash_commit_sha"`
	WebURL          string     `json:"web_url"`
}

// toMergeRequest checks a GitLab merge request of the project into ref.
func toMergeRequest(item gitLabMergeRequest, projectID, ref string, now time.Time) (MergeRequest, bool) {
	merged := item.MergedBy
	if merged == nil {
		merged = item.MergeUser
	}
	if item.IID <= 0 || strconv.FormatInt(item.ProjectID, 10) != projectID || item.TargetBranch != ref ||
		!validMergeRequestState(item.State) || len(item.SourceBranch) > 255 || !validTime(item.CreatedAt, now) ||
		item.UpdatedAt.Before(item.CreatedAt) || !validTime(item.UpdatedAt, now) ||
		(item.MergedAt != nil && !validTime(*item.MergedAt, now)) || (item.ClosedAt != nil && !validTime(*item.ClosedAt, now)) ||
		(item.SHA != "" && !gitSHA.MatchString(item.SHA)) || !validOptionalSHA(item.MergeCommitSHA) ||
		!validOptionalSHA(item.SquashCommitSHA) || !validUser(item.Author) || !validUser(merged) || !validWebURL(item.WebURL) {
		return MergeRequest{}, false
	}
	return MergeRequest{IID: item.IID, Title: cleanText(item.Title, 200),
		State: item.State, SourceBranch: item.SourceBranch, TargetBranch: item.TargetBranch, Author: item.Author,
		MergedBy: merged, CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(),
		MergedAt: utcPointer(item.MergedAt), ClosedAt: utcPointer(item.ClosedAt), SHA: item.SHA,
		MergeCommitSHA: item.MergeCommitSHA, SquashCommitSHA: item.SquashCommitSHA, WebURL: item.WebURL}, true
}

// toDeployment checks a GitLab deployment of environment deployEnv.
func toDeployment(item gitLabDeployment, deployEnv string, now time.Time) (Deployment, bool) {
	if item.ID <= 0 || item.IID <= 0 || !validDeploymentStatus(item.Status) || item.Environment.Name != deployEnv ||
		!gitSHA.MatchString(item.SHA) || len(item.Ref) > 256 || !validTime(item.CreatedAt, now) || item.UpdatedAt.Before(item.CreatedAt) ||
		!validTime(item.UpdatedAt, now) || (item.FinishedAt != nil && !validTime(*item.FinishedAt, now)) || !validUser(item.User) {
		return Deployment{}, false
	}
	deployment := Deployment{ID: item.ID, IID: item.IID, Status: item.Status, Environment: item.Environment.Name,
		Ref: item.Ref, SHA: item.SHA, User: item.User, CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(),
		FinishedAt: utcPointer(item.FinishedAt)}
	if d := item.Deployable; d != nil {
		if d.ID <= 0 || len(d.Name) > 255 || !validWebURL(d.WebURL) || (d.Pipeline != nil && d.Pipeline.ID <= 0) ||
			(d.FinishedAt != nil && !validTime(*d.FinishedAt, now)) {
			return Deployment{}, false
		}
		id := d.ID
		deployment.JobID, deployment.JobName, deployment.WebURL = &id, cleanText(d.Name, 255), d.WebURL
		if d.Pipeline != nil {
			pipelineID := d.Pipeline.ID
			deployment.PipelineID = &pipelineID
		}
		// GitLab 19 has no deployment finished_at; its job has.
		if deployment.FinishedAt == nil && finishedDeployment(item.Status) {
			deployment.FinishedAt = utcPointer(d.FinishedAt)
		}
	}
	if deployment.FinishedAt == nil && finishedDeployment(item.Status) {
		deployment.FinishedAt = utcPointer(&item.UpdatedAt)
	}
	return deployment, true
}

func validDeploymentStatus(status string) bool {
	switch status {
	case "created", "running", "success", "failed", "canceled", "skipped", "blocked":
		return true
	}
	return false
}

func validOptionalSHA(sha *string) bool { return sha == nil || gitSHA.MatchString(*sha) }

// ReadServiceChanges reads the deployments, pipelines and merge requests of a
// service environment's code project updated in [since, until).
func (s *Service) ReadServiceChanges(ctx context.Context, user core.User, app, env, serviceID string, since, until time.Time) (ServiceChanges, error) {
	out := ServiceChanges{ServiceID: serviceID, ApplicationID: app, EnvironmentID: env, Since: since.UTC(), Until: until.UTC(),
		Unavailable: map[string]string{}}
	if !since.Before(until) || until.Sub(since) > MaxChangeWindow {
		return out, ErrTooLarge
	}
	src, err := s.loadSource(ctx, user, app, env, serviceID)
	if err != nil {
		return out, err
	}
	now := time.Now()
	var project gitLabProject
	if _, err = s.get(ctx, src.target, "", url.Values{}, &project); err != nil {
		return out, err
	}
	if strconv.FormatInt(project.ID, 10) != src.ProjectID || project.PathWithNamespace == "" || len(project.PathWithNamespace) > 512 ||
		!validWebURL(project.WebURL) {
		return out, ErrUnavailable
	}
	out.Project = Project{ConnectorID: src.ConnectorID, ProjectID: src.ProjectID, Path: project.PathWithNamespace,
		WebURL: project.WebURL, DeployEnvironment: src.scope.DeployEnvironment, Refs: append([]string(nil), src.scope.Refs...)}
	window := func() url.Values {
		return url.Values{"updated_after": {since.UTC().Format(time.RFC3339)}, "updated_before": {until.UTC().Format(time.RFC3339)},
			"order_by": {"updated_at"}, "sort": {"desc"}}
	}
	// The three lists are independent reads; GitLab answers each in seconds.
	var deployments []gitLabDeployment
	var pipelines []gitLabPipeline
	mergeRequests := make([][]gitLabMergeRequest, len(src.scope.Refs))
	var wg sync.WaitGroup
	errs := make(chan error, 2+len(src.scope.Refs))
	read := func(path string, query url.Values, into any) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.list(ctx, src.target, path, query, into); err != nil {
				errs <- err
			}
		}()
	}
	if src.toolEnabled(ToolDeployments) {
		query := window()
		query.Set("environment", src.scope.DeployEnvironment)
		read("/deployments", query, &deployments)
	} else {
		out.Unavailable["deployments"] = "not_configured"
	}
	if src.toolEnabled(ToolPipelines) {
		read("/pipelines", window(), &pipelines)
	} else {
		out.Unavailable["pipelines"] = "not_configured"
	}
	if src.toolEnabled(ToolMergeRequests) {
		for i, ref := range src.scope.Refs {
			query := window()
			query.Set("state", "all")
			query.Set("target_branch", ref)
			read("/merge_requests", query, &mergeRequests[i])
		}
	} else {
		out.Unavailable["merge_requests"] = "not_configured"
	}
	wg.Wait()
	close(errs)
	if err := firstError(errs); err != nil {
		return out, err
	}
	if src.toolEnabled(ToolMergeRequests) {
		out.MergeRequests = []MergeRequest{}
		for i, ref := range src.scope.Refs {
			for _, item := range mergeRequests[i] {
				mr, ok := toMergeRequest(item, src.ProjectID, ref, now)
				if !ok {
					return out, ErrUnavailable
				}
				out.MergeRequests = append(out.MergeRequests, mr)
			}
		}
		if len(out.MergeRequests) > MaxChangeItems {
			return out, ErrTooLarge
		}
	}
	if src.toolEnabled(ToolDeployments) {
		out.Deployments = make([]Deployment, 0, len(deployments))
		for _, item := range deployments {
			deployment, ok := toDeployment(item, src.scope.DeployEnvironment, now)
			if !ok {
				return out, ErrUnavailable
			}
			out.Deployments = append(out.Deployments, deployment)
		}
	}
	if src.toolEnabled(ToolPipelines) {
		// The reviewed branches, merge request refs, and the source branches
		// of the window's merge requests into a reviewed branch: projects
		// without merge request pipelines run an MR's CI on its branch.
		sources := map[string]bool{}
		for _, mr := range out.MergeRequests {
			sources[mr.SourceBranch] = true
		}
		out.Pipelines = make([]Pipeline, 0, len(pipelines))
		for _, item := range pipelines {
			if item.ID <= 0 || strconv.FormatInt(item.ProjectID, 10) != src.ProjectID || !validPipelineStatus(item.Status) ||
				len(item.Ref) > 256 || !gitSHA.MatchString(item.SHA) || len(item.Source) > 64 || !validTime(item.CreatedAt, now) ||
				item.UpdatedAt.Before(item.CreatedAt) || !validTime(item.UpdatedAt, now) || !validWebURL(item.WebURL) {
				return out, ErrUnavailable
			}
			pipeline := Pipeline{ID: item.ID, Status: item.Status, Ref: item.Ref, SHA: item.SHA, Source: item.Source,
				CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(), WebURL: item.WebURL}
			if _, mr := pipeline.MergeRequestIID(); mr || containsString(src.scope.Refs, pipeline.Ref) || sources[pipeline.Ref] {
				out.Pipelines = append(out.Pipelines, pipeline)
			}
		}
	}
	refreshed, err := s.loadSource(ctx, user, app, env, serviceID)
	if err != nil {
		return out, err
	}
	if !sameSource(src, refreshed) {
		return out, ErrSourceChanged
	}
	out.CapturedAt = time.Now().UTC()
	return out, nil
}

// finishedDeployment reports a deployment that has ended.
func finishedDeployment(status string) bool {
	switch status {
	case "success", "failed", "canceled", "skipped":
		return true
	}
	return false
}

func firstError(errs <-chan error) error {
	var first error
	for err := range errs {
		if first == nil || errors.Is(first, ErrTooLarge) {
			first = err
		}
	}
	return first
}

func validMergeRequestState(state string) bool {
	switch state {
	case "opened", "closed", "merged", "locked":
		return true
	}
	return false
}

func utcPointer(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	value := at.UTC()
	return &value
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

// Job is one job of a pipeline detail.
type Job struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	Manual          bool       `json:"manual"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	DurationSeconds *float64   `json:"duration_seconds"`
	WebURL          string     `json:"web_url"`
}

// Stage groups the jobs of one stage, in pipeline order.
type Stage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Jobs   []Job  `json:"jobs"`
}

// Commit is the pipeline's commit: its title, never the message body or a
// diff text, and the changed paths.
type Commit struct {
	SHA        string    `json:"sha"`
	Title      string    `json:"title"`
	AuthoredAt time.Time `json:"authored_at"`
	ParentIDs  []string  `json:"parent_ids"`
	Additions  int       `json:"additions"`
	Deletions  int       `json:"deletions"`
	Files      *int      `json:"files"`
	Paths      []string  `json:"paths"`
}

// PipelineDetail is one pipeline with its stages and commit.
type PipelineDetail struct {
	Pipeline
	// MergeRequest is the pipeline's merge request by PipelineMergeRequest,
	// the change list's rule (PA-95), or nil.
	MergeRequest    *int64
	ProjectID       string
	User            *User
	StartedAt       *time.Time
	FinishedAt      *time.Time
	DurationSeconds *float64
	Stages          []Stage
	Commit          *Commit
	Unavailable     map[string]string
}

// MaxCommitPaths bounds the changed paths of a commit.
const MaxCommitPaths = 20

type gitLabPipelineDetail struct {
	gitLabPipeline
	User       *User      `json:"user"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Duration   *float64   `json:"duration"`
}

type gitLabJob struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Stage      string     `json:"stage"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Duration   *float64   `json:"duration"`
	WebURL     string     `json:"web_url"`
}

type gitLabCommitDetail struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	AuthoredDate time.Time `json:"authored_date"`
	ParentIDs    []string  `json:"parent_ids"`
	Stats        *struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	} `json:"stats"`
}

// ReadPipeline reads one pipeline of the service's code project. A pipeline
// outside the reviewed branches (and their merge requests) is ErrNotFound.
func (s *Service) ReadPipeline(ctx context.Context, user core.User, app, env, serviceID string, pipelineID int64) (PipelineDetail, error) {
	out := PipelineDetail{Unavailable: map[string]string{}}
	if pipelineID <= 0 {
		return out, ErrNotFound
	}
	src, err := s.loadSource(ctx, user, app, env, serviceID)
	if err != nil {
		return out, err
	}
	if !src.toolEnabled(ToolPipeline) {
		return out, ErrNotConfigured
	}
	now := time.Now()
	id := strconv.FormatInt(pipelineID, 10)
	var item gitLabPipelineDetail
	if _, err = s.get(ctx, src.target, "/pipelines/"+id, url.Values{}, &item); err != nil {
		return out, err
	}
	if item.ID != pipelineID || strconv.FormatInt(item.ProjectID, 10) != src.ProjectID || !validPipelineStatus(item.Status) ||
		len(item.Ref) > 256 || !gitSHA.MatchString(item.SHA) || len(item.Source) > 64 || !validTime(item.CreatedAt, now) ||
		!validTime(item.UpdatedAt, now) || !validWebURL(item.WebURL) || !validUser(item.User) ||
		(item.StartedAt != nil && !validTime(*item.StartedAt, now)) || (item.FinishedAt != nil && !validTime(*item.FinishedAt, now)) ||
		(item.Duration != nil && (*item.Duration < 0 || *item.Duration > 86400*30)) {
		return out, ErrUnavailable
	}
	out.Pipeline = Pipeline{ID: item.ID, Status: item.Status, Ref: item.Ref, SHA: item.SHA, Source: item.Source,
		CreatedAt: item.CreatedAt.UTC(), UpdatedAt: item.UpdatedAt.UTC(), WebURL: item.WebURL}
	candidates, err := s.pipelineMergeRequests(ctx, src, out.Pipeline, now)
	if err != nil {
		return out, err
	}
	if _, mr := out.Pipeline.MergeRequestIID(); !mr && !containsString(src.scope.Refs, out.Pipeline.Ref) {
		// A branch pipeline is in scope as the CI of a merge request from
		// the branch into a reviewed branch.
		inScope := false
		for _, candidate := range candidates {
			inScope = inScope || candidate.SourceBranch == out.Pipeline.Ref
		}
		if !inScope {
			return PipelineDetail{}, ErrNotFound
		}
	}
	out.MergeRequest = PipelineMergeRequest(out.Pipeline, src.scope.Refs, candidates)
	if out.MergeRequest == nil && !src.toolEnabled(ToolMergeRequests) {
		out.Unavailable["merge_request"] = "not_configured"
	}
	out.ProjectID = src.ProjectID
	out.User, out.StartedAt, out.FinishedAt, out.DurationSeconds = item.User, utcPointer(item.StartedAt), utcPointer(item.FinishedAt), item.Duration
	var jobs []gitLabJob
	if err = s.list(ctx, src.target, "/pipelines/"+id+"/jobs", url.Values{}, &jobs); err != nil {
		return out, err
	}
	out.Stages = []Stage{}
	index := map[string]int{}
	// GitLab lists jobs newest first; stages appear in pipeline order.
	for i := len(jobs) - 1; i >= 0; i-- {
		job := jobs[i]
		if job.ID <= 0 || job.Name == "" || len(job.Name) > 255 || job.Stage == "" || len(job.Stage) > 255 ||
			!validPipelineStatus(job.Status) || !validWebURL(job.WebURL) ||
			(job.StartedAt != nil && !validTime(*job.StartedAt, now)) || (job.FinishedAt != nil && !validTime(*job.FinishedAt, now)) ||
			(job.Duration != nil && (*job.Duration < 0 || *job.Duration > 86400*30)) {
			return out, ErrUnavailable
		}
		position, ok := index[job.Stage]
		if !ok {
			position = len(out.Stages)
			index[job.Stage] = position
			out.Stages = append(out.Stages, Stage{Name: cleanText(job.Stage, 255), Jobs: []Job{}})
		}
		out.Stages[position].Jobs = append(out.Stages[position].Jobs, Job{ID: job.ID, Name: cleanText(job.Name, 255),
			Status: job.Status, Manual: job.Status == "manual", StartedAt: utcPointer(job.StartedAt),
			FinishedAt: utcPointer(job.FinishedAt), DurationSeconds: job.Duration, WebURL: job.WebURL})
	}
	for i := range out.Stages {
		out.Stages[i].Status = stageStatus(out.Stages[i].Jobs)
	}
	if src.toolEnabled(ToolCommit) {
		var commit gitLabCommitDetail
		if _, err = s.get(ctx, src.target, "/repository/commits/"+item.SHA, url.Values{"stats": {"true"}}, &commit); err != nil {
			return out, err
		}
		if commit.ID != item.SHA || commit.Stats == nil || commit.Stats.Additions < 0 || commit.Stats.Deletions < 0 ||
			!validTime(commit.AuthoredDate, now) || len(commit.ParentIDs) > 64 {
			return out, ErrUnavailable
		}
		for _, parent := range commit.ParentIDs {
			if !gitSHA.MatchString(parent) {
				return out, ErrUnavailable
			}
		}
		out.Commit = &Commit{SHA: commit.ID, Title: cleanText(commit.Title, 200), AuthoredAt: commit.AuthoredDate.UTC(),
			ParentIDs: append([]string{}, commit.ParentIDs...), Additions: commit.Stats.Additions, Deletions: commit.Stats.Deletions}
		if src.toolEnabled(ToolCommitDiff) {
			var diffs []struct {
				NewPath string `json:"new_path"`
			}
			header, err := s.get(ctx, src.target, "/repository/commits/"+item.SHA+"/diff",
				url.Values{"per_page": {strconv.Itoa(MaxCommitPaths)}, "page": {"1"}}, &diffs)
			if err != nil {
				return out, err
			}
			total, err := strconv.Atoi(header.Get("X-Total"))
			if err != nil || total < len(diffs) || len(diffs) > MaxCommitPaths {
				return out, ErrUnavailable
			}
			out.Commit.Files = &total
			out.Commit.Paths = make([]string, 0, len(diffs))
			for _, diff := range diffs {
				if diff.NewPath == "" || len(diff.NewPath) > 1024 {
					return out, ErrUnavailable
				}
				out.Commit.Paths = append(out.Commit.Paths, cleanText(diff.NewPath, 1024))
			}
		} else {
			out.Unavailable["commit.paths"] = "not_configured"
		}
	} else {
		out.Unavailable["commit"] = "not_configured"
	}
	refreshed, err := s.loadSource(ctx, user, app, env, serviceID)
	if err != nil {
		return out, err
	}
	if !sameSource(src, refreshed) {
		return out, ErrSourceChanged
	}
	return out, nil
}

// pipelineMergeRequests reads the merge requests into a reviewed branch that
// PipelineMergeRequest weighs for one pipeline, as the change list would
// see them: those from the pipeline's branch, when it is not reviewed, and
// those GitLab links to the pipeline's commit. None without the merge
// request tool.
func (s *Service) pipelineMergeRequests(ctx context.Context, src source, p Pipeline, now time.Time) ([]MergeRequest, error) {
	if !src.toolEnabled(ToolMergeRequests) {
		return nil, nil
	}
	out := []MergeRequest{}
	seen := map[int64]bool{}
	// ref is the branch a query asked for; a commit's merge requests into
	// branches that are not reviewed are left out.
	add := func(items []gitLabMergeRequest, ref string) error {
		for _, item := range items {
			target := ref
			if ref == "" {
				if !containsString(src.scope.Refs, item.TargetBranch) {
					continue
				}
				target = item.TargetBranch
			}
			mr, ok := toMergeRequest(item, src.ProjectID, target, now)
			if !ok {
				return ErrUnavailable
			}
			if !seen[mr.IID] {
				seen[mr.IID] = true
				out = append(out, mr)
			}
		}
		return nil
	}
	if _, mr := p.MergeRequestIID(); !mr && !containsString(src.scope.Refs, p.Ref) && p.Ref != "" && len(p.Ref) <= 255 {
		for _, ref := range src.scope.Refs {
			// Two are enough to tell one merge request from several.
			var items []gitLabMergeRequest
			query := url.Values{"source_branch": {p.Ref}, "target_branch": {ref}, "state": {"all"}, "per_page": {"2"}, "page": {"1"}}
			if _, err := s.get(ctx, src.target, "/merge_requests", query, &items); err != nil {
				return nil, err
			}
			if err := add(items, ref); err != nil {
				return nil, err
			}
		}
	}
	if _, mr := p.MergeRequestIID(); mr {
		return out, nil
	}
	var items []gitLabMergeRequest
	query := url.Values{"per_page": {strconv.Itoa(maxCommitMergeRequests)}, "page": {"1"}}
	if _, err := s.get(ctx, src.target, "/repository/commits/"+p.SHA+"/merge_requests", query, &items); err != nil {
		return nil, err
	}
	if len(items) >= maxCommitMergeRequests {
		return out, nil // Not attributed: the commit is in too many.
	}
	if err := add(items, ""); err != nil {
		return nil, err
	}
	return out, nil
}

// maxCommitMergeRequests bounds the merge requests GitLab links to one
// commit; a commit in that many is not attributed to any.
const maxCommitMergeRequests = 20

// stageStatus summarizes the jobs of a stage: a failure or a running job
// decides it before a manual one waits, and success needs every job done.
func stageStatus(jobs []Job) string {
	for _, status := range []string{"failed", "running", "pending", "created", "waiting_for_resource", "preparing", "manual", "canceled"} {
		for _, job := range jobs {
			if job.Status == status {
				return status
			}
		}
	}
	for _, job := range jobs {
		if job.Status == "success" {
			return "success"
		}
	}
	return "skipped"
}
