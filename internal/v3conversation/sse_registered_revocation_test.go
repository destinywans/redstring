package v3conversation

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
)

type registeredSSEEnvironment struct{}

func (registeredSSEEnvironment) AuthorizeEnvironment(_ context.Context, user core.User, env string) error {
	if user.ID == "persisted-owner" && env == "test" {
		return nil
	}
	return store.ErrForbidden
}

// The handler flushes headers first, then the first bounded replay batch. Hold
// the latter at the wire boundary so revocation commits before the next read.
type firstSSEBatchGate struct {
	http.ResponseWriter
	flushed int
	first   chan struct{}
	release <-chan struct{}
}

func (w *firstSSEBatchGate) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	w.flushed++
	if w.flushed == 2 {
		close(w.first)
		<-w.release
	}
}

func TestRegisteredSSEStopsActiveReplayAfterPersistedRevocation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		revokeSession bool
	}{
		{name: "cookie session", revokeSession: true},
		{name: "conversation change grant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := isolatedConversationDB(t)
			ctx := context.Background()
			_, err := db.Exec(ctx, `
				ALTER TABLE v3_conversations ADD COLUMN default_model_id text NOT NULL DEFAULT '';
				ALTER TABLE v3_conversation_replies ADD COLUMN user_message_id text NOT NULL DEFAULT 'u';
				CREATE TABLE rbac_platform_administrators (user_id text PRIMARY KEY);
				INSERT INTO rbac_roles(id) VALUES('sse-role');
				INSERT INTO rbac_user_roles(user_id,role_id) VALUES('persisted-owner','sse-role');
				INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
					('sse-role','environment','view','environment','test'),
					('sse-role','conversation','view','environment','test'),
					('sse-role','conversation','change','environment','test');
				INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
					VALUES('c','r','{"type":"message.started","message":{"id":"a"}}');
				INSERT INTO v3_conversation_events(conversation_id,reply_id,payload)
					SELECT 'c','r',jsonb_build_object('type','text.delta','message_id','a',
						'delta',CASE WHEN n>63 THEN 'after-revocation-'||n::text ELSE n::text END)
					FROM generate_series(1,130) AS n;`)
			if err != nil {
				t.Fatal(err)
			}
			repository := &store.Store{DB: db}
			if user, err := repository.Session(ctx, "test-session"); err != nil || user.ID != "persisted-owner" {
				t.Fatalf("persisted cookie session unavailable before SSE: user=%+v err=%v", user, err)
			}
			grant := func(ctx context.Context, module, action string) (bool, error) {
				var exists bool
				err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_user_roles assignment
					JOIN v3_role_grants permission ON permission.role_id=assignment.role_id
					WHERE assignment.user_id='persisted-owner' AND permission.module=$1
						AND permission.action=$2 AND permission.scope_kind='environment'
						AND permission.scope_id='test')`, module, action).Scan(&exists)
				return exists, err
			}
			server := httpapi.New(repository, nil, true)
			chat := New(db, nil, Options{
				CurrentUser:    httpapi.CurrentUser,
				RefreshSession: repository.Session,
				AllowedEnvironments: func(ctx context.Context, user core.User) ([]string, error) {
					if user.ID != "persisted-owner" {
						return nil, nil
					}
					ok, err := grant(ctx, "conversation", "view")
					if err != nil || !ok {
						return nil, err
					}
					return []string{"test"}, nil
				},
				AuthorizeBackgroundTurn: func(ctx context.Context, owner, env string) error {
					if owner != "persisted-owner" || env != "test" {
						return ErrForbidden
					}
					ok, err := grant(ctx, "conversation", "change")
					if err != nil {
						return err
					}
					if !ok {
						return ErrForbidden
					}
					return nil
				},
			})
			chat.RegisterV3(server)
			if err := server.ConfigureV3Environment(httpapi.V3InstanceConfig{
				Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"},
			}, registeredSSEEnvironment{}, repository.Session, nil); err != nil {
				t.Fatal(err)
			}
			first := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				server.ServeHTTP(&firstSSEBatchGate{ResponseWriter: w, first: first, release: release}, r)
			}))
			t.Cleanup(remote.Close)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			client := &http.Client{Timeout: 5 * time.Second}
			req, err := http.NewRequest(http.MethodGet, remote.URL+"/api/v1/conversations/c/events?reply_id=r", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: "aiops_session", Value: "test-session"})
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("registered SSE status=%d content-type=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), body)
			}
			reader := bufio.NewReader(response.Body)
			for i := 0; i < 64; i++ {
				item, err := readStreamItem(reader)
				if err != nil || item.id == 0 {
					t.Fatalf("first batch item %d: %+v %v", i, item, err)
				}
			}
			select {
			case <-first:
			case <-time.After(3 * time.Second):
				t.Fatal("registered SSE did not flush its first bounded batch")
			}
			var revokeErr error
			if tc.revokeSession {
				_, revokeErr = db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash("test-session"))
			} else {
				_, revokeErr = db.Exec(ctx, `DELETE FROM v3_role_grants
					WHERE role_id='sse-role' AND module='conversation' AND action='change'`)
			}
			if revokeErr != nil {
				t.Fatal(revokeErr)
			}
			if !tc.revokeSession {
				if ok, err := grant(ctx, "conversation", "view"); err != nil || !ok {
					t.Fatalf("view grant must remain after change revocation: allowed=%t err=%v", ok, err)
				}
			}
			releaseOnce.Do(func() { close(release) })
			if item, err := readStreamItem(reader); err != io.EOF {
				t.Fatalf("revoked stream continued with item=%+v err=%v", item, err)
			}
			var state string
			if err := db.QueryRow(ctx, `SELECT state FROM v3_conversation_replies WHERE id='r'`).Scan(&state); err != nil || state != "queued" {
				t.Fatalf("test must revoke an active reply: state=%q err=%v", state, err)
			}
		})
	}
}
