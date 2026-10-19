package v3changeview

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3changes"
	"github.com/jackc/pgx/v5"
)

// GitLabChanges reads a service's GitLab changes and Kubernetes revisions
// (v3changes.Service).
type GitLabChanges interface {
	ReadServiceChanges(ctx context.Context, user core.User, app, env, serviceID string, since, until time.Time) (v3changes.ServiceChanges, error)
	ReadPipeline(ctx context.Context, user core.User, app, env, serviceID string, pipelineID int64) (v3changes.PipelineDetail, error)
	ReadRevisions(ctx context.Context, user core.User, app, env string) ([]v3changes.KubernetesRevision, error)
}

// D11: GET /changes with sources=gitlab is the GitLab view of one service
// and range: every deployment, pipeline and merge request of the window with
// the exact links between them (commit, pipeline, merge request number),
// the alerts of the window and the cluster revisions. An alert next to a
// deployment is only time-adjacent: basis is always time_adjacent.

// Adjacency: an alert starting from 15 minutes before to 60 minutes after a
// deployment took effect.
const (
	adjacentBefore = 15 * time.Minute
	adjacentAfter  = 60 * time.Minute
)

type gitLabUser struct {
	Username string `json:"username"`
}

type adjacentAlert struct {
	IncidentID    string `json:"incident_id"`
	OffsetSeconds int64  `json:"offset_seconds"`
	Basis         string `json:"basis"`
}

type gitLabJobRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type gitLabDeploymentView struct {
	ID                 string          `json:"id"`
	DeploymentID       int64           `json:"deployment_id"`
	IID                int64           `json:"iid"`
	Status             string          `json:"status"`
	Environment        string          `json:"environment"`
	Ref                string          `json:"ref"`
	SHA                string          `json:"sha"`
	ShortSHA           string          `json:"short_sha"`
	PipelineID         *int64          `json:"pipeline_id"`
	Job                *gitLabJobRef   `json:"job"`
	User               *gitLabUser     `json:"user"`
	CreatedAt          string          `json:"created_at"`
	FinishedAt         *string         `json:"finished_at"`
	WebURL             *string         `json:"web_url"`
	MergeRequestIID    *int64          `json:"merge_request_iid"`
	KubernetesRevision *string         `json:"kubernetes_revision"`
	AdjacentAlerts     []adjacentAlert `json:"adjacent_alerts"`
}

