package v3conversation

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
)

type truncatedRuntime struct{}

func (*truncatedRuntime) Models() []inference.Model { return nil }
func (*truncatedRuntime) Run(_ context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	if err := emit(inference.Event{Type: "delta", Delta: "The root cause is the payments "}); err != nil {
		return "", err
	}
	return "The root cause is the payments timeout and", inference.ErrOutputTruncated
}

// PA-46: a reply cut at the output limit keeps its answer, completes, records
// why it ended and tells the reader in the request language how to continue.
func TestTruncatedReplyKeepsTheAnswerAndCompletes(t *testing.T) {
	for _, english := range []bool{false, true} {
		db := isolatedConversationDB(t)
		svc := New(db, &truncatedRuntime{}, Options{
			RefreshSession: func(context.Context, string) (core.User, error) {
				return core.User{ID: "persisted-owner"}, nil
			},
			AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
			AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
		})
		svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", english)
		ctx := context.Background()
		var messageState, parts, replyState, finishReason, replyError string
		if err := db.QueryRow(ctx, `SELECT m.state,m.parts::text,r.state,r.finish_reason,r.error FROM v3_conversation_messages m
			JOIN v3_conversation_replies r ON r.assistant_message_id=m.id WHERE m.id='a'`).Scan(&messageState, &parts, &replyState, &finishReason, &replyError); err != nil {
			t.Fatal(err)
		}
		note := "回答达到输出长度上限，已截断"
		if english {
			note = "Reply truncated at the output limit"
		}
		if messageState != "completed" || replyState != "completed" || finishReason != "max_output_tokens" || replyError != "" ||
			!strings.Contains(parts, "The root cause is the payments timeout and") || !strings.Contains(parts, note) {
			t.Fatalf("english=%v message=%s reply=%s finish=%q error=%q parts=%s", english, messageState, replyState, finishReason, replyError, parts)
		}
		var completed string
		if err := db.QueryRow(ctx, `SELECT payload::text FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='message.completed'`).Scan(&completed); err != nil ||
			!strings.Contains(completed, `"finish_reason": "max_output_tokens"`) {
			t.Fatalf("completed event: %s %v", completed, err)
		}
	}
}
