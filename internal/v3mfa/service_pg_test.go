package v3mfa

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type mfaFixture struct {
	pool  *pgxpool.Pool
	svc   *Service
	actor Actor
	clock *atomic.Int64
	key   []byte
}

func mfaDatabase(t *testing.T) *mfaFixture {
	t.Helper()
	if os.Getenv("AIOPS_TEST_WAN124_MFA_PG") != "dedicated-temp-container" {
		t.Skip("requires explicit WAN-124 disposable PostgreSQL opt-in")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/wan124mfa" {
		t.Fatal("TEST_DATABASE_URL must target dedicated loopback wan124mfa database")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var extensionSchema string
	if err = admin.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e
	 JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='pgcrypto'`).Scan(&extensionSchema); err != nil || extensionSchema != "public" {
		admin.Close()
		t.Fatal("preinstall pgcrypto in public of the disposable test database")
	}
	schema := "wan124_mfa_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	clock := &atomic.Int64{}
	clock.Store(time.Now().UTC().Truncate(totpPeriod).Unix())
	key := []byte("0123456789abcdef0123456789abcdef")
	svc, err := New(pool, key, "test-instance", func() time.Time { return time.Unix(clock.Load(), 0).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	actor := Actor{UserID: "mfa-owner", SessionToken: "mfa-session-one", Tier: "nonprod", EnvironmentID: "lab"}
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
	 ('mfa-owner','owner@example.invalid','Owner','admin','unused'),
	 ('mfa-machine','machine@example.invalid','Machine','admin','unused')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id='mfa-machine'`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at,issued_at) VALUES
	 ($1,'mfa-owner',$4,$3),($2,'mfa-owner',$4,$3),($5,'mfa-machine',$4,$3)`,
		store.Hash(actor.SessionToken), store.Hash("mfa-session-two"),
		time.Unix(clock.Load(), 0).UTC(), time.Unix(clock.Load(), 0).UTC().Add(time.Hour),
		store.Hash("machine-session")); err != nil {
		t.Fatal(err)
	}
	return &mfaFixture{pool: pool, svc: svc, actor: actor, clock: clock, key: key}
}

func (f *mfaFixture) code(t *testing.T, secret string) string {
	t.Helper()
	seed, err := base32NoPad.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return hotp(seed, f.clock.Load()/30, 6)
}

func (f *mfaFixture) enable(t *testing.T) (Enrollment, Confirmation) {
	t.Helper()
	enrollment, err := f.svc.BeginEnrollment(t.Context(), f.actor)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := f.svc.ConfirmEnrollment(t.Context(), f.actor, f.code(t, enrollment.Secret))
	if err != nil {
		t.Fatal(err)
	}
	return enrollment, confirmed
}

func TestMFAFullSchemaLifecycle(t *testing.T) {
	f := mfaDatabase(t)
	ctx := t.Context()
	status, err := f.svc.Status(ctx, f.actor)
	if err != nil || status.State != "none" {
		t.Fatalf("initial status: %+v, %v", status, err)
	}
	enrollment, err := f.svc.BeginEnrollment(ctx, f.actor)
	if err != nil || len(enrollment.Secret) != 32 || !strings.Contains(enrollment.OTPAuthURL, "secret="+enrollment.Secret) {
		t.Fatalf("enrollment: %+v, %v", enrollment, err)
	}
	if _, err = f.svc.BeginEnrollment(ctx, f.actor); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending secret reissued: %v", err)
	}
	other := f.actor
	other.SessionToken = "mfa-session-two"
	if err = f.svc.CancelEnrollment(ctx, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("other session cancelled pending enrollment: %v", err)
	}
	if _, err = f.svc.ConfirmEnrollment(ctx, other, f.code(t, enrollment.Secret)); !errors.Is(err, ErrConflict) {
		t.Fatalf("other session confirmed pending seed: %v", err)
	}
	other = f.actor
	other.EnvironmentID = "other"
	if _, err = f.svc.ConfirmEnrollment(ctx, other, f.code(t, enrollment.Secret)); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrForbidden) {
		t.Fatalf("different environment confirmed pending seed: %v", err)
	}
	if err = f.svc.CancelEnrollment(ctx, f.actor); err != nil {
		t.Fatalf("initiating session could not cancel pending enrollment: %v", err)
	}
	if _, err = f.svc.ConfirmEnrollment(ctx, f.actor, f.code(t, enrollment.Secret)); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled seed still confirmed: %v", err)
	}
	enrollment, err = f.svc.BeginEnrollment(ctx, f.actor)
	if err != nil {
		t.Fatalf("enroll again after cancellation: %v", err)
	}
	confirmed, err := f.svc.ConfirmEnrollment(ctx, f.actor, f.code(t, enrollment.Secret))
	if err != nil || !confirmed.Enabled || len(confirmed.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("confirmation: %+v, %v", confirmed, err)
	}
	status, err = f.svc.Status(ctx, f.actor)
	if err != nil || status.State != "active" || status.RecoveryRemaining != recoveryCodeCount {
		t.Fatalf("active status: %+v, %v", status, err)
	}
	if _, err = f.svc.Verify(ctx, f.actor, f.code(t, enrollment.Secret)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("confirmation step replayed: %v", err)
	}
	f.clock.Add(30)
	result, err := f.svc.Verify(ctx, f.actor, f.code(t, enrollment.Secret))
	if err != nil || !result.Verified || result.Method != "totp" {
		t.Fatalf("TOTP verify: %+v, %v", result, err)
	}
	if _, err = f.svc.Verify(ctx, f.actor, f.code(t, enrollment.Secret)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("TOTP replayed: %v", err)
	}
	result, err = f.svc.Verify(ctx, f.actor, strings.ToLower(confirmed.RecoveryCodes[0]))
	if err != nil || result.Method != "recovery" {
		t.Fatalf("recovery verify: %+v, %v", result, err)
	}
	if _, err = f.svc.Verify(ctx, f.actor, confirmed.RecoveryCodes[0]); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("recovery code replayed: %v", err)
	}
	var plaintext bool
	if err = f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_mfa_totp
	 WHERE encode(seed_ciphertext,'hex') LIKE '%' || $1 || '%')`, enrollment.Secret).Scan(&plaintext); err != nil || plaintext {
		t.Fatalf("plaintext seed persisted: %t, %v", plaintext, err)
	}
	var auditCount, remaining int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action LIKE 'mfa.totp.%'`, f.actor.UserID).Scan(&auditCount); err != nil || auditCount < 6 {
		t.Fatalf("MFA transaction audit count=%d, err=%v", auditCount, err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM v3_mfa_recovery_codes WHERE consumed_at IS NULL`).Scan(&remaining); err != nil || remaining != recoveryCodeCount-1 {
		t.Fatalf("remaining codes=%d, err=%v", remaining, err)
	}
	// A new service with the same trusted key must read durable state; a wrong
	// key must fail visibly instead of treating the factor as absent.
	fresh, err := New(f.pool, f.key, "test-instance", f.svc.now)
	if err != nil {
		t.Fatal(err)
	}
	status, err = fresh.Status(ctx, f.actor)
	if err != nil || status.RecoveryRemaining != recoveryCodeCount-1 {
		t.Fatalf("restart status: %+v, %v", status, err)
	}
	wrong, err := New(f.pool, []byte("abcdef0123456789abcdef0123456789"), "test-instance", f.svc.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Status(ctx, f.actor); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("wrong key silently accepted: %v", err)
	}
	if err = fresh.Cancel(ctx, f.actor, confirmed.RecoveryCodes[1]); err != nil {
		t.Fatalf("cancel with one-use code: %v", err)
	}
	status, err = fresh.Status(ctx, f.actor)
	if err != nil || status.State != "none" {
		t.Fatalf("post-cancel state: %+v, %v", status, err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM v3_mfa_recovery_codes`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("recovery cascade: %d, %v", remaining, err)
	}
}

func TestMFAGuardsAndRateLimit(t *testing.T) {
	f := mfaDatabase(t)
	ctx := t.Context()
	machine := Actor{UserID: "mfa-machine", SessionToken: "machine-session", Tier: "nonprod", EnvironmentID: "lab"}
	if _, err := f.svc.BeginEnrollment(ctx, machine); !errors.Is(err, ErrForbidden) {
		t.Fatalf("machine enrollment: %v", err)
	}
	if _, err := f.svc.BeginEnrollment(ctx, Actor{UserID: f.actor.UserID, SessionToken: f.actor.SessionToken, Tier: "nonprod", EnvironmentID: "all"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("all-environment enrollment: %v", err)
	}
	enrollment, _ := f.enable(t)
	if _, err := f.pool.Exec(ctx, `UPDATE users SET disabled=true WHERE id=$1`, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Verify(ctx, f.actor, f.code(t, enrollment.Secret)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled account verified: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE users SET disabled=false WHERE id=$1`, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(f.actor.SessionToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Verify(ctx, f.actor, f.code(t, enrollment.Secret)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked session verified: %v", err)
	}
	f.actor.SessionToken = "mfa-session-two"
	for i := 0; i < maxFailures; i++ {
		_, err := f.svc.Verify(ctx, f.actor, "000000")
		if i == maxFailures-1 && !errors.Is(err, ErrRateLimited) || i < maxFailures-1 && !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.Verify(ctx, f.actor, f.code(t, enrollment.Secret)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("valid code during lockout: %v", err)
	}
	f.clock.Add(int64(lockoutDuration/time.Second) + 1)
	if _, err := f.svc.Verify(ctx, f.actor, f.code(t, enrollment.Secret)); err != nil {
		t.Fatalf("valid code after lockout: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM v3_role_grants
	 WHERE role_id='admin' AND module='environment' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Status(ctx, f.actor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked environment grant retained MFA access: %v", err)
	}
}

func TestMFAConfirmLockoutAudited(t *testing.T) {
	f := mfaDatabase(t)
	ctx := t.Context()
	enrollment, err := f.svc.BeginEnrollment(ctx, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailures; i++ {
		_, err = f.svc.ConfirmEnrollment(ctx, f.actor, "not-a-code")
		if i == maxFailures-1 && !errors.Is(err, ErrRateLimited) || i < maxFailures-1 && !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("confirm attempt %d: %v", i+1, err)
		}
	}
	if _, err = f.svc.ConfirmEnrollment(ctx, f.actor, f.code(t, enrollment.Secret)); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("valid confirmation during lockout: %v", err)
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM management_audit
	 WHERE actor_id=$1 AND action='mfa.totp.confirm_denied' AND outcome='blocked'`, f.actor.UserID).Scan(&count); err != nil || count != maxFailures+1 {
		t.Fatalf("confirm denial audit count=%d, err=%v", count, err)
	}
}

func TestMFAConcurrentRecoveryConsumption(t *testing.T) {
	f := mfaDatabase(t)
	_, confirmed := f.enable(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.svc.Verify(context.Background(), f.actor, confirmed.RecoveryCodes[0])
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var success, denied int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrInvalidCode) {
			denied++
		} else {
			t.Fatalf("concurrent verify: %v", err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("recovery race: success=%d denied=%d", success, denied)
	}
}

func TestMFAAuxiliaryHTTPRoutes(t *testing.T) {
	f := mfaDatabase(t)
	st := &store.Store{DB: f.pool}
	api := httpapi.New(st, nil, true)
	api.AllowedOrigin = "http://example.com"
	f.svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "lab", EnvironmentIDs: []string{"lab", "other"},
	}, &rbac.V3Policy{Store: st}, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, env, session, origin, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
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
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("status=%d body=%s; want %d %q", w.Code, w.Body.String(), status, code)
		}
	}
	const statusPath = "/api/v1/me/mfa/totp"
	const beginPath = "/api/v1/me/mfa/totp/enrollment"
	assert(request(http.MethodGet, statusPath, "lab", "", "", ""), 401, "unauthorized")
	assert(request(http.MethodGet, statusPath, "lab", f.actor.SessionToken, "", ""), 200, "")
	assert(request(http.MethodPost, beginPath, "all", f.actor.SessionToken, "http://example.com", ""), 409, "env_view_only")
	assert(request(http.MethodPost, beginPath, "lab", f.actor.SessionToken, "", ""), 403, "mfa_origin_required")
	w := request(http.MethodPost, beginPath, "lab", f.actor.SessionToken, "http://example.com", "")
	assert(w, 201, "")
	var enrollment Enrollment
	if err := json.Unmarshal(w.Body.Bytes(), &enrollment); err != nil || len(enrollment.Secret) != 32 {
		t.Fatalf("HTTP enrollment: %v, %s", err, w.Body.String())
	}
	status := request(http.MethodGet, statusPath, "lab", f.actor.SessionToken, "", "")
	assert(status, 200, "")
	if !strings.Contains(status.Body.String(), `"state":"pending"`) {
		t.Fatalf("pending status: %s", status.Body.String())
	}
	w = request(http.MethodPost, "/api/v1/me/mfa/totp/confirm", "lab", f.actor.SessionToken,
		"http://example.com", `{"code":"`+f.code(t, enrollment.Secret)+`"}`)
	assert(w, 200, "")
	var confirmed Confirmation
	if err := json.Unmarshal(w.Body.Bytes(), &confirmed); err != nil || !confirmed.Enabled || len(confirmed.RecoveryCodes) != recoveryCodeCount {
		t.Fatalf("HTTP confirmation: %v, %s", err, w.Body.String())
	}
	status = request(http.MethodGet, statusPath, "lab", f.actor.SessionToken, "", "")
	assert(status, 200, "")
	if !strings.Contains(status.Body.String(), `"state":"active"`) {
		t.Fatalf("active status: %s", status.Body.String())
	}
}
