package v3identity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func bindingAPI(t *testing.T) (*store.Store, *httpapi.Server) {
	t.Helper()
	s := isolatedStore(t)
	_, err := s.DB.Exec(context.Background(), `INSERT INTO users(id,email,name,role,password_hash,source)
 VALUES('actor','actor@example.invalid','Actor','admin','unused','local'),
 ('target','target@example.invalid','Target','viewer','unused','local')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(context.Background(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'actor',clock_timestamp()+interval '1 hour')`, store.Hash("actor-session")); err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(s, nil, true)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}}, allowEnvironment{}, s.Session,
		func(context.Context, core.User, string, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	(&Service{store: s, issuerHash: store.Hash("test-issuer")}).Register(api)
	return s, api
}

func bindingRequest(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-AIOps-Environment", "test")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "actor-session"})
	return r
}

type pausedBindingBody struct {
	entered, release chan struct{}
	once             sync.Once
	reader           io.Reader
}

func (b *pausedBindingBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.reader.Read(p)
}
func (*pausedBindingBody) Close() error { return nil }

func awaitBinding(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case w := <-done:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("binding handler did not finish")
		return nil
	}
}

func assertNoBindingWrite(t *testing.T, s *store.Store) {
	t.Helper()
	var bindings, audits int
	if err := s.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM v3_oidc_identities),
 (SELECT count(*) FROM management_audit WHERE action IN ('account.oidc_bound','account.oidc_unbound'))`).Scan(&bindings, &audits); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 || audits != 0 {
		t.Fatalf("denied request wrote bindings=%d audits=%d", bindings, audits)
	}
}

func TestBindingRejectsAuthorizationRevokedDuringBodyPostgres(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revoke func(*testing.T, *store.Store, *httpapi.Server)
		want   int
	}{
		{"logout", func(t *testing.T, s *store.Store, api *httpapi.Server) {
			w := httptest.NewRecorder()
			api.ServeHTTP(w, bindingRequest("POST", "/api/v1/auth/logout", nil))
			if w.Code != 204 {
				t.Fatalf("logout %d %s", w.Code, w.Body.String())
			}
		}, 401},
		{"expired", func(t *testing.T, s *store.Store, _ *httpapi.Server) {
			if _, err := s.DB.Exec(context.Background(), `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id='actor'`); err != nil {
				t.Fatal(err)
			}
		}, 401},
		{"disabled", func(t *testing.T, s *store.Store, _ *httpapi.Server) {
			if _, err := s.DB.Exec(context.Background(), `UPDATE users SET disabled=true WHERE id='actor'`); err != nil {
				t.Fatal(err)
			}
		}, 403},
		{"platform_admin_removed", func(t *testing.T, s *store.Store, _ *httpapi.Server) {
			if _, err := s.DB.Exec(context.Background(), `DELETE FROM rbac_user_roles WHERE user_id='actor' AND role_id='admin'`); err != nil {
				t.Fatal(err)
			}
		}, 403},
		{"member_grant_removed", func(t *testing.T, s *store.Store, _ *httpapi.Server) {
			if _, err := s.DB.Exec(context.Background(), `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='members' AND action='manage'`); err != nil {
				t.Fatal(err)
			}
		}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := bindingAPI(t)
			b := &pausedBindingBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"user_id":"target","subject":"subject"}`)}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(b.release) }) })
			r := bindingRequest("POST", "/api/v1/auth/oidc/bindings", b)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); api.ServeHTTP(w, r); done <- w }()
			select {
			case <-b.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("handler never reached request body")
			}
			tc.revoke(t, s, api)
			release.Do(func() { close(b.release) })
			w := awaitBinding(t, done)
			if w.Code != tc.want {
				t.Fatalf("bind %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
			assertNoBindingWrite(t, s)
		})
	}
}

