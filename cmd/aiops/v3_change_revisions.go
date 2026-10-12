package main

import (
	"context"
	"errors"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3changes"
	"github.com/wans/aiops-canvas/internal/v3rollouts"
)

// changeRevisions reads a service's Deployment revisions for the change
// views (D11) with the rollback service's read credential and its
// resources.view check.
func changeRevisions(rollouts *v3rollouts.Service) func(context.Context, core.User, string, string) ([]v3changes.KubernetesRevision, error) {
	return func(ctx context.Context, user core.User, app, env string) ([]v3changes.KubernetesRevision, error) {
		evidence, err := rollouts.ReadWorkload(ctx, user, app, env)
		switch {
		case errors.Is(err, v3rollouts.ErrWorkloadNotConfigured):
			return nil, v3changes.ErrRevisionsNotConfigured
		case errors.Is(err, store.ErrForbidden):
			return nil, v3changes.ErrRevisionsForbidden
		case err != nil:
			return nil, v3changes.ErrRevisionsUnavailable
		}
		out := make([]v3changes.KubernetesRevision, 0, len(evidence.Revisions))
		for _, revision := range evidence.Revisions {
			if revision.CreatedAt == nil {
				continue
			}
			at, err := time.Parse(time.RFC3339, *revision.CreatedAt)
			if err != nil {
				return nil, v3changes.ErrRevisionsUnavailable
			}
			out = append(out, v3changes.KubernetesRevision{Revision: revision.Revision, CreatedAt: at.UTC(), ChangeCause: revision.ChangeCause,
				ReplicaSetReused: revision.ReplicaSetReused})
		}
		return out, nil
	}
}
