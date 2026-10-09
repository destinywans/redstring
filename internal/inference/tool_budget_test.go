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

func toolCallChunk(w http.ResponseWriter, calls ...[2]string) {
	list := []any{}
	for i, call := range calls {
		list = append(list, map[string]any{"index": i, "id": call[0], "function": map[string]any{"name": "read_metrics", "arguments": call[1]}})
	}
	openChunk(w, map[string]any{"tool_calls": list}, "tool_calls")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func answerChunk(w http.ResponseWriter, text string) {
	openChunk(w, map[string]any{"content": text}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// PA-52: using the evidence budget closes the tools for the next model call,
// which answers from what was gathered; nothing is canceled.
func TestEvidenceLimitAnswersInsteadOfCanceling(t *testing.T) {
	var calls atomic.Int32
	var closedBody atomic.Value
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		switch n := calls.Add(1); {
		case strings.Contains(string(raw), `"tool_choice":"none"`):
			closedBody.Store(string(raw))
			answerChunk(w, "Node lab-vm-2 is saturated; error rate was not checked because the evidence limit was reached.")
		case n == 1:
			toolCallChunk(w, [2]string{"c1", `{"query":"node cpu"}`})
		default:
			// Two calls in one response: only one read is left.
			toolCallChunk(w, [2]string{"c2", `{"query":"node memory"}`}, [2]string{"c3", `{"query":"error rate"}`})
		}
	}))
	defer s.Close()
	runtime := runtimeFor(t, s, "openai")
	reads := 0
	answer, err := runtime.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "why"}}, MaxToolCalls: 2,
		Tools: []EvidenceTool{{Name: "read_metrics", Description: "metrics", Read: func(_ context.Context, query string) (Evidence, error) {
			reads++
			return Evidence{Kind: "metrics", Title: query, Content: `{"series":[]}`}, nil
		}}}}, func(Event) error { return nil })
	body, _ := closedBody.Load().(string)
	if !errors.Is(err, ErrEvidenceLimitReached) || errors.Is(err, ErrOutputTruncated) || !strings.Contains(answer, "evidence limit") ||
		reads != 2 || calls.Load() != 3 {
		t.Fatalf("answer=%q err=%v reads=%d calls=%d", answer, err, reads, calls.Load())
	}
	if !strings.Contains(body, "evidence-tool limit for this reply has been reached") || !strings.Contains(body, ToolEvidenceLimit) {
		t.Fatalf("closing call was not told why: %s", body)
	}
}

// PA-52: an identical repeat (same tool, same input in any key order or
// spacing) is answered from memory without a read or a count, and so is a
// repeat of a deterministic failure; the last allowed model call has no tools.
func TestRepeatedCallsAreAnsweredFromMemory(t *testing.T) {
	var calls atomic.Int32
	var lastBody atomic.Value
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		lastBody.Store(string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		switch calls.Add(1) {
		case 1:
			toolCallChunk(w, [2]string{"c1", `{"query":"{\"service\":\"checkout\",\"query_id\":\"error_rate\"}"}`},
				[2]string{"c2", `{"query":"{\"service\":\"checkout\",\"query_id\":\"pod_restarts\"}"}`})
		case 2:
			toolCallChunk(w, [2]string{"c3", `{"query":"{ \"query_id\" : \"error_rate\", \"service\" : \"checkout\" }"}`},
				[2]string{"c4", `{"query":"{\"service\":\"checkout\",\"query_id\":\"pod_restarts\"}"}`})
		default:
			answerChunk(w, "done")
		}
	}))
	defer s.Close()
	r, err := New(Config{Providers: []Provider{{ID: "test", Kind: "openai", Model: "test-model", BaseURL: s.URL}}, HTTPClient: s.Client(), Timeout: 10 * time.Second, MaxCalls: 3})
	if err != nil {
		t.Fatal(err)
	}
	reads, evidence := 0, 0
	answer, err := r.Run(context.Background(), Request{ModelID: "test", Messages: []Message{{Role: "user", Content: "why"}}, MaxToolCalls: 32,
		Tools: []EvidenceTool{{Name: "read_metrics", Description: "metrics", Read: func(_ context.Context, query string) (Evidence, error) {
			reads++
			if strings.Contains(query, "pod_restarts") {
				return Evidence{}, NewToolError(ToolNotConfigured, "query_id pod_restarts is not configured")
			}
			return Evidence{Kind: "metrics", Title: "error rate", Source: "prometheus",
				Content: `{"series":[{"index":1,"samples":[{"timestamp":100,"value":"0.1"},{"timestamp":130,"value":"0.4"}]}]}`}, nil
		}}}}, func(e Event) error {
		if e.Evidence != nil {
			evidence++
		}
		return nil
	})
	body, _ := lastBody.Load().(string)
	if err != nil || answer != "done" || reads != 2 || evidence != 1 || calls.Load() != 3 {
		t.Fatalf("answer=%q err=%v reads=%d evidence=%d calls=%d", answer, err, reads, evidence, calls.Load())
	}
	if !strings.Contains(body, "repeat_of_call") || !strings.Contains(body, `last\\\":0.4`) || strings.Count(body, "is not configured") != 2 ||
		!strings.Contains(body, "This is the last model step") || !strings.Contains(body, `"tool_choice":"none"`) {
		t.Fatalf("final request: %s", body)
	}
}

func TestTrimmedMetricEvidenceIsSummarizedPerSeries(t *testing.T) {
	content := `{"service":"checkout","query_id":"node_cpu_busy_ratio","status":"observed","series":[` +
		`{"index":1,"labels":{"node":"lab-vm-1"},"samples":[{"timestamp":1791537600,"value":"0.20"},{"timestamp":1791537630,"value":"0.95"},{"timestamp":1791537660,"value":"0.90"}]},` +
		`{"index":2,"labels":{"node":"lab-vm-2"},"samples":[{"timestamp":1791537600,"value":"NaN"}]}]}` + strings.Repeat(" ", 4000)
	req := &model.LLMRequest{Contents: []*genai.Content{{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: "m", Name: "read_metrics", Response: map[string]any{"kind": "metrics", "title": "cpu", "source": "prometheus", "content": content}}}}}}}
	size := func(r *model.LLMRequest) int { b, _ := json.Marshal(r.Contents); return len(b) }
	fitted, _, err := fitEvidence(req, func(r *model.LLMRequest) (bool, error) { return size(r) < 2000, nil })
	if err != nil || fitted == nil {
		t.Fatalf("fit: %v", err)
	}
	response := fitted.Contents[0].Parts[0].FunctionResponse.Response
	raw, _ := json.Marshal(response)
	text := string(raw)
	if response["content"] != nil || !strings.Contains(text, `"first":0.2`) || !strings.Contains(text, `"max":0.95`) ||
		!strings.Contains(text, `"last":0.9`) || !strings.Contains(text, `"node":"lab-vm-2"`) || !strings.Contains(text, `"points":1`) ||
		!strings.Contains(text, "do not re-read it") {
		t.Fatalf("summary: %s", text)
	}
}
