package v3sourceregistry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type overlappingWebReader struct {
	calls             atomic.Int32
	started, release  chan struct{}
	seed, old, newest string
}

func (*overlappingWebReader) Read(context.Context, ReadSpec) (ReadResult, error) {
	return ReadResult{}, errors.New("content reader required")
}
func (f *overlappingWebReader) ReadContent(ctx context.Context, spec ReadSpec) (ContentReadResult, error) {
	n := f.calls.Add(1)
	body := f.newest
	if n == 0 {
		body = f.seed
	}
	if n == 1 {
		close(f.started)
		select {
		case <-f.release:
		case <-ctx.Done():
			return ContentReadResult{}, ctx.Err()
		}
		body = f.old
	}
	return ContentReadResult{Body: body, Observation: Observation{ExternalID: spec.URL,
		ContentHash: store.Hash(body), EnvironmentID: spec.EnvironmentID, ApplicationID: spec.ApplicationID, ACLVerified: true}}, nil
}

func webRaceFixture(t *testing.T, reader Reader) (*pgxpool.Pool, *httpapi.Server, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated disposable schema only")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "web_sync_race_" + store.ID()
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
	if _, err := db.Exec(t.Context(), `INSERT INTO users(id,email,name,role,password_hash) VALUES('reader','reader@example.invalid','Reader','viewer','unused');
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),('viewer','knowledge','manage','application','app');
 INSERT INTO applications(id,key,name,environment) VALUES('app','app','App','dev');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
 INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev');
 INSERT INTO v3_knowledge_source_registrations(id,environment_id,application_id,name,source_type,source_url,secret_ref,spaces,schedule,chunk,inherit_acl,created_by)
 VALUES('web-source','dev','app','Public page','web','https://docs.example.invalid/page.md','','[]','manual','heading',true,'reader');`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'reader',clock_timestamp()+interval '1 hour')`, store.Hash("test-reader-session")); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	NewWithReaders(db, policy, map[string]Reader{"web": reader}).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session,
		func(ctx context.Context, u core.User, m, a, e string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: m, Action: a, EnvironmentID: e})
		}); err != nil {
		t.Fatal(err)
	}
	return db, api, "test-reader-session"
}

func webSyncRequest(api *httpapi.Server, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-sources/registrations/web-source/sync", nil)
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestPublicWebSyncRejectsLateSnapshotAfterNewerCommit(t *testing.T) {
	for _, tc := range []struct {
		name, seed string
		version    int
	}{
		{"first import", "", 1}, {"new content", "# Initial\n", 2}, {"unchanged content still fences", "# Newest\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &overlappingWebReader{started: make(chan struct{}), release: make(chan struct{}), seed: tc.seed, old: "# Older\n", newest: "# Newest\n"}
			db, api, token := webRaceFixture(t, reader)
			if tc.seed != "" {
				reader.calls.Store(-1)
				w := webSyncRequest(api, token)
				if w.Code != 200 {
					t.Fatalf("seed: %d %s", w.Code, w.Body.String())
				}
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- webSyncRequest(api, token) }()
			select {
			case <-reader.started:
			case <-time.After(5 * time.Second):
				t.Fatal("first snapshot never started")
			}
			newer := webSyncRequest(api, token)
			close(reader.release)
			if newer.Code != 200 {
				t.Fatalf("newer snapshot: %d %s", newer.Code, newer.Body.String())
			}
			var late *httptest.ResponseRecorder
			select {
			case late = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("late snapshot never finished")
			}
			if late.Code != 409 {
				t.Fatalf("older snapshot committed after newer one: status=%d want=409 body=%s", late.Code, late.Body.String())
			}
			var body string
			var version, reads, audits int
			if err := db.QueryRow(t.Context(), `SELECT body,version,(SELECT count(*) FROM v3_knowledge_source_reads),
 (SELECT count(*) FROM management_audit WHERE action='v3.knowledge_source.read') FROM v3_public_web_documents WHERE registration_id='web-source'`).Scan(&body, &version, &reads, &audits); err != nil {
				t.Fatal(err)
			}
			wantReads := 1
			if tc.seed != "" {
				wantReads = 2
			}
			if body != reader.newest || version != tc.version || reads != wantReads || audits != wantReads {
				t.Fatalf("snapshot/audit changed: body=%q version=%d reads=%d audits=%d", body, version, reads, audits)
			}
			// A later explicit retry starts from the committed read and is allowed.
			if w := webSyncRequest(api, token); w.Code != 200 {
				t.Fatalf("fresh retry: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
