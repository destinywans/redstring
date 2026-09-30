package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3alertrules"
	"github.com/wans/aiops-canvas/internal/v3configsync"
)

const alertRuleDraftPrefix = "aiops-alert-rule-drafts/"

// Preserve disabled repositories as a nil interface at the Service boundary.
// This adapter is specific to the optional GitLab draft loaders above it.
func optionalV3AlertmanagerRepository(repository *v3alertmanager.RouteGitLabRepository) v3alertmanager.RouteRepository {
	if repository == nil {
		return nil
	}
	return repository
}

// A configured pair enables reviewed, nondeployable draft exports. No pair
// means the write routes remain registered but cannot contact GitLab.
// Optional repositories retain their concrete pointer type: the loaders return
// nil pointers when disabled, which would be non-nil inside RouteRepository.
func loadV3AlertRuleDraftRepository(promotion *v3configsync.Service,
	route, inhibition *v3alertmanager.RouteGitLabRepository, target, prefix string) (v3alertrules.DraftRepository, error) {
	if target == "" && prefix == "" {
		return nil, nil
	}
	if target == "" || prefix != alertRuleDraftPrefix || promotion == nil {
		return nil, errors.New("alert rule drafts require a target and the dedicated branch prefix with config promotion")
	}
	base, ok := promotion.Repository.(*v3configsync.GitLabClient)
	if !ok || base == nil {
		return nil, errors.New("alert rule drafts require the configured GitLab promotion repository")
	}
	if !distinctDraftNamespace(target, prefix, base.TargetBranch(), base.BranchPrefix()) ||
		(route != nil && !distinctDraftNamespace(target, prefix, route.TargetBranch(), route.BranchPrefix())) ||
		(inhibition != nil && !distinctDraftNamespace(target, prefix, inhibition.TargetBranch(), inhibition.BranchPrefix())) {
		return nil, errors.New("alert rule draft target and prefix must differ from promotion, notification and inhibition")
	}
	client, err := base.ForAlertRuleDrafts(target, prefix)
	if err != nil {
		return nil, err
	}
	return &alertRuleDraftGitLabRepository{client: client}, nil
}

func distinctDraftNamespace(target, prefix, otherTarget, otherPrefix string) bool {
	return target != otherTarget && !strings.HasPrefix(prefix, otherPrefix) && !strings.HasPrefix(otherPrefix, prefix)
}

type alertRuleDraftGitLabRepository struct {
	client *v3configsync.GitLabClient
}

func (r *alertRuleDraftGitLabRepository) ProjectID() int64 { return r.client.ProjectID() }
func (r *alertRuleDraftGitLabRepository) BranchPrefix() string {
	return r.client.BranchPrefix()
}

func (r *alertRuleDraftGitLabRepository) ExportDraft(ctx context.Context, branch, path string,
	payload []byte, title string) (v3alertrules.DraftMR, error) {
	id := strings.TrimPrefix(branch, alertRuleDraftPrefix)
	if r == nil || r.client == nil || id == branch || id == "" ||
		path != "ops-config/alert-rule-drafts/"+id+".json" {
		return v3alertrules.DraftMR{}, errors.New("invalid alert rule draft export identity")
	}
	mr, err := r.client.Export(ctx, branch, path, payload, title)
	if err != nil {
		if errors.Is(err, v3configsync.ErrConflict) {
			return v3alertrules.DraftMR{}, fmt.Errorf("%w: %v", v3alertrules.ErrDraftRemoteConflict, err)
		}
		return v3alertrules.DraftMR{}, err
	}
	return v3alertrules.DraftMR{IID: mr.IID, SourceProjectID: mr.SourceProjectID,
		TargetProjectID: mr.TargetProjectID, URL: mr.URL, State: mr.State,
		SourceBranch: mr.SourceBranch}, nil
}
