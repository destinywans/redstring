package v3tokens

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIssueLockKeyPostgresTextAndBoundaries(t *testing.T) {
	pairs := [][2]string{{"a", "bc"}, {"ab", "c"}, {"a|b", "c"}, {"a", "b|c"}, {"a\x00b", "c"}, {"a", "b\x00c"}, {"a\"", "b\\c"}}
	seen := map[string]bool{}
	for _, pair := range pairs {
		key := issueLockKey(pair[0], pair[1])
		if strings.ContainsRune(key, 0) || seen[key] {
			t.Fatal("issuance lock key is invalid PostgreSQL text or loses tuple boundaries")
		}
		seen[key] = true
		var tuple []string
		if json.Unmarshal([]byte(key), &tuple) != nil || len(tuple) != 3 || tuple[1] != pair[0] || tuple[2] != pair[1] {
			t.Fatal("issuance lock key does not preserve owner/environment")
		}
	}
}

func TestIssueConcurrentCapRealPostgres(t *testing.T) {
	if os.Getenv("AIOPS_TESTLAB_BACKEND_ENABLED") != "dedicated-temp-container" {
		t.Skip("explicit disposable testlab opt-in required")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/aiops_testlab" {
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
	results := make(chan error, 60)
	var wg sync.WaitGroup
	for range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Issue(t.Context(), actor, IssueInput{Name: "Concurrent fixture", Scopes: []string{"connectors:view"}, TTL: 3600})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, limited := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrTokenLimit) {
			limited++
		} else {
			t.Errorf("real issuance failed type=%T", err)
		}
	}
	if success != 50 || limited != 10 {
		t.Fatalf("concurrent cap success=%d limited=%d want 50/10", success, limited)
	}
	var count int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM v3_api_tokens WHERE revoked_at IS NULL`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 50 {
		t.Fatal("persisted token cap exceeded or issuance was lost")
	}
}
