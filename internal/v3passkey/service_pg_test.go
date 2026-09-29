package v3passkey

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type passkeyFixture struct {
	pool  *pgxpool.Pool
	svc   *Service
	actor Actor
}

// This fixture can only touch an explicitly selected disposable loopback DB.
// Each test gets a new schema and the production migration sequence.
func passkeyDatabase(t *testing.T) *passkeyFixture {
	t.Helper()
	if os.Getenv("AIOPS_TEST_WAN124_PASSKEY_PG") != "dedicated-temp-container" {
		t.Skip("requires explicit WAN-124 disposable PostgreSQL opt-in")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/wan124passkey" {
		t.Fatal("TEST_DATABASE_URL must target dedicated loopback wan124passkey database")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"pgcrypto", "vector"} {
		var schema string
		if err := admin.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e
		 JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname=$1`, ext).Scan(&schema); err != nil || schema != "public" {
			admin.Close()
			t.Fatalf("preinstall %s in public of the disposable test database: %v", ext, err)
		}
	}
	schema := "wan124_passkey_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = (&store.Store{DB: pool}).Migrate(ctx); err != nil {
		t.Fatalf("full migration in %s: %v", schema, err)
	}
	svc, err := New(testConfig(pool))
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{UserID: "passkey-owner", SessionToken: "passkey-session-one", Tier: "nonprod", EnvironmentID: "lab"}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
	 ('passkey-owner','passkey-owner@example.invalid','Owner','admin','unused'),
	 ('passkey-machine','passkey-machine@example.invalid','Machine','admin','unused')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id='passkey-machine'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at,issued_at) VALUES
	 ($1,'passkey-owner',$4,$3),($2,'passkey-owner',$4,$3),($5,'passkey-machine',$4,$3)`,
		store.Hash(actor.SessionToken), store.Hash("passkey-session-two"), now,
		now.Add(time.Hour), store.Hash("machine-session")); err != nil {
		t.Fatal(err)
	}
	return &passkeyFixture{pool: pool, svc: svc, actor: actor}
}

