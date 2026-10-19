package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testRuntime struct{ models []inference.Model }

func (r testRuntime) Models() []inference.Model { return r.models }
func (testRuntime) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	return "", errors.New("unused")
}

func TestEnvironmentScopeRejectsAllWrites(t *testing.T) {
	svc := New(nil, nil, Options{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", nil)
	req.Header.Set("X-AIOps-Environment", "all")
	_, err := svc.environment(req, true)
	if !errors.Is(err, ErrViewOnly) {
		t.Fatalf("write in all scope: %v", err)
	}
	env, err := svc.environment(req, false)
	if err != nil || env != "all" {
		t.Fatalf("read in all scope: %q %v", env, err)
	}
}

func TestModelSelectionConfiguredOnly(t *testing.T) {
	svc := New(nil, testRuntime{models: []inference.Model{{ID: "ollama:small", Name: "small"}, {ID: "openai:large", Name: "large"}}}, Options{})
	picked, err := svc.model("", "openai:large")
	if err != nil || picked.ID != "openai:large" {
		t.Fatalf("fallback: %#v %v", picked, err)
	}
	picked, err = svc.model("ollama:small", "openai:large")
	if err != nil || picked.ID != "ollama:small" {
		t.Fatalf("override: %#v %v", picked, err)
	}
	_, err = svc.model("unconfigured", "")
	if !errors.Is(err, ErrModel) {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestPageBounds(t *testing.T) {
	for _, tc := range []struct {
		query string
		code  string
	}{
		{query: "?page=-1", code: "invalid_page"},
		{query: "?page=9223372036854775807", code: "invalid_page"},
		{query: "?page_size=99999", code: "invalid_page_size"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations"+tc.query, nil)
		if _, _, code := pagination(req); code != tc.code {
			t.Fatalf("query=%s code=%q, want %q", tc.query, code, tc.code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations?page=100000&page_size=100", nil)
	page, size, code := pagination(req)
	if code != "" || page != 100000 || size != 100 {
		t.Fatalf("page=%d size=%d code=%q", page, size, code)
	}
}

func TestLastEventCursorRejectsAmbiguousOrInvalidReplayPositions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		want   int64
		valid  bool
	}{
		{name: "initial", valid: true},
		{name: "last committed", values: []string{"42"}, want: 42, valid: true},
		{name: "explicit zero", values: []string{"0"}, valid: true},
		{name: "empty", values: []string{""}},
		{name: "negative", values: []string{"-1"}},
		{name: "non decimal", values: []string{"4x"}},
		{name: "overflow", values: []string{"9223372036854775808"}},
		{name: "duplicate", values: []string{"4", "5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := lastEventCursor(tc.values)
			if got != tc.want || valid != tc.valid {
				t.Fatalf("lastEventCursor(%q) = (%d,%t), want (%d,%t)", tc.values, got, valid, tc.want, tc.valid)
			}
		})
	}
}

func TestAllowedEnvironmentsFailsClosedAndExcludesProduction(t *testing.T) {
	user := core.User{ID: "u1"}
	if _, err := New(nil, nil, Options{}).allowed(context.Background(), user, "test"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing authorization source: %v", err)
	}
	svc := New(nil, nil, Options{AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
		return []string{"prod", "test", "staging"}, nil
	}})
	visible, err := svc.allowed(context.Background(), user, "all")
	if err != nil || len(visible) != 2 || visible[0] != "test" || visible[1] != "staging" {
		t.Fatalf("nonproduction union: %#v %v", visible, err)
	}
	if _, err := svc.allowed(context.Background(), user, "dev"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized environment: %v", err)
	}
}

func TestCompletionSuffixKeepsStreamAndPersistedReplyConsistent(t *testing.T) {
	for _, tc := range []struct {
		streamed, final, suffix string
		wantErr                 bool
	}{
		{"", "complete answer", "complete answer", false},
		{"first", "first answer", " answer", false},
		{"complete answer", "complete answer", "", false},
		{"first", "revised answer", "", true},
	} {
		suffix, err := completionSuffix(tc.streamed, tc.final)
		if suffix != tc.suffix || (err != nil) != tc.wantErr {
			t.Fatalf("streamed=%q final=%q: suffix=%q err=%v", tc.streamed, tc.final, suffix, err)
		}
	}
}

func TestBackgroundAuthorizationFailsClosed(t *testing.T) {
	ctx := context.Background()
	if err := New(nil, nil, Options{}).authorizeBackgroundTurn(ctx, "owner", "test"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing callback: %v", err)
	}
	active := true
	svc := New(nil, nil, Options{AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
		if owner != "owner" || env != "test" {
			t.Fatalf("unexpected persisted scope: %s %s", owner, env)
		}
		if !active {
			return errors.New("disabled or revoked")
		}
		return nil
	}})
	if err := svc.authorizeBackgroundTurn(ctx, "owner", "test"); err != nil {
		t.Fatalf("active owner: %v", err)
	}
	active = false
	if err := svc.authorizeBackgroundTurn(ctx, "owner", "test"); !errors.Is(err, ErrForbidden) || err.Error() != ErrForbidden.Error() {
		t.Fatalf("revoked owner should receive sanitized failure: %v", err)
	}
	if err := svc.authorizeBackgroundTurn(ctx, "", "test"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing owner: %v", err)
	}
}

func TestActiveTurnRequiresTheInitiatingSession(t *testing.T) {
	svc := New(nil, nil, Options{
		RefreshSession: func(_ context.Context, token string) (core.User, error) {
			if token != "active" {
				return core.User{}, store.ErrNotFound
			}
			return core.User{ID: "owner"}, nil
		},
		AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
			if owner != "owner" || env != "test" {
				return ErrForbidden
			}
			return nil
		},
	})
	for _, tc := range []struct {
		owner, token string
		allowed      bool
	}{
		{"owner", "active", true},
		{"owner", "revoked", false},
		{"different-owner", "active", false},
		{"owner", "", false},
	} {
		err := svc.authorizeActiveTurn(context.Background(), tc.owner, "test", tc.token)
		if (err == nil) != tc.allowed {
			t.Fatalf("owner=%s token=%s: %v", tc.owner, tc.token, err)
		}
	}
}

type recordingRuntime struct{ calls int }

func (*recordingRuntime) Models() []inference.Model { return nil }
func (r *recordingRuntime) Run(_ context.Context, _ inference.Request, emit func(inference.Event) error) (string, error) {
	r.calls++
	if err := emit(inference.Event{Type: "delta", Delta: "private output"}); err != nil {
		return "", err
	}
	return "private output", nil
}

type finalOnlyRuntime struct{}

func (*finalOnlyRuntime) Models() []inference.Model { return nil }
func (*finalOnlyRuntime) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	return "private final output", nil
}

func isolatedConversationDB(t *testing.T) *pgxpool.Pool {
	return isolatedConversationDBWithConns(t, 0)
}

func isolatedConversationDBWithConns(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates an isolated random schema")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_v3conversation_" + store.ID()
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
		_, dropErr := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
		if dropErr != nil {
			t.Error(dropErr)
		}
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `
		CREATE TABLE v3_conversations (id text PRIMARY KEY, owner_id text NOT NULL, environment_id text NOT NULL, active_reply_id text, updated_at timestamptz, metrics_source_required boolean NOT NULL DEFAULT false);
		CREATE SEQUENCE v3_conversation_message_order_seq;
		CREATE TABLE v3_conversation_messages (id text PRIMARY KEY, conversation_id text NOT NULL, role text NOT NULL, author text NOT NULL, parts jsonb NOT NULL, attachments jsonb NOT NULL DEFAULT '[]', attachment_context text NOT NULL DEFAULT '', run jsonb, state text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), sort_order bigint NOT NULL DEFAULT nextval('v3_conversation_message_order_seq'), UNIQUE(conversation_id,sort_order));
		CREATE TABLE v3_conversation_replies (id text PRIMARY KEY, conversation_id text NOT NULL, assistant_message_id text NOT NULL, initiating_session_hash text NOT NULL DEFAULT '', worker_claim_id text NOT NULL DEFAULT '', lease_expires_at timestamptz, state text NOT NULL, error text NOT NULL DEFAULT '', finish_reason text NOT NULL DEFAULT '', finished_at timestamptz);
		CREATE TABLE v3_metrics_questions (owner_id text NOT NULL,request_id text NOT NULL,conversation_id text NOT NULL UNIQUE,initial_reply_id text NOT NULL,environment_id text NOT NULL,application_id text NOT NULL,view_id text NOT NULL,range_id text NOT NULL,payload_hash text NOT NULL,binding_id text NOT NULL,connector_id text NOT NULL,resource_id text NOT NULL,query_id text NOT NULL,source_fingerprint text NOT NULL,window_start timestamptz NOT NULL,window_end timestamptz NOT NULL,evidence_sha256 text NOT NULL,PRIMARY KEY(owner_id,request_id));
		CREATE TABLE v3_conversation_events (seq bigserial PRIMARY KEY, conversation_id text NOT NULL, reply_id text NOT NULL, payload jsonb NOT NULL);
		CREATE TABLE sessions (token_hash text PRIMARY KEY, user_id text NOT NULL, expires_at timestamptz NOT NULL);
		CREATE TABLE users (id text PRIMARY KEY,email text NOT NULL DEFAULT '',name text NOT NULL DEFAULT '',role text NOT NULL DEFAULT '',department_id text,disabled boolean NOT NULL DEFAULT false,deleted_at timestamptz);
		CREATE TABLE rbac_user_roles (user_id text NOT NULL,role_id text NOT NULL);
		CREATE TABLE rbac_department_roles (department_id text NOT NULL,role_id text NOT NULL);
		CREATE TABLE rbac_roles (id text PRIMARY KEY);
		CREATE TABLE v3_role_grants (role_id text NOT NULL,module text NOT NULL,action text NOT NULL,scope_kind text NOT NULL,scope_id text NOT NULL);
		CREATE TABLE management_audit (id bigserial PRIMARY KEY, chain_seq bigserial NOT NULL UNIQUE, actor_id text NOT NULL, actor_kind text NOT NULL, action text NOT NULL, target text NOT NULL, outcome text NOT NULL, changes jsonb NOT NULL);
		INSERT INTO users(id,email,name,role) VALUES('persisted-owner','owner@local','Owner','viewer');
		INSERT INTO v3_conversations(id,owner_id,environment_id,active_reply_id) VALUES('c','persisted-owner','test','r');
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state) VALUES('u','c','user','user','[{"type":"text","markdown":"private history"}]','completed'),('a','c','assistant','AI','[]','streaming');
		INSERT INTO v3_conversation_replies(id,conversation_id,assistant_message_id,worker_claim_id,lease_expires_at,state) VALUES('r','c','a','r',clock_timestamp()+interval '1 hour','queued');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET initiating_session_hash=$1 WHERE id='r'`, store.Hash("test-session")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'persisted-owner',now()+interval '1 hour')`, store.Hash("test-session")); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestBackgroundTurnRejectsRevokedOwnerBeforeModel(t *testing.T) {
	for _, name := range []string{"disabled", "grant revoked", "callback unavailable"} {
		t.Run(name, func(t *testing.T) {
			db := isolatedConversationDB(t)
			runtime := &recordingRuntime{}
			opts := Options{RefreshSession: func(_ context.Context, token string) (core.User, error) {
				if token != "test-session" {
					t.Fatalf("unexpected session token")
				}
				return core.User{ID: "persisted-owner"}, nil
			}}
			if name != "callback unavailable" {
				opts.AuthorizeBackgroundTurn = func(_ context.Context, owner, env string) error {
					if owner != "persisted-owner" || env != "test" {
						t.Fatalf("background scope must come from database: %q %q", owner, env)
					}
					return errors.New(name)
				}
			}
			New(db, runtime, opts).runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", false)
			if runtime.calls != 0 {
				t.Fatalf("model called after %s", name)
			}
			var state, reason string
			if err := db.QueryRow(context.Background(), `SELECT state,error FROM v3_conversation_replies WHERE id='r'`).Scan(&state, &reason); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || reason != ErrForbidden.Error() {
				t.Fatalf("reply state=%q error=%q", state, reason)
			}
		})
	}
}

func TestBackgroundTurnRevocationBeforeCompletionDiscardsOutput(t *testing.T) {
	db := isolatedConversationDB(t)
	runtime := &recordingRuntime{}
	checks := 0
	svc := New(db, runtime, Options{RefreshSession: func(_ context.Context, _ string) (core.User, error) {
		return core.User{ID: "persisted-owner"}, nil
	}, AuthorizeBackgroundTurn: func(_ context.Context, owner, env string) error {
		checks++
		if owner != "persisted-owner" || env != "test" {
			t.Fatalf("unexpected persisted scope: %q %q", owner, env)
		}
		if checks == 3 {
			return ErrForbidden
		}
		return nil
	}})
	svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", false)
	if runtime.calls != 1 || checks != 3 {
		t.Fatalf("model calls=%d auth checks=%d", runtime.calls, checks)
	}
	var state, parts string
	if err := db.QueryRow(context.Background(), `SELECT state,parts::text FROM v3_conversation_messages WHERE id='a'`).Scan(&state, &parts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || parts == "" || strings.Contains(parts, "private output") {
		t.Fatalf("assistant state=%q parts=%q", state, parts)
	}
	var leaked bool
	if err := db.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM v3_conversation_events WHERE reply_id='r' AND payload::text LIKE '%private output%')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("replayable model output survived revocation")
	}
}

func TestFinishRechecksSessionAndGrantInTerminalTransaction(t *testing.T) {
	for _, tc := range []struct {
		name          string
		revokeSession bool
		revokeGrant   bool
		providerError bool
		wantState     string
	}{
		{name: "active", wantState: "completed"},
		{name: "session revoked", revokeSession: true, wantState: "failed"},
		{name: "grant revoked", revokeGrant: true, wantState: "failed"},
		{name: "provider failed after session revocation", revokeSession: true, providerError: true, wantState: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := isolatedConversationDBWithConns(t, 1)
			ctx := context.Background()
			if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='running' WHERE id='r';
				INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
				VALUES('c','r','{"type":"text.delta","message_id":"a","delta":"private answer"}')`); err != nil {
				t.Fatal(err)
			}
			if tc.revokeSession {
				if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE user_id='persisted-owner'`); err != nil {
					t.Fatal(err)
				}
			}
			checks := 0
			svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(ctx context.Context, tx pgx.Tx, owner, env string) error {
				checks++
				if owner != "persisted-owner" || env != "test" {
					t.Fatalf("wrong persisted scope: %q %q", owner, env)
				}
				var one int
				if err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
					t.Fatalf("transaction callback used another connection: %d %v", one, err)
				}
				if tc.revokeGrant {
					return ErrForbidden
				}
				return nil
			}})
			var modelErr error
			if tc.providerError {
				modelErr = errors.New("provider failed after partial output")
			}
			if err := svc.finish(ctx, "c", "r", "r", "a", []json.RawMessage{textPart("private answer")}, map[string]any{"id": "r", "tokens": 2}, modelErr, ""); err != nil {
				t.Fatal(err)
			}
			if tc.revokeSession && checks != 0 || !tc.revokeSession && checks != 1 {
				t.Fatalf("grant checks=%d", checks)
			}
			var state, parts string
			var run []byte
			if err := db.QueryRow(ctx, `SELECT state,parts::text,run FROM v3_conversation_messages WHERE id='a'`).Scan(&state, &parts, &run); err != nil {
				t.Fatal(err)
			}
			if state != tc.wantState {
				t.Fatalf("state=%s want=%s", state, tc.wantState)
			}
			var count int
			if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_events WHERE reply_id='r' AND payload->>'type'='message.completed'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("terminal events=%d err=%v", count, err)
			}
			if tc.wantState == "failed" {
				var replayed bool
				if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_conversation_events WHERE reply_id='r' AND payload::text LIKE '%private answer%')`).Scan(&replayed); err != nil || replayed || strings.Contains(parts, "private answer") || run != nil {
					t.Fatalf("revoked answer retained: replayed=%v parts=%s run=%s err=%v", replayed, parts, run, err)
				}
			} else if !strings.Contains(parts, "private answer") || !strings.Contains(string(run), `"tokens": 2`) {
				t.Fatalf("successful answer missing: parts=%s run=%s", parts, run)
			}
		})
	}
}

