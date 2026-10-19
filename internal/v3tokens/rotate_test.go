package v3tokens

import (
	"context"
	"errors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTokenRotationOverlapAndAuditRollbackIsolated(t *testing.T) {
	if os.Getenv("AIOPS_TESTLAB_BACKEND_ENABLED") != "dedicated-temp-container" {
		t.Skip("explicit disposable testlab opt-in required")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/postgres" {
		t.Fatal("explicit disposable loopback PostgreSQL required")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal("test pool unavailable")
	}
	defer admin.Close()
	schema := "token_issue_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test pool")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 8
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("test pool unavailable")
	}
	defer func() {
		db.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	s := &store.Store{DB: db}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap(t.Context(), "token-fixture@example.invalid", "isolated-token-fixture-password"); err != nil {
		t.Fatal(err)
	}
	user, session, err := s.Login(t.Context(), "token-fixture@example.invalid", "isolated-token-fixture-password")
	if err != nil {
		t.Fatal("fixture login failed")
	}
	svc := New(db, &rbac.V3Policy{Store: s})
	actor := Actor{User: user, SessionToken: session, EnvironmentID: "test"}

	for _, ttl := range []int64{3600, 604800} {
		issued, err := svc.Issue(t.Context(), actor, IssueInput{Name: "Rotation fixture", Scopes: []string{"connectors:view"}, TTL: ttl})
		if err != nil {
			t.Fatal(err)
		}
		rotated, err := svc.Rotate(t.Context(), actor, issued.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rotated.ID == issued.ID || rotated.SecretShownOnce == "" || rotated.SecretShownOnce == issued.SecretShownOnce {
			t.Fatal("rotation did not issue unique secret")
		}
		if rotated.OldExpiresAt.After(issued.ExpiresAt) || rotated.OldExpiresAt.After(time.Now().Add(24*time.Hour+time.Second)) {
			t.Fatal("rotation extended old expiry")
		}
		if ttl == 3600 && !rotated.OldExpiresAt.Equal(issued.ExpiresAt) {
			t.Fatal("short old lifespan changed")
		}
		if _, err := svc.Rotate(t.Context(), actor, issued.ID); !errors.Is(err, ErrTokenInactive) {
			t.Fatalf("repeat rotation=%v", err)
		}
		var stored int
		if err := db.QueryRow(t.Context(), `SELECT octet_length(secret_hash) FROM v3_api_tokens WHERE id=$1`, rotated.ID).Scan(&stored); err != nil || stored != 32 {
			t.Fatal("digest storage invalid", err)
		}
	}

	concurrent, err := svc.Issue(t.Context(), actor, IssueInput{Name: "Concurrent rotation", Scopes: []string{"connectors:view"}, TTL: 3600})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := svc.Rotate(t.Context(), actor, concurrent.ID); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrTokenInactive) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent rotation %d/%d", success, conflict)
	}
	policy := &rbac.V3Policy{Store: s}
	api := httpapi.New(s, nil, true)
	svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "dev"}}, policy, s.Session, func(ctx context.Context, user core.User, module, action, env string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
	}); err != nil {
		t.Fatal(err)
	}
	call := func(env, id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/api-tokens/"+id+"/rotate", nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := call("all", concurrent.ID); w.Code != 409 {
		t.Fatalf("all rotation %d %s", w.Code, w.Body.String())
	}
	if w := call("dev", concurrent.ID); w.Code != 404 {
		t.Fatalf("cross env rotation %d %s", w.Code, w.Body.String())
	}
	if w := call("test", concurrent.ID); w.Code != 409 || strings.Contains(w.Body.String(), "aiops_pat_") {
		t.Fatalf("repeat HTTP rotation %d %s", w.Code, w.Body.String())
	}
	issued, err := svc.Issue(t.Context(), actor, IssueInput{Name: "Rollback fixture", Scopes: []string{"connectors:view"}, TTL: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `CREATE FUNCTION reject_rotation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='v3.api_token.rotate' THEN RAISE EXCEPTION 'audit fixture'; END IF;RETURN NEW;END $$;CREATE TRIGGER reject_rotation_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_rotation_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rotate(t.Context(), actor, issued.ID); err == nil {
		t.Fatal("rotation succeeded without audit")
	}
	var unchanged bool
	if err := db.QueryRow(t.Context(), `SELECT rotated_at IS NULL AND expires_at=$2 FROM v3_api_tokens WHERE id=$1`, issued.ID, issued.ExpiresAt).Scan(&unchanged); err != nil || !unchanged {
		t.Fatal("audit failure changed old token", err)
	}
}
