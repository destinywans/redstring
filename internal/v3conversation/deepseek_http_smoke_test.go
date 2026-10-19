package v3conversation

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

type deepSeekSmokeEnvironmentAuthorizer struct{}

func (deepSeekSmokeEnvironmentAuthorizer) AuthorizeEnvironment(_ context.Context, user core.User, env string) error {
	if user.ID == "smoke-owner" && env == "test" {
		return nil
	}
	return store.ErrForbidden
}

type deepSeekSmokeEvent struct {
	Type      string          `json:"type"`
	MessageID string          `json:"message_id"`
	Delta     string          `json:"delta"`
	Run       json.RawMessage `json:"run"`
}

// readDeepSeekSmokeEvent reads one SSE frame, ignoring keepalives. It never logs
// response data, which may contain model output.
func readDeepSeekSmokeEvent(reader *bufio.Reader) (int64, deepSeekSmokeEvent, error) {
	for {
		var seq int64
		var event deepSeekSmokeEvent
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return 0, event, err
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "id: ") {
				seq, err = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
				if err != nil {
					return 0, event, err
				}
			}
			if strings.HasPrefix(line, "data: ") {
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					return 0, event, err
				}
			}
		}
		if event.Type != "" {
			return seq, event, nil
		}
	}
}

func deepSeekSmokeJSONEqual(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

// TestV3DeepSeekHTTPPGSmoke is an opt-in, one-request paid smoke test. Supply a
// dedicated local database via TEST_DATABASE_URL; isolatedConversationDB creates
// and drops a random schema inside it. Never point this at a product database.
func TestV3DeepSeekHTTPPGSmoke(t *testing.T) {
	if os.Getenv("AIOPS_V3_REAL_DEEPSEEK_SMOKE") != "1" {
		t.Skip("opt-in paid V3 DeepSeek HTTP/SSE/PostgreSQL smoke")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL must name a dedicated temporary local database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid TEST_DATABASE_URL")
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "aiops_v3_smoke_") ||
		(cfg.ConnConfig.Host != "127.0.0.1" && cfg.ConnConfig.Host != "localhost" && cfg.ConnConfig.Host != "::1") {
		t.Fatal("TEST_DATABASE_URL must use a local aiops_v3_smoke_* database")
	}
	outputLimit, err := strconv.Atoi(os.Getenv("OPENAI_MAX_OUTPUT_TOKENS"))
	if os.Getenv("AIOPS_INFERENCE_MAX_CALLS") != "1" || err != nil || outputLimit < 16 || outputLimit > 32 {
		t.Fatal("configure one model call and 16-32 output tokens before opting in")
	}
	if os.Getenv("OPENAI_MODEL") != "deepseek-flash" || os.Getenv("OPENAI_BASE_URL") != "https://api.deepseek.com" ||
		(os.Getenv("OPENAI_API") != "" && os.Getenv("OPENAI_API") != "chat_completions") ||
		os.Getenv("OLLAMA_MODEL") != "" || os.Getenv("CLAUDE_MODEL") != "" || os.Getenv("AIOPS_V3_TOOL_MODELS") != "" {
		t.Fatal("configure only DeepSeek Flash Chat Completions and leave V3 tools disabled")
	}

	runtime, err := inference.NewFromEnv()
	if err != nil {
		t.Fatal("invalid inference configuration")
	}
	models := runtime.Models()
	if len(models) != 1 || models[0].ID != "openai:deepseek-flash" {
		t.Fatal("the smoke requires exactly one configured model")
	}

	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err = db.Exec(ctx, `
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN status text NOT NULL DEFAULT 'active', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT 'openai:deepseek-flash';
		ALTER TABLE v3_conversation_messages ALTER COLUMN state SET DEFAULT 'completed';
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT '', ADD COLUMN model_id text NOT NULL DEFAULT '', ADD COLUMN reasoning text NOT NULL DEFAULT '', ADD COLUMN started_at timestamptz NOT NULL DEFAULT now();
		ALTER TABLE management_audit ALTER COLUMN actor_kind SET DEFAULT 'user';
		CREATE TABLE IF NOT EXISTS users (id text PRIMARY KEY,email text NOT NULL,name text NOT NULL,role text NOT NULL,disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz);
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		CREATE TABLE IF NOT EXISTS rbac_user_roles (user_id text NOT NULL,role_id text NOT NULL);
		INSERT INTO users(id,email,name,role) VALUES('smoke-owner','smoke@local','Smoke Owner','viewer');
		INSERT INTO v3_conversations(id,owner_id,environment_id,title,default_model_id) VALUES('smoke-conv','smoke-owner','test','DeepSeek smoke','openai:deepseek-flash');`)
	if err != nil {
		t.Fatal(err)
	}
	const sessionToken = "v3-deepseek-smoke-session"
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'smoke-owner',now()+interval '1 hour')`, store.Hash(sessionToken)); err != nil {
		t.Fatal(err)
	}
	observer, err := pgxpool.NewWithConfig(ctx, db.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)

	repository := &store.Store{DB: db}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	server := httpapi.New(repository, runtime, true)
	chat := New(db, runtime, Options{
		CurrentUser:    httpapi.CurrentUser,
		RefreshSession: repository.Session,
		Shutdown:       shutdown,
		RunTimeout:     25 * time.Second,
		Environment: func(r *http.Request) (string, error) {
			scope, ok := httpapi.V3ScopeFromContext(r.Context())
			if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
				return "", ErrEnvironment
			}
			return scope.EnvironmentIDs[0], nil
		},
		AllowedEnvironments: func(_ context.Context, user core.User) ([]string, error) {
			if user.ID == "smoke-owner" {
				return []string{"test"}, nil
			}
			return nil, nil
		},
		AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
			if owner == "smoke-owner" && env == "test" {
				return nil
			}
			return ErrForbidden
		},
	})
	chat.Register(server.Register)
	server.MarkV3Route("POST /api/v1/conversations/{id}/messages", "conversation", "change")
	server.MarkV3Route("GET /api/v1/conversations/{id}/events", "", "")
	server.MarkV3Route("GET /api/v1/conversations/{id}", "conversation", "view")
	policy := func(_ context.Context, user core.User, module, action, env string) error {
		if user.ID == "smoke-owner" && module == "conversation" && (action == "change" || action == "view") && env == "test" {
			return nil
		}
		return store.ErrForbidden
	}
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}, deepSeekSmokeEnvironmentAuthorizer{}, repository.Session, policy); err != nil {
		cancel()
		t.Fatal(err)
	}
	remote := httptest.NewServer(server)
	t.Cleanup(func() {
		cancel()
		remote.CloseClientConnections()
		remote.Close()
	})
	client := &http.Client{Timeout: 35 * time.Second}
	path := remote.URL + "/api/v1/conversations/smoke-conv"
	request := func(method, suffix string, body io.Reader) *http.Request {
		t.Helper()
		req, err := http.NewRequest(method, path+suffix, body)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: sessionToken})
		if suffix != "/events" {
			req.Header.Set("X-AIOps-Environment", "test")
		}
		return req
	}

	// This is the only POST in the test. A timeout or provider error is final;
	// retrying could create another billable request.
	send := request(http.MethodPost, "/messages", strings.NewReader(`{"text":"Reply with exactly OK.","model":"openai:deepseek-flash"}`))
	send.Header.Set("Content-Type", "application/json")
	response, err := client.Do(send)
	if err != nil {
		t.Fatal("message request failed; do not retry automatically")
	}
	var accepted struct {
		Message Message `json:"message"`
		ReplyID string  `json:"reply_id"`
	}
	if response.StatusCode != http.StatusAccepted || json.NewDecoder(response.Body).Decode(&accepted) != nil || accepted.Message.ID == "" || accepted.ReplyID == "" {
		response.Body.Close()
		t.Fatal("message was not accepted; do not retry automatically")
	}
	response.Body.Close()

	response, err = client.Do(request(http.MethodGet, "/events", nil))
	if err != nil {
		t.Fatal("SSE connection failed; do not resend the message")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("SSE endpoint did not return an event stream")
	}
	reader := bufio.NewReader(response.Body)
	var text strings.Builder
	var lastSeq int64
	var completed deepSeekSmokeEvent
	for completed.Type != "message.completed" {
		seq, event, err := readDeepSeekSmokeEvent(reader)
		if err != nil {
			t.Fatal("SSE ended before completion; do not resend the message")
		}
		if seq <= lastSeq {
			t.Fatal("SSE event sequence did not increase")
		}
		lastSeq = seq
		if event.Type == "text.delta" {
			text.WriteString(event.Delta)
		}
		if event.Type == "message.completed" {
			completed = event
		}
	}
	if completed.MessageID == "" || text.Len() == 0 {
		t.Fatal("completed event is missing an assistant message or streamed text")
	}

	// Observe immediately after completion on a separate pool. A visible SSE
	// completion must imply that the full reply was committed already.
	var messageState, replyState string
	var partsRaw, runRaw []byte
	err = observer.QueryRow(ctx, `SELECT m.state,m.parts,m.run,r.state FROM v3_conversation_messages m
		JOIN v3_conversation_replies r ON r.assistant_message_id=m.id
		WHERE m.id=$1 AND r.id=$2 AND r.conversation_id='smoke-conv'`, completed.MessageID, accepted.ReplyID).
		Scan(&messageState, &partsRaw, &runRaw, &replyState)
	if err != nil || messageState != "completed" || replyState != "completed" {
		t.Fatal("completion event became visible before the completed message and reply")
	}
	var parts []struct {
		Type     string `json:"type"`
		Markdown string `json:"markdown"`
	}
	if json.Unmarshal(partsRaw, &parts) != nil || len(parts) != 1 || parts[0].Type != "text" || parts[0].Markdown != text.String() {
		t.Fatal("persisted complete text differs from streamed deltas")
	}
	var run struct {
		ModelID string `json:"model_id"`
		Tokens  int    `json:"tokens"`
	}
	if len(runRaw) == 0 || string(runRaw) == "null" ||
		json.Unmarshal(runRaw, &run) != nil || run.ModelID != "openai:deepseek-flash" || run.Tokens <= 0 {
		t.Fatal("provider usage was not recorded in the completed message run")
	}
	if !deepSeekSmokeJSONEqual(runRaw, completed.Run) {
		t.Fatal("completed event run differs from persisted run")
	}
	var terminalType string
	if err := observer.QueryRow(ctx, `SELECT payload->>'type' FROM v3_conversation_events WHERE reply_id=$1 ORDER BY seq DESC LIMIT 1`, accepted.ReplyID).Scan(&terminalType); err != nil || terminalType != "message.completed" {
		t.Fatal("completion event was not the durable terminal event")
	}

	detail, err := client.Do(request(http.MethodGet, "", nil))
	if err != nil {
		t.Fatal("conversation detail readback failed")
	}
	defer detail.Body.Close()
	if detail.StatusCode != http.StatusOK {
		t.Fatal("conversation detail readback returned a non-OK status")
	}
	var saved Conversation
	if err := json.NewDecoder(detail.Body).Decode(&saved); err != nil {
		t.Fatal("conversation detail readback was invalid JSON")
	}
	for _, message := range saved.Messages {
		if message.ID == completed.MessageID {
			var savedPart struct {
				Type     string `json:"type"`
				Markdown string `json:"markdown"`
			}
			if len(message.Parts) != 1 || json.Unmarshal(message.Parts[0], &savedPart) != nil ||
				message.Streaming || savedPart.Type != "text" || savedPart.Markdown != text.String() {
				t.Fatal("conversation detail does not contain the completed text")
			}
			if !deepSeekSmokeJSONEqual(message.Run, runRaw) {
				t.Fatal("conversation detail run differs from persisted run")
			}
			return
		}
	}
	t.Fatal("conversation detail omitted the completed assistant message")
}
