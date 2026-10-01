package v3conversation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// These tests opt into the shared-transaction production path. An accidental
// fallback to a legacy callback panics; it cannot make the regression pass.
// The existing fixture owns one UUID schema, including every fault-injection
// table. Its independent admin pool never borrows the tested pool's only conn.
type sharedSSEFixture struct {
	db, admin    *pgxpool.Pool
	svc          *Service
	sessionCalls atomic.Int32
	allowedCalls atomic.Int32
}

func newSharedSSEFixture(t *testing.T) *sharedSSEFixture {
	t.Helper()
	db := isolatedConversationDBWithConns(t, 1)
	cfg := db.Config()
	cfg.MaxConns = 2
	admin, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	f := &sharedSSEFixture{db: db, admin: admin}
	f.exec(t, `ALTER TABLE v3_conversations ADD COLUMN default_model_id text NOT NULL DEFAULT '';
		ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT 'u';
		CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
		INSERT INTO rbac_roles(id) VALUES('shared-sse-role');
		INSERT INTO rbac_user_roles(user_id,role_id) VALUES('persisted-owner','shared-sse-role');
		INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
			('shared-sse-role','environment','view','environment','test'),
			('shared-sse-role','conversation','view','environment','test'),
			('shared-sse-role','conversation','change','environment','test');
		CREATE TABLE shared_sse_metric_access(fingerprint text PRIMARY KEY, allowed boolean NOT NULL);
		INSERT INTO shared_sse_metric_access VALUES('shared-fingerprint',true);
		UPDATE v3_conversation_replies SET state='running' WHERE id='r'`)
	repository := &store.Store{DB: db}
	f.svc = New(db, nil, Options{
		CurrentUser: func(*http.Request) core.User { return core.User{ID: "persisted-owner"} },
		RefreshSession: func(context.Context, string) (core.User, error) {
			panic("shared SSE called legacy RefreshSession")
		},
		AllowedEnvironments: func(context.Context, core.User) ([]string, error) {
			panic("shared SSE called legacy AllowedEnvironments")
		},
		AuthorizeBackgroundTurn: func(context.Context, string, string) error {
			panic("shared SSE called legacy AuthorizeBackgroundTurn")
		},
		AuthorizeMetricsSource: func(context.Context, string, string, MetricsSource) error {
			panic("shared SSE called legacy AuthorizeMetricsSource")
		},
		RefreshSessionTx: func(ctx context.Context, tx pgx.Tx, token string) (core.User, error) {
			f.sessionCalls.Add(1)
			return repository.SessionTx(ctx, tx, token)
		},
		AllowedEnvironmentsTx: func(ctx context.Context, tx pgx.Tx, user core.User) ([]string, error) {
			f.allowedCalls.Add(1)
			var isolation string
			if err := tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&isolation); err != nil {
				return nil, err
			}
			if isolation != "read committed" {
				return nil, fmt.Errorf("shared SSE isolation is %s", isolation)
			}
			ok, err := sharedSSEGrant(ctx, tx, user.ID, "conversation", "view", "test")
			if err != nil || !ok {
				return nil, err
			}
			return []string{"test"}, nil
		},
		AuthorizeBackgroundTurnTx: func(ctx context.Context, tx pgx.Tx, owner, env string) error {
			ok, err := sharedSSEGrant(ctx, tx, owner, "conversation", "change", env)
			if err != nil {
				return err
			}
			if !ok {
				return ErrForbidden
			}
			return nil
		},
		AuthorizeMetricsSourceTx: func(ctx context.Context, tx pgx.Tx, owner, env string, source MetricsSource) error {
			if owner != "persisted-owner" || env != "test" || source.ApplicationID != "checkout" ||
				source.BindingID != "binding" || source.ConnectorID != "prom" || source.ResourceID != "checkout" ||
				source.QueryID != "request_rate" || source.Fingerprint != "shared-fingerprint" {
				return ErrForbidden
			}
			var allowed bool
			err := tx.QueryRow(ctx, `SELECT allowed FROM shared_sse_metric_access WHERE fingerprint=$1`, source.Fingerprint).Scan(&allowed)
			if err != nil {
				return err
			}
			if !allowed {
				return ErrForbidden
			}
			return nil
		},
	})
	return f
}

