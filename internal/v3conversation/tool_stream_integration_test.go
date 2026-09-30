package v3conversation

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

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
)

// Unlike a stub Runner, this crosses the real provider parser and ADK tool
// loop before persisting the reply. Every streamed byte must survive both
// completionSuffix and the context builder used by later conversation turns.
func TestRealRuntimeToolStreamCompletesAndEntersNextTurnContext(t *testing.T) {
	db := isolatedConversationDB(t)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		frame := func(delta any, finish any) {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		if calls.Add(1) == 1 {
			frame(map[string]any{"content": "I will check health.\n"}, nil)
			frame(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call1", "function": map[string]any{"name": "read_health", "arguments": `{"query":"status"}`}}}}, "tool_calls")
		} else {
			b, _ := json.Marshal(body)
			if !strings.Contains(string(b), "evidence-healthy") {
				t.Error("follow-up request missing tool result")
			}
			frame(map[string]any{"content": "The service "}, nil)
			frame(map[string]any{"content": "is healthy."}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "model", Kind: "openai", Model: "test", BaseURL: provider.URL}}, HTTPClient: provider.Client(), Timeout: 10 * time.Second, MaxCalls: 3})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(db, runtime, Options{
		RefreshSession: func(context.Context, string) (core.User, error) {
			return core.User{ID: "persisted-owner"}, nil
		},
		AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
		AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
		ToolsForTurn: func(context.Context, string, string, string) ([]inference.EvidenceTool, error) {
			return []inference.EvidenceTool{{Name: "read_health", Description: "Read health", Read: func(context.Context, string) (inference.Evidence, error) {
				return inference.Evidence{Content: "evidence-healthy", Source: "test-health", Title: "Health"}, nil
			}}}, nil
		},
	})
	svc.runTurn("c", "r", runtime.Models()[0], "", "test-session")
	ctx := context.Background()
	var replyState, messageState, saved, streamed string
	if err := db.QueryRow(ctx, `SELECT r.state,m.state,m.parts->0->>'markdown'
		FROM v3_conversation_replies r JOIN v3_conversation_messages m ON m.id=r.assistant_message_id
		WHERE r.id='r'`).Scan(&replyState, &messageState, &saved); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT coalesce(string_agg(payload->>'delta','' ORDER BY seq),'')
		FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='text.delta'`).Scan(&streamed); err != nil {
		t.Fatal(err)
	}
	want := "I will check health.\nThe service is healthy."
	if replyState != "completed" || messageState != "completed" || saved != want || streamed != want || calls.Load() != 2 {
		t.Fatalf("reply=%s message=%s saved=%q streamed=%q calls=%d", replyState, messageState, saved, streamed, calls.Load())
	}
	var completions, failures int
	if err := db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE payload->>'type'='message.completed'),
		count(*) FILTER (WHERE payload->>'type'='message.failed')
		FROM v3_conversation_events WHERE reply_id='r'`).Scan(&completions, &failures); err != nil {
		t.Fatal(err)
	}
	if completions != 1 || failures != 0 {
		t.Fatalf("completion events=%d failure events=%d", completions, failures)
	}
	msgs, err := svc.contextMessages(ctx, "c")
	if err != nil || len(msgs) != 2 || msgs[1].Role != "assistant" || msgs[1].Content != want {
		t.Fatalf("next turn context=%#v err=%v", msgs, err)
	}
}
