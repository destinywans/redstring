package v3changes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
)

// KubernetesRevision is one ReplicaSet revision of the service's Deployment:
// a change seen in the cluster, whatever made it. GitLabPipelineID is set
// when its change cause names the GitLab pipeline that deployed it.
//
// CreatedAt is the ReplicaSet's creation. When the Deployment returns to an
// earlier template, Kubernetes reuses that ReplicaSet with a new revision
// number and keeps its creation time, so for a reused ReplicaSet CreatedAt
// is not when the revision took effect (PA-94). At is that time when it is
// known, with TimeBasis saying from what; otherwise At is nil and NotBefore
// and NotAfter bound it by the revisions around it.
type KubernetesRevision struct {
	Revision          string     `json:"revision"`
	CreatedAt         time.Time  `json:"created_at"`
	ChangeCause       *string    `json:"change_cause"`
	GitLabPipelineID  *int64     `json:"gitlab_pipeline_id"`
	ReplicaSetReused  bool       `json:"replicaset_reused"`
	At                *time.Time `json:"at"`
	TimeBasis         string     `json:"time_basis"`
	TimeUnknownReason *string    `json:"time_unknown_reason"`
	NotBefore         *time.Time `json:"not_before"`
	NotAfter          *time.Time `json:"not_after"`
}

// Time bases of a revision (PA-94).
const (
	TimeBasisReplicaSetCreated = "replicaset_created"
	TimeBasisGitLabDeployment  = "gitlab_deployment"
	TimeBasisRollbackExecution = "rollback_execution"
	TimeBasisUnknown           = "unknown"
)

// InWindow reports whether the revision took effect in [start, end]: its
// time when known, else whether its bounds meet the window.
func (r KubernetesRevision) InWindow(start, end time.Time) bool {
	if r.At != nil {
		return !r.At.Before(start) && !r.At.After(end)
	}
	return r.NotBefore != nil && !r.NotBefore.After(end) && (r.NotAfter == nil || !r.NotAfter.Before(start))
}