func TestTerminalGrantLockSerializesRevocation(t *testing.T) {
	db := isolatedConversationDBWithConns(t, 4)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='running' WHERE id='r';
		INSERT INTO rbac_roles(id) VALUES('conversation-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('persisted-owner','conversation-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
			VALUES('conversation-role','conversation','change','environment','test')`); err != nil {
		t.Fatal(err)
	}
	checked := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(ctx context.Context, tx pgx.Tx, owner, env string) error {
		var granted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_role_grants
			WHERE role_id='conversation-role' AND module='conversation' AND action='change')`).Scan(&granted); err != nil {
			return err
		}
		if !granted || owner != "persisted-owner" || env != "test" {
			return ErrForbidden
		}
		close(checked)
		<-release
		return nil
	}})
	done := make(chan error, 1)
	go func() {
		done <- svc.finish(ctx, "c", "r", "r", "a", []json.RawMessage{textPart("answer")}, nil, nil, "")
	}()
	select {
	case <-checked:
	case err := <-done:
		t.Fatalf("finish returned before grant check: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("finish did not reach grant check")
	}
	deleteCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, deleteErr := db.Exec(deleteCtx, `DELETE FROM v3_role_grants
		WHERE role_id='conversation-role' AND module='conversation' AND action='change'`)
	cancel()
	if !errors.Is(deleteErr, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("grant deletion should wait for terminal commit: %v", deleteErr)
	}
	insertCtx, insertCancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, insertErr := db.Exec(insertCtx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
		VALUES('conversation-role','conversation','view','environment','test')`)
	insertCancel()
	if !errors.Is(insertErr, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("new grant should wait for terminal commit: %v", insertErr)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal commit did not finish")
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='conversation-role'`); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id='r'`).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("revocation after terminal commit state=%s err=%v", state, err)
	}
}

func TestRecoveryLocksAuthorizationBeforeRetainingPartialOutput(t *testing.T) {
	db := isolatedConversationDBWithConns(t, 4)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies
		SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r';
		INSERT INTO rbac_roles(id) VALUES('conversation-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('persisted-owner','conversation-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
			VALUES('conversation-role','conversation','change','environment','test');
		INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
			VALUES('c','r','{"type":"text.delta","message_id":"a","delta":"authorized partial"}')`); err != nil {
		t.Fatal(err)
	}
	checked := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(ctx context.Context, tx pgx.Tx, owner, env string) error {
		var granted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_role_grants
			WHERE role_id='conversation-role' AND action='change')`).Scan(&granted); err != nil {
			return err
		}
		if !granted || owner != "persisted-owner" || env != "test" {
			return ErrForbidden
		}
		close(checked)
		<-release
		return nil
	}})
	done := make(chan error, 1)
	go func() { done <- svc.recoverOne(ctx, "c", "r") }()
	select {
	case <-checked:
	case err := <-done:
		t.Fatalf("recovery returned before grant check: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not reach grant check")
	}
	deleteCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, deleteErr := db.Exec(deleteCtx, `DELETE FROM sessions WHERE user_id='persisted-owner'`)
	cancel()
	if !errors.Is(deleteErr, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("session revocation should wait for recovery commit: %v", deleteErr)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovery did not finish")
	}
	var state, parts string
	if err := db.QueryRow(ctx, `SELECT state,parts::text FROM v3_conversation_messages WHERE id='a'`).Scan(&state, &parts); err != nil || state != "failed" || !strings.Contains(parts, "authorized partial") {
		t.Fatalf("authorized recovery state=%s parts=%s err=%v", state, parts, err)
	}
}

func TestTerminalLockFollowsRoleWriterOrder(t *testing.T) {
	db := isolatedConversationDBWithConns(t, 4)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='running' WHERE id='r';
		INSERT INTO rbac_roles(id) VALUES('conversation-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('persisted-owner','conversation-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
			VALUES('conversation-role','conversation','change','environment','test')`); err != nil {
		t.Fatal(err)
	}
	writer, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	var role string
	if err := writer.QueryRow(ctx, `SELECT id FROM rbac_roles WHERE id='conversation-role' FOR UPDATE`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	checked := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error {
		close(checked)
		<-release
		return nil
	}})
	done := make(chan error, 1)
	go func() {
		done <- svc.finish(ctx, "c", "r", "r", "a", []json.RawMessage{textPart("answer")}, nil, nil, "")
	}()
	select {
	case <-checked:
		// The terminal transaction reached its policy read despite a role
		// writer holding the rbac_roles row first.
	case err := <-done:
		t.Fatalf("terminal write returned before policy read: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("terminal write waited on the role row held by the writer")
	}
	writerDone := make(chan error, 1)
	go func() {
		_, err := writer.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='conversation-role'`)
		if err == nil {
			err = writer.Commit(ctx)
		}
		writerDone <- err
	}()
	select {
	case err := <-writerDone:
		close(release)
		t.Fatalf("role writer passed terminal table lock early: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal write deadlocked with role writer")
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("role writer did not resume after terminal commit")
	}
}

func TestFinalOnlyModelDoesNotEmitSuffixAfterRevocation(t *testing.T) {
	db := isolatedConversationDB(t)
	checks := 0
	svc := New(db, &finalOnlyRuntime{}, Options{
		RefreshSession: func(context.Context, string) (core.User, error) {
			return core.User{ID: "persisted-owner"}, nil
		},
		AuthorizeBackgroundTurn: func(context.Context, string, string) error {
			checks++
			if checks == 2 {
				return ErrForbidden
			}
			return nil
		},
	})
	svc.runTurn("c", "r", inference.Model{ID: "model", Name: "model"}, "", "test-session", false)
	var state, parts string
	if err := db.QueryRow(context.Background(), `SELECT state,parts::text FROM v3_conversation_messages WHERE id='a'`).Scan(&state, &parts); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || strings.Contains(parts, "private final output") {
		t.Fatalf("state=%s parts=%s", state, parts)
	}
	var leaked bool
	if err := db.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM v3_conversation_events WHERE reply_id='r' AND payload::text LIKE '%private final output%')`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("final-only model output was published after revocation")
	}
}

