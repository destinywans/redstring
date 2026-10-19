package v3conversation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/jackc/pgx/v5"
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

type noticeRuntime struct {
	answer string
	err    error
}

func (*noticeRuntime) Models() []inference.Model { return nil }
func (n *noticeRuntime) Run(_ context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	if n.answer != "" {
		if err := emit(inference.Event{Type: "delta", Delta: n.answer}); err != nil {
			return "", err
		}
	}
	return n.answer, n.err
}

// PA-52: an answer given after the evidence-tool limit completes with its own
// finish reason and note; a canceled run fails with a readable reason. PA-60:
// the failure status follows the language of the starting request and has a
// stable code.
func TestEvidenceLimitReplyCompletesAndCancellationIsReadable(t *testing.T) {
	for name, tc := range map[string]struct {
		runtime                     *noticeRuntime
		state, finish, note, errStr string
		english                     bool
	}{
		"evidence limit": {&noticeRuntime{"Node lab-vm-2 is saturated.", inference.ErrEvidenceLimitReached},
			"completed", "evidence_limit", "取证次数已达上限", "", false},
		"limit and truncation": {&noticeRuntime{"Node lab-vm-2 is", errors.Join(inference.ErrOutputTruncated, inference.ErrEvidenceLimitReached)},
			"completed", "max_output_tokens", "回答达到输出长度上限", "", false},
		"canceled":            {&noticeRuntime{"", context.Canceled}, "failed", "", `"code": "reply_failed", "text": "模型回复失败"`, "reply_canceled: ", false},
		"canceled in English": {&noticeRuntime{"", context.Canceled}, "failed", "", `"code": "reply_failed", "text": "Model reply failed"`, "reply_canceled: ", true},
	} {
		t.Run(name, func(t *testing.T) {
			db := isolatedConversationDB(t)
			svc := New(db, tc.runtime, Options{
				RefreshSession:            func(context.Context, string) (core.User, error) { return core.User{ID: "persisted-owner"}, nil },
				AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
				AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
			})
			svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", tc.english)
			var state, parts, finish, replyError string
			if err := db.QueryRow(context.Background(), `SELECT m.state,m.parts::text,r.finish_reason,r.error FROM v3_conversation_messages m
				JOIN v3_conversation_replies r ON r.assistant_message_id=m.id WHERE m.id='a'`).Scan(&state, &parts, &finish, &replyError); err != nil {
				t.Fatal(err)
			}
			if state != tc.state || finish != tc.finish || !strings.Contains(parts, tc.note) || !strings.HasPrefix(replyError, tc.errStr) {
				t.Fatalf("state=%s finish=%q error=%q parts=%s", state, finish, replyError, parts)
			}
			if name == "limit and truncation" && !strings.Contains(parts, "取证次数已达上限") {
				t.Fatalf("both notes expected: %s", parts)
			}
		})
	}
}

type languageRuntime struct{ requests []inference.Request }

func (*languageRuntime) Models() []inference.Model { return nil }
func (l *languageRuntime) Run(_ context.Context, req inference.Request, emit func(inference.Event) error) (string, error) {
	l.requests = append(l.requests, req)
	if err := emit(inference.Event{Type: "delta", Delta: "答复"}); err != nil {
		return "", err
	}
	return "答复", nil
}

// PA-77: a chat turn asks the model for the request's language. The chat
// stream is append-only, so it keeps every round (no FinalRoundOnly), and it
// asks the runtime to hold back a process sentence before a tool call (PA-92).
func TestChatTurnAsksForTheRequestLanguage(t *testing.T) {
	for english, want := range map[bool]string{false: "zh", true: "en"} {
		db := isolatedConversationDB(t)
		runtime := &languageRuntime{}
		svc := New(db, runtime, Options{
			RefreshSession: func(context.Context, string) (core.User, error) {
				return core.User{ID: "persisted-owner"}, nil
			},
			AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
			AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
		})
		svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", english)
		if len(runtime.requests) != 1 || runtime.requests[0].Language != want || runtime.requests[0].FinalRoundOnly ||
			!runtime.requests[0].DropToolPreamble {
			t.Fatalf("english=%v requests=%+v", english, runtime.requests)
		}
	}
}
