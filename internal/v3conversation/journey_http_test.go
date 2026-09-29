package v3conversation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type journeyEnvironmentAuthorizer struct{}

func (journeyEnvironmentAuthorizer) AuthorizeEnvironment(_ context.Context, user core.User, env string) error {
	if (user.ID == "journey-owner" || user.ID == "journey-other") && env == "test" {
		return nil
	}
	return store.ErrForbidden
}

// Exercise the route composition against one isolated database. The fake model
// pauses after its first durable delta so the browser can disconnect, then
// proves the same reply is visible through replay, list and detail reads.
func TestConversationRegisteredJourneyPersistsReplayAndReads(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		CREATE FUNCTION aiops_search_match(value text, query text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
			SELECT strpos(lower(value),lower(query))>0
		$$;
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN status text NOT NULL DEFAULT 'active', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		ALTER TABLE v3_conversation_messages ALTER COLUMN state SET DEFAULT 'completed';
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT '', ADD COLUMN model_id text NOT NULL DEFAULT '', ADD COLUMN reasoning text NOT NULL DEFAULT '', ADD COLUMN started_at timestamptz NOT NULL DEFAULT now();
		ALTER TABLE management_audit ALTER COLUMN actor_kind SET DEFAULT 'user';
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE VIEW v3_effective_role_grants AS
			SELECT a.user_id,g.module,g.action,g.scope_kind,g.scope_id FROM rbac_user_roles a
			JOIN rbac_roles r ON r.id=a.role_id JOIN v3_role_grants g ON g.role_id=r.id;
		CREATE TABLE v3_canvases (id text PRIMARY KEY,owner_id text NOT NULL,environment_id text NOT NULL,title text NOT NULL,status text NOT NULL,services text[] NOT NULL DEFAULT '{}',nodes jsonb NOT NULL DEFAULT '[]',updated_at timestamptz NOT NULL);
		CREATE TABLE v3_canvas_node_exchanges (id text PRIMARY KEY,canvas_id text NOT NULL,state text NOT NULL);
		INSERT INTO users(id,email,name,role) VALUES
			('journey-owner','owner@local','Journey Owner','viewer'),
			('journey-other','other@local','Journey Other','viewer');
		INSERT INTO rbac_roles(id) VALUES('journey-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('journey-owner','journey-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
			('journey-role','environment','view','environment','test'),
			('journey-role','conversation','view','environment','test'),
			('journey-role','conversation','change','environment','test');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES
		($1,'journey-owner',now()+interval '1 hour'),($2,'journey-other',now()+interval '1 hour')`,
		store.Hash("journey-owner-session"), store.Hash("journey-other-session")); err != nil {
		t.Fatal(err)
	}

	repository := &store.Store{DB: db}
	runtime := &gatedStreamRuntime{first: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		runtime.once.Do(func() { close(runtime.first) })
		select {
		case <-runtime.release:
		default:
			close(runtime.release)
		}
	})
	server := httpapi.New(repository, runtime, true)
	chat := New(db, runtime, Options{
		CurrentUser:    httpapi.CurrentUser,
		RefreshSession: repository.Session,
		Environment: func(r *http.Request) (string, error) {
			scope, ok := httpapi.V3ScopeFromContext(r.Context())
			if !ok || len(scope.EnvironmentIDs) != 1 {
				return "", ErrEnvironment
			}
			if scope.All {
				return "all", nil
			}
			return scope.EnvironmentIDs[0], nil
		},
		AllowedEnvironments: func(_ context.Context, user core.User) ([]string, error) {
			if user.ID == "journey-owner" || user.ID == "journey-other" {
				return []string{"test"}, nil
			}
			return nil, nil
		},
		DefaultModel: func(_ context.Context, owner string) (string, error) {
			if owner != "journey-owner" {
				return "", ErrForbidden
			}
			return "test-model", nil
		},
		AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
			if owner == "journey-owner" && env == "test" {
				return nil
			}
			return ErrForbidden
		},
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if owner == "journey-owner" && env == "test" {
				return nil
			}
			return ErrForbidden
		},
		ToolsForTurn: func(_ context.Context, owner, env, _ string) ([]inference.EvidenceTool, error) {
			if owner != "journey-owner" || env != "test" {
				return nil, ErrForbidden
			}
			return []inference.EvidenceTool{{Name: "fake_read", Read: func(_ context.Context, query string) (inference.Evidence, error) {
				if query != "health" {
					return inference.Evidence{}, fmt.Errorf("unexpected query %q", query)
				}
				return inference.Evidence{Source: "fake", Title: "health", Content: "healthy"}, nil
			}}}, nil
		},
	})
	chat.RegisterV3(server)
	policy := func(_ context.Context, user core.User, module, action, env string) error {
		if (user.ID == "journey-owner" || user.ID == "journey-other") && module == "conversation" &&
			(action == "view" || action == "change") && env == "test" {
			return nil
		}
		return store.ErrForbidden
	}
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		journeyEnvironmentAuthorizer{}, repository.Session, policy); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(server)
	defer remote.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path, env, session, cursor string, body io.Reader) *http.Request {
		t.Helper()
		req, err := http.NewRequest(method, remote.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if env != "" {
			req.Header.Set("X-AIOps-Environment", env)
		}
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		if session != "" {
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		}
		return req
	}
	call := func(method, path, env, session, cursor string, body io.Reader) (int, []byte) {
		t.Helper()
		response, err := client.Do(request(method, path, env, session, cursor, body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	const base = "/api/v1/conversations"
	if status, _ := call(http.MethodPost, base, "test", "", "", strings.NewReader(`{"text":"investigate checkout"}`)); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: %d", status)
	}
	status, body := call(http.MethodPost, base, "test", "journey-owner-session", "", strings.NewReader(`{"text":"investigate checkout","context":"service:checkout-api"}`))
	var created struct {
		ID string `json:"id"`
	}
	if status != http.StatusCreated || json.Unmarshal(body, &created) != nil || created.ID == "" {
		t.Fatalf("create: %d %s", status, body)
	}
	path := base + "/" + created.ID
	if status, _ := call(http.MethodPost, path+"/messages", "test", "journey-other-session", "", strings.NewReader(`{"text":"private"}`)); status != http.StatusNotFound {
		t.Fatalf("other owner send: %d", status)
	}
	status, body = call(http.MethodPost, path+"/messages", "test", "journey-owner-session", "",
		strings.NewReader(`{"text":"`+strings.Repeat("x", 5000)+`"}`))
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "model_input_too_large") {
		t.Fatalf("oversized text-only model input: %d %s", status, body)
	}
	var rejectedMessages, rejectedReplies int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_messages WHERE conversation_id=$1`, created.ID).Scan(&rejectedMessages); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_replies WHERE conversation_id=$1`, created.ID).Scan(&rejectedReplies); err != nil {
		t.Fatal(err)
	}
	if rejectedMessages != 0 || rejectedReplies != 0 {
		t.Fatalf("rejected text-only turn persisted messages=%d replies=%d", rejectedMessages, rejectedReplies)
	}
	status, body = call(http.MethodPost, path+"/messages", "test", "journey-owner-session", "", strings.NewReader(`{"text":"check health"}`))
	var accepted struct {
		Message Message `json:"message"`
		ReplyID string  `json:"reply_id"`
	}
	if status != http.StatusAccepted || json.Unmarshal(body, &accepted) != nil || accepted.ReplyID == "" || accepted.Message.ID == "" {
		t.Fatalf("send: %d %s", status, body)
	}
	select {
	case <-runtime.first:
	case <-time.After(3 * time.Second):
		t.Fatal("fake model did not emit first delta")
	}
	response, err := client.Do(request(http.MethodGet, path+"/events", "", "journey-owner-session", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE start: %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	var cursor int64
	for cursor == 0 {
		item, err := readStreamItem(reader)
		if err != nil {
			t.Fatal(err)
		}
		if item.typ == "text.delta" {
			cursor = item.id
		}
	}
	response.Body.Close()
	close(runtime.release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var state string
		if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id=$1`, accepted.ReplyID).Scan(&state); err != nil {
			t.Fatal(err)
		} else if state == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reply not completed after disconnect: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status, _ := call(http.MethodGet, path+"/events", "", "journey-other-session", fmt.Sprint(cursor), nil); status != http.StatusNotFound {
		t.Fatalf("other owner replay: %d", status)
	}
	response, err = client.Do(request(http.MethodGet, path+"/events?reply_id="+accepted.ReplyID, "", "journey-owner-session", fmt.Sprint(cursor), nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("SSE replay: %d", response.StatusCode)
	}
	reader = bufio.NewReader(response.Body)
	completed := false
	for {
		item, err := readStreamItem(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if item.id <= cursor {
			t.Fatalf("replayed cursor: %d <= %d", item.id, cursor)
		}
		if item.typ == "message.completed" {
			completed = true
			var savedState string
			if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_messages WHERE id=$1`, item.message).Scan(&savedState); err != nil || savedState != "completed" {
				t.Fatalf("terminal event before durable message: state=%q err=%v", savedState, err)
			}
		}
	}
	if !completed {
		t.Fatal("replay missed durable message.completed")
	}
	status, body = call(http.MethodGet, base+"?mode=chat", "test", "journey-owner-session", "", nil)
	var page struct {
		Items []struct {
			ID     string `json:"id"`
			Tokens *int   `json:"tokens"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &page) != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != created.ID || page.Items[0].Tokens == nil || *page.Items[0].Tokens != 22 {
		t.Fatalf("same owner list: %d %s", status, body)
	}
	status, body = call(http.MethodGet, path, "test", "journey-owner-session", "", nil)
	var detail Conversation
	if status != http.StatusOK || json.Unmarshal(body, &detail) != nil || detail.ID != created.ID || detail.Scope["service"] != "checkout-api" || len(detail.Messages) != 2 || detail.Messages[0].ID != accepted.Message.ID || detail.Messages[1].Role != "assistant" {
		t.Fatalf("same owner detail: %d %s", status, body)
	}
	contextSide, ok := detail.Side["context"].(map[string]any)
	if !ok || contextSide["used"] != float64(20) || contextSide["budget"] != float64(8192) {
		t.Fatalf("reported context: %s", body)
	}
	connectors, ok := detail.Side["connectors"].([]any)
	if !ok || len(connectors) != 0 {
		t.Fatalf("fake read without evidence event must not claim a connector: %s", body)
	}
	if status, _ := call(http.MethodGet, path, "test", "journey-other-session", "", nil); status != http.StatusNotFound {
		t.Fatalf("other owner detail: %d", status)
	}
	// A later turn changes the default /events target. A second tab must still
	// be able to identify the original reply without remembering an event ID.
	status, body = call(http.MethodPost, path+"/messages", "test", "journey-owner-session", "", strings.NewReader(`{"text":"follow up"}`))
	var next struct {
		ReplyID string `json:"reply_id"`
	}
	if status != http.StatusAccepted || json.Unmarshal(body, &next) != nil || next.ReplyID == "" {
		t.Fatalf("follow-up send: %d %s", status, body)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		var state string
		if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id=$1`, next.ReplyID).Scan(&state); err != nil {
			t.Fatal(err)
		} else if state == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("follow-up reply not completed: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, err = client.Do(request(http.MethodGet, path+"/events?reply_id="+accepted.ReplyID, "", "journey-owner-session", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("original reply replay after new turn: %d", response.StatusCode)
	}
	reader = bufio.NewReader(response.Body)
	var originalCompletedID string
	for {
		item, err := readStreamItem(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if item.typ == "message.completed" {
			originalCompletedID = item.message
		}
	}
	if originalCompletedID != detail.Messages[1].ID {
		t.Fatalf("reply_id replay selected message %q, want original %q", originalCompletedID, detail.Messages[1].ID)
	}
}
