package v3canvas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/inference"
)

// PA-77 through the real inference runtime: the model says something in
// English before its tool call, then answers in Chinese. The stored answer,
// the node summary and the replies route hold only the answer after the tool
// call, and the model was asked for Chinese.
func TestInvestigationAnswerExcludesWordsBeforeToolCalls(t *testing.T) {
	_, st := suggestionDB(t)
	const preamble = "I'll investigate this systematically, starting with independent observations.\n"
	const final = "修订 8 上线后错误率从 0.2% 升到 48%，建议回滚到修订 7。"
	var mu sync.Mutex
	var bodies []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		raw, _ := json.Marshal(body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		round := len(bodies) - 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta any, finish any) {
			data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		if round == 0 {
			chunk(map[string]any{"content": preamble}, nil)
			chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_0",
				"function": map[string]any{"name": "read_service_metrics", "arguments": `{"query":"service-key"}`}}}}, "tool_calls")
		} else {
			chunk(map[string]any{"content": final}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "evidence-model", Kind: "openai",
		API: "chat_completions", Model: "test-model", BaseURL: provider.URL}}, HTTPClient: provider.Client(), MaxCalls: 8, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	service := base64.RawURLEncoding.EncodeToString([]byte("app\x00test"))
	svc := NewWithRuntime(st, runtime)
	svc.InvestigationToolsForTurn = func(context.Context, string, string, string, string) ([]inference.EvidenceTool, error) {
		return []inference.EvidenceTool{{Name: "read_service_metrics", Description: "metrics",
			Read: func(context.Context, string) (inference.Evidence, error) {
				return inference.Evidence{Kind: "metrics", Title: "Prometheus 样本：service-key / error_rate", Content: `{"status":"observed"}`,
					Source: "prometheus", TargetID: service}, nil
			}}}, nil
	}
	api := suggestionAPI(t, st, svc)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "test")
		r.Header.Set("Accept-Language", "zh-CN")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "exact-cookie"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/api/v1/investigations/inv/nodes/branch/messages", `{"text":"是发布引起的吗？","model":"evidence-model"}`)
	var reply struct {
		Summary string `json:"summary"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.Summary != final {
		t.Fatalf("reply: %d %s", w.Code, w.Body.String())
	}
	if len(bodies) != 2 || !strings.Contains(bodies[0], "Write every visible word in Simplified Chinese") {
		t.Fatalf("the model was not asked for Chinese (%d calls)", len(bodies))
	}
	var replies struct {
		Replies []investigationReply `json:"replies"`
	}
	if w := call("GET", "/api/v1/investigations/inv/nodes/branch/replies", ""); w.Code != 200 ||
		json.Unmarshal(w.Body.Bytes(), &replies) != nil || len(replies.Replies) != 1 || replies.Replies[0].Answer == nil ||
		*replies.Replies[0].Answer != final || len(replies.Replies[0].EvidenceIDs) != 1 {
		t.Fatalf("replies: %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/v1/investigations/inv/canvas", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "I'll investigate") || !strings.Contains(w.Body.String(), `"summary":"`+final+`"`) {
		t.Fatalf("canvas: %d %s", w.Code, w.Body.String())
	}
}
