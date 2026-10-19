package v3agents

import (
	"context"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/inference"
)

type evalControlledRunner struct {
	calls int
	run   func(context.Context, inference.Request, func(inference.Event) error) (string, error)
}

func (r *evalControlledRunner) Models() []inference.Model {
	return []inference.Model{{ID: "eval-model", Name: "Controlled eval"}}
}
func (r *evalControlledRunner) Run(ctx context.Context, in inference.Request, emit func(inference.Event) error) (string, error) {
	r.calls++
	return r.run(ctx, in, emit)
}
func evalInputFixture() EvalInput {
	return EvalInput{AgentID: "agent", Title: "Human failure case", Input: "Please analyze this supplied case", Queue: "Human supplied cases", Alert: EvalCaseAlert{Sev: "P3", Title: "Human-supplied alert", Labels: []EvalCaseLabel{}}, Evidence: []EvalCaseEvidence{{Brand: "manual", Title: "Operator note", Sub: "Human supplied evidence, not a live read"}}, Expected: EvalCaseExpected{Cause: "healthy", Advice: "restart"}, Checks: []string{"healthy", "restart"}}
}
func TestEvalInputAndLiteralCheckBoundaries(t *testing.T) {
	in := evalInputFixture()
	if !validEvalInput(in) {
		t.Fatal("valid input rejected")
	}
	for _, mutate := range []func(*EvalInput){func(v *EvalInput) { v.Input = strings.Repeat("x", 8193) }, func(v *EvalInput) { v.Input = "a\x00b" }, func(v *EvalInput) { v.Checks = []string{"same", "same"} }, func(v *EvalInput) { v.Evidence = nil }, func(v *EvalInput) { v.Alert.Sev = "unknown" }, func(v *EvalInput) { v.Expected.Cause = "" }} {
		v := evalInputFixture()
		mutate(&v)
		if validEvalInput(v) {
			t.Fatalf("invalid accepted: %#v", v)
		}
	}
	checks, score := evalCheckResults("Healthy; restart", []string{"healthy", "restart"})
	if checks[0] || !checks[1] || score != .5 {
		t.Fatalf("literal semantics %v %f", checks, score)
	}
	if !evalReportText("\nActual provider answer\n", 100) || evalReportText("\x00", 10) {
		t.Fatal("provider text preservation")
	}
}
func TestEvalRequiresActualToolFreeModelConfiguration(t *testing.T) {
	r := &evalControlledRunner{}
	c := ExecutionConfig{ModelID: "eval-model", PromptID: "p", PromptVersion: 1, Prompt: "System", ToolNames: []string{}, MaxModelCalls: 1, MaxToolCalls: 0, MaxInputBytes: 100, MaxOutputBytes: 100, MaxOutputTokens: 16, TimeoutSeconds: 5}
	if !evalCompatible(c, "Input", r) {
		t.Fatal("valid rejected")
	}
	c.ToolNames = []string{"read_service_profile"}
	c.MaxToolCalls = 1
	if evalCompatible(c, "Input", r) {
		t.Fatal("tool configuration evaluated as prompt-only")
	}
	c.ToolNames = []string{}
	c.ModelID = "unregistered"
	if evalCompatible(c, "Input", r) {
		t.Fatal("unknown model admitted")
	}
	c.ModelID = "eval-model"
	c.MaxInputBytes = 2
	if evalCompatible(c, "Input", r) {
		t.Fatal("input admission exceeded")
	}
}
