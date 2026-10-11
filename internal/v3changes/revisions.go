package v3changes

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
)

// KubernetesRevision is one ReplicaSet revision of the service's Deployment:
// a change seen in the cluster, whatever made it. GitLabPipelineID is set
// when its change cause names the GitLab pipeline that deployed it.
type KubernetesRevision struct {
	Revision         string    `json:"revision"`
	CreatedAt        time.Time `json:"created_at"`
	ChangeCause      *string   `json:"change_cause"`
	GitLabPipelineID *int64    `json:"gitlab_pipeline_id"`
}

// Errors of the revision read.
var (
	ErrRevisionsNotConfigured = errors.New("Kubernetes workload is not configured for this service")
	ErrRevisionsForbidden     = errors.New("Kubernetes workload outside the caller's scope")
	ErrRevisionsUnavailable   = errors.New("Kubernetes workload unavailable")
)

// gitLabChangeCause is the change cause a GitLab deploy job writes
// (kubernetes.io/change-cause: gitlab pipeline <id> <sha>).
var gitLabChangeCause = regexp.MustCompile(`^gitlab pipeline ([1-9][0-9]{0,18}) [0-9a-f]{7,64}$`)

// ReadRevisions reads the revisions through the composition root's reader
// (the rollback service's read credential and resources.view check).
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
	return revisions, nil
}
