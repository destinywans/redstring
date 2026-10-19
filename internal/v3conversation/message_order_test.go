package v3conversation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/migrations"
)

// Both rows share the transaction timestamp, while the assistant ID sorts
// first. The durable insertion order must win in the API and model context.
func TestConversationMessagesUseDurableOrder(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '',
			ADD COLUMN status text NOT NULL DEFAULT 'active',
			ADD COLUMN context_ref text NOT NULL DEFAULT '',
			ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE VIEW v3_effective_role_grants AS
			SELECT a.user_id,g.module,g.action,g.scope_kind,g.scope_id FROM rbac_user_roles a
			JOIN rbac_roles r ON r.id=a.role_id JOIN v3_role_grants g ON g.role_id=r.id;
		INSERT INTO users(id,email,name,role) VALUES('owner','owner@local','Owner','viewer');
		INSERT INTO rbac_roles(id) VALUES('order-reader');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('owner','order-reader');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
			('order-reader','environment','view','environment','test'),
			('order-reader','conversation','view','environment','test');
		INSERT INTO v3_conversations(id,owner_id,environment_id,title) VALUES('ordered','owner','test','Ordered');
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state) VALUES
			('z-user','ordered','user','Owner','[{"type":"text","markdown":"question"}]','completed'),
			('a-assistant','ordered','assistant','AI','[{"type":"text","markdown":"answer"}]','completed');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'owner',now()+interval '1 hour')`, store.Hash("order-session")); err != nil {
		t.Fatal(err)
	}
	var userAt, assistantAt string
	if err := db.QueryRow(ctx, `SELECT created_at::text FROM v3_conversation_messages WHERE id='z-user'`).Scan(&userAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT created_at::text FROM v3_conversation_messages WHERE id='a-assistant'`).Scan(&assistantAt); err != nil || userAt != assistantAt {
		t.Fatalf("test requires tied timestamps: user=%s assistant=%s err=%v", userAt, assistantAt, err)
	}
	repository := &store.Store{DB: db}
	server := httpapi.New(repository, nil, true)
	svc := New(db, nil, Options{
		CurrentUser: httpapi.CurrentUser,
		Environment: func(r *http.Request) (string, error) {
			scope, ok := httpapi.V3ScopeFromContext(r.Context())
			if !ok || len(scope.EnvironmentIDs) != 1 || scope.All {
				return "", ErrEnvironment
			}
			return scope.EnvironmentIDs[0], nil
		},
		AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
			return []string{"test"}, nil
		},
	})
	svc.RegisterV3(server)
	policy := func(_ context.Context, user core.User, module, action, env string) error {
		if user.ID == "owner" && module == "conversation" && action == "view" && env == "test" {
			return nil
		}
		return store.ErrForbidden
	}
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		detailRouteEnvironmentAuthorizer{}, repository.Session, policy); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/ordered", nil)
	req.Header.Set("X-AIOps-Environment", "test")
	req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "order-session"})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].ID != "z-user" || got.Messages[1].ID != "a-assistant" {
		t.Fatalf("GET message order: %+v", got.Messages)
	}
	contextMessages, err := svc.contextMessages(ctx, "ordered")
	if err != nil {
		t.Fatal(err)
	}
	if len(contextMessages) != 2 || contextMessages[0].Role != "user" || contextMessages[0].Content != "question" || contextMessages[1].Role != "assistant" || contextMessages[1].Content != "answer" {
		t.Fatalf("next model context order: %+v", contextMessages)
	}
}

func TestMessageOrderMigrationBackfillsReplyPairsAndContinuesSequence(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	// Rewind the test schema to the relevant pre-migration shape, then apply
	// the actual embedded migration, including its backfill and index change.
	_, err := db.Exec(ctx, `
		ALTER TABLE v3_conversation_messages DROP COLUMN sort_order;
		DROP SEQUENCE v3_conversation_message_order_seq;
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT '',
			ADD COLUMN started_at timestamptz NOT NULL DEFAULT now();
		UPDATE v3_conversation_replies SET user_message_id='u' WHERE id='r';
		CREATE INDEX v3_conversation_messages_order ON v3_conversation_messages(conversation_id,created_at,id);
		INSERT INTO v3_conversations(id,owner_id,environment_id) VALUES('historical','owner','test');
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state) VALUES
			('z-historical-user','historical','user','Owner','[]','completed'),
			('a-historical-assistant','historical','assistant','AI','[]','completed');
		INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,state)
			VALUES('historical-reply','historical','z-historical-user','a-historical-assistant','completed');
		INSERT INTO v3_conversations(id,owner_id,environment_id) VALUES('inverted','owner','test');
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state,created_at) VALUES
			('z-older-user','inverted','user','Owner','[]','completed',now()-interval '1 hour'),
			('a-older-assistant','inverted','assistant','AI','[]','completed',now()-interval '1 hour'),
			('z-first-user','inverted','user','Owner','[]','completed',now()+interval '2 minutes'),
			('a-first-assistant','inverted','assistant','AI','[]','completed',now()+interval '2 minutes'),
			('z-second-user','inverted','user','Owner','[]','completed',now()),
			('a-second-assistant','inverted','assistant','AI','[]','completed',now());
		INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,state,started_at) VALUES
			('older-unlinked-reply','inverted','z-older-user','a-older-assistant','completed',now()-interval '1 hour'),
			('first-reply','inverted','z-first-user','a-first-assistant','completed',now()+interval '2 minutes'),
			('second-reply','inverted','z-second-user','a-second-assistant','completed',now());
		INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes) VALUES
			('owner','user','v3.conversation.message','inverted','accepted','{"reply_id":"first-reply"}'),
			('owner','user','v3.conversation.message','inverted','accepted','{"reply_id":"second-reply"}');`)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := migrations.Files.ReadFile("085_v3_conversation_message_order.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("migration: %v", err)
	}
	var userOrder, assistantOrder int64
	if err := db.QueryRow(ctx, `SELECT sort_order FROM v3_conversation_messages WHERE id='z-historical-user'`).Scan(&userOrder); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT sort_order FROM v3_conversation_messages WHERE id='a-historical-assistant'`).Scan(&assistantOrder); err != nil || assistantOrder != userOrder+1 {
		t.Fatalf("reply pair order: user=%d assistant=%d err=%v", userOrder, assistantOrder, err)
	}
	var ordered []string
	rows, err := db.Query(ctx, `SELECT id FROM v3_conversation_messages WHERE conversation_id='inverted' ORDER BY sort_order`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ordered = append(ordered, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	want := []string{"z-older-user", "a-older-assistant", "z-first-user", "a-first-assistant", "z-second-user", "a-second-assistant"}
	if len(ordered) != len(want) {
		t.Fatalf("mixed reply order: got %v, want %v", ordered, want)
	}
	for i, id := range want {
		if ordered[i] != id {
			t.Fatalf("mixed reply order at %d: got %v, want %v", i, ordered, want)
		}
	}
	latest, err := New(db, nil, Options{}).latestReply(ctx, "inverted")
	if err != nil || latest != "second-reply" {
		t.Fatalf("SSE latest reply=%q err=%v; want second-reply despite older started_at", latest, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state)
		VALUES('new-message','historical','user','Owner','[]','completed')`); err != nil {
		t.Fatal(err)
	}
	var newOrder int64
	if err := db.QueryRow(ctx, `SELECT sort_order FROM v3_conversation_messages WHERE id='new-message'`).Scan(&newOrder); err != nil || newOrder <= assistantOrder {
		t.Fatalf("next sequence order=%d after assistant=%d err=%v", newOrder, assistantOrder, err)
	}
}
