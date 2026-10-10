package v3observe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	"github.com/wans/aiops-canvas/internal/v3conversation"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

type metricAskFixtureRunner struct {
	mu      sync.Mutex
	request inference.Request
	started chan struct{}
	gate    chan struct{}
	once    sync.Once
	noModel atomic.Bool
	calls   atomic.Int32
}

func (r *metricAskFixtureRunner) Models() []inference.Model {
	if r.noModel.Load() {
		return nil
	}
	return []inference.Model{{ID: "fixture-model", Name: "Fixture model", InputBudget: 32768, Streaming: true}}
}

func (r *metricAskFixtureRunner) Run(ctx context.Context, request inference.Request, emit func(inference.Event) error) (string, error) {
	r.calls.Add(1)
	r.mu.Lock()
	r.request = request
	r.mu.Unlock()
	if err := emit(inference.Event{Type: "delta", Delta: "Observed rate is 1.25"}); err != nil {
		return "", err
	}
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.gate:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "Observed rate is 1.25", nil
}

func (r *metricAskFixtureRunner) lastRequest() inference.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.request
}

// Covers the complete registered write/read path with an isolated database,
// exact reviewed Prometheus query and deterministic model. Neither provider is
// a paid or shared service.
func TestMetricsAskRegisteredRoutePersistsSSEAndRefresh(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_metrics_ask_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, dropErr := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); dropErr != nil {
			t.Error(dropErr)
		}
		adminDB.Close()
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
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-metrics-ask-password"); err != nil {
		t.Fatal(err)
	}
	admin, session, err := st.Login(ctx, "admin@example.invalid", "isolated-metrics-ask-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}

	const expression = `sum(rate(http_requests_total{application="app",environment="dev"}[5m]))`
	var providerCalls atomic.Int32
	var empty atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" || r.FormValue("query") != expression {
			t.Errorf("unexpected provider request %s", r.URL)
			http.Error(w, "bad fixture query", 400)
			return
		}
		providerCalls.Add(1)
		if empty.Load() {
			_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
			return
		}
		end, parseErr := time.Parse(time.RFC3339Nano, r.FormValue("end"))
		if parseErr != nil {
			t.Errorf("invalid provider window: %v", parseErr)
			http.Error(w, "bad fixture window", 400)
			return
		}
		start, parseErr := time.Parse(time.RFC3339Nano, r.FormValue("start"))
		if parseErr != nil || end.Sub(start) != time.Hour || r.FormValue("step") != "120" {
			t.Errorf("wrong selected one-hour window: %s", r.URL)
			http.Error(w, "bad fixture window", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "matrix", "result": []any{
				map[string]any{"metric": map[string]string{"private_label": "hidden"}, "values": []any{[]any{end.Unix(), "1.25"}}},
			}},
		})
	}))
	defer provider.Close()
	if _, err = db.Exec(ctx, `INSERT INTO v3_connectors
 (id,environment_id,provider,name,category,endpoint,auth_method,tools,enabled)
 VALUES('metric-connector','dev','prometheus','Metrics','observe',$1,'none',
 '[{"name":"query_metrics","enabled":true}]',true)`, provider.URL); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "reviewed-queries.json")
	config := fmt.Sprintf(`{"queries":[{"connector_id":"metric-connector","application_id":"app","environment_id":"dev","resource_id":"app-metrics","query_id":"request_rate","expression":%q}]}`, expression)
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3integrations.LoadPrometheusQueryCatalog(configPath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := v3integrations.NewPrometheusReader(db, "nonprod", nil, catalog)
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	runner := &metricAskFixtureRunner{started: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-runner.gate:
		default:
			close(runner.gate)
		}
	})
	background := func(ctx context.Context, ownerID, env string) error {
		member, err := st.Member(ctx, ownerID)
		if err != nil || member.Disabled || member.DeletedAt != nil {
			return store.ErrForbidden
		}
		return policy.Authorize(ctx, member.User, rbac.V3Decision{Module: "conversation", Action: "change", EnvironmentID: env})
	}
	backgroundTx := func(ctx context.Context, tx pgx.Tx, ownerID, env string) error {
		member, err := st.MemberTx(ctx, tx, ownerID)
		if err != nil || member.Disabled || member.DeletedAt != nil {
			return store.ErrForbidden
		}
		return policy.AuthorizeWithTx(ctx, tx, member.User, rbac.V3Decision{Module: "conversation", Action: "change", EnvironmentID: env})
	}
	chat := v3conversation.New(db, runner, v3conversation.Options{
		CurrentUser: httpapi.CurrentUser, RefreshSession: st.Session,
		Environment: func(r *http.Request) (string, error) {
			scope, ok := httpapi.V3ScopeFromContext(r.Context())
			if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
				return "", v3conversation.ErrEnvironment
			}
			return scope.EnvironmentIDs[0], nil
		},
		AllowedEnvironments:     func(context.Context, core.User) ([]string, error) { return []string{"dev"}, nil },
		DefaultModel:            func(context.Context, string, string) (string, error) { return "fixture-model", nil },
		AuthorizeBackgroundTurn: background, AuthorizeBackgroundTurnTx: backgroundTx,
		AuthorizeMetricsSource: func(ctx context.Context, ownerID, env string, source v3conversation.MetricsSource) error {
			return reader.AuthorizeMetricSourceVersion(ctx, ownerID, env, source.ApplicationID, source.BindingID, source.ConnectorID, source.ResourceID, source.QueryID, source.Fingerprint)
		},
		AuthorizeMetricsSourceTx: func(ctx context.Context, tx pgx.Tx, ownerID, env string, source v3conversation.MetricsSource) error {
			return reader.AuthorizeMetricSourceVersionTx(ctx, tx, ownerID, env, source.ApplicationID, source.BindingID, source.ConnectorID, source.ResourceID, source.QueryID, source.Fingerprint)
		},
	})
	api := httpapi.New(st, runner, true)
	chat.RegisterV3(api)
	observe := New(st, policy)
	observe.MetricReader = reader
	observe.MetricsConversation = chat
	observe.Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(api)
	defer remote.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, token, body string) (int, string) {
		t.Helper()
		req, reqErr := http.NewRequest(method, remote.URL+path, strings.NewReader(body))
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("X-AIOps-Environment", "dev")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		res, callErr := client.Do(req)
		if callErr != nil {
			t.Fatal(callErr)
		}
		defer res.Body.Close()
		payload, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return res.StatusCode, string(payload)
	}
	const ask = `{"service":"app","view":"request_rate","range":"1h","question":"What does this rate show?","request_id":"metric-ask-fixture-0001"}`
	if status, _ := call("POST", "/api/v1/metrics/ask", session, ask); status != 503 {
		t.Fatalf("unbound source accepted: %d", status)
	}
	if providerCalls.Load() != 0 {
		t.Fatal("unbound source queried provider")
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('metric-target','app','dev','metric-connector','metric_resource','app-metrics',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if status, _ := call("POST", "/api/v1/metrics/ask", "", ask); status != 401 {
		t.Fatalf("unauthenticated ask: %d", status)
	}
	runner.noModel.Store(true)
	if status, body := call("POST", "/api/v1/metrics/ask", session,
		strings.Replace(ask, "metric-ask-fixture-0001", "metric-ask-fixture-no-model", 1)); status != 503 || !strings.Contains(body, `model_unavailable`) {
		t.Fatalf("missing model=%d %s", status, body)
	}
	runner.noModel.Store(false)
	var questionCount int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_metrics_questions`).Scan(&questionCount); err != nil || questionCount != 0 {
		t.Fatalf("missing-model question rows=%d err=%v", questionCount, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_conversations WHERE context_ref='service:app'`).Scan(&questionCount); err != nil || questionCount != 0 {
		t.Fatalf("missing-model conversation rows=%d err=%v", questionCount, err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_connector_targets
 (id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('metric-target-ambiguous','app','dev','metric-connector','metric_resource','other-metrics',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	beforeAmbiguous := providerCalls.Load()
	if status, body := call("POST", "/api/v1/metrics/ask", session,
		strings.Replace(ask, "metric-ask-fixture-0001", "metric-ask-fixture-ambiguous", 1)); status != 409 || !strings.Contains(body, `ambiguous_binding`) {
		t.Fatalf("ambiguous target=%d %s", status, body)
	}
	if providerCalls.Load() != beforeAmbiguous {
		t.Fatal("ambiguous target queried provider")
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_metrics_questions`).Scan(&questionCount); err != nil || questionCount != 0 {
		t.Fatalf("ambiguous-target question rows=%d err=%v", questionCount, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_conversations WHERE context_ref='service:app'`).Scan(&questionCount); err != nil || questionCount != 0 {
		t.Fatalf("ambiguous-target conversation rows=%d err=%v", questionCount, err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='metric-target-ambiguous'`); err != nil {
		t.Fatal(err)
	}
	beforeSuccess := providerCalls.Load()
	status, body := call("POST", "/api/v1/metrics/ask", session, ask)
	var accepted struct {
		ConversationID string `json:"conversation_id"`
	}
	if status != 201 || json.Unmarshal([]byte(body), &accepted) != nil || accepted.ConversationID == "" {
		t.Fatalf("ask=%d %s", status, body)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	request := runner.lastRequest()
	if len(request.Tools) != 0 {
		t.Fatalf("metric question offered extra read tools: %d", len(request.Tools))
	}
	if len(request.Messages) == 0 || !strings.Contains(request.Messages[len(request.Messages)-1].Content, "1.25") ||
		!strings.Contains(request.Messages[len(request.Messages)-1].Content, "What does this rate show?") {
		t.Fatalf("model missed question or captured metric: %+v", request.Messages)
	}
	for _, private := range []string{"private_label", "hidden", expression, provider.URL} {
		for _, message := range request.Messages {
			if strings.Contains(message.Content, private) {
				t.Fatalf("private provider detail %q reached model request", private)
			}
		}
	}
	var replyID, attachment string
	if err = db.QueryRow(ctx, `SELECT r.id,m.attachment_context FROM v3_conversation_replies r
 JOIN v3_conversation_messages m ON m.id=r.user_message_id WHERE r.conversation_id=$1`, accepted.ConversationID).Scan(&replyID, &attachment); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(attachment, `request_rate`) || !strings.Contains(attachment, `step_seconds`) || !strings.Contains(attachment, `120`) ||
		!strings.Contains(attachment, `1.25`) {
		t.Fatalf("snapshot missing bound view/window/value: %s", attachment)
	}
	if status, duplicate := call("POST", "/api/v1/metrics/ask", session, ask); status != 200 || !strings.Contains(duplicate, accepted.ConversationID) {
		t.Fatalf("idempotent retry=%d %s", status, duplicate)
	}
	if providerCalls.Load() != beforeSuccess+1 {
		t.Fatalf("retry called provider %d times", providerCalls.Load())
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("retry started %d model calls", runner.calls.Load())
	}
	var count int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_conversations WHERE id=$1`, accepted.ConversationID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("conversation count=%d err=%v", count, err)
	}
	if conflictStatus, _ := call("POST", "/api/v1/metrics/ask", session,
		strings.Replace(ask, "What does this rate show?", "Different question", 1)); conflictStatus != 409 {
		t.Fatalf("request ID conflict=%d", conflictStatus)
	}
	close(runner.gate)
	deadline := time.Now().Add(4 * time.Second)
	for {
		var state string
		if err = db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id=$1`, replyID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reply never completed: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	conversationPath := "/api/v1/conversations/" + accepted.ConversationID
	if status, detail := call("GET", conversationPath, session, ""); status != 200 ||
		!strings.Contains(detail, "Observed rate is 1.25") || !strings.Contains(detail, "What does this rate show?") {
		t.Fatalf("refresh detail=%d %s", status, detail)
	} else {
		for _, private := range []string{"private_label", "hidden", expression, provider.URL} {
			if strings.Contains(detail, private) {
				t.Fatalf("private provider detail %q leaked through public conversation detail", private)
			}
		}
	}
	if status, events := call("GET", conversationPath+"/events?reply_id="+replyID, session, ""); status != 200 ||
		!strings.Contains(events, "message.completed") || !strings.Contains(events, "text.delta") {
		t.Fatalf("SSE replay=%d %s", status, events)
	}
	// Two first attempts with the same request ID may both read the provider,
	// but the database claim must yield one conversation and one model turn.
	concurrentAsk := strings.Replace(ask, "metric-ask-fixture-0001", "metric-ask-concurrent-0003", 1)
	type askResponse struct {
		status int
		body   string
	}
	results := make(chan askResponse, 2)
	var concurrent sync.WaitGroup
	for i := 0; i < 2; i++ {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			status, body := call("POST", "/api/v1/metrics/ask", session, concurrentAsk)
			results <- askResponse{status: status, body: body}
		}()
	}
	concurrent.Wait()
	close(results)
	var simultaneousID string
	for response := range results {
		var item struct {
			ConversationID string `json:"conversation_id"`
		}
		if (response.status != 200 && response.status != 201) ||
			json.Unmarshal([]byte(response.body), &item) != nil || item.ConversationID == "" {
			t.Fatalf("concurrent ask=%d %s", response.status, response.body)
		}
		if simultaneousID != "" && item.ConversationID != simultaneousID {
			t.Fatalf("concurrent request created two conversations: %s and %s", simultaneousID, item.ConversationID)
		}
		simultaneousID = item.ConversationID
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_metrics_questions`).Scan(&questionCount); err != nil || questionCount != 2 {
		t.Fatalf("concurrent request records=%d err=%v", questionCount, err)
	}
	modelDeadline := time.Now().Add(3 * time.Second)
	for runner.calls.Load() < 2 && time.Now().Before(modelDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runner.calls.Load() != 2 {
		t.Fatalf("concurrent request started %d model calls, want two total", runner.calls.Load())
	}
	// Missing samples must not mint a second conversation or ask the model.
	empty.Store(true)
	if status, _ := call("POST", "/api/v1/metrics/ask", session,
		strings.Replace(ask, "metric-ask-fixture-0001", "metric-ask-fixture-0002", 1)); status != 503 {
		t.Fatalf("no samples accepted: %d", status)
	}
	var allQuestions int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_metrics_questions`).Scan(&allQuestions); err != nil || allQuestions != 2 {
		t.Fatalf("question records=%d err=%v", allQuestions, err)
	}
	if runner.calls.Load() != 2 {
		t.Fatalf("missing samples started %d model calls", runner.calls.Load())
	}
	// Revoking the explicit source hides the saved conversation on both retry
	// and refresh; it cannot be laundered through the idempotency key.
	if _, err = db.Exec(ctx, `DELETE FROM v3_connector_targets WHERE id='metric-target'`); err != nil {
		t.Fatal(err)
	}
	if status, _ := call("POST", "/api/v1/metrics/ask", session, ask); status == 200 || status == 201 {
		t.Fatalf("revoked source replay accepted: %d", status)
	}
	if status, _ := call("GET", conversationPath, session, ""); status == 200 {
		t.Fatalf("revoked source detail accepted: %d", status)
	}
}
