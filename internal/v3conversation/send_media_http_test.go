package v3conversation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3knowledgesources"
	"github.com/wans/aiops-canvas/internal/v3uploads"
)

// This runner never calls a provider. Holding its first reply open lets the
// registered HTTP routes prove that another send cannot create a second turn.
type mediaSendFixtureRunner struct {
	mu       sync.Mutex
	requests []inference.Request
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (*mediaSendFixtureRunner) Models() []inference.Model {
	return []inference.Model{
		{ID: "fixture:text", Name: "Text fixture", InputBudget: 100000, Streaming: true},
		{ID: "fixture:image", Name: "Image fixture", InputBudget: 100000, Streaming: true, ImageInput: true},
		{ID: "fixture:media", Name: "Media fixture", InputBudget: 100000, Streaming: true, ImageInput: true, PDFInput: true},
	}
}

func (r *mediaSendFixtureRunner) Run(ctx context.Context, request inference.Request, emit func(inference.Event) error) (string, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	first := len(r.requests) == 1
	r.mu.Unlock()
	if first {
		r.once.Do(func() { close(r.started) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if len(request.Messages) == 0 {
		return "", errors.New("fixture request has no user message")
	}
	if strings.Contains(request.Messages[len(request.Messages)-1].Content, "fail after acceptance") {
		return "", errors.New("controlled provider failure")
	}
	if err := emit(inference.Event{Type: "delta", Delta: "fixture answer"}); err != nil {
		return "", err
	}
	if err := emit(inference.Event{Type: "step", Step: &inference.Step{
		ID: "fixture-model-step", Kind: "model", Status: "succeeded",
		Usage: &inference.Usage{InputTokens: 40, OutputTokens: 2},
	}}); err != nil {
		return "", err
	}
	return "fixture answer", nil
}

func (r *mediaSendFixtureRunner) firstRequest() inference.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[0]
}

func (r *mediaSendFixtureRunner) lastRequest() inference.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[len(r.requests)-1]
}

func mediaSendDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated random schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_send_media_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&store.Store{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func mediaSendMultipart(t *testing.T, kind, name string, contents []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("kind", kind); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), w.FormDataContentType()
}

// One migrated database and one registered HTTP server cover the complete
// upload -> accepted turn -> pinned SSE -> durable detail path. The fixtures
// deliberately have no provider, document parser, or external HTTP client.
func TestConversationMediaSendRegisteredRoundTripAndRecovery(t *testing.T) {
	db := mediaSendDatabase(t)
	ctx := t.Context()
	const linkURL = "https://docs.example.com/fixture.md"
	const unreadableURL = "https://docs.example.com/unreadable.md"
	const linkedMarkdown = "# Fixture handbook\nOnly controlled fixture content."
	var linkHits atomic.Int32
	st := &store.Store{DB: db}
	if err := st.Bootstrap(ctx, "media-admin@example.invalid", "isolated-media-admin-password"); err != nil {
		t.Fatal(err)
	}
	owner, err := st.CreateUser(ctx, core.User{Email: "media-owner@example.invalid", Name: "Media Owner", Role: "viewer"}, "isolated-media-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, core.User{Email: "media-other@example.invalid", Name: "Media Other", Role: "viewer"}, "isolated-media-other-password")
	if err != nil {
		t.Fatal(err)
	}
	_, ownerToken, err := st.Login(ctx, owner.Email, "isolated-media-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := st.Login(ctx, other.Email, "isolated-media-other-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_roles(id,name) VALUES('media-send-fixture','Media Send Fixture')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'media-send-fixture'),($2,'media-send-fixture')`, owner.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
		 ('media-send-fixture','environment','view','environment','test'),
		 ('media-send-fixture','environment','view','environment','dev'),
		 ('media-send-fixture','conversation','view','environment','test'),
		 ('media-send-fixture','conversation','change','environment','test'),
		 ('media-send-fixture','conversation','view','environment','dev'),
		 ('media-send-fixture','conversation','change','environment','dev')`); err != nil {
		t.Fatal(err)
	}

	policy := &rbac.V3Policy{Store: st}
	runtime := &mediaSendFixtureRunner{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		runtime.once.Do(func() { close(runtime.started) })
		select {
		case <-runtime.release:
		default:
			close(runtime.release)
		}
	})
	server := httpapi.New(st, runtime, true)
	uploads := v3uploads.New(db)
	uploads.Environments = map[string]bool{"test": true, "dev": true}
	uploads.ReadAuthorizer = func(ctx context.Context, user core.User, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "conversation", Action: "view", EnvironmentID: env})
	}
	uploads.Register(server)
	chat := New(db, runtime, Options{
		CurrentUser: httpapi.CurrentUser, RefreshSession: st.Session,
		FetchLink: func(_ context.Context, address string) (string, error) {
			if address == unreadableURL {
				return "", v3knowledgesources.ErrWebContent
			}
			if address != linkURL {
				return "", fmt.Errorf("unexpected fixture link %q", address)
			}
			linkHits.Add(1)
			return linkedMarkdown, nil
		},
		DefaultModel: func(_ context.Context, _, _ string) (string, error) { return "fixture:text", nil },
		AllowedEnvironments: func(_ context.Context, user core.User) ([]string, error) {
			if user.ID == owner.ID || user.ID == other.ID {
				return []string{"test", "dev"}, nil
			}
			return nil, nil
		},
		AuthorizeBackgroundTurn: func(ctx context.Context, userID, env string) error {
			return policy.Authorize(ctx, core.User{ID: userID}, rbac.V3Decision{Module: "conversation", Action: "change", EnvironmentID: env})
		},
		AuthorizeBackgroundTurnTx: func(ctx context.Context, tx pgx.Tx, userID, env string) error {
			return policy.AuthorizeWithTx(ctx, tx, core.User{ID: userID}, rbac.V3Decision{Module: "conversation", Action: "change", EnvironmentID: env})
		},
		ValidateAttachments: uploads.ValidateAttachments,
	})
	chat.RegisterV3(server)
	if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "dev"},
	}, policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(server)
	defer remote.Close()
	client := &http.Client{Timeout: 8 * time.Second}
	call := func(method, path, env, token, contentType string, body []byte) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, remote.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if env != "" {
			req.Header.Set("X-AIOps-Environment", env)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		response, err := client.Do(req)
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
	uploadJSON := func(env, token string, body string) v3uploads.Upload {
		t.Helper()
		status, data := call(http.MethodPost, "/api/v1/uploads", env, token, "application/json", []byte(body))
		if status != http.StatusCreated {
			t.Fatalf("upload JSON: %d %s", status, data)
		}
		var uploaded v3uploads.Upload
		if err := json.Unmarshal(data, &uploaded); err != nil || uploaded.ID == "" {
			t.Fatalf("upload JSON result: %s err=%v", data, err)
		}
		return uploaded
	}
	uploadFile := func(env, token, kind, name string, data []byte) v3uploads.Upload {
		t.Helper()
		body, contentType := mediaSendMultipart(t, kind, name, data)
		status, response := call(http.MethodPost, "/api/v1/uploads", env, token, contentType, body)
		if status != http.StatusCreated {
			t.Fatalf("upload %s %s: %d %s", kind, name, status, response)
		}
		var uploaded v3uploads.Upload
		if err := json.Unmarshal(response, &uploaded); err != nil || uploaded.ID == "" {
			t.Fatalf("upload %s result: %s err=%v", kind, response, err)
		}
		return uploaded
	}
	postMessage := func(conversationID, env, token, text, model string, attachments ...v3uploads.Upload) (int, []byte) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"text": text, "model": model, "attachments": attachments})
		if err != nil {
			t.Fatal(err)
		}
		return call(http.MethodPost, "/api/v1/conversations/"+conversationID+"/messages", env, token, "application/json", payload)
	}
	readReply := func(conversationID, token, replyID string) ([]streamItem, error) {
		req, err := http.NewRequest(http.MethodGet, remote.URL+"/api/v1/conversations/"+conversationID+"/events?reply_id="+replyID, nil)
		if err != nil {
			return nil, err
		}
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		response, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			return nil, fmt.Errorf("SSE status=%d body=%s", response.StatusCode, data)
		}
		reader := bufio.NewReader(response.Body)
		var events []streamItem
		for {
			item, err := readStreamItem(reader)
			if err != nil {
				return nil, err
			}
			events = append(events, item)
			if item.typ == "message.completed" {
				return events, nil
			}
		}
	}
	assertNoTurns := func(id string) {
		t.Helper()
		var messages, replies, accepts int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_messages WHERE conversation_id=$1`, id).Scan(&messages); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_replies WHERE conversation_id=$1`, id).Scan(&replies); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.conversation.message' AND target=$1`, id).Scan(&accepts); err != nil {
			t.Fatal(err)
		}
		if messages != 0 || replies != 0 || accepts != 0 {
			t.Fatalf("rejected send persisted messages=%d replies=%d accepts=%d", messages, replies, accepts)
		}
	}

	paste := uploadJSON("test", ownerToken, `{"kind":"paste","name":"paste-3","text":"alpha\nbeta\ngamma","size":16,"mime":"text/plain"}`)
	textFile := uploadFile("test", ownerToken, "file", "trace.log", []byte("fixture log line\n"))
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a8a8AAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	image := uploadFile("test", ownerToken, "image", "screen.png", png)
	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<</Type/Catalog>>\nendobj\n%%EOF\n")
	pdf := uploadFile("test", ownerToken, "file", "readme.pdf", pdfBytes)
	linkPayload, err := json.Marshal(map[string]any{"kind": "link", "name": "fixture link", "url": linkURL, "size": 0, "mime": "text/uri-list"})
	if err != nil {
		t.Fatal(err)
	}
	link := uploadJSON("test", ownerToken, string(linkPayload))
	unreadablePayload, err := json.Marshal(map[string]any{"kind": "link", "name": "unreadable fixture", "url": unreadableURL})
	if err != nil {
		t.Fatal(err)
	}
	unreadableLink := uploadJSON("test", ownerToken, string(unreadablePayload))
	foreign := uploadJSON("test", otherToken, `{"kind":"paste","name":"foreign","text":"private"}`)
	crossEnv := uploadJSON("dev", ownerToken, `{"kind":"paste","name":"dev note","text":"dev-only"}`)
	status, body := call(http.MethodPost, "/api/v1/conversations", "test", ownerToken, "application/json", []byte(`{"text":"inspect fixture","model":"fixture:text"}`))
	var created struct {
		ID string `json:"id"`
	}
	if status != http.StatusCreated || json.Unmarshal(body, &created) != nil || created.ID == "" {
		t.Fatalf("create conversation: %d %s", status, body)
	}
	for _, rejection := range []struct {
		name, model, code string
		attachment        v3uploads.Upload
	}{
		{"text model image", "fixture:text", "attachment_model_unsupported", image},
		{"image model PDF", "fixture:image", "attachment_model_unsupported", pdf},
		{"unreadable link", "fixture:media", "attachment_link_unreadable", unreadableLink},
		{"foreign owner", "fixture:media", "invalid_attachment", foreign},
		{"other environment", "fixture:media", "invalid_attachment", crossEnv},
	} {
		t.Run(rejection.name, func(t *testing.T) {
			status, response := postMessage(created.ID, "test", ownerToken, "rejected fixture", rejection.model, rejection.attachment)
			if status != http.StatusUnprocessableEntity || !strings.Contains(string(response), rejection.code) {
				t.Fatalf("rejection status=%d body=%s", status, response)
			}
			assertNoTurns(created.ID)
		})
	}

	alias := paste
	alias.Name = "Pasted text · 3 lines" // Frozen composer changes only the display name.
	status, body = postMessage(created.ID, "test", ownerToken, "inspect all attachments", "fixture:media", alias, textFile, image, pdf, link)
	var accepted struct {
		Message Message `json:"message"`
		ReplyID string  `json:"reply_id"`
	}
	if status != http.StatusAccepted || json.Unmarshal(body, &accepted) != nil || accepted.ReplyID == "" || accepted.Message.ID == "" {
		t.Fatalf("accepted media turn: %d %s", status, body)
	}
	if len(accepted.Message.Attachments) != 5 || strings.Contains(string(body), "fixture log line") || strings.Contains(string(body), "Fixture handbook") || strings.Contains(string(body), base64.StdEncoding.EncodeToString(png)) {
		t.Fatalf("public response leaked attachment bytes or lost refs: %s", body)
	}
	var privateContext string
	if err := db.QueryRow(ctx, `SELECT attachment_context FROM v3_conversation_messages WHERE id=$1`, accepted.Message.ID).Scan(&privateContext); err != nil || !strings.Contains(privateContext, "Fixture handbook") || !strings.Contains(privateContext, link.URL) {
		t.Fatalf("accepted turn did not freeze fetched Markdown privately: %q err=%v", privateContext, err)
	}
	var canonical v3uploads.Upload
	if err := json.Unmarshal(accepted.Message.Attachments[0], &canonical); err != nil || canonical != paste {
		t.Fatalf("paste alias was not canonicalized: got=%+v want=%+v err=%v", canonical, paste, err)
	}
	select {
	case <-runtime.started:
	case <-time.After(3 * time.Second):
		t.Fatal("fake runtime did not receive the accepted turn")
	}
	request := runtime.firstRequest()
	if request.ModelID != "fixture:media" || len(request.Messages) == 0 {
		t.Fatalf("model selection/request: %+v", request)
	}
	last := request.Messages[len(request.Messages)-1]
	if len(last.Media) != 2 || last.Media[0].Kind != "image" || last.Media[0].Name != image.Name || last.Media[0].MIME != image.MIME || !bytes.Equal(last.Media[0].Data, png) || last.Media[1].Kind != "pdf" || last.Media[1].Name != pdf.Name || last.Media[1].MIME != pdf.MIME || !bytes.Equal(last.Media[1].Data, pdfBytes) {
		t.Fatalf("fake model did not receive exact image/PDF bytes: %+v", last.Media)
	}
	if !strings.Contains(last.Content, `alpha\nbeta\ngamma`) || !strings.Contains(last.Content, `fixture log line\n`) || !strings.Contains(last.Content, link.URL) || !strings.Contains(last.Content, "Fixture handbook") {
		t.Fatalf("model text lost uploaded or fetched Markdown content: %q", last.Content)
	}
	if linkHits.Load() != 1 {
		t.Fatalf("link fetches=%d, want one bounded fixture fetch", linkHits.Load())
	}
	status, body = postMessage(created.ID, "test", ownerToken, "concurrent link", "fixture:media", link)
	if status != http.StatusConflict || !strings.Contains(string(body), "reply_in_progress") {
		t.Fatalf("concurrent send: %d %s", status, body)
	}
	if linkHits.Load() != 1 {
		t.Fatalf("busy second turn refetched its link: fetches=%d", linkHits.Load())
	}
	close(runtime.release)
	events, err := readReply(created.ID, ownerToken, accepted.ReplyID)
	if err != nil || len(events) < 3 || events[0].typ != "message.started" || events[len(events)-1].typ != "message.completed" {
		t.Fatalf("pinned media stream=%+v err=%v", events, err)
	}
	completedID := events[len(events)-1].message
	if completedID == "" || len(events[len(events)-1].run) == 0 {
		t.Fatalf("completed media reply lacks message ID or reported run: %+v", events[len(events)-1])
	}
	var leakedEvent bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_conversation_events WHERE reply_id=$1 AND payload::text LIKE '%Fixture handbook%')`, accepted.ReplyID).Scan(&leakedEvent); err != nil || leakedEvent {
		t.Fatalf("private fetched Markdown leaked into SSE: leaked=%t err=%v", leakedEvent, err)
	}
	// Later turns must reconstruct the original media from the accepted turn,
	// even after its upload records are gone. References in the public message
	// remain historical display data; they are not fetched again for the model.
	deleted, err := db.Exec(ctx, `DELETE FROM v3_uploads WHERE id=$1 OR id=$2 OR id=$3`, image.ID, pdf.ID, link.ID)
	if err != nil || deleted.RowsAffected() != 3 {
		t.Fatalf("remove original media uploads: rows=%d err=%v", deleted.RowsAffected(), err)
	}
	status, body = postMessage(created.ID, "test", ownerToken, "text-only follow up cannot read image history", "fixture:text")
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(body), "attachment_model_unsupported") {
		t.Fatalf("incapable model accepted media history: %d %s", status, body)
	}
	var messagesAfterReject, repliesAfterReject int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_messages WHERE conversation_id=$1`, created.ID).Scan(&messagesAfterReject); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_replies WHERE conversation_id=$1`, created.ID).Scan(&repliesAfterReject); err != nil {
		t.Fatal(err)
	}
	if messagesAfterReject != 2 || repliesAfterReject != 1 {
		t.Fatalf("incapable model persisted a turn: messages=%d replies=%d", messagesAfterReject, repliesAfterReject)
	}

	status, body = postMessage(created.ID, "test", ownerToken, "fail after acceptance", "fixture:media")
	var failed struct {
		ReplyID string `json:"reply_id"`
	}
	if status != http.StatusAccepted || json.Unmarshal(body, &failed) != nil || failed.ReplyID == "" {
		t.Fatalf("accepted failing turn: %d %s", status, body)
	}
	failureEvents, err := readReply(created.ID, ownerToken, failed.ReplyID)
	if err != nil || len(failureEvents) < 3 || failureEvents[0].typ != "message.started" || failureEvents[len(failureEvents)-1].typ != "message.completed" {
		t.Fatalf("renderable failure stream=%+v err=%v", failureEvents, err)
	}
	firstHistory := runtime.lastRequest().Messages
	if len(firstHistory) < 2 || len(firstHistory[0].Media) != 2 || !bytes.Equal(firstHistory[0].Media[0].Data, png) || !bytes.Equal(firstHistory[0].Media[1].Data, pdfBytes) || !strings.Contains(firstHistory[0].Content, "Fixture handbook") {
		t.Fatalf("first capable follow-up lost original media after upload deletion: %+v", firstHistory)
	}
	status, body = postMessage(created.ID, "test", ownerToken, "retry after failure", "fixture:media")
	var retried struct {
		ReplyID string `json:"reply_id"`
	}
	if status != http.StatusAccepted || json.Unmarshal(body, &retried) != nil || retried.ReplyID == "" {
		t.Fatalf("retry after failure: %d %s", status, body)
	}
	retryEvents, err := readReply(created.ID, ownerToken, retried.ReplyID)
	if err != nil || retryEvents[len(retryEvents)-1].typ != "message.completed" {
		t.Fatalf("retry stream=%+v err=%v", retryEvents, err)
	}
	history := runtime.lastRequest().Messages
	if len(history) < 3 || len(history[0].Media) != 2 || !bytes.Equal(history[0].Media[0].Data, png) || !bytes.Equal(history[0].Media[1].Data, pdfBytes) || !strings.Contains(history[0].Content, "Fixture handbook") {
		t.Fatalf("later model turn did not replay persisted media history: %+v", history)
	}
	replayed, err := readReply(created.ID, ownerToken, accepted.ReplyID)
	if err != nil || replayed[len(replayed)-1].message != completedID {
		t.Fatalf("pinned replay changed after newer turns: %+v err=%v", replayed, err)
	}

	status, body = call(http.MethodGet, "/api/v1/conversations/"+created.ID, "test", ownerToken, "", nil)
	var detail struct {
		Messages []struct {
			Role        string             `json:"role"`
			Parts       []json.RawMessage  `json:"parts"`
			Attachments []v3uploads.Upload `json:"attachments"`
		} `json:"messages"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &detail) != nil || len(detail.Messages) != 6 || strings.Contains(string(body), "Fixture handbook") {
		t.Fatalf("persisted conversation detail: %d %s", status, body)
	}
	var failureStatusPart bool
	for _, part := range detail.Messages[3].Parts {
		var marker struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(part, &marker) == nil && marker.Type == "status" {
			failureStatusPart = true
		}
	}
	if len(detail.Messages[0].Attachments) != 5 || detail.Messages[0].Attachments[0] != paste || !failureStatusPart {
		t.Fatalf("detail lost canonical refs or failure status: %+v", detail.Messages)
	}
	var failureState, retryState string
	if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_messages WHERE conversation_id=$1 ORDER BY sort_order OFFSET 3 LIMIT 1`, created.ID).Scan(&failureState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_messages WHERE conversation_id=$1 ORDER BY sort_order OFFSET 5 LIMIT 1`, created.ID).Scan(&retryState); err != nil {
		t.Fatal(err)
	}
	if failureState != "failed" || retryState != "completed" {
		t.Fatalf("persisted failure/retry states=%q/%q", failureState, retryState)
	}
	if linkHits.Load() != 1 {
		t.Fatalf("link was fetched again while replaying history: fetches=%d", linkHits.Load())
	}
	var acceptedAudits, successfulReplies, failedReplies, uploadAudits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.conversation.message' AND target=$2 AND outcome='accepted'`, owner.ID, created.ID).Scan(&acceptedAudits); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.conversation.reply' AND outcome='completed'`, owner.ID).Scan(&successfulReplies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.conversation.reply' AND outcome='failed'`, owner.ID).Scan(&failedReplies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.upload.created' AND outcome='success'`, owner.ID).Scan(&uploadAudits); err != nil {
		t.Fatal(err)
	}
	if acceptedAudits != 3 || successfulReplies != 2 || failedReplies != 1 || uploadAudits != 7 {
		t.Fatalf("audit counts: accepted=%d completed=%d failed=%d uploads=%d", acceptedAudits, successfulReplies, failedReplies, uploadAudits)
	}
	var leakedAudit bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM management_audit WHERE actor_id=$1 AND changes::text LIKE '%Fixture handbook%')`, owner.ID).Scan(&leakedAudit); err != nil || leakedAudit {
		t.Fatalf("private fetched Markdown leaked into audit: leaked=%t err=%v", leakedAudit, err)
	}
}
