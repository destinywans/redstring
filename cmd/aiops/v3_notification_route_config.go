package main

import (
	"errors"
	"strings"

	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3configsync"
)

// Notification drafts share the reviewed config-sync GitLab project and
// credential, while using a disjoint target and source branch namespace.
func loadV3NotificationRouteRepository(promotion *v3configsync.Service, target, prefix string) (*v3alertmanager.RouteGitLabRepository, error) {
	if target == "" && prefix == "" {
		return nil, nil
	}
	if target == "" || prefix == "" || promotion == nil {
		return nil, errors.New("notification route draft requires a target and branch prefix with config promotion")
	}
	base, ok := promotion.Repository.(*v3configsync.GitLabClient)
	if !ok || base == nil {
		return nil, errors.New("notification route draft requires the configured GitLab promotion repository")
	}
	if target == base.TargetBranch() || strings.HasPrefix(prefix, base.BranchPrefix()) || strings.HasPrefix(base.BranchPrefix(), prefix) {
		return nil, errors.New("notification route draft must use a distinct GitLab target and branch prefix")
	}
	return v3alertmanager.NewRouteGitLabRepositoryFromPromotion(base, target, prefix)
}
