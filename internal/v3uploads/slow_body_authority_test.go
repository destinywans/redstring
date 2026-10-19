package v3uploads

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type blockedUploadBody struct {
	io.Reader
	started, release chan struct{}
	once             sync.Once
	releaseOnce      sync.Once
}

func (b *blockedUploadBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release })
	return b.Reader.Read(p)
}
func (b *blockedUploadBody) Close() error {
	b.releaseOnce.Do(func() { close(b.release) })
	return nil
}

func slowUploadFixture(t *testing.T) (*store.Store, *httpapi.Server, core.User, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated disposable schema only")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "slow_upload_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	st := &store.Store{DB: db}
	if err := st.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(t.Context(), "admin@example.invalid", "temporary-test-password"); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(t.Context(), core.User{Email: "uploader@example.invalid", Name: "Uploader", Role: "viewer"}, "temporary-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'), ('viewer','conversation','change','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(t.Context(), u.Email, "temporary-test-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	return st, api, u, token
}

func TestUploadRejectsAuthorityRevokedDuringSlowBody(t *testing.T) {
	for _, tc := range []struct {
		name, revoke string
		want         int
	}{
		{"session deleted", `DELETE FROM sessions WHERE user_id=$1`, 401},
		{"session expired", `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, 401},
		{"user disabled", `UPDATE users SET disabled=true WHERE id=$1`, 401},
		{"write grant revoked", `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='conversation' AND action='change' AND $1::text<>''`, 403},
		{"environment grant revoked", `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='environment' AND $1::text<>''`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, api, user, token := slowUploadFixture(t)
			body := &blockedUploadBody{Reader: strings.NewReader(`{"kind":"paste","name":"note","text":"bounded content"}`), started: make(chan struct{}), release: make(chan struct{})}
			defer body.Close()
			r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", body)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-AIOps-Environment", "dev")
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); api.ServeHTTP(w, r) }()
			select {
			case <-body.started:
			case <-time.After(5 * time.Second):
				t.Fatal("upload did not reach body read after middleware")
			}
			if _, err := st.DB.Exec(t.Context(), tc.revoke, user.ID); err != nil {
				body.Close()
				<-done
				t.Fatal(err)
			}
			body.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("upload did not finish")
			}
			if w.Code != tc.want {
				t.Fatalf("slow body after revocation: status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			var uploads, audits int
			if err := st.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM v3_uploads), (SELECT count(*) FROM management_audit WHERE action='v3.upload.created')`).Scan(&uploads, &audits); err != nil {
				t.Fatal(err)
			}
			if uploads != 0 || audits != 0 {
				t.Fatalf("rejected upload persisted bytes/audit: uploads=%d audits=%d", uploads, audits)
			}
		})
	}
}

func TestUploadRollsBackWhenSessionExpiresDuringStorageWait(t *testing.T) {
	st, api, user, token := slowUploadFixture(t)
	blocker, err := st.DB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(t.Context(), `LOCK TABLE v3_uploads IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	// Set expiry before the save transaction locks the session row. It remains
	// initially valid but expires while the INSERT is blocked by storage.
	if _, err := st.DB.Exec(t.Context(), `UPDATE sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE user_id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", strings.NewReader(`{"kind":"paste","name":"note","text":"bounded content"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); api.ServeHTTP(w, r) }()
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err := st.DB.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'INSERT INTO v3_uploads(%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("upload never waited on the storage lock")
	}
	for {
		var expired bool
		if err := st.DB.QueryRow(t.Context(), `SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1`, store.Hash(token)).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test session did not expire")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("upload did not finish after storage release")
	}
	if w.Code != 401 {
		t.Fatalf("expired transaction committed: status=%d body=%s", w.Code, w.Body.String())
	}
	var uploads, audits int
	if err := st.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM v3_uploads), (SELECT count(*) FROM management_audit WHERE action='v3.upload.created')`).Scan(&uploads, &audits); err != nil {
		t.Fatal(err)
	}
	if uploads != 0 || audits != 0 {
		t.Fatalf("expired upload/audit was not rolled back: %d/%d", uploads, audits)
	}
}
