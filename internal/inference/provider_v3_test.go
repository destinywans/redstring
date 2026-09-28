package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
)

func responseFrame(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func TestResponsesStreamToolRoundTrip(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["store"] != false || body["stream"] != true {
			t.Errorf("Responses request must be stateless and streamed: %#v", body)
		}
		if calls.Add(1) == 1 {
			if len(body["tools"].([]any)) != 1 {
				t.Error("missing Responses tool declaration")
			}
			responseFrame(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_health"}})
			responseFrame(w, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": "{\"query\":"})
			responseFrame(w, map[string]any{"type": "response.function_call_arguments.done", "output_index": 0, "name": "read_health", "arguments": "{\"query\":\"status\"}"})
			responseFrame(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_health", "arguments": "{\"query\":\"status\"}"}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4}}})
		} else {
			encoded, _ := json.Marshal(body["input"])
			if !strings.Contains(string(encoded), `"function_call_output"`) || !strings.Contains(string(encoded), `"call_1"`) || !strings.Contains(string(encoded), "healthy") {
				t.Errorf("missing tool result in follow-up: %s", encoded)
			}
			responseFrame(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant"}})
			responseFrame(w, map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "Healthy"})
			responseFrame(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "id": "msg_1", "content": []any{map[string]any{"type": "output_text", "text": "Healthy"}}}}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 5}}})
		}
	}))
	defer s.Close()
	r, err := New(Config{Providers: []Provider{{ID: "r", Kind: "openai", API: "responses", Model: "test", BaseURL: s.URL, ReasoningKind: "openai_effort", ReasoningLevels: []string{"high"}}}, HTTPClient: s.Client(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Models()[0].API != "responses" {
		t.Fatal("catalog lost Responses API identity")
	}
	answer, err := r.Run(context.Background(), Request{ModelID: "r", Reasoning: "high", Messages: []Message{{Role: "user", Content: "Check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) { return Evidence{Content: "healthy"}, nil }}}}, func(Event) error { return nil })
	if err != nil || answer != "Healthy" || calls.Load() != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
}

func ollamaFrame(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintln(w, string(b))
}

func TestOllamaNativeStreamToolRoundTrip(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["think"] != true {
			t.Errorf("Ollama think not sent: %#v", body)
		}
		if calls.Add(1) == 1 {
			ollamaFrame(w, map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "read_health", "arguments": map[string]any{"query": "status"}}}}}, "done": false})
		} else {
			encoded, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(encoded), `"tool_name":"read_health"`) || !strings.Contains(string(encoded), "healthy") {
				t.Errorf("Ollama tool result missing: %s", encoded)
			}
			ollamaFrame(w, map[string]any{"message": map[string]any{"role": "assistant", "content": "Healthy"}, "done": false})
		}
		ollamaFrame(w, map[string]any{"done": true, "done_reason": "stop", "prompt_eval_count": 10, "eval_count": 5})
	}))
	defer s.Close()
	r, err := New(Config{Providers: []Provider{{ID: "o", Kind: "ollama", Model: "test", BaseURL: s.URL, ReasoningKind: "ollama_think", ReasoningLevels: []string{"on", "off"}}}, HTTPClient: s.Client(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := r.Run(context.Background(), Request{ModelID: "o", Reasoning: "on", Messages: []Message{{Role: "user", Content: "Check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) { return Evidence{Content: "healthy"}, nil }}}}, func(Event) error { return nil })
	if err != nil || answer != "Healthy" || calls.Load() != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
}

func TestV3ProtocolsRejectTruncatedStreams(t *testing.T) {
	for name, fn := range map[string]func(string) error{
		"responses": func(s string) error {
			return parseResponses(strings.NewReader(s), func(*model.LLMResponse, error) bool { return true })
		},
		"ollama": func(s string) error {
			return parseOllama(strings.NewReader(s), 1, func(*model.LLMResponse, error) bool { return true })
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(`{"type":"response.output_text.delta","delta":"partial"}` + "\n"); err == nil {
				t.Fatal("accepted truncated stream")
			}
		})
	}
}

func TestV3ProtocolConfigurationRejectsMismatch(t *testing.T) {
	for _, p := range []Provider{
		{ID: "m", Kind: "claude", API: "responses", Model: "m", BaseURL: "https://example.com/v1"},
		{ID: "m", Kind: "ollama", API: "ollama", Model: "m", BaseURL: "https://example.com/v1"},
		{ID: "m", Kind: "openai", API: "messages", Model: "m", BaseURL: "https://example.com/v1"},
	} {
		if _, err := New(Config{Providers: []Provider{p}}); err == nil {
			t.Errorf("accepted invalid provider: %#v", p)
		}
	}
}
