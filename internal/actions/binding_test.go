package actions

import (
	"context"
	"errors"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/store"
	"testing"
)

func TestApprovalAndEnqueueRejectStaleEnvironmentBinding(t *testing.T) {
	s, connector, inv := isolatedActions(t, 4)
	ctx := context.Background()
	revision := int64(1)
	s.Resolve = func(_ context.Context, _ string, p *connectors.RollbackPlan) (Connector, error) {
		if p.Digest != "" && (p.BindingRevision != revision || p.EnvironmentID != "app-default" || p.ConnectorID != "registered") {
			return nil, store.ErrConflict
		}
		p.BindingRevision = revision
		p.EnvironmentID = "app-default"
		p.ConnectorID = "registered"
		p.ConfigurationDigest = "cluster-a"
		return connector, nil
	}
	p, e := s.Create(ctx, inv, "1", "u")
	if e != nil {
		t.Fatal(e)
	}
	revision = 2
	if _, e = s.Approve(ctx, p.ID, p.Proposal.Digest, "u"); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("stale approval accepted: %v", e)
	}
	p, e = s.Create(ctx, inv, "1", "u")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Approve(ctx, p.ID, p.Proposal.Digest, "u"); e != nil {
		t.Fatal(e)
	}
	revision = 3
	if _, e = s.Enqueue(ctx, p.ID, "u"); !errors.Is(e, store.ErrConflict) {
		t.Fatalf("stale approved execution accepted: %v", e)
	}
	if connector.executed.Load() != 0 {
		t.Fatal("external write invoked")
	}
}