// RevisionBefore orders revisions by number, the order they took effect
// in; a reused ReplicaSet's CreatedAt does not give it (PA-94). A revision
// without a number orders by CreatedAt.
func RevisionBefore(a, b KubernetesRevision) bool {
	x, errX := strconv.ParseInt(a.Revision, 10, 64)
	y, errY := strconv.ParseInt(b.Revision, 10, 64)
	if errX != nil || errY != nil {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return x < y
}

// Errors of the revision read.
var (
	ErrRevisionsNotConfigured = errors.New("Kubernetes workload is not configured for this service")
	ErrRevisionsForbidden     = errors.New("Kubernetes workload outside the caller's scope")
	ErrRevisionsUnavailable   = errors.New("Kubernetes workload unavailable")
)

var (
	// gitLabChangeCause is the change cause a GitLab deploy job writes
	// (kubernetes.io/change-cause: gitlab pipeline <id> <sha>).
	gitLabChangeCause = regexp.MustCompile(`^gitlab pipeline ([1-9][0-9]{0,18}) ([0-9a-f]{7,64})$`)
	// rollbackChangeCause is the one the rollback executor writes (PA-76).
	rollbackChangeCause = regexp.MustCompile(`^aiops rollback to revision [0-9]{1,19} \(execution ([0-9a-f]{32})\)$`)
)

// revisionTimeSkew is how far a GitLab deployment may finish after the next
// revision took effect: the deploy job applies, then waits for the rollout.
const revisionTimeSkew = 10 * time.Minute

// ReadRevisions reads the revisions through the composition root's reader
// (the rollback service's read credential and resources.view check), and
// works out when each took effect.
func (s *Service) ReadRevisions(ctx context.Context, user core.User, app, env string) ([]KubernetesRevision, error) {
	if s == nil || s.Revisions == nil {
		return nil, ErrRevisionsNotConfigured
	}
	revisions, err := s.Revisions(ctx, user, app, env)
	if err != nil {
		return nil, err
	}
	for i := range revisions {
		revisions[i].GitLabPipelineID = nil
		if cause := revisions[i].ChangeCause; cause != nil {
			if match := gitLabChangeCause.FindStringSubmatch(*cause); match != nil {
				if id, err := strconv.ParseInt(match[1], 10, 64); err == nil {
					revisions[i].GitLabPipelineID = &id
				}
			}
		}
	}
	s.resolveRevisionTimes(ctx, user, app, env, revisions, time.Now().UTC())
	return revisions, nil
}

// resolveRevisionTimes sets each revision's time (PA-94). A new ReplicaSet
// took effect when it was created. A reused one took effect when the
// rollback execution its change cause names wrote it (the ledger's applied
// step), or when the GitLab deployment its change cause names finished;
// otherwise its time is unknown and bounded by the revisions around it.
//
// A GitLab change cause can be stale: the controller copies the
// Deployment's cause onto the ReplicaSet it reuses, and a rollback that does
// not set one leaves the previous release's. So a GitLab deployment counts
// only if it started after the ReplicaSet existed, no other ReplicaSet
// carries the same cause, and its time fits the revision order.
func (s *Service) resolveRevisionTimes(ctx context.Context, user core.User, app, env string, revisions []KubernetesRevision, now time.Time) {
	var executions []string
	var gitlabSince *time.Time
	for _, r := range revisions {
		if !r.ReplicaSetReused || r.ChangeCause == nil {
			continue
		}
		if match := rollbackChangeCause.FindStringSubmatch(*r.ChangeCause); match != nil {
			executions = append(executions, match[1])
		} else if r.GitLabPipelineID != nil && (gitlabSince == nil || r.CreatedAt.Before(*gitlabSince)) {
			since := r.CreatedAt
			gitlabSince = &since
		}
	}
	applied, ledgerRead := s.rollbackAppliedTimes(ctx, app, env, executions)
	deployments, gitlabReason := []Deployment(nil), ""
	if gitlabSince != nil {
		deployments, gitlabReason = s.revisionDeployments(ctx, user, app, env, *gitlabSince, now)
	}
	assignRevisionTimes(revisions, applied, ledgerRead, deployments, gitlabReason, now)
}

// assignRevisionTimes applies resolveRevisionTimes's rules to what it read:
// the rollback ledger's applied times by execution and revision (ledgerRead
// false when it could not be read), and the GitLab deployments (gitlabReason
// set when they could not be read).
func assignRevisionTimes(revisions []KubernetesRevision, applied map[string]time.Time, ledgerRead bool,
	deployments []Deployment, gitlabReason string, now time.Time) {
	causes := map[string]int{}
	for _, r := range revisions {
		if r.ChangeCause != nil {
			causes[*r.ChangeCause]++
		}
	}
	unknown := func(r *KubernetesRevision, reason string) {
		r.At, r.TimeBasis = nil, TimeBasisUnknown
		r.TimeUnknownReason = &reason
	}
	for i := range revisions {
		r := &revisions[i]
		r.At, r.TimeUnknownReason, r.NotBefore, r.NotAfter = nil, nil, nil, nil
		if !r.ReplicaSetReused {
			at := r.CreatedAt.UTC()
			r.At, r.TimeBasis = &at, TimeBasisReplicaSetCreated
			continue
		}
		unknown(r, "replicaset_reused")
	}
	for i := range revisions {
		r := &revisions[i]
		if !r.ReplicaSetReused || r.ChangeCause == nil {
			continue
		}
		if match := rollbackChangeCause.FindStringSubmatch(*r.ChangeCause); match != nil {
			if at, ok := applied[match[1]+"\x00"+r.Revision]; ok {
				r.At, r.TimeBasis, r.TimeUnknownReason = &at, TimeBasisRollbackExecution, nil
			} else if !ledgerRead {
				unknown(r, "rollback_ledger_unavailable")
			} else {
				unknown(r, "rollback_execution_not_found")
			}
			continue
		}
		if r.GitLabPipelineID == nil {
			continue
		}
		if gitlabReason != "" {
			unknown(r, gitlabReason)
			continue
		}
		if causes[*r.ChangeCause] > 1 {
			unknown(r, "change_cause_stale")
			continue
		}
		sha := gitLabChangeCause.FindStringSubmatch(*r.ChangeCause)[2]
		var found, before *Deployment
		for j := range deployments {
			d := &deployments[j]
			if d.PipelineID == nil || *d.PipelineID != *r.GitLabPipelineID || !strings.HasPrefix(d.SHA, sha) {
				continue
			}
			if !d.CreatedAt.After(r.CreatedAt) {
				before = d
			} else if found == nil || d.CreatedAt.Before(found.CreatedAt) {
				found = d
			}
		}
		switch {
		case found != nil && found.FinishedAt != nil:
			at := found.FinishedAt.UTC()
			r.At, r.TimeBasis, r.TimeUnknownReason = &at, TimeBasisGitLabDeployment, nil
		case found != nil:
			unknown(r, "gitlab_deployment_unfinished")
		case before != nil:
			unknown(r, "change_cause_stale")
		default:
			unknown(r, "gitlab_deployment_not_found")
		}
	}
	boundRevisionTimes(revisions, now)
}

// boundRevisionTimes checks the times not taken from a ReplicaSet's creation
// against the revision order, and bounds the unknown ones: revision numbers
// only grow, so a revision took effect after every lower one and before
// every higher one. A rollback's time is checked against the ReplicaSet
// creations, a GitLab deployment's against those and the rollbacks, so a
// wrong time never discredits a more certain one.
func boundRevisionTimes(revisions []KubernetesRevision, now time.Time) {
	order := make([]int, 0, len(revisions))
	numbers := map[int]int64{}
	for i, r := range revisions {
		if n, err := strconv.ParseInt(r.Revision, 10, 64); err == nil && n > 0 {
			order, numbers[i] = append(order, i), n
		}
	}
	sort.Slice(order, func(a, b int) bool { return numbers[order[a]] < numbers[order[b]] })
	// bounds are the latest time below position and the earliest above it,
	// among the revisions whose basis counts.
	bounds := func(position int, counts func(string) bool) (*time.Time, *time.Time) {
		var lower, upper *time.Time
		for p, i := range order {
			at := revisions[i].At
			if p == position || at == nil || !counts(revisions[i].TimeBasis) {
				continue
			}
			if p < position && (lower == nil || at.After(*lower)) {
				lower = at
			}
			if p > position && (upper == nil || at.Before(*upper)) {
				upper = at
			}
		}
		return lower, upper
	}
	for _, check := range []struct {
		basis   string
		anchors func(string) bool
	}{
		{TimeBasisRollbackExecution, func(b string) bool { return b == TimeBasisReplicaSetCreated }},
		{TimeBasisGitLabDeployment, func(b string) bool {
			return b == TimeBasisReplicaSetCreated || b == TimeBasisRollbackExecution
		}},
	} {
		for position, i := range order {
			r := &revisions[i]
			if r.At == nil || r.TimeBasis != check.basis {
				continue
			}
			lower, upper := bounds(position, check.anchors)
			if (lower != nil && r.At.Before(*lower)) || (upper != nil && r.At.After(upper.Add(revisionTimeSkew))) || r.At.After(now) {
				reason := "out_of_order"
				r.At, r.TimeBasis, r.TimeUnknownReason = nil, TimeBasisUnknown, &reason
			}
		}
	}
	known := func(b string) bool { return b != TimeBasisUnknown }
	for position, i := range order {
		r := &revisions[i]
		if r.At != nil {
			continue
		}
		lower, upper := bounds(position, known)
		notBefore := r.CreatedAt.UTC()
		if lower != nil && lower.After(notBefore) {
			notBefore = *lower
		}
		notAfter := now
		if upper != nil {
			notAfter = *upper
		}
		r.NotBefore, r.NotAfter = &notBefore, &notAfter
	}
	for i := range revisions {
		if _, ordered := numbers[i]; !ordered && revisions[i].At == nil {
			notBefore, notAfter := revisions[i].CreatedAt.UTC(), now
			revisions[i].NotBefore, revisions[i].NotAfter = &notBefore, &notAfter
		}
	}
}

// rollbackAppliedTimes reads when the named rollback executions of the
// service wrote the Deployment, keyed by execution ID and the revision the
// rollout reached; executions without either are left out. ok is false when
// the ledger could not be read.
func (s *Service) rollbackAppliedTimes(ctx context.Context, app, env string, executions []string) (map[string]time.Time, bool) {
	out := map[string]time.Time{}
	if len(executions) == 0 {
		return out, true
	}
	if s.DB == nil {
		return out, false
	}
	rows, err := s.DB.Query(ctx, `SELECT execution_id,COALESCE(rollout->>'current_revision',''),timeline
 FROM v3_rollout_executions WHERE application_id=$1 AND environment_id=$2 AND execution_id=ANY($3)`, app, env, executions)
	if err != nil {
		return out, false
	}
	defer rows.Close()
	for rows.Next() {
		var id, revision string
		var raw []byte
		if rows.Scan(&id, &revision, &raw) != nil || revision == "" {
			continue
		}
		var timeline []struct {
			State string    `json:"state"`
			At    time.Time `json:"at"`
		}
		if json.Unmarshal(raw, &timeline) != nil {
			continue
		}
		for _, step := range timeline {
			if step.State == "applied" && !step.At.IsZero() {
				out[id+"\x00"+revision] = step.At.UTC()
				break
			}
		}
	}
	return out, rows.Err() == nil
}

// revisionDeployments reads the service's GitLab deployments updated since
// the oldest reused ReplicaSet with a GitLab cause was created, for the
// caller, or says why it cannot.
func (s *Service) revisionDeployments(ctx context.Context, user core.User, app, env string, since, now time.Time) ([]Deployment, string) {
	serviceID := base64.RawURLEncoding.EncodeToString([]byte(app + "\x00" + env))
	src, err := s.loadSource(ctx, user, app, env, serviceID)
	if err != nil || !src.toolEnabled(ToolDeployments) {
		return nil, "gitlab_unavailable"
	}
	query := url.Values{"environment": {src.scope.DeployEnvironment}, "updated_after": {since.UTC().Format(time.RFC3339)},
		"order_by": {"updated_at"}, "sort": {"asc"}, "per_page": {"100"}, "page": {"1"}}
	var items []gitLabDeployment
	header, err := s.get(ctx, src.target, "/deployments", query, &items)
	if err != nil || header.Get("X-Next-Page") != "" || len(items) > 100 {
		return nil, "gitlab_unavailable"
	}
	out := make([]Deployment, 0, len(items))
	for _, item := range items {
		deployment, ok := toDeployment(item, src.scope.DeployEnvironment, now)
		if !ok {
			return nil, "gitlab_unavailable"
		}
		out = append(out, deployment)
	}
	return out, ""
}
