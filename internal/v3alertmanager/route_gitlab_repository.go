package v3alertmanager

import (
	"context"

	"github.com/wans/aiops-canvas/internal/v3configsync"
)

// RouteGitLabRepository keeps the configured target branch attached to the
// client that performs the export. The route service checks that target and
// source branch prefix against config-sync before any external write.
type RouteGitLabRepository struct {
	client       *v3configsync.GitLabClient
	targetBranch string
}

func NewRouteGitLabRepository(config v3configsync.Config) (*RouteGitLabRepository, error) {
	client, err := v3configsync.NewGitLabClient(config)
	if err != nil {
		return nil, err
	}
	return &RouteGitLabRepository{client: client, targetBranch: config.TargetBranch}, nil
}

func (r *RouteGitLabRepository) ProjectID() int64     { return r.client.ProjectID() }
func (r *RouteGitLabRepository) BranchPrefix() string { return r.client.BranchPrefix() }
func (r *RouteGitLabRepository) TargetBranch() string { return r.targetBranch }
func (r *RouteGitLabRepository) Export(ctx context.Context, branch, path string, payload []byte, title string) (v3configsync.MR, error) {
	return r.client.Export(ctx, branch, path, payload, title)
}
