package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the real provider parsers, ADK tool loop and Runtime event contract.
// Text precedes the call in separate frames: filtering only call-bearing
// partial events would still leak the text and return a divergent answer.
func TestRuntimeToolRoundsKeepStreamAndAnswerIdentical(t *testing.T) {
	for _, api := range []string{"chat_completions", "messages", "responses", "ollama"} {
		t.Run(api, func(t *testing.T) {
			var calls, reads atomic.Int32
			texts := []string{"Checking health.\n", "Checking again.\n", "Service is healthy."}
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				i := int(calls.Add(1)) - 1
				if i >= len(texts) {
					t.Error("unexpected extra model call")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if i > 0 {
					b, _ := json.Marshal(body)
					if reads.Load() != int32(i) || !strings.Contains(string(b), "TOOL_RESULT_NOT_ASSISTANT_TEXT") {
						t.Errorf("next call omitted persisted tool output: %s", b)
					}
				}
				writeToolRound(w, api, texts[i], i < 2, i)
			}))
			defer s.Close()
			kind := "openai"
			if api == "messages" {
				kind = "claude"
			} else if api == "ollama" {
				kind = "ollama"
			}
			r, err := New(Config{Providers: []Provider{{ID: "test", Kind: kind, API: api, Model: "test-model", BaseURL: s.URL}}, HTTPClient: s.Client(), MaxCalls: 3, Timeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var streamed strings.Builder
			answer, err := r.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "Investigate"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) {
				return Evidence{Content: "TOOL_RESULT_NOT_ASSISTANT_TEXT"}, nil
			}}}}, func(e Event) error {
				if e.Type == "delta" {
					streamed.WriteString(e.Delta)
				}
				if e.Evidence != nil {
					reads.Add(1)
				}
				return nil
			})
			want := strings.Join(texts, "")
			if err != nil || answer != want || streamed.String() != want || calls.Load() != 3 || reads.Load() != 2 {
				t.Fatalf("answer=%q streamed=%q calls=%d reads=%d err=%v", answer, streamed.String(), calls.Load(), reads.Load(), err)
			}
		})
	}
}

func writeToolRound(w http.ResponseWriter, api, text string, call bool, round int) {
	w.Header().Set("Content-Type", "text/event-stream")
	id := fmt.Sprintf("call_%d", round)
	switch api {
	case "messages":
		sse(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		for _, chunk := range []string{text[:3], text[3:]} {
			sse(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": chunk}})
		}
		stop := "end_turn"
		if call {
			stop = "tool_use"
			sse(w, map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": id, "name": "read_health", "input": map[string]any{"query": "status"}}})
		}
		sse(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}})
		sse(w, map[string]any{"type": "message_stop"})
	case "responses":
		message := map[string]any{"type": "message", "id": "msg", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}
		output := []any{message}
		sse(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": message})
		// Round 1 has only accumulated final text. Round 2 streams a prefix;
		// the final event adds a suffix. Both must append exactly once.
		if round != 1 {
			chunk := text
			if round == 2 {
				chunk = text[:3]
			}
			sse(w, map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": chunk})
		}
		if call {
			output = append(output, map[string]any{"type": "function_call", "id": id, "call_id": id, "name": "read_health", "arguments": `{"query":"status"}`})
		}
		sse(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": output}})
	case "ollama":
		for _, chunk := range []string{text[:3], text[3:]} {
			ollamaFrame(w, map[string]any{"message": map[string]any{"role": "assistant", "content": chunk}, "done": false})
		}
		if call {
			ollamaFrame(w, map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "read_health", "arguments": map[string]any{"query": "status"}}}}}, "done": false})
		}
		ollamaFrame(w, map[string]any{"done": true, "done_reason": "stop"})
	default:
		for _, chunk := range []string{text[:3], text[3:]} {
			openChunk(w, map[string]any{"content": chunk}, nil)
		}
		stop := "stop"
		delta := map[string]any{}
		if call {
			stop = "tool_calls"
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": id, "function": map[string]any{"name": "read_health", "arguments": `{"query":"status"}`}}}
		}
		openChunk(w, delta, stop)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func TestRuntimeRejectsDivergentRoundFinal(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "published"})
		sse(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "rewritten"}}}}}})
	}))
	defer s.Close()
	r, err := New(Config{Providers: []Provider{{ID: "test", Kind: "openai", API: "responses", Model: "test", BaseURL: s.URL}}, HTTPClient: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	var streamed strings.Builder
	answer, err := r.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "Check"}}}, func(e Event) error {
		if e.Type == "delta" {
			streamed.WriteString(e.Delta)
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "differs from streamed") || answer != "" || streamed.String() != "published" {
		t.Fatalf("answer=%q streamed=%q err=%v", answer, streamed.String(), err)
	}
}

func TestRuntimeToolRoundTruncationIsNotSuccess(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeToolRound(w, "chat_completions", "Checking.\n", true, 0)
			return
		}
		openChunk(w, map[string]any{"content": "incomplete answer"}, nil)
	}))
	defer s.Close()
	var streamed strings.Builder
	answer, err := runtimeFor(t, s, "openai").Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "Check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) {
		return Evidence{Content: "healthy"}, nil
	}}}}, func(e Event) error {
		if e.Type == "delta" {
			streamed.WriteString(e.Delta)
		}
		return nil
	})
	if err == nil || answer != "" || streamed.String() != "Checking.\nincomplete answer" || calls.Load() != 2 {
		t.Fatalf("answer=%q streamed=%q calls=%d err=%v", answer, streamed.String(), calls.Load(), err)
	}
}

func TestRuntimeFinalSuffixPersistenceFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeToolRound(w, "responses", "Final-only answer", false, 1)
	}))
	defer s.Close()
	r, err := New(Config{Providers: []Provider{{ID: "test", Kind: "openai", API: "responses", Model: "test", BaseURL: s.URL}}, HTTPClient: s.Client()})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("commit failed")
	answer, err := r.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "Check"}}}, func(e Event) error {
		if e.Type == "delta" {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) || answer != "" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}
