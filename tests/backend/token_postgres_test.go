package backend_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3tokens"
)

// A real PostgreSQL diagnostic complements the public 201 assertion: failures
// expose only a SQLSTATE, never the original session or a generated token.
func TestTokenIssueRealPostgres(t *testing.T) {
	f := readFixture(t, true)
	c := authenticatedClient(t, f)
	db, err := pgxpool.New(t.Context(), f.DatabaseURL)
	if err != nil {
		t.Fatal("test pool unavailable")
	}
	defer db.Close()
	u, _ := url.Parse(f.BaseURLs[0])
	var token string
	for _, cookie := range c.Jar.Cookies(u) {
		if cookie.Name == "aiops_session" {
			token = cookie.Value
		}
	}
	s := &store.Store{DB: db}
	user, err := s.Session(t.Context(), token)
	if err != nil {
		t.Fatal("generated login session unavailable")
	}
	svc := v3tokens.New(db, &rbac.V3Policy{Store: s})
	actor := v3tokens.Actor{User: user, SessionToken: token, EnvironmentID: f.Environment}
	issued, err := svc.Issue(t.Context(), actor, v3tokens.IssueInput{Name: "Postgres transport regression", Scopes: []string{"connectors:view"}, TTL: 3600})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatalf("real token issuance failed SQLSTATE=%s", pgErr.Code)
		}
		t.Fatalf("real token issuance failed type=%T", err)
	}
	if issued.ID == "" || issued.SecretShownOnce == "" {
		t.Fatal("issuance omitted id or one-time secret")
	}
	if err := svc.Revoke(t.Context(), actor, issued.ID); err != nil {
		t.Fatal("generated token cleanup failed")
	}
}
