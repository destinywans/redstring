package inference

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// PA-45: evidence gathered by earlier tool calls must not fail the turn when
// the next model call would exceed the input budget. The request carries a
// trimmed preview and a note; the stored evidence is complete.
func TestEvidenceOverInputBudgetIsTrimmedNotFatal(t *testing.T) {
	var calls atomic.Int32
	var lastBody atomic.Value
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		lastBody.Store(string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		if n := calls.Add(1); n <= 3 {
			openChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call%d", n),
				"function": map[string]any{"name": "read_logs", "arguments": fmt.Sprintf("{\"query\":\"recent-%d\"}", n)}}}}, "tool_calls")
		} else {
			openChunk(w, map[string]any{"content": "Answered from trimmed evidence"}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	runtime, err := New(Config{Providers: []Provider{{ID: "test", Kind: "openai", Model: "test-model", BaseURL: s.URL,
		ContextWindow: 8192, InputBudget: 4096}}, HTTPClient: s.Client(), Timeout: 10 * time.Second, MaxCalls: 5})
	if err != nil {
		t.Fatal(err)
	}
	var stored []string
	reads := 0
	answer, err := runtime.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "why is checkout failing"}},
		Tools: []EvidenceTool{{Name: "read_logs", Description: "Read masked logs", Read: func(context.Context, string) (Evidence, error) {
			reads++
			return Evidence{Kind: "logs", Title: fmt.Sprintf("logs %d", reads), Source: "loki",
				Content: fmt.Sprintf("sample %d ", reads) + strings.Repeat("timeout talking to payments; ", 60)}, nil
		}}}}, func(e Event) error {
		if e.Evidence != nil {
			stored = append(stored, e.Evidence.Content)
		}
		return nil
	})
	if err != nil || answer != "Answered from trimmed evidence" || calls.Load() != 4 || len(stored) != 3 {
		t.Fatalf("answer=%q err=%v calls=%d stored=%d", answer, err, calls.Load(), len(stored))
	}
	body := lastBody.Load().(string)
	if !strings.Contains(body, "Evidence trimmed to fit the model input budget") || !strings.Contains(body, "logs 1") ||
		len(body) > 4096 {
		t.Fatalf("final request (%d bytes) was not trimmed with a note: %s", len(body), body)
	}
	for _, content := range stored {
		if len(content) < 1500 {
			t.Fatal("stored evidence was trimmed too")
		}
	}
}

func TestFitEvidenceCutsOldestFirstAndLeavesErrors(t *testing.T) {
	result := func(id, content string) *genai.Part {
		return &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: id, Name: "read", Response: map[string]any{
			"kind": "logs", "title": id, "source": "loki", "content": content}}}
	}
	failed := &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "e", Name: "read", Response: map[string]any{"error": "unavailable"}}}
	req := &model.LLMRequest{Contents: []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "question"}}},
		{Role: "user", Parts: []*genai.Part{result("old", strings.Repeat("a", 3000)), failed}},
		{Role: "user", Parts: []*genai.Part{result("new", strings.Repeat("b", 3000))}},
	}}
	size := func(r *model.LLMRequest) int { b, _ := json.Marshal(r.Contents); return len(b) }
	fitted, trimmed, err := fitEvidence(req, func(r *model.LLMRequest) (bool, error) { return size(r) < 5000, nil })
	if err != nil || fitted == nil || trimmed != 1 {
		t.Fatalf("fit: %v %d %v", fitted, trimmed, err)
	}
	old, newest := fitted.Contents[1].Parts[0].FunctionResponse.Response, fitted.Contents[2].Parts[0].FunctionResponse.Response
	if old["trimmed"] == nil || len(old["content"].(string)) > evidencePreviewBytes+3 || newest["trimmed"] != nil ||
		fitted.Contents[1].Parts[1].FunctionResponse.Response["error"] != "unavailable" {
		t.Fatalf("cut order: old=%v newest trimmed=%v", old["trimmed"], newest["trimmed"])
	}
	if req.Contents[1].Parts[0].FunctionResponse.Response["trimmed"] != nil {
		t.Fatal("the caller's request was modified")
	}
	// Titles only, then nothing more to cut.
	if fitted, _, _ = fitEvidence(req, func(r *model.LLMRequest) (bool, error) { return size(r) < 900, nil }); fitted == nil ||
		fitted.Contents[2].Parts[0].FunctionResponse.Response["content"] != nil {
		t.Fatal("evidence was not reduced to titles")
	}
	if fitted, _, _ = fitEvidence(req, func(*model.LLMRequest) (bool, error) { return false, nil }); fitted != nil {
		t.Fatal("an impossible budget was reported as fitting")
	}
	if got := previewText("中文日志内容", 4); got != "中…" {
		t.Fatalf("preview must cut on a rune boundary: %q", got)
	}
}