func sharedSSEGrant(ctx context.Context, tx pgx.Tx, owner, module, action, env string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_user_roles assignment
		JOIN v3_role_grants grant_row ON grant_row.role_id=assignment.role_id
		WHERE assignment.user_id=$1 AND grant_row.module=$2 AND grant_row.action=$3
			AND grant_row.scope_kind='environment' AND grant_row.scope_id=$4)`,
		owner, module, action, env).Scan(&allowed)
	return allowed, err
}

func (f *sharedSSEFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := f.admin.Exec(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *sharedSSEFixture) retainedBatches(t *testing.T) {
	t.Helper()
	f.exec(t, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		SELECT 'c','r',jsonb_build_object('type','text.delta','delta',
			CASE WHEN n>64 THEN 'after-revocation-'||n::text ELSE n::text END)
		FROM generate_series(1,130) AS n;
		INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"message.completed"}')`)
}

func (f *sharedSSEFixture) metrics(t *testing.T) {
	t.Helper()
	f.exec(t, `UPDATE v3_conversations SET metrics_source_required=true WHERE id='c';
		INSERT INTO v3_metrics_questions(owner_id,request_id,conversation_id,initial_reply_id,
			environment_id,application_id,view_id,range_id,payload_hash,binding_id,connector_id,
			resource_id,query_id,source_fingerprint,window_start,window_end,evidence_sha256)
		VALUES('persisted-owner','shared-request','c','r','test','checkout','request_rate','15m',
			'hash','binding','prom','checkout','request_rate','shared-fingerprint',
			clock_timestamp()-interval '15 minutes',clock_timestamp(),'digest')`)
}

func sharedSSERequest(ctx context.Context, cursor int64, token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/c/events?reply_id=r", nil).WithContext(ctx)
	req.SetPathValue("id", "c")
	req.Header.Set("Last-Event-ID", strconv.FormatInt(cursor, 10))
	req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	return req
}

type sharedSSEExecution struct {
	done       chan struct{}
	panicValue any // written before closing done; readers wait for that signal
}

func sharedSSEStart(t *testing.T, svc *Service, w http.ResponseWriter, req *http.Request) *sharedSSEExecution {
	t.Helper()
	ctx, cancel := context.WithCancel(req.Context())
	run := &sharedSSEExecution{done: make(chan struct{})}
	go func() {
		defer func() { run.panicValue = recover(); close(run.done) }()
		svc.events(w, req.WithContext(ctx))
	}()
	// Stop the handler before the fixture pools/schema are cleaned up, including
	// assertion failures that happen before the test's explicit wait.
	t.Cleanup(func() { cancel(); run.wait(t) })
	return run
}

