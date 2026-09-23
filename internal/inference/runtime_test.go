package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
)

func sse(w http.ResponseWriter, v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
func openChunk(w http.ResponseWriter, delta any, finish any) {
	sse(w, map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
}
func runtimeFor(t *testing.T, s *httptest.Server, kind string) *Runtime {
	t.Helper()
	r, e := New(Config{Providers: []Provider{{ID: "test", Kind: kind, Model: "test-model", BaseURL: s.URL}}, HTTPClient: s.Client(), Timeout: 10 * time.Second, MaxCalls: 3})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestADKToolPersistsBeforeNextModelCallAndFrozenHistory(t *testing.T) {
	var calls atomic.Int32
	var persisted atomic.Bool
	var observedHistory atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var input map[string]any
		json.NewDecoder(r.Body).Decode(&input)
		b, _ := json.Marshal(input)
		if strings.Contains(string(b), "previous symptom") {
			observedHistory.Store(true)
		}
		if calls.Add(1) == 1 {
			if !strings.Contains(string(b), "read_health") {
				t.Error("missing tool declaration")
			}
			openChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call1", "function": map[string]any{"name": "read_health", "arguments": "{\"query\":\"status\"}"}}}}, "tool_calls")
		} else {
			if !persisted.Load() {
				t.Error("tool result reached model before durable callback")
			}
			if !strings.Contains(string(b), "service unhealthy") {
				t.Error("missing result")
			}
			openChunk(w, map[string]any{"content": "Investigated"}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	rr := runtimeFor(t, s, "openai")
	events := 0
	answer, err := rr.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "previous symptom"}, {Role: "assistant", Content: "prior answer"}, {Role: "user", Content: "Investigate now"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "Read registered app health", Read: func(context.Context, string) (Evidence, error) {
		return Evidence{Kind: "health", Content: "service unhealthy", Source: "registered-app"}, nil
	}}}}, func(e Event) error {
		events++
		if e.Evidence != nil {
			persisted.Store(true)
		}
		return nil
	})
	if err != nil || answer != "Investigated" || calls.Load() != 2 || !observedHistory.Load() || events < 2 {
		t.Fatalf("answer=%q err=%v calls=%d events=%d", answer, err, calls.Load(), events)
	}
}
func TestEvidencePersistenceFailureStopsModel(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		openChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call1", "function": map[string]any{"name": "read_health", "arguments": "{\"query\":\"status\"}"}}}}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	sentinel := errors.New("commit lost lease")
	_, err := runtimeFor(t, s, "openai").Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "Check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) { return Evidence{Content: "observed"}, nil }}}}, func(e Event) error {
		if e.Evidence != nil {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
func TestClaudeToolStreamingRoundTrip(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" || r.Header.Get("anthropic-version") == "" {
			t.Error("Claude protocol missing")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		if calls.Add(1) == 1 {
			sse(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "tool1", "name": "read_health", "input": map[string]any{}}})
			sse(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": "{\"query\":\"status\"}"}})
			sse(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}})
		} else {
			if !strings.Contains(string(b), "tool_result") {
				t.Error("tool result omitted")
			}
			sse(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			sse(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Recovered evidence"}})
			sse(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}})
		}
		sse(w, map[string]any{"type": "message_stop"})
	}))
	defer s.Close()
	answer, err := runtimeFor(t, s, "claude").Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "Check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) { return Evidence{Content: "healthy"}, nil }}}}, func(Event) error { return nil })
	if err != nil || answer != "Recovered evidence" || calls.Load() != 2 {
		t.Fatalf("answer=%q err=%v calls=%d", answer, err, calls.Load())
	}
}
func TestTruncatedStreamIsNotSuccess(t *testing.T) {
	err := parseOpenAI(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"), func(*model.LLMResponse, error) bool { return true })
	if err == nil {
		t.Fatal("accepted incomplete stream")
	}
}
func TestDeltaPersistenceFailureReturnsOriginalError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openChunk(w, map[string]any{"content": "hello"}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	sentinel := errors.New("database down")
	_, err := runtimeFor(t, s, "openai").Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "hi"}}}, func(Event) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v", err)
	}
}
func TestEmbeddingReordersAndRejectsMissing(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"index":1,"embedding":[3,4]},{"index":0,"embedding":[1,2]}]}`)
	}))
	defer s.Close()
	e, _ := NewEmbedder(EmbeddingConfig{Provider: "openai", Model: "embedding-test", BaseURL: s.URL})
	v, err := e.Embed(context.Background(), []string{"a", "b"})
	if err != nil || v[0][0] != 1 || v[1][0] != 3 {
		t.Fatalf("%v %v", v, err)
	}
	if _, err = e.Embed(context.Background(), []string{"a", "b", "c"}); err == nil {
		t.Fatal("accepted missing vector")
	}
}
func TestOllamaRealSmoke(t *testing.T) {
	if os.Getenv("AIOPS_REAL_OLLAMA") != "1" {
		t.Skip("explicit opt-in; invokes local model")
	}
	r, err := New(Config{Providers: []Provider{{ID: "ollama:qwen3.6:latest", Kind: "ollama", Model: "qwen3.6:latest", BaseURL: "http://localhost:11434/v1"}}, Timeout: 4 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	answer, err := r.Run(context.Background(), Request{ModelID: "ollama:qwen3.6:latest", Messages: []Message{{Role: "user", Content: "Reply only with READY."}}}, func(e Event) error { events++; return nil })
	if err != nil || answer == "" || events == 0 {
		t.Fatalf("answer=%q events=%d err=%v", answer, events, err)
	}
	t.Logf("real Ollama response %q; %d stream events", answer, events)
	e, _ := NewEmbedder(EmbeddingConfig{Provider: "ollama", Model: "bge-m3:latest", BaseURL: "http://localhost:11434"})
	vectors, err := e.Embed(context.Background(), []string{"Deployment rollback runbook"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real bge-m3 embedding dimension %d", len(vectors[0]))
}

func TestRunCancellationClosesProviderRequest(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runtimeFor(t, s, "openai").Run(ctx, Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "wait"}}}, func(Event) error { return nil })
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime failed to cancel")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("provider request not canceled")
	}
}
func TestAttemptsDoNotShareHistory(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v any
		json.NewDecoder(r.Body).Decode(&v)
		b, _ := json.Marshal(v)
		if n.Add(1) == 2 && strings.Contains(string(b), "SECRET_FIRST_BRANCH") {
			t.Error("branch history leaked")
		}
		openChunk(w, map[string]any{"content": "answer"}, "stop")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	r := runtimeFor(t, s, "openai")
	for _, q := range []string{"SECRET_FIRST_BRANCH", "second branch"} {
		_, err := r.Run(context.Background(), Request{ModelID: "test", AttemptID: "same-id", Messages: []Message{{Role: "user", Content: q}}}, func(Event) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestModelCallBudgetStopsRepeatedTools(t *testing.T) {
	var n atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		openChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprint("call", i), "function": map[string]any{"name": "read_health", "arguments": "{\"query\":\"status\"}"}}}}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	r := runtimeFor(t, s, "openai")
	_, err := r.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "check"}}, Tools: []EvidenceTool{{Name: "read_health", Description: "health", Read: func(context.Context, string) (Evidence, error) { return Evidence{Content: "healthy"}, nil }}}}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "budget") || n.Load() != 3 {
		t.Fatalf("calls=%d err=%v", n.Load(), err)
	}
}

func TestOpenAIFragmentedToolsKeepIdentityAndArguments(t *testing.T) {
	var stream strings.Builder
	write := func(v any) { b, _ := json.Marshal(v); fmt.Fprintf(&stream, "data: %s\n\n", b) }
	write(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "abc", "function": map[string]any{"name": "read_health", "arguments": "{\"query\":"}}}}}}})
	write(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "\"status\"}"}}}}, "finish_reason": "tool_calls"}}})
	stream.WriteString("data: [DONE]\n\n")
	var final *model.LLMResponse
	err := parseOpenAI(strings.NewReader(stream.String()), func(r *model.LLMResponse, _ error) bool {
		if !r.Partial {
			final = r
		}
		return true
	})
	if err != nil || final == nil {
		t.Fatalf("%v", err)
	}
	call := final.Content.Parts[0].FunctionCall
	if call.ID != "abc" || call.Name != "read_health" || call.Args["query"] != "status" {
		t.Fatalf("bad call: %+v", call)
	}
}