func (f *passkeyFixture) challengeCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM v3_passkey_challenges`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPasskeyProposalProofUnavailableWithoutImmutableDigest(t *testing.T) {
	f := passkeyDatabase(t)
	before := f.challengeCount(t)
	for _, proposalID := range []string{"proposal-1", ""} {
		if _, err := f.svc.BeginProposalAssertion(t.Context(), f.actor, proposalID); !errors.Is(err, ErrProposalDigestUnavailable) {
			t.Fatalf("proposal %q assertion: %v", proposalID, err)
		}
		if err := f.svc.FinishProposalAssertion(t.Context(), f.actor, proposalID, "challenge-id", []byte(`{"type":"public-key"}`)); !errors.Is(err, ErrProposalDigestUnavailable) {
			t.Fatalf("proposal %q proof exchange: %v", proposalID, err)
		}
	}
	if got := f.challengeCount(t); got != before {
		t.Fatalf("proposal path issued %d challenges, want %d", got, before)
	}
}

func TestPasskeyRegistrationChallengeScopeAndGuards(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	machine := Actor{UserID: "passkey-machine", SessionToken: "machine-session", Tier: "nonprod", EnvironmentID: "lab"}
	if _, err := f.svc.BeginRegistration(ctx, machine); !errors.Is(err, ErrForbidden) {
		t.Fatalf("machine account began passkey registration: %v", err)
	}
	all := f.actor
	all.EnvironmentID = "all"
	if _, err := f.svc.BeginRegistration(ctx, all); !errors.Is(err, ErrForbidden) {
		t.Fatalf("all-environment registration: %v", err)
	}
	if got := f.challengeCount(t); got != 0 {
		t.Fatalf("ineligible actors created %d challenges", got)
	}
	ceremony, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil || ceremony.ID == "" || ceremony.Options == nil || ceremony.ExpiresAt.Before(time.Now()) {
		t.Fatalf("registration ceremony: %+v, %v", ceremony, err)
	}
	var challengeUser, sessionHash, tier, environment, kind string
	var expiresAt time.Time
	if err = f.pool.QueryRow(ctx, `SELECT user_id,session_hash,tier,environment_id,kind,expires_at
	 FROM v3_passkey_challenges WHERE id=$1`, ceremony.ID).Scan(
		&challengeUser, &sessionHash, &tier, &environment, &kind, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if challengeUser != f.actor.UserID || sessionHash != store.Hash(f.actor.SessionToken) ||
		tier != f.actor.Tier || environment != f.actor.EnvironmentID || kind != "register" ||
		expiresAt.After(time.Now().Add(10*time.Minute)) {
		t.Fatalf("incorrect durable challenge scope or TTL: user=%q tier=%q env=%q kind=%q expires=%s",
			challengeUser, tier, environment, kind, expiresAt)
	}
	other := f.actor
	other.SessionToken = "passkey-session-two"
	if _, err = f.svc.FinishRegistration(ctx, other, ceremony.ID, []byte("invalid"), "Laptop"); err == nil {
		t.Fatal("another session completed registration challenge")
	}
	other = f.actor
	other.EnvironmentID = "other"
	if _, err = f.svc.FinishRegistration(ctx, other, ceremony.ID, []byte("invalid"), "Laptop"); err == nil {
		t.Fatal("another environment completed registration challenge")
	}
	var consumed bool
	if err = f.pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM v3_passkey_challenges WHERE id=$1`, ceremony.ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed {
		t.Fatal("other context consumed initiating session's challenge")
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, ceremony.ID, []byte("invalid"), "Laptop"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("invalid authenticator response: %v", err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, ceremony.ID, []byte("invalid"), "Laptop"); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("invalid response challenge replay: %v", err)
	}
	var credentials int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM v3_passkey_credentials`).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("invalid registration saved %d credentials: %v", credentials, err)
	}
}

func TestPasskeyAssertionRequiresCredentialAndActiveSession(t *testing.T) {
	f := passkeyDatabase(t)
	if _, err := f.svc.BeginAssertion(t.Context(), f.actor); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("assertion without credential: %v", err)
	}
	if got := f.challengeCount(t); got != 0 {
		t.Fatalf("assertion without credential issued %d challenges", got)
	}
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(f.actor.SessionToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BeginRegistration(t.Context(), f.actor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked session began registration: %v", err)
	}
}

func TestPasskeyFreshLoginAndChallengeExpiry(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET issued_at=now()-interval '11 minutes'
	 WHERE token_hash=$1`, store.Hash(f.actor.SessionToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BeginRegistration(ctx, f.actor); !errors.Is(err, ErrFreshLoginRequired) {
		t.Fatalf("stale login began registration: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET issued_at=now()
	 WHERE token_hash=$1`, store.Hash(f.actor.SessionToken)); err != nil {
		t.Fatal(err)
	}
	ceremony, err := f.svc.BeginRegistration(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE v3_passkey_challenges
	 SET created_at=now()-interval '6 minutes',expires_at=now()-interval '1 minute'
	 WHERE id=$1`, ceremony.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.FinishRegistration(ctx, f.actor, ceremony.ID, []byte("invalid"), "Device"); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("expired challenge: %v", err)
	}
	if got := f.challengeCount(t); got != 1 {
		t.Fatalf("expired challenge unexpectedly removed: %d", got)
	}
	var consumed bool
	if err = f.pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM v3_passkey_challenges WHERE id=$1`, ceremony.ID).Scan(&consumed); err != nil || !consumed {
		t.Fatalf("expired challenge not consumed: %t, %v", consumed, err)
	}
}

func TestPasskeyRegisteredHTTPRoute(t *testing.T) {
	f := passkeyDatabase(t)
	st := &store.Store{DB: f.pool}
	api := httpapi.New(st, nil, true)
	api.AllowedOrigin = "https://aiops.example.invalid"
	f.svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "lab", EnvironmentIDs: []string{"lab", "other"},
	}, &rbac.V3Policy{Store: st}, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	const path = "https://aiops.example.invalid/api/v1/me/passkeys/registration/options"
	requestAt := func(target, env, session, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	request := func(env, session, origin, body string) *httptest.ResponseRecorder {
		return requestAt(path, env, session, origin, body)
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("status=%d body=%s; want %d %q", w.Code, w.Body.String(), status, code)
		}
	}
	assert(request("lab", "", "https://aiops.example.invalid", ""), 401, "unauthorized")
	assert(request("all", f.actor.SessionToken, "https://aiops.example.invalid", ""), 409, "env_view_only")
	assert(request("lab", f.actor.SessionToken, "", ""), 403, "passkey_origin_required")
	assert(request("lab", f.actor.SessionToken, "https://attacker.example.invalid", ""), 403, "origin_rejected")
	assert(request("lab", f.actor.SessionToken, "https://aiops.example.invalid", `{}`), 422, "passkey_invalid_input")
	const internalPath = "http://127.0.0.1:8080/api/v1/me/passkeys/registration/options"
	assert(requestAt(internalPath, "lab", f.actor.SessionToken, "https://aiops.example.invalid", ""),
		403, "passkey_secure_origin_required")
	api.SecureCookies = true
	api.AllowedOrigin = "https://other.example.invalid"
	assert(requestAt(internalPath, "lab", f.actor.SessionToken, "https://other.example.invalid", ""),
		403, "passkey_secure_origin_required")
	api.AllowedOrigin = "https://aiops.example.invalid"
	if got := f.challengeCount(t); got != 0 {
		t.Fatalf("denied HTTP writes created %d challenges", got)
	}
	w := requestAt(internalPath, "lab", f.actor.SessionToken, "https://aiops.example.invalid", "")
	assert(w, 201, "")
	var ceremony Ceremony
	if err := json.Unmarshal(w.Body.Bytes(), &ceremony); err != nil || ceremony.ID == "" || ceremony.Options == nil {
		t.Fatalf("registered HTTP begin response: %v, %s", err, w.Body.String())
	}
	if got := f.challengeCount(t); got != 1 {
		t.Fatalf("HTTP begin stored %d challenges, want one", got)
	}
}

func TestPasskeyRegistrationRateLimitPreservesPendingChallenge(t *testing.T) {
	f := passkeyDatabase(t)
	ctx := t.Context()
	var latest Ceremony
	for i := 0; i < 10; i++ {
		var err error
		latest, err = f.svc.BeginRegistration(ctx, f.actor)
		if err != nil {
			t.Fatalf("registration challenge %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.BeginRegistration(ctx, f.actor); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("eleventh challenge issuance: %v", err)
	}
	var count int
	var consumed bool
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM v3_passkey_challenges`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM v3_passkey_challenges WHERE id=$1`, latest.ID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if count != 10 || consumed {
		t.Fatalf("rate limit count=%d latest consumed=%t", count, consumed)
	}
}
