package v3conversation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type gatedStreamRuntime struct {
	first   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *gatedStreamRuntime) Models() []inference.Model {
	return []inference.Model{{ID: "test-model", Name: "Fake model", InputBudget: 8192, Streaming: true}, {ID: "preferred-model", Name: "Preferred fake model", InputBudget: 8192, Streaming: true}}
}

func (r *gatedStreamRuntime) Run(ctx context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	if err := emit(inference.Event{Type: "delta", Delta: "first"}); err != nil {
		return "", err
	}
	r.once.Do(func() { close(r.first) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := emit(inference.Event{Type: "delta", Delta: " rest"}); err != nil {
		return "", err
	}
	if err := emit(inference.Event{Type: "step", Step: &inference.Step{Kind: "model", Status: "succeeded", Usage: &inference.Usage{InputTokens: 20, OutputTokens: 2}}}); err != nil {
		return "", err
	}
	return "first rest", nil
}

type streamEnvironmentAuthorizer struct{}

func (streamEnvironmentAuthorizer) AuthorizeEnvironment(_ context.Context, user core.User, env string) error {
	if user.ID == "stream-owner" && env == "test" {
		return nil
	}
	return store.ErrForbidden
}

type streamItem struct {
	id      int64
	typ     string
	message string
}

func readStreamItem(r *bufio.Reader) (streamItem, error) {
	var item streamItem
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return item, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if item.id == 0 && item.typ == "" {
				continue
			}
			return item, nil
		}
		if strings.HasPrefix(line, "id: ") {
			item.id, err = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if err != nil {
				return item, err
			}
		}
		if strings.HasPrefix(line, "data: ") {
			var payload struct {
				Type      string `json:"type"`
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				return item, err
			}
			item.typ, item.message = payload.Type, payload.MessageID
		}
	}
}

