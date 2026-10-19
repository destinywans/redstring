package v3alertmanager

import (
	"context"

	"github.com/destinywans/redstring/internal/v3configsync"
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

// The product uses the same reviewed GitOps project and credential as config
// promotion, with a separate target and source branch namespace for drafts.
func NewRouteGitLabRepositoryFromPromotion(promotion *v3configsync.GitLabClient, target, prefix string) (*RouteGitLabRepository, error) {
	client, err := promotion.ForNotificationDrafts(target, prefix)
	if err != nil {
		return nil, err
	}
	return &RouteGitLabRepository{client: client, targetBranch: target}, nil
}

func NewInhibitionGitLabRepositoryFromPromotion(promotion *v3configsync.GitLabClient, target, prefix string) (*RouteGitLabRepository, error) {
	client, err := promotion.ForInhibitionDrafts(target, prefix)
	if err != nil {
		return nil, err
	}
	return &RouteGitLabRepository{client: client, targetBranch: target}, nil
}

func (r *RouteGitLabRepository) ProjectID() int64     { return r.client.ProjectID() }
func (r *RouteGitLabRepository) BranchPrefix() string { return r.client.BranchPrefix() }
func (r *RouteGitLabRepository) TargetBranch() string { return r.targetBranch }
func (r *RouteGitLabRepository) Export(ctx context.Context, branch, path string, payload []byte, title string) (v3configsync.MR, error) {
	return r.client.Export(ctx, branch, path, payload, title)
}
