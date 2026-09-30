package v3conversation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
)

// sseFlushRecorder runs a database change at the precise boundary after a
// replay query has closed and before the handler checks the reply state.
type sseFlushRecorder struct {
	*httptest.ResponseRecorder
	flushes         int
	afterEmptyBatch func()
}

func (w *sseFlushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.flushes++
	if w.flushes == 2 && w.afterEmptyBatch != nil {
		w.afterEmptyBatch()
	}
}

func sseRegressionService(t *testing.T) (*Service, func(string) *http.Request) {
	t.Helper()
	db := isolatedConversationDB(t)
	if _, err := db.Exec(context.Background(), `ALTER TABLE v3_conversations ADD COLUMN default_model_id text NOT NULL DEFAULT ''`); err != nil {
		t.Fatal(err)
	}
	svc := New(db, nil, Options{
		CurrentUser: func(*http.Request) core.User { return core.User{ID: "persisted-owner"} },
		RefreshSession: func(context.Context, string) (core.User, error) {
			return core.User{ID: "persisted-owner"}, nil
		},
		AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
			return []string{"test"}, nil
		},
		AuthorizeBackgroundTurn: func(context.Context, string, string) error { return nil },
	})
	request := func(cursor string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/c/events", nil)
		req.SetPathValue("id", "c")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "test-session"})
		req.Header.Set("Last-Event-ID", cursor)
		return req
	}
	return svc, request
}

func TestSSECompletedBetweenEmptyBatchAndStateReadReplaysEveryEvent(t *testing.T) {
	svc, request := sseRegressionService(t)
	db := svc.db
	ctx := context.Background()
	var cursor int64
	if err := db.QueryRow(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"message.started","message":{"id":"a"}}') RETURNING seq`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	w := &sseFlushRecorder{ResponseRecorder: httptest.NewRecorder()}
	w.afterEmptyBatch = func() {
		_, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES
			('c','r','{"type":"text.delta","message_id":"a","delta":"first"}'),
			('c','r','{"type":"text.delta","message_id":"a","delta":"second"}'),
			('c','r','{"type":"message.completed","message_id":"a"}');
			UPDATE v3_conversation_replies SET state='completed' WHERE id='r'`)
		if err != nil {
			t.Fatal(err)
		}
	}
	svc.events(w, request(strconv.FormatInt(cursor, 10)))
	if w.Code != http.StatusOK || w.flushes < 2 {
		t.Fatalf("stream status=%d flushes=%d", w.Code, w.flushes)
	}
	body := w.Body.String()
	for _, fragment := range []string{`"delta": "first"`, `"delta": "second"`, `"type": "message.completed"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("completion committed after empty replay batch lost %s: %s", fragment, body)
		}
	}
}

func TestSSEReconnectAtTerminalCursorDoesNotEmitTrailingEvent(t *testing.T) {
	svc, request := sseRegressionService(t)
	db := svc.db
	ctx := context.Background()
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='completed' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	var terminal int64
	if err := db.QueryRow(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"message.completed","message_id":"a"}') RETURNING seq`).Scan(&terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"text.delta","message_id":"a","delta":"after-terminal-private"}')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	svc.events(w, request(strconv.FormatInt(terminal, 10)))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "after-terminal-private") || strings.Contains(w.Body.String(), "data: ") {
		t.Fatalf("terminal cursor replayed trailing event: status=%d body=%s", w.Code, w.Body.String())
	}
}