func (run *sharedSSEExecution) wait(t *testing.T) {
	t.Helper()
	select {
	case <-run.done:
		if run.panicValue != nil {
			t.Fatalf("shared SSE panic: %v", run.panicValue)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("shared SSE failed to stop within request/cleanup budget")
	}
}

func sharedSSEPoolAvailable(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var answer int
	if err := db.QueryRow(ctx, "SELECT 42").Scan(&answer); err != nil || answer != 42 {
		t.Fatalf("shared SSE retained the only connection: answer=%d err=%v", answer, err)
	}
}

// Both the header flush and every batch flush use the real stream writer. The
// hook executes only after the first bounded batch has left its DB transaction.
type sharedSSEFlushHook struct {
	*httptest.ResponseRecorder
	flushes    int
	afterBatch func() error
	err        error
}

func (w *sharedSSEFlushHook) FlushError() error {
	w.ResponseRecorder.Flush()
	w.flushes++
	if w.flushes == 2 && w.afterBatch != nil {
		w.err = w.afterBatch()
		return w.err
	}
	return nil
}

func TestSSESharedConnectionReleasesPoolBeforeBlockedWrite(t *testing.T) {
	f := newSharedSSEFixture(t)
	f.exec(t, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
		VALUES('c','r','{"type":"text.delta","delta":"first"}'),
		('c','r','{"type":"message.completed"}')`)
	w := &blockedSSEWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	var release sync.Once
	run := sharedSSEStart(t, f.svc, w, sharedSSERequest(ctx, 0, "test-session"))
	defer func() { release.Do(func() { close(w.release) }); cancel(); run.wait(t) }()
	select {
	case <-w.entered:
	case <-run.done:
		run.wait(t)
		t.Fatal("shared SSE returned before the client write boundary")
	case <-time.After(2 * time.Second):
		t.Fatal("single-connection shared SSE did not reach its client write")
	}
	sharedSSEPoolAvailable(t, f.db) // actual query while the stream remains blocked
	if f.db.Config().MaxConns != 1 || f.sessionCalls.Load() < 1 || f.allowedCalls.Load() < 2 {
		t.Fatal("test did not execute shared header and batch authorization with one connection")
	}
	if w.deadline.IsZero() {
		t.Fatal("blocked stream write has no transport deadline")
	}
	release.Do(func() { close(w.release) })
	run.wait(t)
	if w.Code != http.StatusOK || strings.Count(w.Body.String(), "data: ") != 2 {
		t.Fatalf("shared replay lost its batch: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSSESharedConnectionSessionExpiryUsesWallClockInsideTransaction(t *testing.T) {
	f := newSharedSSEFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := f.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var transactionStarted time.Time
	if err := tx.QueryRow(ctx, "SELECT now()").Scan(&transactionStarted); err != nil {
		t.Fatal(err)
	}
	repository := &store.Store{DB: f.db}
	current, err := repository.SessionTx(ctx, tx, "test-session")
	if err != nil || current.ID != "persisted-owner" {
		t.Fatalf("session was not initially valid in the shared transaction: user=%q err=%v", current.ID, err)
	}
	var expires time.Time
	if err := f.admin.QueryRow(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '250 milliseconds'
		WHERE token_hash=$1 RETURNING expires_at`, store.Hash("test-session")).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if !expires.After(transactionStarted) {
		t.Fatal("fixture expiry must follow the open transaction's fixed now()")
	}
	for {
		var expired bool
		if err := tx.QueryRow(ctx, "SELECT clock_timestamp()>$1", expires).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture session did not reach natural expiry")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// now() remains earlier than expires in this same transaction. Only the
	// database's advancing wall clock can reject this still-persisted session.
	if _, err := repository.SessionTx(ctx, tx, "test-session"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired session survived the transaction wall-clock boundary: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	sharedSSEPoolAvailable(t, f.db)
}

func TestSSESharedConnectionRechecksPersistedAccessBetweenBatches(t *testing.T) {
	for _, name := range []string{"natural expiry", "view grant", "change grant", "initiating session", "metrics source"} {
		t.Run(name, func(t *testing.T) {
			f := newSharedSSEFixture(t)
			f.retainedBatches(t)
			token := "test-session"
			if name == "initiating session" {
				token = "viewer-session"
				f.exec(t, `INSERT INTO sessions VALUES($1,'persisted-owner',clock_timestamp()+interval '1 hour')`, store.Hash(token))
			}
			if name == "metrics source" {
				f.metrics(t)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			w := &sharedSSEFlushHook{ResponseRecorder: httptest.NewRecorder()}
			w.afterBatch = func() error {
				if name == "natural expiry" {
					var expires time.Time
					if err := f.admin.QueryRow(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '80 milliseconds'
						WHERE token_hash=$1 RETURNING expires_at`, store.Hash(token)).Scan(&expires); err != nil {
						return err
					}
					// Expire by the PG wall clock, without deleting/revoking the row.
					for {
						var expired bool
						if err := f.admin.QueryRow(ctx, "SELECT clock_timestamp()>$1", expires).Scan(&expired); err != nil {
							return err
						}
						if expired {
							return nil
						}
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				query := `DELETE FROM v3_role_grants WHERE role_id='shared-sse-role' AND module='conversation' AND action='view'`
				switch name {
				case "change grant":
					query = `DELETE FROM v3_role_grants WHERE role_id='shared-sse-role' AND module='conversation' AND action='change'`
				case "initiating session":
					_, err := f.admin.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash("test-session"))
					return err
				case "metrics source":
					query = `UPDATE shared_sse_metric_access SET allowed=false`
				}
				_, err := f.admin.Exec(ctx, query)
				return err
			}
			run := sharedSSEStart(t, f.svc, w, sharedSSERequest(ctx, 0, token))
			run.wait(t)
			if w.err != nil {
				t.Fatal(w.err)
			}
			body := w.Body.String()
			if w.Code != http.StatusOK || strings.Count(body, "data: ") != 64 ||
				strings.Contains(body, "after-revocation-") || strings.Contains(body, "message.completed") {
				t.Fatalf("shared replay delivered a batch after %s: status=%d body=%s", name, w.Code, body)
			}
			if f.sessionCalls.Load() < 2 {
				t.Fatal("shared request session was not refreshed before the next batch")
			}
			sharedSSEPoolAvailable(t, f.db)
		})
	}
}

func TestSSESharedConnectionPartialCallbacksAndMetricsFailClosed(t *testing.T) {
	for _, name := range []string{"missing session Tx", "missing allowed Tx", "missing background Tx", "missing metrics Tx", "missing metrics record", "denied metrics source", "revoked initiating session"} {
		t.Run(name, func(t *testing.T) {
			f := newSharedSSEFixture(t)
			f.retainedBatches(t)
			switch name {
			case "missing session Tx":
				f.svc.opts.RefreshSessionTx = nil
			case "missing allowed Tx":
				f.svc.opts.AllowedEnvironmentsTx = nil
			case "missing background Tx":
				f.svc.opts.AuthorizeBackgroundTurnTx = nil
			case "missing metrics Tx", "missing metrics record", "denied metrics source":
				f.metrics(t)
				if name == "missing metrics Tx" {
					f.svc.opts.AuthorizeMetricsSourceTx = nil
				} else if name == "missing metrics record" {
					f.exec(t, "DELETE FROM v3_metrics_questions")
				} else {
					f.exec(t, "UPDATE shared_sse_metric_access SET allowed=false")
				}
			case "revoked initiating session":
				f.exec(t, `UPDATE v3_conversation_replies SET initiating_session_hash=$1 WHERE id='r'`, store.Hash("missing-original-session"))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			w := httptest.NewRecorder()
			run := sharedSSEStart(t, f.svc, w, sharedSSERequest(ctx, 0, "test-session"))
			run.wait(t)
			if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "data: ") || strings.Contains(w.Body.String(), "after-revocation-") {
				t.Fatalf("shared header admitted %s: status=%d body=%s", name, w.Code, w.Body.String())
			}
			sharedSSEPoolAvailable(t, f.db)
		})
	}
}

func TestSSESharedConnectionFailureAndCancellationReleasePool(t *testing.T) {
	for _, name := range []string{"callback error", "real commit failure", "query cancellation"} {
		t.Run(name, func(t *testing.T) {
			f := newSharedSSEFixture(t)
			f.retainedBatches(t)
			original := f.svc.opts.AllowedEnvironmentsTx
			queryEntered := make(chan int32, 1)
			if name == "real commit failure" {
				f.exec(t, `CREATE TABLE shared_sse_parent(id int PRIMARY KEY);
					CREATE TABLE shared_sse_child(parent_id int REFERENCES shared_sse_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
			}
			f.svc.opts.AllowedEnvironmentsTx = func(ctx context.Context, tx pgx.Tx, user core.User) ([]string, error) {
				allowed, err := original(ctx, tx, user)
				if err != nil {
					return nil, err
				}
				switch name {
				case "callback error":
					return nil, errors.New("isolated shared authorization error")
				case "real commit failure":
					// Real deferred FK violation at Commit, not a mocked pgx.Tx.
					_, err = tx.Exec(ctx, "INSERT INTO shared_sse_child VALUES(1)")
					return allowed, err
				default:
					var pid int32
					if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
						return nil, err
					}
					queryEntered <- pid
					_, err = tx.Exec(ctx, "SELECT pg_sleep(10)")
					return nil, err
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			w := httptest.NewRecorder()
			run := sharedSSEStart(t, f.svc, w, sharedSSERequest(ctx, 0, "test-session"))
			if name == "query cancellation" {
				var pid int32
				select {
				case pid = <-queryEntered:
				case <-time.After(time.Second):
					t.Fatal("shared callback did not enter its real transaction")
				}
				for {
					var sleeping bool
					if err := f.admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
						WHERE pid=$1 AND wait_event='PgSleep')`, pid).Scan(&sleeping); err != nil {
						t.Fatal(err)
					}
					if sleeping {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("callback query was not observed sleeping before cancellation")
					case <-time.After(10 * time.Millisecond):
					}
				}
				cancel()
			}
			run.wait(t)
			if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "data: ") {
				t.Fatalf("failed shared transaction released data: status=%d body=%s", w.Code, w.Body.String())
			}
			sharedSSEPoolAvailable(t, f.db)
			if name == "real commit failure" {
				var rows int
				if err := f.admin.QueryRow(context.Background(), "SELECT count(*) FROM shared_sse_child").Scan(&rows); err != nil || rows != 0 {
					t.Fatalf("failed header transaction did not roll back: rows=%d err=%v", rows, err)
				}
			}
		})
	}
}

func TestSSESharedConnectionCompletionRaceAndPinnedTerminalCursor(t *testing.T) {
	for _, name := range []string{"completion after empty flush", "terminal cursor while newer reply is active"} {
		t.Run(name, func(t *testing.T) {
			f := newSharedSSEFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var cursor int64
			marker := `{"type":"message.started"}`
			if name == "terminal cursor while newer reply is active" {
				marker = `{"type":"message.completed"}`
			}
			if err := f.admin.QueryRow(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
				VALUES('c','r',$1::jsonb) RETURNING seq`, marker).Scan(&cursor); err != nil {
				t.Fatal(err)
			}
			w := &sharedSSEFlushHook{ResponseRecorder: httptest.NewRecorder()}
			if name == "completion after empty flush" {
				w.afterBatch = func() error {
					_, err := f.admin.Exec(ctx, `INSERT INTO v3_conversation_events(conversation_id,reply_id,payload) VALUES
						('c','r','{"type":"text.delta","delta":"race-first"}'),
						('c','r','{"type":"text.delta","delta":"race-second"}'),
						('c','r','{"type":"message.completed"}');
						UPDATE v3_conversation_replies SET state='completed' WHERE id='r'`)
					return err
				}
			} else {
				f.exec(t, `UPDATE v3_conversation_replies SET state='completed' WHERE id='r';
					INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
					VALUES('c','r','{"type":"text.delta","delta":"after-terminal-private"}');
					INSERT INTO v3_conversation_messages(id,conversation_id,role,author,parts,state)
					VALUES('a2','c','assistant','AI','[]','streaming')`)
				f.exec(t, `INSERT INTO v3_conversation_replies(id,conversation_id,assistant_message_id,initiating_session_hash,state)
					VALUES('r2','c','a2',$1,'running')`, store.Hash("test-session"))
				f.exec(t, `UPDATE v3_conversations SET active_reply_id='r2' WHERE id='c';
					INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
					VALUES('c','r2','{"type":"text.delta","delta":"new-reply-private"}')`)
			}
			run := sharedSSEStart(t, f.svc, w, sharedSSERequest(ctx, cursor, "test-session"))
			run.wait(t)
			if w.err != nil {
				t.Fatal(w.err)
			}
			body := w.Body.String()
			if w.Code != http.StatusOK {
				t.Fatalf("shared cursor status=%d body=%s", w.Code, body)
			}
			if name == "completion after empty flush" {
				if strings.Count(body, "data: ") != 3 || !strings.Contains(body, "race-first") ||
					!strings.Contains(body, "race-second") || !strings.Contains(body, "message.completed") {
					t.Fatalf("empty batch/state race lost completion events: %s", body)
				}
				last := cursor
				for _, line := range strings.Split(body, "\n") {
					if strings.HasPrefix(line, "id: ") {
						id, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
						if err != nil || id <= last {
							t.Fatalf("shared cursor replay is not monotonic: %q after%d", line, last)
						}
						last = id
					}
				}
			} else if strings.Contains(body, "data: ") || strings.Contains(body, "private") {
				t.Fatalf("terminal cursor replayed trailing or newer-reply data: %s", body)
			}
			sharedSSEPoolAvailable(t, f.db)
		})
	}
}
