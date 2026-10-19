package v3conversation

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/jackc/pgx/v5"
)

type metricsQuestionRuntime struct{}

func (metricsQuestionRuntime) Models() []inference.Model {
	return []inference.Model{{ID: "fixture-model", Name: "Fixture", InputBudget: 8192}}
}
func (metricsQuestionRuntime) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	return "", errors.New("worker must be stopped in this test")
}

type metricsCaptureRuntime struct{ requests chan inference.Request }

func (*metricsCaptureRuntime) Models() []inference.Model {
	return []inference.Model{{ID: "fixture-model", Name: "Fixture", InputBudget: 8192, Tools: true}}
}
func (r *metricsCaptureRuntime) Run(_ context.Context, request inference.Request, _ func(inference.Event) error) (string, error) {
	r.requests <- request
	return "Bounded answer", nil
}

func metricsQuestionTestService(t *testing.T) (*Service, func(bool)) {
	return metricsQuestionTestServiceWithConns(t, 0)
}

func metricsQuestionTestServiceWithConns(t *testing.T, maxConns int32) (*Service, func(bool)) {
	t.Helper()
	db := isolatedConversationDBWithConns(t, maxConns)
	_, err := db.Exec(context.Background(), `
		ALTER TABLE v3_conversations ADD COLUMN title text NOT NULL DEFAULT '', ADD COLUMN context_ref text NOT NULL DEFAULT '', ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		ALTER TABLE v3_conversation_messages ALTER COLUMN state SET DEFAULT 'completed';
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT '', ADD COLUMN model_id text NOT NULL DEFAULT '', ADD COLUMN reasoning text NOT NULL DEFAULT '';
		ALTER TABLE management_audit ALTER COLUMN actor_kind SET DEFAULT 'user';
		CREATE VIEW v3_effective_role_grants AS
			SELECT a.user_id,g.module,g.action,g.scope_kind,g.scope_id FROM rbac_user_roles a
			JOIN v3_role_grants g ON g.role_id=a.role_id;
		INSERT INTO rbac_roles(id) VALUES('metrics-owner-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('persisted-owner','metrics-owner-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
			('metrics-owner-role','environment','view','environment','test'),
			('metrics-owner-role','conversation','view','environment','test'),
			('metrics-owner-role','conversation','change','environment','test');`)
	if err != nil {
		t.Fatal(err)
	}
	shutdown, cancel := context.WithCancel(context.Background())
	cancel() // accepted reply remains durable; no fake model execution.
	var mu sync.Mutex
	allowed := true
	check := func(_ context.Context, _ pgx.Tx, owner, env string, source MetricsSource) error {
		mu.Lock()
		defer mu.Unlock()
		if !allowed || owner != "persisted-owner" || env != "test" ||
			source.ApplicationID != "checkout" || source.QueryID != "request_rate" || source.Fingerprint != "fixture-fingerprint" {
			return ErrForbidden
		}
		return nil
	}
	svc := New(db, metricsQuestionRuntime{}, Options{
		Shutdown: shutdown,
		RefreshSession: func(_ context.Context, token string) (core.User, error) {
			if token != "test-session" {
				return core.User{}, ErrForbidden
			}
			return core.User{ID: "persisted-owner"}, nil
		},
		DefaultModel:              func(context.Context, string, string) (string, error) { return "fixture-model", nil },
		AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil },
		AuthorizeBackgroundTurn:   func(context.Context, string, string) error { return nil },
		AuthorizeMetricsSourceTx:  check,
		AuthorizeMetricsSource: func(_ context.Context, owner, env string, source MetricsSource) error {
			return check(context.Background(), nil, owner, env, source)
		},
	})
	return svc, func(v bool) { mu.Lock(); allowed = v; mu.Unlock() }
}

func metricFixtureInput() MetricsQuestionInput {
	end := time.Now().UTC().Truncate(time.Second)
	return MetricsQuestionInput{
		Owner: core.User{ID: "persisted-owner", Name: "Owner"}, SessionToken: "test-session",
		EnvironmentID: "test", Source: MetricsSource{ApplicationID: "checkout", BindingID: "binding",
			ConnectorID: "prom", ResourceID: "checkout", QueryID: "request_rate", Fingerprint: "fixture-fingerprint"},
		View: "request_rate", Range: "15m", Question: "Why did traffic fall?", RequestID: "metrics-idempotency-1",
		WindowStart: end.Add(-15 * time.Minute), WindowEnd: end,
		EvidenceSnapshot: `{"source":"prom","value":12}`,
	}
}