func TestRecoverInterruptedRetainsOnlyAuthorizedSessionOutput(t *testing.T) {
	for _, tc := range []struct {
		name          string
		revokeSession bool
		revokeRole    bool
		retainPartial bool
	}{
		{"active", false, false, true},
		{"session revoked", true, false, false},
		{"role revoked", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := isolatedConversationDB(t)
			ctx := context.Background()
			if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES('c','r','{"type":"text.delta","delta":"partial private output"}')`); err != nil {
				t.Fatal(err)
			}
			if tc.revokeSession {
				if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE user_id='persisted-owner'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r'`); err != nil {
				t.Fatal(err)
			}
			svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error {
				if tc.revokeRole {
					return ErrForbidden
				}
				return nil
			}})
			if err := svc.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			var state, parts string
			if err := db.QueryRow(ctx, `SELECT state,parts::text FROM v3_conversation_messages WHERE id='a'`).Scan(&state, &parts); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || strings.Contains(parts, "partial private output") != tc.retainPartial {
				t.Fatalf("state=%s parts=%s", state, parts)
			}
		})
	}
}

func TestLiveLeaseIsNotRecoveredAndExpiredLeaseFencesWorker(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(context.Context, pgx.Tx, string, string) error { return nil }})
	if err := svc.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id='r'`).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("live queued reply state=%q err=%v", state, err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET state='running' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id='r'`).Scan(&state); err != nil || state != "running" {
		t.Fatalf("live running reply state=%q err=%v", state, err)
	}
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.appendToolAudit(ctx, "persisted-owner", "test", "c", "r", "r", &inference.ToolInvocation{Name: "read_service_profile", StepID: "s", Status: "started"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("expired worker audit: %v", err)
	}
	if err := svc.appendEvent(ctx, "c", "r", "r", map[string]any{"type": "text.delta", "delta": "stale"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("expired worker event: %v", err)
	}
	if err := svc.finish(ctx, "c", "r", "r", "a", nil, nil, nil, ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("expired worker completion: %v", err)
	}
	if err := svc.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id='r'`).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("expired reply state=%q err=%v", state, err)
	}
	var leaked bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_conversation_events WHERE reply_id='r' AND payload::text LIKE '%stale%')`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("stale event persisted=%v err=%v", leaked, err)
	}
}

