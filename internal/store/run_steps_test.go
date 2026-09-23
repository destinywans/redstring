package store

import (
	"context"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
)

func TestRunStepsLinkEvidenceAndRunMessage(t *testing.T) {
	s := isolated(t)
	ctx := context.Background()
	inv, e := s.CreateInvestigation(ctx, "app", "steps")
	if e != nil {
		t.Fatal(e)
	}
	bid := inv.Branches[0].ID
	if _, e = s.Enqueue(ctx, bid, "inspect", "model-a", "steps-1"); e != nil {
		t.Fatal(e)
	}
	c, e := s.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	started := time.Now().UTC()
	step := core.RunStep{ID: c.Token + "-1", Seq: 1, Kind: "tool", Name: "query_metrics", Status: "running", Input: "5xx", StartedAt: started}
	if e = s.RecordStep(ctx, c, step); e != nil {
		t.Fatal(e)
	}
	evidenceID, e := s.SaveEvidenceID(ctx, c, core.Evidence{Kind: "metrics", Title: "5xx 8%", Content: "series", Source: "prom"})
	if e != nil || evidenceID == "" {
		t.Fatalf("evidence %q %v", evidenceID, e)
	}
	finished := time.Now().UTC()
	in, out := 10, 3
	step.Status, step.Output, step.EvidenceID, step.FinishedAt = "succeeded", "5xx 8%", evidenceID, &finished
	step.InputTokens, step.OutputTokens = &in, &out
	if e = s.RecordStep(ctx, c, step); e != nil {
		t.Fatal(e)
	}
	if e = s.Finish(ctx, c, "answer", nil); e != nil {
		t.Fatal(e)
	}
	steps, e := s.RunSteps(ctx, c.Run.ID)
	if e != nil || len(steps) != 1 {
		t.Fatalf("steps %v %+v", e, steps)
	}
	got := steps[0]
	if got.Status != "succeeded" || got.EvidenceID != evidenceID || got.AttemptID != c.Token || got.FinishedAt == nil || got.InputTokens == nil || *got.InputTokens != 10 || len(got.ToolCalls) != 0 {
		t.Fatalf("step %+v", got)
	}
	events, e := s.Events(ctx, inv.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	stepEvents := 0
	for _, ev := range events {
		if ev.Type == "attempt.step" {
			stepEvents++
		}
	}
	if stepEvents != 2 {
		t.Fatalf("want running and finished step events, got %d", stepEvents)
	}
	b, e := s.Branch(ctx, bid)
	if e != nil {
		t.Fatal(e)
	}
	last := b.Messages[len(b.Messages)-1]
	if last.Role != "assistant" || last.RunID != c.Run.ID {
		t.Fatalf("assistant message not linked to run: %+v", last)
	}
	// A lost lease must not let a stale worker rewrite steps.
	if e = s.RecordStep(ctx, c, step); e != ErrLease {
		t.Fatalf("stale step write = %v", e)
	}
}