func TestMetricsQuestionAtomicRetryConflictAndRevocation(t *testing.T) {
	svc, setAllowed := metricsQuestionTestService(t)
	ctx := context.Background()
	in := metricFixtureInput()
	var results [2]MetricsQuestionResult
	var errs [2]error
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			results[i], errs[i] = svc.StartMetricsQuestion(ctx, in)
		}(i)
	}
	group.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent start %d: %v", i, err)
		}
	}
	if results[0].ConversationID == "" || results[0].ConversationID != results[1].ConversationID || results[0].ReplyID != results[1].ReplyID || results[0].Replayed == results[1].Replayed {
		t.Fatalf("duplicate request results: %+v", results)
	}
	var conversations, messages, replies, asks int
	if err := svc.db.QueryRow(ctx, `SELECT count(*) FROM v3_conversations WHERE owner_id='persisted-owner' AND id<> 'c'`).Scan(&conversations); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_messages WHERE conversation_id=$1`, results[0].ConversationID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_replies WHERE conversation_id=$1`, results[0].ConversationID).Scan(&replies); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.QueryRow(ctx, `SELECT count(*) FROM v3_metrics_questions WHERE owner_id='persisted-owner'`).Scan(&asks); err != nil {
		t.Fatal(err)
	}
	if conversations != 1 || messages != 2 || replies != 1 || asks != 1 {
		t.Fatalf("partial or duplicate write: conversations=%d messages=%d replies=%d asks=%d", conversations, messages, replies, asks)
	}
	var publicParts, privateContext string
	if err := svc.db.QueryRow(ctx, `SELECT parts::text,attachment_context FROM v3_conversation_messages
		WHERE conversation_id=$1 AND role='user'`, results[0].ConversationID).Scan(&publicParts, &privateContext); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(publicParts, "source") || !strings.Contains(privateContext, "untrusted data") ||
		!strings.Contains(privateContext, "Sparse samples cannot establish") || !strings.Contains(privateContext, "value") {
		t.Fatalf("evidence leaked or missing: parts=%s context=%s", publicParts, privateContext)
	}
	lookup := in.lookup()
	prior, found, err := svc.FindMetricsQuestion(ctx, lookup)
	if err != nil || !found || prior.ConversationID != results[0].ConversationID || !prior.Replayed {
		t.Fatalf("recovery: %+v found=%v err=%v", prior, found, err)
	}
	svc.runtime = nil
	retryWithoutNewEvidence := in
	retryWithoutNewEvidence.EvidenceSnapshot = ""
	if replay, err := svc.StartMetricsQuestion(ctx, retryWithoutNewEvidence); err != nil || !replay.Replayed || replay.ConversationID != prior.ConversationID {
		t.Fatalf("retry after model and metrics loss: %+v err=%v", replay, err)
	}
	other := in
	other.Question = "Different question"
	if _, err := svc.StartMetricsQuestion(ctx, other); !errors.Is(err, ErrMetricsRequestConflict) {
		t.Fatalf("request ID conflict: %v", err)
	}
	setAllowed(false)
	if _, _, err := svc.FindMetricsQuestion(ctx, lookup); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked retry: %v", err)
	}
	if err := svc.authorizeActiveMetricsTurn(ctx, results[0].ConversationID, in.Owner.ID, in.EnvironmentID, in.SessionToken); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked worker: %v", err)
	}
}