// PA-46: an answer cut at the output limit is returned with
// ErrOutputTruncated instead of being thrown away.
func TestOutputLimitKeepsTheAnswer(t *testing.T) {
	serve := func(write func(w http.ResponseWriter)) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			write(w)
		}))
	}
	for name, tc := range map[string]struct {
		kind, api string
		write     func(http.ResponseWriter)
		want      error
	}{
		"openai length": {"openai", "", func(w http.ResponseWriter) {
			openChunk(w, map[string]any{"content": "The root cause is the payments "}, nil)
			openChunk(w, map[string]any{"content": "timeout and"}, "length")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}, ErrOutputTruncated},
		"openai length while calling a tool": {"openai", "", func(w http.ResponseWriter) {
			openChunk(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c", "function": map[string]any{"name": "read", "arguments": "{\"query\":"}}}}, "length")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}, nil},
		"claude max_tokens": {"claude", "messages", func(w http.ResponseWriter) {
			sse(w, map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 10}}})
			sse(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			sse(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "The root cause is the payments timeout and"}})
			sse(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "max_tokens"}, "usage": map[string]any{"output_tokens": 4096}})
			sse(w, map[string]any{"type": "message_stop"})
		}, ErrOutputTruncated},
	} {
		t.Run(name, func(t *testing.T) {
			s := serve(tc.write)
			defer s.Close()
			provider := Provider{ID: "test", Kind: tc.kind, Model: "test-model", BaseURL: s.URL, API: tc.api}
			runtime, err := New(Config{Providers: []Provider{provider}, HTTPClient: s.Client(), Timeout: 10 * time.Second, MaxCalls: 2})
			if err != nil {
				t.Fatal(err)
			}
			answer, err := runtime.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "why"}},
				Tools: []EvidenceTool{{Name: "read", Description: "read", Read: func(context.Context, string) (Evidence, error) { return Evidence{}, nil }}}},
				func(Event) error { return nil })
			if tc.want != nil {
				if !errors.Is(err, tc.want) || answer != "The root cause is the payments timeout and" {
					t.Fatalf("answer=%q err=%v", answer, err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrOutputTruncated) {
				t.Fatalf("a cut tool call was accepted: answer=%q err=%v", answer, err)
			}
		})
	}
}

func TestParsersMarkOutputLimit(t *testing.T) {
	final := func(parse func(func(*model.LLMResponse, error) bool) error) (*model.LLMResponse, error) {
		var last *model.LLMResponse
		err := parse(func(r *model.LLMResponse, e error) bool {
			if r != nil && !r.Partial {
				last = r
			}
			return true
		})
		return last, err
	}
	ollama := `{"message":{"content":"partial answer"},"done":false}` + "\n" + `{"done":true,"done_reason":"length","prompt_eval_count":5,"eval_count":7}` + "\n"
	if r, err := final(func(y func(*model.LLMResponse, error) bool) error {
		return parseOllama(strings.NewReader(ollama), 1, y)
	}); err != nil ||
		r == nil || r.FinishReason != genai.FinishReasonMaxTokens {
		t.Fatalf("ollama: %+v %v", r, err)
	}
	responses := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"partial answer"}`,
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial answer"}]}],"usage":{"input_tokens":5,"output_tokens":7}}}`,
	}, "\n\n") + "\n\n"
	if r, err := final(func(y func(*model.LLMResponse, error) bool) error {
		return parseResponses(strings.NewReader(responses), y)
	}); err != nil ||
		r == nil || r.FinishReason != genai.FinishReasonMaxTokens {
		t.Fatalf("responses: %+v %v", r, err)
	}
	filtered := strings.Replace(responses, "max_output_tokens", "content_filter", 1)
	if _, err := final(func(y func(*model.LLMResponse, error) bool) error {
		return parseResponses(strings.NewReader(filtered), y)
	}); err == nil {
		t.Fatal("an incomplete response for another reason was accepted")
	}
}