func TestRecoveryAuthorizationWithSingleConnection(t *testing.T) {
	db := isolatedConversationDBWithConns(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES('c','r','{"type":"text.delta","delta":"authorized partial"}')`); err != nil {
		t.Fatal(err)
	}
	queryOK := false
	svc := New(db, nil, Options{AuthorizeBackgroundTurnTx: func(ctx context.Context, tx pgx.Tx, owner, env string) error {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1`).Scan(&one)
		queryOK = err == nil && one == 1 && owner == "persisted-owner" && env == "test"
		return err
	}})
	if err := svc.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	var parts string
	if err := db.QueryRow(ctx, `SELECT parts::text FROM v3_conversation_messages WHERE id='a'`).Scan(&parts); err != nil {
		t.Fatal(err)
	}
	if !queryOK || !strings.Contains(parts, "authorized partial") {
		t.Fatalf("authorization query succeeded=%v, recovered parts=%s", queryOK, parts)
	}
}

func TestRecoveryTrustsOnlyTransactionAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		txCallback bool
	}{
		{"revoked after prior authorization", true},
		{"missing transaction callback", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := isolatedConversationDB(t)
			ctx := context.Background()
			if _, err := db.Exec(ctx, `UPDATE v3_conversation_replies SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES('c','r','{"type":"text.delta","delta":"sensitive partial"}')`); err != nil {
				t.Fatal(err)
			}
			opts := Options{AuthorizeBackgroundTurn: func(context.Context, string, string) error {
				return nil // a prior, out-of-transaction authorization
			}}
			if tc.txCallback {
				opts.AuthorizeBackgroundTurnTx = func(context.Context, pgx.Tx, string, string) error {
					return ErrForbidden // grant revoked before recovery acquires the row lock
				}
			}
			if err := New(db, nil, opts).RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			var leaked bool
			if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_conversation_events WHERE reply_id='r' AND payload::text LIKE '%sensitive partial%')`).Scan(&leaked); err != nil || leaked {
				t.Fatalf("replayable output retained=%v err=%v", leaked, err)
			}
		})
	}
}

func TestRecoveryProcessesMoreThanOneBatch(t *testing.T) {
	db := isolatedConversationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `
		UPDATE v3_conversation_replies SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id='r';
		INSERT INTO v3_conversations(id,owner_id,environment_id,active_reply_id)
		SELECT 'c'||lpad(i::text,3,'0'),'persisted-owner','test','r'||lpad(i::text,3,'0') FROM generate_series(1,100) i;
		INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state)
		SELECT 'a'||lpad(i::text,3,'0'),'c'||lpad(i::text,3,'0'),'assistant','AI','[]','streaming' FROM generate_series(1,100) i;
		INSERT INTO v3_conversation_replies(id,conversation_id,assistant_message_id,worker_claim_id,lease_expires_at,state)
		SELECT 'r'||lpad(i::text,3,'0'),'c'||lpad(i::text,3,'0'),'a'||lpad(i::text,3,'0'),
			'r'||lpad(i::text,3,'0'),clock_timestamp()-interval '1 second','queued' FROM generate_series(1,100) i;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := New(db, nil, Options{}).RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_conversation_replies WHERE state IN ('queued','running')`).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active replies after batch recovery=%d err=%v", active, err)
	}
}
