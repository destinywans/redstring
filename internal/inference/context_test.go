package inference

import (
	"context"
	"strings"
	"testing"
)

type contextTestRunner struct {
	budget   int
	calls    int
	response string
}

func (r *contextTestRunner) Models() []Model { return []Model{{ID: "test", InputBudget: r.budget}} }
func (r *contextTestRunner) Run(_ context.Context, _ Request, _ func(Event) error) (string, error) {
	r.calls++
	return r.response, nil
}
func TestContextBudgetPreservesOriginalAndProvenance(t *testing.T) {
	r := &contextTestRunner{budget: 12000, response: "Confirmed user constraints retained; historical observation [m2]."}
	history := []ContextMessage{{"m1", "user", "Investigate checkout without changes"}, {"m2", "assistant", strings.Repeat("evidence ", 700)}, {"m2a", "user", "preserve constraints"}, {"m2b", "assistant", strings.Repeat("observed ", 700)}, {"m3", "user", "check metrics"}, {"m4", "assistant", "errors increased"}, {"m5", "user", "continue"}}
	req := Request{ModelID: "test", RunID: "r", AttemptID: "a"}
	for _, m := range history {
		req.Messages = append(req.Messages, Message{Role: m.Role, Content: m.Content})
	}
	original := req.Messages[1].Content
	m, e := PrepareContext(context.Background(), r, req, history, 7, []string{"e1"})
	if e != nil {
		t.Fatal(e)
	}
	if m.Summary == "" || len(m.SummarySourceIDs) != 4 || m.SummarySourceIDs[1] != "m2" || m.ContextRevision != 7 {
		t.Fatalf("missing summary provenance: %+v", m)
	}
	if req.Messages[1].Content != original || m.Messages[len(m.Messages)-1].Content != "continue" {
		t.Fatal("history changed or current round lost")
	}
}
func TestContextBudgetRejectsOversizedCurrentRound(t *testing.T) {
	r := &contextTestRunner{budget: 4096}
	history := []ContextMessage{{"m1", "user", strings.Repeat("x", 5000)}}
	req := Request{ModelID: "test", Messages: []Message{{Role: "user", Content: history[0].Content}}}
	if _, e := PrepareContext(context.Background(), r, req, history, 1, nil); e == nil {
		t.Fatal("silently accepted over-budget input")
	}
	if r.calls != 0 {
		t.Fatal("called provider for oversized current round")
	}
}