func waitBindingLock(t *testing.T, s *store.Store, key int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		err := s.DB.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
 WHERE NOT l.granted AND l.locktype='advisory' AND l.objid=$1::oid AND a.application_name=current_schema())`, key).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("handler never waited for identity authority lock")
}

func TestBindingSessionFenceAfterIdentityWaitPostgres(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			s, api := bindingAPI(t)
			if method == "DELETE" {
				if _, err := s.DB.Exec(context.Background(), `INSERT INTO v3_oidc_identities(issuer_hash,subject,user_id,created_by) VALUES($1,'subject','target','actor')`, store.Hash("test-issuer")); err != nil {
					t.Fatal(err)
				}
			}
			blocker, err := s.DB.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(context.Background(), `SELECT pg_advisory_xact_lock(752018996)`); err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/auth/oidc/bindings"
			var body io.Reader = strings.NewReader(`{"user_id":"target","subject":"subject"}`)
			if method == "DELETE" {
				path += "/target"
				body = nil
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); api.ServeHTTP(w, bindingRequest(method, path, body)); done <- w }()
			waitBindingLock(t, s, 752018996)
			// Logout does not need the identity/governance locks and must finish first.
			w := httptest.NewRecorder()
			api.ServeHTTP(w, bindingRequest("POST", "/api/v1/auth/logout", nil))
			if w.Code != 204 {
				t.Fatalf("logout %d", w.Code)
			}
			if err = blocker.Commit(context.Background()); err != nil {
				t.Fatal(err)
			}
			denied := awaitBinding(t, done)
			if denied.Code != 401 {
				t.Fatalf("stale %s %d %s", method, denied.Code, denied.Body.String())
			}
			var n int
			if err = s.DB.QueryRow(context.Background(), `SELECT count(*) FROM v3_oidc_identities`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := 0
			if method == "DELETE" {
				want = 1
			}
			if n != want {
				t.Fatalf("binding count=%d want=%d", n, want)
			}
			if err = s.DB.QueryRow(context.Background(), `SELECT count(*) FROM management_audit WHERE action IN ('account.oidc_bound','account.oidc_unbound')`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("denied audit count=%d err=%v", n, err)
			}
		})
	}
}

func TestBindingSelfBindAndUnbindRevokeSessionsPostgres(t *testing.T) {
	s, api := bindingAPI(t)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, bindingRequest("POST", "/api/v1/auth/oidc/bindings", strings.NewReader(`{"user_id":"actor","subject":"self-subject"}`)))
	if w.Code != 201 {
		t.Fatalf("self bind %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id='actor'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("self bind sessions=%d err=%v", n, err)
	}
	// A new valid session can explicitly remove its own binding.
	if _, err := s.DB.Exec(context.Background(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'actor',clock_timestamp()+interval '1 hour')`, store.Hash("actor-session")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	api.ServeHTTP(w, bindingRequest("DELETE", "/api/v1/auth/oidc/bindings/actor", nil))
	if w.Code != 204 {
		t.Fatalf("self unbind %d %s", w.Code, w.Body.String())
	}
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id='actor'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("self unbind sessions=%d err=%v", n, err)
	}
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM v3_oidc_identities`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("self unbind identities=%d err=%v", n, err)
	}
	if err := s.DB.QueryRow(context.Background(), `SELECT count(*) FROM management_audit WHERE action IN ('account.oidc_bound','account.oidc_unbound')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("self bind/unbind audits=%d err=%v", n, err)
	}
}

func TestBindingRechecksAfterAuthorityWaitPostgres(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  int64
		want int
	}{
		{"permission", 752018992, 403}, {"expiry", 752018996, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := bindingAPI(t)
			ctx := context.Background()
			blocker, err := s.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, tc.key); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				api.ServeHTTP(w, bindingRequest("POST", "/api/v1/auth/oidc/bindings", strings.NewReader(`{"user_id":"target","subject":"subject"}`)))
				done <- w
			}()
			waitBindingLock(t, s, tc.key)
			if tc.name == "permission" {
				if _, err = blocker.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='members' AND action='manage'`); err != nil {
					t.Fatal(err)
				}
			} else {
				// Expire after the handler transaction began: now() in that transaction
				// would still accept this deadline once the identity lock is released.
				if _, err = s.DB.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '100 milliseconds' WHERE user_id='actor'`); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					var expired bool
					if err = s.DB.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM sessions WHERE user_id='actor'`).Scan(&expired); err != nil {
						t.Fatal(err)
					}
					if expired {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("test session did not expire")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := awaitBinding(t, done)
			if w.Code != tc.want {
				t.Fatalf("bind %d want=%d %s", w.Code, tc.want, w.Body.String())
			}
			assertNoBindingWrite(t, s)
		})
	}
}

func TestBindingSessionExpiresDuringSessionWaitPostgres(t *testing.T) {
	s, api := bindingAPI(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE user_id='actor'`); err != nil {
		t.Fatal(err)
	}
	blocker, err := s.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	var actor string
	if err = blocker.QueryRow(ctx, `SELECT user_id FROM sessions WHERE user_id='actor' FOR UPDATE`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, bindingRequest("POST", "/api/v1/auth/oidc/bindings", strings.NewReader(`{"user_id":"target","subject":"subject"}`)))
		done <- w
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE application_name=current_schema() AND wait_event_type='Lock' AND query LIKE '%FROM sessions%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handler never waited for session row lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for {
		var expired bool
		if err = s.DB.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM sessions WHERE user_id='actor'`).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test session did not expire")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := awaitBinding(t, done)
	if w.Code != 401 {
		t.Fatalf("expired session after row lock wait %d %s", w.Code, w.Body.String())
	}
	assertNoBindingWrite(t, s)
}
