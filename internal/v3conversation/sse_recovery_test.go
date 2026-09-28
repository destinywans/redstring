package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

func TestSSECursorStaysWithOriginalReplyAfterNewTurn(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		ALTER TABLE v3_conversations ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT 'u';
		UPDATE v3_conversations SET active_reply_id=NULL WHERE id='c';
		UPDATE v3_conversation_replies SET state='completed' WHERE id='r';
		UPDATE v3_conversation_messages SET state='completed' WHERE id='a';
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state) VALUES
			('u2','c','user','Owner','[]','completed'),
			('a2','c','assistant','AI','[]','completed');
		INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,state)
			VALUES('r2','c','u2','a2','completed');
		INSERT INTO v3_conversations(id,owner_id,environment_id) VALUES('other','persisted-owner','test');
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state) VALUES
			('other-user','other','user','Owner','[]','completed'),
			('other-assistant','other','assistant','AI','[]','completed');
		INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,state)
			VALUES('other-reply','other','other-user','other-assistant','completed');
		INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES
			('c','r','{"type":"message.started","message":{"id":"a"}}'),
			('c','r','{"type":"part.added","message_id":"a"}'),
			('c','r','{"type":"message.completed","message_id":"a"}'),
			('c','r2','{"type":"message.started","message":{"id":"a2"}}'),
			('c','r2','{"type":"message.completed","message_id":"a2"}'),
			('other','other-reply','{"type":"message.completed","message_id":"other-assistant"}');`)
	if err != nil {
		t.Fatal(err)
	}
	var aCursor, foreignCursor int64
	if err := db.QueryRow(ctx, `SELECT seq FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='part.added'`).Scan(&aCursor); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT seq FROM v3_conversation_events WHERE reply_id='other-reply'`).Scan(&foreignCursor); err != nil {
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
	})
	open := func(cursor string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/c/events", nil)
		req.SetPathValue("id", "c")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "valid"})
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		w := httptest.NewRecorder()
		svc.events(w, req)
		return w
	}
	resume := open(strconv.FormatInt(aCursor, 10))
	if resume.Code != http.StatusOK || !strings.Contains(resume.Body.String(), `"message_id": "a"`) || strings.Contains(resume.Body.String(), `"message_id": "a2"`) {
		t.Fatalf("A cursor must replay A terminal after B exists: %d %s", resume.Code, resume.Body.String())
	}
	latest := open("")
	if latest.Code != http.StatusOK || !strings.Contains(latest.Body.String(), `"message_id": "a2"`) || strings.Contains(latest.Body.String(), `"message_id": "a"`) {
		t.Fatalf("no cursor must select latest B: %d %s", latest.Code, latest.Body.String())
	}
	for _, cursor := range []string{strconv.FormatInt(foreignCursor, 10), strconv.FormatInt(foreignCursor+100, 10)} {
		invalid := open(cursor)
		if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "invalid_event_cursor") {
			t.Fatalf("unknown or cross-conversation cursor %s: %d %s", cursor, invalid.Code, invalid.Body.String())
		}
	}
}

func TestRecoveryBeforeStartedPublishesRenderableFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		revoked bool
	}{
		{name: "expired before worker started"},
		{name: "revoked after partial output", revoked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := isolatedConversationDB(t)
			ctx := context.Background()
			if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies
				SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r'`); err != nil {
				t.Fatal(err)
			}
			if tc.revoked {
				if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
					VALUES('c','r','{"type":"text.delta","message_id":"a","delta":"private output"}')`); err != nil {
					t.Fatal(err)
				}
			}
			svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error {
				if tc.revoked {
					return ErrForbidden
				}
				return nil
			}})
			if err := svc.recoverOne(ctx, "c", "r"); err != nil {
				t.Fatal(err)
			}
			rows, err := db.Query(ctx, `SELECT payload FROM v3_conversation_events WHERE reply_id='r' ORDER BY seq`)
			if err != nil {
				t.Fatal(err)
			}
			var types []string
			for rows.Next() {
				var raw []byte
				if err := rows.Scan(&raw); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "private output") {
					rows.Close()
					t.Fatal("revoked model output remained replayable")
				}
				var event struct {
					Type    string `json:"type"`
					Message struct {
						ID        string    `json:"id"`
						CreatedAt time.Time `json:"created_at"`
						Parts     []any     `json:"parts"`
					} `json:"message"`
				}
				if err := json.Unmarshal(raw, &event); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				if event.Type == "message.started" && (event.Message.ID != "a" || event.Message.CreatedAt.IsZero() || event.Message.Parts == nil || len(event.Message.Parts) != 0) {
					rows.Close()
					t.Fatalf("unrenderable start: %s", raw)
				}
				types = append(types, event.Type)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
			if len(types) != 3 || types[0] != "message.started" || types[1] != "part.added" || types[2] != "message.completed" {
				t.Fatalf("failure stream must be renderable: %v", types)
			}
			var state, parts string
			if err := db.QueryRow(ctx, `SELECT state,parts::text FROM v3_conversation_messages WHERE id='a'`).Scan(&state, &parts); err != nil || state != "failed" || (tc.revoked && strings.Contains(parts, "private output")) {
				t.Fatalf("saved assistant after recovery state=%s parts=%s err=%v", state, parts, err)
			}
		})
	}
}
