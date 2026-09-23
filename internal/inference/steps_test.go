package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Steps: model(tool call) -> tool (linked evidence) -> model(answer), each
// emitted running then finished, with provider usage on model steps.
func TestStepsRecordToolCallsEvidenceAndUsage(t *testing.T) {
	var calls atomic.Int32
	var sawUsageOption atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if opts, ok := body["stream_options"].(map[string]any); ok && opts["include_usage"] == true {
			sawUsageOption.Store(true)
		}
		if calls.Add(1) == 1 {
			openChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "function": map[string]any{"name": "read_health", "arguments": `{"query":"5xx rate"}`}}}}, "tool_calls")
			sse(w, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 9}})
		} else {
			openChunk(w, map[string]any{"content": "done"}, "stop")
			sse(w, map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": 180, "completion_tokens": 4}})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	var steps []Step
	evidenceStep := ""
	_, err := runtimeFor(t, s, "openai").Run(context.Background(), Request{AttemptID: "a1", ModelID: "test", Messages: []Message{{Role: "user", Content: "check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) {
		return Evidence{Title: "5xx 0.4%", Content: "ok"}, nil
	}}}}, func(e Event) error {
		if e.Step != nil {
			steps = append(steps, *e.Step)
		}
		if e.Evidence != nil {
			evidenceStep = e.StepID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawUsageOption.Load() {
		t.Error("OpenAI request did not ask for stream usage")
	}
	if len(steps) != 6 {
		t.Fatalf("want 3 steps x (running, finished), got %d: %+v", len(steps), steps)
	}
	model1, tool, model2 := steps[1], steps[3], steps[5]
	if model1.Kind != "model" || model1.Status != "succeeded" || len(model1.ToolCalls) != 1 || model1.ToolCalls[0] != "read_health" || model1.Usage == nil || model1.Usage.InputTokens != 120 {
		t.Errorf("first model step = %+v", model1)
	}
	if tool.Kind != "tool" || tool.Name != "read_health" || tool.Input != "5xx rate" || tool.Output != "5xx 0.4%" || tool.Status != "succeeded" || tool.FinishedAt == nil {
		t.Errorf("tool step = %+v", tool)
	}
	if evidenceStep != tool.ID || tool.ID != "a1-2" {
		t.Errorf("evidence linked to %q, tool step %q", evidenceStep, tool.ID)
	}
	if model2.Output != "done" || model2.Usage == nil || model2.Usage.OutputTokens != 4 {
		t.Errorf("final model step = %+v", model2)
	}
}

func TestClaudeUsageFromMessageStartAndDelta(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 42, "output_tokens": 1}}})
		sse(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		sse(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "hi"}})
		sse(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 7}})
		sse(w, map[string]any{"type": "message_stop"})
	}))
	defer s.Close()
	var last *Step
	_, err := runtimeFor(t, s, "claude").Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, func(e Event) error {
		if e.Step != nil {
			last = e.Step
		}
		return nil
	})
	if err != nil || last == nil || last.Usage == nil || last.Usage.InputTokens != 42 || last.Usage.OutputTokens != 7 {
		t.Fatalf("err=%v step=%+v", err, last)
	}
}