func TestConversationSendAndSSERegisteredRoutesResumeAfterDisconnect(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		ALTER TABLE v3_conversations ADD COLUMN default_model_id text NOT NULL DEFAULT 'test-model';
		ALTER TABLE v3_conversation_messages ALTER COLUMN state SET DEFAULT 'completed';
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT '', ADD COLUMN model_id text NOT NULL DEFAULT '', ADD COLUMN reasoning text NOT NULL DEFAULT '', ADD COLUMN started_at timestamptz NOT NULL DEFAULT now();
		ALTER TABLE management_audit ALTER COLUMN actor_kind SET DEFAULT 'user';
		CREATE TABLE users (id text PRIMARY KEY,email text NOT NULL,name text NOT NULL,role text NOT NULL,disabled boolean NOT NULL DEFAULT false);
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE TABLE rbac_user_roles (user_id text NOT NULL,role_id text NOT NULL);
		INSERT INTO users(id,email,name,role) VALUES('stream-owner','stream@local','Stream Owner','viewer');
		INSERT INTO v3_conversations(id,owner_id,environment_id,default_model_id) VALUES('stream-conv','stream-owner','test','test-model');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'stream-owner',now()+interval '1 hour')`, store.Hash("stream-session")); err != nil {
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
		DefaultModel: func(_ context.Context, owner string) (string, error) {
			if owner != "stream-owner" {
				return "", ErrForbidden
			}
			return "preferred-model", nil
		},
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
			if user.ID == "stream-owner" {
				return []string{"test"}, nil
			}
			return nil, nil
		},
		AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
			if owner == "stream-owner" && env == "test" {
				return nil
			}
			return ErrForbidden
		},
		AuthorizeBackgroundTurnTx: func(_ context.Context, _ pgx.Tx, owner, env string) error {
			if owner == "stream-owner" && env == "test" {
				return nil
			}
			return ErrForbidden
		},
	})
	chat.Register(server.Register)
	server.MarkV3Route("POST /api/v1/conversations/{id}/messages", "conversation", "change")
	server.MarkV3Route("GET /api/v1/conversations/{id}/events", "", "")
	policy := func(_ context.Context, user core.User, module, action, env string) error {
		if user.ID == "stream-owner" && module == "conversation" && action == "change" && env == "test" {
			return nil
		}
		return store.ErrForbidden
	}
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}, streamEnvironmentAuthorizer{}, repository.Session, policy); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(server)
	defer remote.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, path, env, cursor string, body io.Reader, cookie bool) *http.Request {
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
		if cookie {
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "stream-session"})
		}
		return req
	}
	url := "/api/v1/conversations/stream-conv"
	if response, err := client.Do(request(http.MethodPost, url+"/messages", "all", "", strings.NewReader(`{"text":"investigate"}`), true)); err != nil {
		t.Fatal(err)
	} else {
		response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("all write: %d", response.StatusCode)
		}
	}
	if response, err := client.Do(request(http.MethodPost, url+"/messages", "test", "", strings.NewReader(`{"text":"investigate","model":"not-configured"}`), true)); err != nil {
		t.Fatal(err)
	} else {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "invalid_model") {
			t.Fatalf("invalid model: %d %s", response.StatusCode, body)
		}
	}
	response, err := client.Do(request(http.MethodPost, url+"/messages", "test", "", strings.NewReader(`{"text":"investigate"}`), true))
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		Message Message `json:"message"`
		ReplyID string  `json:"reply_id"`
	}
	sendBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if err := json.Unmarshal(sendBody, &accepted); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusAccepted || accepted.Message.ID == "" || accepted.ReplyID == "" {
		t.Fatalf("send: %d %s", response.StatusCode, sendBody)
	}
	var selectedModel string
	if err := db.QueryRow(ctx, `SELECT model_id FROM v3_conversation_replies WHERE id=$1`, accepted.ReplyID).Scan(&selectedModel); err != nil || selectedModel != "preferred-model" {
		t.Fatalf("turn did not use current default model: %q %v", selectedModel, err)
	}
	var saved string
	if err := db.QueryRow(ctx, `SELECT parts::text FROM v3_conversation_messages WHERE id=$1`, accepted.Message.ID).Scan(&saved); err != nil || !strings.Contains(saved, "investigate") {
		t.Fatalf("202 before user message persisted: %q %v", saved, err)
	}
	select {
	case <-runtime.first:
	case <-time.After(3 * time.Second):
		t.Fatal("fake runtime did not reach first streamed delta")
	}
	response, err = client.Do(request(http.MethodPost, url+"/messages", "test", "", strings.NewReader(`{"text":"second reply"}`), true))
	if err != nil {
		t.Fatal(err)
	}
	busyBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(busyBody), "reply_in_progress") {
		t.Fatalf("concurrent turn: %d %s", response.StatusCode, busyBody)
	}
	var userMessages int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_messages WHERE conversation_id='stream-conv' AND role='user'`).Scan(&userMessages); err != nil || userMessages != 1 {
		t.Fatalf("busy turn added a message: count=%d err=%v", userMessages, err)
	}
	response, err = client.Do(request(http.MethodGet, url+"/events", "", "", nil, false))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("SSE without session: %d", response.StatusCode)
	}
	response, err = client.Do(request(http.MethodGet, url+"/events", "", "", nil, true))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE open: %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	var cursor int64
	for {
		item, err := readStreamItem(reader)
		if err != nil {
			t.Fatal(err)
		}
		if item.typ == "text.delta" {
			cursor = item.id
			break
		}
	}
	response.Body.Close() // The worker must survive EventSource disconnect.
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
			t.Fatalf("worker did not complete after SSE disconnect: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, err = client.Do(request(http.MethodGet, url+"/events", "", fmt.Sprint(cursor), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader = bufio.NewReader(response.Body)
	terminal, last, count := false, cursor, 0
	for {
		item, err := readStreamItem(reader)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if item.id <= last {
			t.Fatalf("replayed event at or before Last-Event-ID: %d <= %d", item.id, last)
		}
		last = item.id
		count++
		if item.typ == "message.completed" {
			terminal = true
			var state, parts string
			if err := db.QueryRow(ctx, `SELECT state,parts::text FROM v3_conversation_messages WHERE id=$1`, item.message).Scan(&state, &parts); err != nil || state != "completed" || !strings.Contains(parts, "first rest") {
				t.Fatalf("completion event before full message commit: state=%q parts=%q err=%v", state, parts, err)
			}
		}
	}
	if !terminal || count == 0 {
		t.Fatalf("resume missed terminal event: terminal=%v count=%d", terminal, count)
	}
	response, err = client.Do(request(http.MethodGet, url+"/events", "", fmt.Sprint(last), nil, true))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if body, err := io.ReadAll(response.Body); err != nil || len(body) != 0 {
		t.Fatalf("resume after terminal must not duplicate event: %q %v", body, err)
	}
}
