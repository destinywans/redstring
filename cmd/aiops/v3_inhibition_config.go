package main

import (
	"errors"
	"strings"

	"github.com/destinywans/redstring/internal/v3alertmanager"
	"github.com/destinywans/redstring/internal/v3configsync"
)

// Inhibition MRs must be review-only in a separate target, source namespace
// and label; an unset pair leaves outbound GitLab draft writes disabled.
func loadV3InhibitionRepository(promotion *v3configsync.Service, route *v3alertmanager.RouteGitLabRepository,
	target, prefix string) (*v3alertmanager.RouteGitLabRepository, error) {
	if target == "" && prefix == "" {
		return nil, nil
	}
	if target == "" || prefix == "" || promotion == nil {
		return nil, errors.New("inhibition drafts require target and branch prefix with config promotion")
	}
	base, ok := promotion.Repository.(*v3configsync.GitLabClient)
	if !ok || base == nil {
		return nil, errors.New("inhibition drafts require the configured GitLab promotion repository")
	}
	if target == base.TargetBranch() || strings.HasPrefix(prefix, base.BranchPrefix()) ||
		strings.HasPrefix(base.BranchPrefix(), prefix) {
		return nil, errors.New("inhibition draft target and prefix must differ from config promotion")
	}
	if route != nil && (target == route.TargetBranch() || strings.HasPrefix(prefix, route.BranchPrefix()) ||
		strings.HasPrefix(route.BranchPrefix(), prefix)) {
		return nil, errors.New("inhibition draft target and prefix must differ from notification route drafts")
	}
	return v3alertmanager.NewInhibitionGitLabRepositoryFromPromotion(base, target, prefix)
}
