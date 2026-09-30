package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/wans/aiops-canvas/internal/actions"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/delivery"
	"github.com/wans/aiops-canvas/internal/deployment"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func loadDeliveryConfig(path string) (delivery.Config, error) {
	var config delivery.Config
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return config, e
		}
		d := json.NewDecoder(strings.NewReader(string(b)))
		d.DisallowUnknownFields()
		if e = d.Decode(&config); e != nil {
			return config, e
		}
	}
	return config, nil
}

func configureDelivery(ctx context.Context, s *store.Store, api *httpapi.Server, acts *actions.Service, config delivery.Config, v3Scope *httpapi.V3InstanceConfig) (*delivery.Service, *deployment.Service, error) {
	d, e := delivery.New(s.DB, config)
	if e != nil {
		return nil, nil, e
	}
	if v3Scope != nil {
		// Native GitLab ingress has no browser environment header. Its fixed
		// project binding must still belong to this V3 instance at receipt time.
		environments := append([]string(nil), v3Scope.EnvironmentIDs...)
		d.V3WebhookEnvironments = environments
		d.ValidateEnvironment = func(ctx context.Context, app, env string) error {
			var active bool
			err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM application_environments e
			 JOIN applications a ON a.id=e.application_id AND NOT a.archived
			 JOIN v3_application_environments ve ON ve.application_id=e.application_id AND ve.environment_id=e.key
			 WHERE e.id=$1 AND e.application_id=$2 AND e.key=ANY($3::text[]) AND NOT e.archived)`,
				env, app, environments).Scan(&active)
			if err != nil {
				return err
			}
			if !active {
				return store.ErrForbidden
			}
			return nil
		}
	}
	if e = d.Migrate(ctx); e != nil {
		return nil, nil, e
	}
	d.Register(api)
	dc, e := deployment.LoadConfig(os.Getenv("AIOPS_DEPLOYMENT_CONFIG"))
	if e != nil {
		return nil, nil, e
	}
	releases, e := deployment.New(s.DB, s, dc)
	if e != nil {
		return nil, nil, e
	}
	if e = releases.Migrate(ctx); e != nil {
		return nil, nil, e
	}
	releases.VerifySource = d.VerifySource
	releases.ResolveArtifactURL = d.ResolveArtifactURL
	releases.Register(api)
	if len(dc.Targets) > 0 {
		d.Release = &releaseBridge{s: s, releases: releases}
	}
	// Product and recovery paths share the registered target authority. A direct
	// Deployment patch must never fight the GitOps controller for the same target.
	previous := acts.Resolve
	acts.Resolve = func(ctx context.Context, inv string, p *connectors.RollbackPlan) (actions.Connector, error) {
		var env string
		if e := s.DB.QueryRow(ctx, "SELECT COALESCE(environment_id,'') FROM investigations WHERE id=$1", inv).Scan(&env); e != nil {
			return nil, e
		}
		if releases.IsGitOpsEnvironment(env) {
			return nil, fmt.Errorf("GitOps environment requires an approved Git revision restore through delivery")
		}
		return previous(ctx, inv, p)
	}
	caps := api.EnvironmentCapabilities
	api.EnvironmentCapabilities = func(env core.Environment) []string {
		items := caps(env)
		if !releases.IsGitOpsEnvironment(env.ID) {
			return items
		}
		filtered := []string{}
		for _, v := range items {
			if !strings.HasPrefix(v, "rollback.") && v != "recovery.verify" {
				filtered = append(filtered, v)
			}
		}
		return append(filtered, "gitops.restore_after_approval")
	}
	for _, g := range config.GitLabs {
		api.Integrations = append(api.Integrations, httpapi.NewIntegration("delivery-gitlab-"+g.ID, g.Name, "connector", "gitlab", true, nil, []string{"pipelines.read", "pipelines.trigger", "jobs.logs"}))
	}
	api.Integrations = append(api.Integrations, httpapi.NewIntegration("delivery-argo", "Argo Workflows", "connector", "argo-workflows", d.Argo != nil, nil, []string{"workflow.execute"}))
	return d, releases, nil
}

// The bridge reloads the actor for every state transition; a disabled account or
// revoked scope cannot retain authority through a queued native workflow.
type releaseBridge struct {
	s        *store.Store
	releases *deployment.Service
}

func (b *releaseBridge) Prepare(ctx context.Context, actor, run, node, digest, target string) (delivery.ReleasePlan, error) {
	u, e := b.s.ActiveUser(ctx, actor)
	if e != nil {
		return delivery.ReleasePlan{}, e
	}
	p, e := b.releases.Prepare(ctx, u, deployment.Request{IntentID: run + ":" + node + ":" + target, RunID: run, NodeID: node, ArtifactDigest: digest, TargetID: target})
	return delivery.ReleasePlan{ID: p.ID, Digest: p.Digest, Status: p.Status}, e
}
func (b *releaseBridge) Plan(ctx context.Context, actor, id string) (delivery.ReleasePlan, error) {
	u, e := b.s.ActiveUser(ctx, actor)
	if e != nil {
		return delivery.ReleasePlan{}, e
	}
	p, e := b.releases.Plan(ctx, u, id)
	return delivery.ReleasePlan{ID: p.ID, Digest: p.Digest, Status: p.Status}, e
}
func (b *releaseBridge) Execute(ctx context.Context, actor, id string) (delivery.ReleaseExecution, error) {
	u, e := b.s.ActiveUser(ctx, actor)
	if e != nil {
		return delivery.ReleaseExecution{}, e
	}
	x, e := b.releases.Execute(ctx, u, id)
	return delivery.ReleaseExecution{ID: x.ID, Status: x.Status}, e
}
func (b *releaseBridge) Execution(ctx context.Context, actor, id string) (delivery.ReleaseExecution, error) {
	u, e := b.s.ActiveUser(ctx, actor)
	if e != nil {
		return delivery.ReleaseExecution{}, e
	}
	x, e := b.releases.Execution(ctx, u, id)
	return delivery.ReleaseExecution{ID: x.ID, Status: x.Status}, e
}
func (b *releaseBridge) Cancel(ctx context.Context, actor, id string) error {
	u, e := b.s.ActiveUser(ctx, actor)
	if e != nil {
		return e
	}
	return b.releases.Cancel(ctx, u, id)
}
