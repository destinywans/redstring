package main

import (
	"context"
	"errors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"testing"
)

func TestEnvironmentToolsRejectRebindArchiveAndNeverReadNewTarget(t *testing.T) {
	frozen := core.Environment{ID: "env-a", ApplicationID: "app-a", BindingRevision: 1}
	current := frozen
	calls := 0
	tools := freezeEnvironmentTools(func(context.Context, string, string) (core.Environment, error) { return current, nil }, "app-a", frozen, []inference.EvidenceTool{{Name: "query_metrics", Read: func(context.Context, string) (inference.Evidence, error) { calls++; return inference.Evidence{}, nil }}, {Name: "inspect_deployment", Read: func(context.Context, string) (inference.Evidence, error) { calls++; return inference.Evidence{}, nil }}})
	for _, tool := range tools {
		if _, e := tool.Read(context.Background(), ""); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 2 {
		t.Fatal("initial read not allowed")
	}
	current.BindingRevision = 2
	for _, tool := range tools {
		if _, e := tool.Read(context.Background(), ""); !errors.Is(e, store.ErrConflict) {
			t.Fatalf("rebind allowed: %v", e)
		}
	}
	current = frozen
	current.Archived = true
	for _, tool := range tools {
		if _, e := tool.Read(context.Background(), ""); !errors.Is(e, store.ErrConflict) {
			t.Fatalf("archive allowed: %v", e)
		}
	}
	if calls != 2 {
		t.Fatal("stale or rebound connector invoked")
	}
}
