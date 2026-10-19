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

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestSSEActiveReplayRequiresInitiatingSessionAndChangeGrant(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `ALTER TABLE v3_conversations ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT 'u'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES
			('c','r','{"type":"message.started","message":{"id":"a"}}'),
			('c','r','{"type":"text.delta","message_id":"a","delta":"prior model output"}')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at)
		VALUES($1,'persisted-owner',now()+interval '1 hour')`, store.Hash("viewer-session")); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	if err := db.QueryRow(ctx, `SELECT seq FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='message.started'`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	changeAllowed := true
	svc := New(db, nil, Options{
		CurrentUser: func(*http.Request) core.User { return core.User{ID: "persisted-owner"} },
		RefreshSession: func(_ context.Context, token string) (core.User, error) {
			if token == "viewer-session" {
				return core.User{ID: "persisted-owner"}, nil
			}
			return core.User{}, ErrForbidden
		},
		AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
			return []string{"test"}, nil // conversation.view remains granted
		},
		AuthorizeBackgroundTurn: func(context.Context, string, string) error {
			if changeAllowed {
				return nil
			}
			return ErrForbidden
		},
	})
	open := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/c/events", nil)
		req.SetPathValue("id", "c")
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "viewer-session"})
		req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
		w := httptest.NewRecorder()
		svc.events(w, req)
		return w
	}
	if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash("test-session")); err != nil {
		t.Fatal(err)
	}
	if got := open(); got.Code != http.StatusForbidden || strings.Contains(got.Body.String(), "prior model output") {
		t.Fatalf("revoked initiating session replayed active delta: %d %s", got.Code, got.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'persisted-owner',now()+interval '1 hour')`, store.Hash("test-session")); err != nil {
		t.Fatal(err)
	}
	changeAllowed = false
	if got := open(); got.Code != http.StatusForbidden || strings.Contains(got.Body.String(), "prior model output") {
		t.Fatalf("revoked change grant replayed active delta: %d %s", got.Code, got.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='completed' WHERE id='r';
		INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"message.completed","message_id":"a"}'),
			('c','r','{"type":"text.delta","message_id":"a","delta":"after-terminal-private"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash("test-session")); err != nil {
		t.Fatal(err)
	}
	if got := open(); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "prior model output") || strings.Contains(got.Body.String(), "after-terminal-private") {
		t.Fatalf("completed reply should remain viewable: %d %s", got.Code, got.Body.String())
	}
}

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

func TestSSEReplayRechecksAuthorizationBetweenBoundedBatches(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `ALTER TABLE v3_conversations ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		UPDATE v3_conversation_replies SET state='completed' WHERE id='r';
		UPDATE v3_conversation_messages SET state='completed' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	if err := db.QueryRow(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"message.started","message":{"id":"a"}}') RETURNING seq`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		SELECT 'c','r',jsonb_build_object('type','text.delta','message_id','a','index',0,
			'delta',CASE WHEN n>64 THEN 'after-revocation-'||n::text ELSE n::text END)
		FROM generate_series(1,130) AS n;
		INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"message.completed","message_id":"a"}')`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		revoke     bool
		wantEvents int
	}{
		{name: "full replay", wantEvents: 131},
		{name: "revoked after first batch", revoke: true, wantEvents: 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			svc := New(db, nil, Options{
				CurrentUser: func(*http.Request) core.User { return core.User{ID: "persisted-owner"} },
				RefreshSession: func(_ context.Context, token string) (core.User, error) {
					checks++
					if token != "test-session" || tc.revoke && checks > 1 {
						return core.User{}, ErrForbidden
					}
					return core.User{ID: "persisted-owner"}, nil
				},
				AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
					return []string{"test"}, nil
				},
			})
			req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/c/events", nil)
			req.SetPathValue("id", "c")
			req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "test-session"})
			response := httptest.NewRecorder()
			svc.events(response, req)
			if response.Code != http.StatusOK || checks < 2 {
				t.Fatalf("status=%d authorization checks=%d", response.Code, checks)
			}
			body := response.Body.String()
			if got := strings.Count(body, "data: "); got != tc.wantEvents {
				t.Fatalf("replayed events=%d want=%d", got, tc.wantEvents)
			}
			last, ids := cursor, 0
			for _, line := range strings.Split(body, "\n") {
				if !strings.HasPrefix(line, "id: ") {
					continue
				}
				id, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
				if err != nil || id <= last {
					t.Fatalf("replay cursor did not advance: last=%d line=%q err=%v", last, line, err)
				}
				last, ids = id, ids+1
			}
			if ids != tc.wantEvents {
				t.Fatalf("replayed IDs=%d want=%d", ids, tc.wantEvents)
			}
			if tc.revoke {
				if strings.Contains(body, "after-revocation-") || strings.Contains(body, "message.completed") {
					t.Fatal("replayed a later event after authorization was revoked")
				}
			} else if checks < 3 || !strings.Contains(body, "message.completed") || !strings.Contains(body, "after-revocation-130") {
				t.Fatalf("full replay did not reach the terminal batch: checks=%d", checks)
			}
		})
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