type gitLabPipelineView struct {
	ID              string  `json:"id"`
	PipelineID      int64   `json:"pipeline_id"`
	Status          string  `json:"status"`
	Ref             string  `json:"ref"`
	SHA             string  `json:"sha"`
	ShortSHA        string  `json:"short_sha"`
	Source          string  `json:"source"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	WebURL          *string `json:"web_url"`
	MergeRequestIID *int64  `json:"merge_request_iid"`
	DeploymentIDs   []int64 `json:"deployment_ids"`
}

type gitLabMergeRequestView struct {
	ID              string      `json:"id"`
	IID             int64       `json:"iid"`
	Title           string      `json:"title"`
	State           string      `json:"state"`
	SourceBranch    string      `json:"source_branch"`
	TargetBranch    string      `json:"target_branch"`
	Author          *gitLabUser `json:"author"`
	MergedBy        *gitLabUser `json:"merged_by"`
	CreatedAt       string      `json:"created_at"`
	MergedAt        *string     `json:"merged_at"`
	ClosedAt        *string     `json:"closed_at"`
	SHA             *string     `json:"sha"`
	MergeCommitSHA  *string     `json:"merge_commit_sha"`
	SquashCommitSHA *string     `json:"squash_commit_sha"`
	WebURL          *string     `json:"web_url"`
	PipelineIDs     []int64     `json:"pipeline_ids"`
	DeploymentIDs   []int64     `json:"deployment_ids"`
}

type alertView struct {
	IncidentID string  `json:"incident_id"`
	Title      string  `json:"title"`
	StartedAt  string  `json:"started_at"`
	EndedAt    *string `json:"ended_at"`
	Href       string  `json:"href"`
}

// revisionView is a Kubernetes revision. created_at is its ReplicaSet's
// creation; at is when the revision took effect, or null with not_before
// and not_after bounding it (PA-94).
type revisionView struct {
	Revision          string  `json:"revision"`
	CreatedAt         string  `json:"created_at"`
	ChangeCause       *string `json:"change_cause"`
	GitLabPipelineID  *int64  `json:"gitlab_pipeline_id"`
	ReplicaSetReused  bool    `json:"replicaset_reused"`
	At                *string `json:"at"`
	TimeBasis         string  `json:"time_basis"`
	TimeUnknownReason *string `json:"time_unknown_reason"`
	NotBefore         *string `json:"not_before"`
	NotAfter          *string `json:"not_after"`
}

func toRevisionView(r v3changes.KubernetesRevision) revisionView {
	return revisionView{Revision: r.Revision, CreatedAt: rfc(r.CreatedAt), ChangeCause: r.ChangeCause,
		GitLabPipelineID: r.GitLabPipelineID, ReplicaSetReused: r.ReplicaSetReused, At: rfcPointer(r.At),
		TimeBasis: r.TimeBasis, TimeUnknownReason: r.TimeUnknownReason, NotBefore: rfcPointer(r.NotBefore), NotAfter: rfcPointer(r.NotAfter)}
}

type gitLabCoverage struct {
	GitLab     string `json:"gitlab"`
	Alerts     string `json:"alerts"`
	Kubernetes string `json:"kubernetes"`
}

type gitLabWindow struct {
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
}

type gitLabView struct {
	ServiceID           string                   `json:"service_id"`
	EnvironmentID       string                   `json:"environment_id"`
	Source              string                   `json:"source"`
	Window              gitLabWindow             `json:"window"`
	Project             v3changes.Project        `json:"project"`
	KPIs                []kpi                    `json:"kpis"`
	SeriesStartAt       string                   `json:"series_start_at"`
	SeriesStepSeconds   int64                    `json:"series_step_seconds"`
	Deployments         []gitLabDeploymentView   `json:"deployments"`
	Pipelines           []gitLabPipelineView     `json:"pipelines"`
	MergeRequests       []gitLabMergeRequestView `json:"merge_requests"`
	Alerts              []alertView              `json:"alerts"`
	KubernetesRevisions []revisionView           `json:"kubernetes_revisions"`
	Coverage            gitLabCoverage           `json:"coverage"`
	CapturedAt          string                   `json:"captured_at"`
	Unavailable         map[string]string        `json:"unavailable"`
}

func rfc(at time.Time) string { return at.UTC().Format(time.RFC3339) }

func rfcPointer(at *time.Time) *string {
	if at == nil {
		return nil
	}
	value := rfc(*at)
	return &value
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func user(u *v3changes.User) *gitLabUser {
	if u == nil {
		return nil
	}
	return &gitLabUser{Username: u.Username}
}

// changeText is the view's localized copy: English when Accept-Language
// starts with "en", Chinese otherwise.
type changeText struct{ en bool }

func changeTextFor(acceptLanguage string) changeText {
	return changeText{en: strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLanguage)), "en")}
}

func (t changeText) pick(zh, en string) string {
	if t.en {
		return en
	}
	return zh
}

func (t changeText) kpiLabel(id string) string {
	switch id {
	case "deployments":
		return t.pick("成功部署", "Successful deployments")
	case "failed_deployments":
		return t.pick("失败部署", "Failed deployments")
	case "pipelines":
		return t.pick("流水线", "Pipelines")
	case "merged_merge_requests":
		return t.pick("已合并 MR", "Merged merge requests")
	}
	return id
}

// adjacency lists the alerts starting near at.
func adjacency(alerts []alertRecord, at time.Time) []adjacentAlert {
	out := []adjacentAlert{}
	for _, alert := range alerts {
		if !alert.StartsAt.Before(at.Add(-adjacentBefore)) && !alert.StartsAt.After(at.Add(adjacentAfter)) {
			out = append(out, adjacentAlert{IncidentID: alert.ID, OffsetSeconds: int64(alert.StartsAt.Sub(at) / time.Second),
				Basis: "time_adjacent"})
		}
	}
	return out
}

// gitLabRequest is the parsed GitLab view request.
func gitLabWindowOf(f requestFilter, now time.Time) (time.Time, time.Time, time.Duration) {
	end := now.UTC().Truncate(time.Second)
	step := time.Hour
	if f.Window > 24*time.Hour {
		step = 24 * time.Hour
	}
	return end.Add(-f.Window), end, step
}

func (s *Service) gitLabView(w http.ResponseWriter, r *http.Request, f requestFilter) {
	app, env, _ := decodeServiceID(f.ServiceID)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	row, current, ok := s.gitLabScope(w, r, ctx, app, env)
	if !ok {
		return
	}
	start, end, step := gitLabWindowOf(f, nowOf(s))
	changes, err := s.GitLab.ReadServiceChanges(ctx, current, app, env, f.ServiceID, start, end)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	t := changeTextFor(r.Header.Get("Accept-Language"))
	out := gitLabView{ServiceID: f.ServiceID, EnvironmentID: env, Source: "gitlab",
		Window: gitLabWindow{StartAt: rfc(start), EndAt: rfc(end)}, Project: changes.Project,
		SeriesStartAt: rfc(start), SeriesStepSeconds: int64(step / time.Second),
		Coverage: gitLabCoverage{GitLab: "complete"}, CapturedAt: rfc(changes.CapturedAt), Unavailable: map[string]string{}}
	for key, reason := range changes.Unavailable {
		out.Unavailable[key] = reason
	}
	alerts, alertState, err := s.gitLabAlerts(ctx, current, row, start, end)
	if err != nil {
		writeReadError(w, err)
		return
	}
	out.Coverage.Alerts = alertState
	if alertState != "complete" {
		out.Unavailable["alerts"] = alertReason(alertState)
	} else {
		out.Alerts = make([]alertView, 0, len(alerts))
		for _, alert := range alerts {
			out.Alerts = append(out.Alerts, alertView{IncidentID: alert.ID, Title: alert.Title, StartedAt: rfc(alert.StartsAt),
				EndedAt: rfcPointer(alert.EndsAt), Href: "/incidents/" + alert.ID})
		}
	}
	revisions, revisionState := s.gitLabRevisions(ctx, current, app, env)
	out.Coverage.Kubernetes = revisionState
	if revisionState == "history_limited" {
		out.KubernetesRevisions = []revisionView{}
		// A revision whose time is unknown is listed when its bounds meet
		// the window, with a null at (PA-94).
		for _, revision := range revisions {
			if !revision.InWindow(start, end) {
				continue
			}
			out.KubernetesRevisions = append(out.KubernetesRevisions, toRevisionView(revision))
		}
	} else {
		out.Unavailable["kubernetes_revisions"] = revisionState
	}
	project := changes.Project.ProjectID
	if changes.MergeRequests != nil {
		out.MergeRequests = make([]gitLabMergeRequestView, 0, len(changes.MergeRequests))
		for _, mr := range changes.MergeRequests {
			out.MergeRequests = append(out.MergeRequests, gitLabMergeRequestView{
				ID: "gitlab:mr:" + project + ":" + strconv.FormatInt(mr.IID, 10), IID: mr.IID, Title: mr.Title, State: mr.State,
				SourceBranch: mr.SourceBranch, TargetBranch: mr.TargetBranch, Author: user(mr.Author), MergedBy: user(mr.MergedBy),
				CreatedAt: rfc(mr.CreatedAt), MergedAt: rfcPointer(mr.MergedAt), ClosedAt: rfcPointer(mr.ClosedAt),
				SHA: optional(mr.SHA), MergeCommitSHA: mr.MergeCommitSHA, SquashCommitSHA: mr.SquashCommitSHA,
				WebURL: optional(mr.WebURL), PipelineIDs: []int64{}, DeploymentIDs: []int64{}})
		}
	}
	// The merge request a commit landed from, when exactly one did.
	landed := func(sha string) *int64 { return v3changes.LandedMergeRequest(sha, changes.MergeRequests) }
	mrIndex := map[int64]int{}
	for i, mr := range out.MergeRequests {
		mrIndex[mr.IID] = i
	}
	pipelineIndex := map[int64]int{}
	if changes.Pipelines != nil {
		out.Pipelines = make([]gitLabPipelineView, 0, len(changes.Pipelines))
		for _, p := range changes.Pipelines {
			view := gitLabPipelineView{ID: "gitlab:pipeline:" + project + ":" + strconv.FormatInt(p.ID, 10), PipelineID: p.ID,
				Status: p.Status, Ref: p.Ref, SHA: p.SHA, ShortSHA: shortSHA(p.SHA), Source: p.Source, CreatedAt: rfc(p.CreatedAt),
				UpdatedAt: rfc(p.UpdatedAt), WebURL: optional(p.WebURL), DeploymentIDs: []int64{}}
			// The pipeline detail uses the same rule (PA-95).
			view.MergeRequestIID = v3changes.PipelineMergeRequest(p, changes.Project.Refs, changes.MergeRequests)
			if view.MergeRequestIID != nil {
				if i, ok := mrIndex[*view.MergeRequestIID]; ok {
					out.MergeRequests[i].PipelineIDs = append(out.MergeRequests[i].PipelineIDs, p.ID)
				}
			}
			pipelineIndex[p.ID] = len(out.Pipelines)
			out.Pipelines = append(out.Pipelines, view)
		}
	}
	if changes.Deployments != nil {
		out.Deployments = make([]gitLabDeploymentView, 0, len(changes.Deployments))
		for _, d := range changes.Deployments {
			view := gitLabDeploymentView{ID: "gitlab:deployment:" + project + ":" + strconv.FormatInt(d.ID, 10), DeploymentID: d.ID,
				IID: d.IID, Status: d.Status, Environment: d.Environment, Ref: d.Ref, SHA: d.SHA, ShortSHA: shortSHA(d.SHA),
				PipelineID: d.PipelineID, User: user(d.User), CreatedAt: rfc(d.CreatedAt), FinishedAt: rfcPointer(d.FinishedAt),
				WebURL: optional(d.WebURL), MergeRequestIID: landed(d.SHA)}
			if d.JobID != nil {
				view.Job = &gitLabJobRef{ID: *d.JobID, Name: d.JobName}
			}
			if d.PipelineID != nil {
				if i, ok := pipelineIndex[*d.PipelineID]; ok {
					out.Pipelines[i].DeploymentIDs = append(out.Pipelines[i].DeploymentIDs, d.ID)
				}
				for _, revision := range out.KubernetesRevisions {
					if revision.GitLabPipelineID != nil && *revision.GitLabPipelineID == *d.PipelineID && d.Status == "success" {
						value := revision.Revision
						view.KubernetesRevision = &value
					}
				}
			}
			if view.MergeRequestIID != nil {
				if i, ok := mrIndex[*view.MergeRequestIID]; ok {
					out.MergeRequests[i].DeploymentIDs = append(out.MergeRequests[i].DeploymentIDs, d.ID)
				}
			}
			if out.Alerts != nil {
				view.AdjacentAlerts = adjacency(alerts, d.At())
			}
			out.Deployments = append(out.Deployments, view)
		}
	}
	out.KPIs = gitLabKPIs(t, changes, start, end, step, out.Unavailable)
	if err := s.gitLabFinalCheck(ctx, r, row); err != nil {
		writeReadError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func alertReason(state string) string {
	switch state {
	case "unknown":
		return "not_configured"
	case "not_authorized":
		return "no_authorized_target"
	}
	return "unavailable"
}

// gitLabKPIs counts each known list per bucket; an unknown list has no KPI.
func gitLabKPIs(t changeText, changes v3changes.ServiceChanges, start, end time.Time, step time.Duration, unavailable map[string]string) []kpi {
	buckets := int(end.Sub(start) / step)
	if end.Sub(start)%step != 0 {
		buckets++
	}
	count := func(id string, times []time.Time) kpi {
		series := make([]float64, buckets)
		total := 0
		for _, at := range times {
			if at.Before(start) || !at.Before(end) {
				continue
			}
			series[int(at.Sub(start)/step)]++
			total++
		}
		return kpi{ID: id, Label: t.kpiLabel(id), Value: strconv.Itoa(total), Series: series}
	}
	out := []kpi{}
	if changes.Deployments != nil {
		var succeeded, failed []time.Time
		for _, d := range changes.Deployments {
			switch d.Status {
			case "success":
				succeeded = append(succeeded, d.At())
			case "failed":
				failed = append(failed, d.At())
			}
		}
		out = append(out, count("deployments", succeeded), count("failed_deployments", failed))
	} else {
		unavailable["kpis.deployments"], unavailable["kpis.failed_deployments"] = "not_configured", "not_configured"
	}
	if changes.Pipelines != nil {
		var created []time.Time
		for _, p := range changes.Pipelines {
			created = append(created, p.CreatedAt)
		}
		out = append(out, count("pipelines", created))
	} else {
		unavailable["kpis.pipelines"] = "not_configured"
	}
	if changes.MergeRequests != nil {
		var merged []time.Time
		for _, mr := range changes.MergeRequests {
			if mr.MergedAt != nil {
				merged = append(merged, *mr.MergedAt)
			}
		}
		out = append(out, count("merged_merge_requests", merged))
	} else {
		unavailable["kpis.merged_merge_requests"] = "not_configured"
	}
	return out
}

// gitLabScope checks the selected environment, the service and its
// services.view, and returns the current user.
func (s *Service) gitLabScope(w http.ResponseWriter, r *http.Request, ctx context.Context, app, env string) (serviceRow, core.User, bool) {
	var zero serviceRow
	if s == nil || s.Store == nil || s.Store.DB == nil || s.Access == nil || s.GitLab == nil {
		httpapi.Error(w, 503, "change_source_not_configured", "No GitLab change source is configured")
		return zero, core.User{}, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	current := httpapi.CurrentUser(r)
	if !ok || current.ID == "" {
		writeReadError(w, errIncomplete)
		return zero, current, false
	}
	selected := false
	for _, id := range scope.EnvironmentIDs {
		selected = selected || id == env
	}
	if !selected {
		writeReadError(w, pgx.ErrNoRows)
		return zero, current, false
	}
	row, err := s.loadService(ctx, app, env)
	if err != nil {
		writeReadError(w, err)
		return zero, current, false
	}
	allowed, err := s.canView(ctx, current, row, "services")
	if err != nil {
		writeReadError(w, err)
		return zero, current, false
	}
	if !allowed {
		writeReadError(w, pgx.ErrNoRows)
		return zero, current, false
	}
	return row, current, true
}

// gitLabAlerts reads the window's alerts when the caller may see events and
// a reviewed source covers the whole window; otherwise the state says why.
func (s *Service) gitLabAlerts(ctx context.Context, current core.User, row serviceRow, start, end time.Time) ([]alertRecord, string, error) {
	allowed, err := s.canView(ctx, current, row, "events")
	if err != nil {
		return nil, "", err
	}
	if !allowed {
		return nil, "not_authorized", nil
	}
	if s.AlertCovered == nil {
		return nil, "unknown", nil
	}
	alerts, err := s.loadAlerts(ctx, row, start, end)
	if errors.Is(err, errIncomplete) {
		return nil, "unknown", nil
	}
	if err != nil {
		return nil, "", err
	}
	return alerts, "complete", nil
}

// gitLabRevisions reads the cluster revisions; a missing workload, grant or
// cluster leaves them unknown.
func (s *Service) gitLabRevisions(ctx context.Context, current core.User, app, env string) ([]v3changes.KubernetesRevision, string) {
	revisions, err := s.GitLab.ReadRevisions(ctx, current, app, env)
	switch {
	case err == nil:
		// In the order they took effect (PA-94).
		sort.SliceStable(revisions, func(i, j int) bool { return v3changes.RevisionBefore(revisions[i], revisions[j]) })
		return revisions, "history_limited"
	case errors.Is(err, v3changes.ErrRevisionsNotConfigured):
		return nil, "not_configured"
	case errors.Is(err, v3changes.ErrRevisionsForbidden):
		return nil, "no_authorized_target"
	}
	return nil, "unavailable"
}

// gitLabFinalCheck rechecks the session, the service and services.view
// before a GitLab view is returned.
func (s *Service) gitLabFinalCheck(ctx context.Context, r *http.Request, before serviceRow) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return store.ErrForbidden
	}
	current, err := s.Store.Session(ctx, cookie.Value)
	if err != nil || current.ID != httpapi.CurrentUser(r).ID {
		return store.ErrForbidden
	}
	after, err := s.loadService(ctx, before.AppID, before.EnvKey)
	if err != nil || after != before {
		return store.ErrForbidden
	}
	allowed, err := s.canView(ctx, current, before, "services")
	if err != nil {
		return err
	}
	if !allowed {
		return store.ErrForbidden
	}
	return nil
}

func writeGitLabError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, v3changes.ErrNotVisible):
		httpapi.Error(w, 403, "forbidden", "The service's change source is outside your authorized scope")
	case errors.Is(err, v3changes.ErrNotConfigured):
		httpapi.Error(w, 503, "change_source_not_configured", "No GitLab change source is configured for this service")
	case errors.Is(err, v3changes.ErrCredential):
		httpapi.Error(w, 503, "credential_unavailable", "The GitLab credential is unavailable or was rejected")
	case errors.Is(err, v3changes.ErrTooLarge):
		httpapi.Error(w, 413, "change_window_too_large", "More changes than one bounded read; choose a shorter range")
	case errors.Is(err, v3changes.ErrSourceChanged):
		httpapi.Error(w, 409, "source_changed", "The change source changed during reading")
	case errors.Is(err, v3changes.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Change not found")
	default:
		httpapi.Error(w, 503, "source_unavailable", "GitLab is unavailable")
	}
}

type gitLabJobView struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	Status          string   `json:"status"`
	Manual          bool     `json:"manual"`
	StartedAt       *string  `json:"started_at"`
	FinishedAt      *string  `json:"finished_at"`
	DurationSeconds *float64 `json:"duration_seconds"`
	WebURL          *string  `json:"web_url"`
}

type gitLabStageView struct {
	Name   string          `json:"name"`
	Status string          `json:"status"`
	Jobs   []gitLabJobView `json:"jobs"`
}

type gitLabCommitStats struct {
	Files     *int `json:"files"`
	Additions int  `json:"additions"`
	Deletions int  `json:"deletions"`
}

type gitLabCommitView struct {
	SHA        string            `json:"sha"`
	Title      string            `json:"title"`
	AuthoredAt string            `json:"authored_at"`
	ParentIDs  []string          `json:"parent_ids"`
	Stats      gitLabCommitStats `json:"stats"`
	Paths      []string          `json:"paths"`
}

type gitLabPipelineDetailView struct {
	ID              string            `json:"id"`
	PipelineID      int64             `json:"pipeline_id"`
	Status          string            `json:"status"`
	Ref             string            `json:"ref"`
	SHA             string            `json:"sha"`
	Source          string            `json:"source"`
	User            *gitLabUser       `json:"user"`
	CreatedAt       string            `json:"created_at"`
	StartedAt       *string           `json:"started_at"`
	FinishedAt      *string           `json:"finished_at"`
	DurationSeconds *float64          `json:"duration_seconds"`
	WebURL          *string           `json:"web_url"`
	Stages          []gitLabStageView `json:"stages"`
	Commit          *gitLabCommitView `json:"commit"`
	MergeRequestIID *int64            `json:"merge_request_iid"`
	AdjacentAlerts  []adjacentAlert   `json:"adjacent_alerts"`
	Unavailable     map[string]string `json:"unavailable"`
}

// gitLabPipeline is GET /changes/pipelines/{pipeline_id}?service_id=: one
// pipeline of the service's code project with its stages and commit. Its
// adjacent alerts are relative to the pipeline's end (its last update while
// it runs).
func (s *Service) gitLabPipeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	pipelineID, err := strconv.ParseInt(r.PathValue("pipeline_id"), 10, 64)
	serviceID := q.Get("service_id")
	app, env, valid := decodeServiceID(serviceID)
	if err != nil || pipelineID <= 0 || strconv.FormatInt(pipelineID, 10) != r.PathValue("pipeline_id") || !valid ||
		len(q) != 1 || len(q["service_id"]) != 1 {
		httpapi.Error(w, 422, "invalid_change_request", "Select one service and pipeline")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	row, current, ok := s.gitLabScope(w, r, ctx, app, env)
	if !ok {
		return
	}
	detail, err := s.GitLab.ReadPipeline(ctx, current, app, env, serviceID, pipelineID)
	if err != nil {
		writeGitLabError(w, err)
		return
	}
	out := gitLabPipelineDetailView{PipelineID: detail.ID, Status: detail.Status, Ref: detail.Ref, SHA: detail.SHA,
		Source: detail.Source, User: user(detail.User), CreatedAt: rfc(detail.CreatedAt), StartedAt: rfcPointer(detail.StartedAt),
		FinishedAt: rfcPointer(detail.FinishedAt), DurationSeconds: detail.DurationSeconds, WebURL: optional(detail.WebURL),
		Stages: []gitLabStageView{}, Unavailable: map[string]string{}}
	for key, reason := range detail.Unavailable {
		out.Unavailable[key] = reason
	}
	out.MergeRequestIID = detail.MergeRequest
	for _, stage := range detail.Stages {
		view := gitLabStageView{Name: stage.Name, Status: stage.Status, Jobs: []gitLabJobView{}}
		for _, job := range stage.Jobs {
			view.Jobs = append(view.Jobs, gitLabJobView{ID: job.ID, Name: job.Name, Status: job.Status, Manual: job.Manual,
				StartedAt: rfcPointer(job.StartedAt), FinishedAt: rfcPointer(job.FinishedAt), DurationSeconds: job.DurationSeconds,
				WebURL: optional(job.WebURL)})
		}
		out.Stages = append(out.Stages, view)
	}
	if c := detail.Commit; c != nil {
		out.Commit = &gitLabCommitView{SHA: c.SHA, Title: c.Title, AuthoredAt: rfc(c.AuthoredAt), ParentIDs: c.ParentIDs,
			Stats: gitLabCommitStats{Files: c.Files, Additions: c.Additions, Deletions: c.Deletions}, Paths: c.Paths}
	}
	out.ID = "gitlab:pipeline:" + detail.ProjectID + ":" + strconv.FormatInt(detail.ID, 10)
	end := detail.UpdatedAt
	if detail.FinishedAt != nil {
		end = *detail.FinishedAt
	}
	alerts, alertState, err := s.gitLabAlerts(ctx, current, row, end.Add(-adjacentBefore), end.Add(adjacentAfter).Add(time.Second))
	if err != nil {
		writeReadError(w, err)
		return
	}
	if alertState == "complete" {
		out.AdjacentAlerts = adjacency(alerts, end)
	} else {
		out.Unavailable["adjacent_alerts"] = alertReason(alertState)
	}
	if err := s.gitLabFinalCheck(ctx, r, row); err != nil {
		writeReadError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, out)
}