func TestMetricsQuestionMissingModelRollsBackEntireRequest(t *testing.T) {
	svc, _ := metricsQuestionTestService(t)
	svc.runtime = nil
	in := metricFixtureInput()
	_, err := svc.StartMetricsQuestion(context.Background(), in)
	if !errors.Is(err, ErrModel) {
		t.Fatalf("missing model: %v", err)
	}
	var count int
	if err := svc.db.QueryRow(context.Background(), `SELECT count(*) FROM v3_metrics_questions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("request recorded without model: count=%d err=%v", count, err)
	}
}

func TestMetricsQuestionModelReceivesOnlySavedEvidenceWithoutGenericTools(t *testing.T) {
	svc, _ := metricsQuestionTestService(t)
	runtime := &metricsCaptureRuntime{requests: make(chan inference.Request, 1)}
	svc.runtime = runtime
	svc.opts.Shutdown = context.Background()
	svc.opts.ToolsForTurn = func(context.Context, string, string, string) ([]inference.EvidenceTool, error) {
		return []inference.EvidenceTool{{Name: "read_other_service", Read: func(context.Context, string) (inference.Evidence, error) {
			return inference.Evidence{Content: "outside scope"}, nil
		}}}, nil
	}
	in := metricFixtureInput()
	in.RequestID = "metrics-tool-boundary"
	result, err := svc.StartMetricsQuestion(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-runtime.requests:
		if len(request.Tools) != 0 || len(request.Messages) != 1 ||
			!strings.Contains(request.Messages[0].Content, "untrusted data") ||
			!strings.Contains(request.Messages[0].Content, "value") {
			t.Fatalf("model scope: tools=%d messages=%+v", len(request.Tools), request.Messages)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("model request was not started")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var state string
		if err := svc.db.QueryRow(context.Background(), `SELECT state FROM v3_conversation_replies WHERE id=$1`, result.ReplyID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("model reply did not finish: %s", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMetricsQuestionSourceOutageFailsListAndDetailAuthorization(t *testing.T) {
	svc, _ := metricsQuestionTestService(t)
	in := metricFixtureInput()
	in.RequestID = "metrics-source-outage"
	result, err := svc.StartMetricsQuestion(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	svc.opts.AuthorizeMetricsSource = func(context.Context, string, string, MetricsSource) error {
		return &v3integrations.PrometheusReadError{Code: "storage_unavailable"}
	}
	if _, err := svc.visibleMetricsConversations(context.Background(), in.Owner.ID, []string{"test"}); !errors.Is(err, ErrMetricsSourceUnavailable) {
		t.Fatalf("list source outage: %v", err)
	}
	if err := svc.authorizeMetricsSource(context.Background(), result.ConversationID, in.Owner.ID, in.EnvironmentID); !errors.Is(err, ErrMetricsSourceUnavailable) {
		t.Fatalf("detail source outage: %v", err)
	}
	if err := metricAuthorizationError(&v3integrations.PrometheusReadError{Code: "source_changed"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("actual source change should deny: %v", err)
	}
}

func TestMetricsQuestionModelPreferenceUsesSingleConnectionPool(t *testing.T) {
	svc, _ := metricsQuestionTestServiceWithConns(t, 1)
	svc.opts.DefaultModel = func(ctx context.Context, _, _ string) (string, error) {
		var selected string
		err := svc.db.QueryRow(ctx, `SELECT 'fixture-model'`).Scan(&selected)
		return selected, err
	}
	in := metricFixtureInput()
	in.RequestID = "metrics-single-connection"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if result, err := svc.StartMetricsQuestion(ctx, in); err != nil || result.ConversationID == "" {
		t.Fatalf("one connection result=%+v err=%v", result, err)
	}
}

func TestMetricsQuestionMissingSourceRecordNeverBecomesOrdinaryChat(t *testing.T) {
	svc, _ := metricsQuestionTestService(t)
	in := metricFixtureInput()
	in.RequestID = "metrics-missing-record"
	result, err := svc.StartMetricsQuestion(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(context.Background(), `DELETE FROM v3_metrics_questions WHERE conversation_id=$1`, result.ConversationID); err != nil {
		t.Fatal(err)
	}
	backed, err := svc.metricsBackedConversation(context.Background(), result.ConversationID)
	if err != nil || !backed {
		t.Fatalf("lost source marker: backed=%v err=%v", backed, err)
	}
	if err := svc.authorizeMetricsSource(context.Background(), result.ConversationID, in.Owner.ID, in.EnvironmentID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing persisted source: %v", err)
	}
}

func TestMetricsQuestionRecoveryClosesReplyWhenSourceAuthorizationUnavailable(t *testing.T) {
	svc, _ := metricsQuestionTestService(t)
	ctx := context.Background()
	_, err := svc.db.Exec(ctx, `UPDATE v3_conversations SET metrics_source_required=true WHERE id='c';
		UPDATE v3_conversation_replies SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r';
		INSERT INTO v3_metrics_questions(owner_id,request_id,conversation_id,initial_reply_id,environment_id,
			application_id,view_id,range_id,payload_hash,binding_id,connector_id,resource_id,query_id,
			source_fingerprint,window_start,window_end,evidence_sha256)
		VALUES('persisted-owner','recovery','c','r','test','checkout','request_rate','15m','hash',
			'binding','prom','checkout','request_rate','fixture-fingerprint',now()-interval '15 minutes',now(),'digest')`)
	if err != nil {
		t.Fatal(err)
	}
	svc.opts.AuthorizeMetricsSourceTx = func(context.Context, pgx.Tx, string, string, MetricsSource) error {
		return &v3integrations.PrometheusReadError{Code: "storage_unavailable"}
	}
	if err := svc.recoverOne(ctx, "c", "r"); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := svc.db.QueryRow(ctx, `SELECT state,error FROM v3_conversation_replies WHERE id='r'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || reason != ErrForbidden.Error() {
		t.Fatalf("source outage recovery state=%q reason=%q", state, reason)
	}
}

func TestMetricsQuestionRequiresCurrentViewGrantForCreationAndReplay(t *testing.T) {
	svc, _ := metricsQuestionTestService(t)
	ctx := context.Background()
	in := metricFixtureInput()
	in.RequestID = "metrics-change-without-view"
	removeView := `DELETE FROM v3_role_grants WHERE role_id='metrics-owner-role' AND module='conversation' AND action='view'`
	addView := `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
		VALUES('metrics-owner-role','conversation','view','environment','test')`
	if _, err := svc.db.Exec(ctx, removeView); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartMetricsQuestion(ctx, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("change-only role should not accept unrecoverable question: %v", err)
	}
	var count int
	if err := svc.db.QueryRow(ctx, `SELECT count(*) FROM v3_metrics_questions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unreadable question persisted: count=%d err=%v", count, err)
	}
	if _, err := svc.db.Exec(ctx, addView); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartMetricsQuestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(ctx, removeView); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.FindMetricsQuestion(ctx, in.lookup()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("replay after view revoke: %v", err)
	}
}

func TestMetricsQuestionSilentRunIsCancelledOnSourceRevoke(t *testing.T) {
	svc, setAllowed := metricsQuestionTestService(t)
	in := metricFixtureInput()
	in.RequestID = "metrics-silent-run-revoke"
	result, err := svc.StartMetricsQuestion(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.Exec(context.Background(), `UPDATE v3_conversation_replies
		SET state='running',lease_expires_at=clock_timestamp()+interval '30 seconds'
		WHERE id=$1`, result.ReplyID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := svc.renewWhileRunning(ctx, cancel, result.ConversationID, result.ReplyID, result.ReplyID,
		in.Owner.ID, in.EnvironmentID, in.SessionToken, true, 100*time.Millisecond)
	defer stop()
	setAllowed(false)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("silent metric run remained active after source revoke")
	}
}

func TestMetricsQuestionListSourceCapReturnsSpecific503(t *testing.T) {
	svc, _ := metricsQuestionTestService(t)
	_, err := svc.db.Exec(context.Background(), `INSERT INTO v3_metrics_questions
		(owner_id,request_id,conversation_id,initial_reply_id,environment_id,application_id,view_id,range_id,
		payload_hash,binding_id,connector_id,resource_id,query_id,source_fingerprint,window_start,window_end,evidence_sha256)
		SELECT 'persisted-owner','cap-request-'||g,'cap-conversation-'||g,'cap-reply-'||g,'test','checkout',
		'request_rate','15m','hash','binding','prom','checkout','request_rate','fixture-fingerprint',
		now()-interval '15 minutes',now(),'digest' FROM generate_series(1,1001) g`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.visibleMetricsConversations(context.Background(), "persisted-owner", []string{"test"})
	if !errors.Is(err, errMetricsListSourceLimit) {
		t.Fatalf("list source limit: %v", err)
	}
	w := httptest.NewRecorder()
	operationError(w, err)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "metrics_list_scope_limit") {
		t.Fatalf("limit HTTP response: status=%d body=%s", w.Code, w.Body.String())
	}
}
