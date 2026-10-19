package v3identity

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func isolatedStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_oidc_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
	})
	s := &store.Store{DB: db}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

type fakeOIDC struct {
	url                    string
	key                    *rsa.PrivateKey
	challenge, nonce, mode string
	requestCount           int
	discoveryToken         string
	discoveryJWKS          string
}

func (f *fakeOIDC) signedToken(subject string) string {
	nonce, audience := f.nonce, "aiops-test"
	issuer := f.url
	expires := time.Now().Add(5 * time.Minute).Unix()
	if f.mode == "wrong_nonce" {
		nonce = "wrong"
	}
	if f.mode == "wrong_audience" {
		audience = "other-client"
	}
	if f.mode == "unknown_subject" {
		subject = "other-subject"
	}
	if f.mode == "wrong_issuer" {
		issuer = "http://127.0.0.1:1"
	}
	if f.mode == "expired_token" {
		expires = time.Now().Add(-time.Minute).Unix()
	}
	claims := map[string]any{"iss": issuer, "sub": subject, "aud": audience, "nonce": nonce,
		"exp": expires, "iat": time.Now().Add(-time.Second).Unix(),
		"email": "admin@example.invalid"}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "test-key"})
	payload, _ := json.Marshal(claims)
	content := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(content))
	key := f.key
	if f.mode == "bad_signature" {
		key, _ = rsa.GenerateKey(rand.Reader, 2048)
	}
	signature, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return content + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func newFakeOIDC(t *testing.T) (*fakeOIDC, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{key: key}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			tokenURL, jwksURL := f.url+"/token", f.url+"/jwks"
			if f.discoveryToken != "" {
				tokenURL = f.discoveryToken
			}
			if f.discoveryJWKS != "" {
				jwksURL = f.discoveryJWKS
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.url, "authorization_endpoint": f.url + "/authorize", "token_endpoint": tokenURL, "jwks_uri": jwksURL})
		case "/jwks":
			e := big.NewInt(int64(f.key.PublicKey.E)).Bytes()
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test-key", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(f.key.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e)}}})
		case "/token":
			f.requestCount++
			w.Header().Set("Content-Type", "application/json")
			_ = r.ParseForm()
			verifier := r.Form.Get("code_verifier")
			digest := sha256.Sum256([]byte(verifier))
			if r.Form.Get("code") != "one-code" || base64.RawURLEncoding.EncodeToString(digest[:]) != f.challenge {
				http.Error(w, "invalid_grant", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "not-used", "token_type": "Bearer", "expires_in": 300, "id_token": f.signedToken("provisioned-subject")})
		default:
			http.NotFound(w, r)
		}
	}))
	f.url = server.URL
	return f, server
}

func startFlow(t *testing.T, api *httpapi.Server, provider *fakeOIDC) (string, *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/auth/oidc/start", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("start %d %s", w.Code, w.Body.String())
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := location.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("client_id") != "aiops-test" {
		t.Fatalf("missing OAuth defenses in authorization URL: %s", location.String())
	}
	provider.challenge, provider.nonce = q.Get("code_challenge"), q.Get("nonce")
	for _, c := range w.Result().Cookies() {
		if c.Name == stateCookie {
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != callbackPath {
				t.Fatalf("unsafe state cookie %+v", c)
			}
			return q.Get("state"), c
		}
	}
	t.Fatal("missing state cookie")
	return "", nil
}

func callback(api *httpapi.Server, state string, cookie *http.Cookie) *httptest.ResponseRecorder {
	u := "/api/v1/auth/oidc/callback?state=" + url.QueryEscape(state) + "&code=one-code"
	r := httptest.NewRequest("GET", u, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestOIDCExactSubjectAndOneTimeFlowPostgres(t *testing.T) {
	s := isolatedStore(t)
	provider, server := newFakeOIDC(t)
	defer server.Close()
	service, err := New(context.Background(), s, Config{Issuer: server.URL, ClientID: "aiops-test", RedirectURL: "http://127.0.0.1:18081" + callbackPath,
		PublicOrigin: "http://127.0.0.1:18081", Tier: "nonprod", AllowLoopbackHTTP: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,disabled,source,external_id)
		VALUES ('local-admin','admin@example.invalid','Admin','admin',$1,false,'local',''),
		('external','external@example.invalid','External','viewer',$2,false,'local','')`, store.PasswordHash("local-secure-password"), store.PasswordHash("another-local-password"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'local-admin',now()+interval '1 hour')`, store.Hash("admin-session"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash,source)
		VALUES('delegated','delegated@example.invalid','Delegated','viewer',$1,'local')`, store.PasswordHash("delegated-password"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO rbac_roles(id,name) VALUES('delegated-oidc-test','Delegated OIDC test')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES('delegated','delegated-oidc-test')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
		 VALUES('delegated-oidc-test','environment','view','global','*'),('delegated-oidc-test','members','manage','global','*')`,
	} {
		if _, err = s.DB.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'delegated',now()+interval '1 hour')`, store.Hash("delegated-session"))
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(s, nil, true)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		allowEnvironment{}, s.Session, func(_ context.Context, u core.User, _, _, _ string) error {
			if u.ID == "local-admin" || u.ID == "delegated" {
				return nil
			}
			return store.ErrForbidden
		}); err != nil {
		t.Fatal(err)
	}
	service.Register(api)
	unauthorizedBody, _ := json.Marshal(map[string]string{"user_id": "local-admin", "subject": "attacker-subject"})
	unauthorizedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/bindings", bytes.NewReader(unauthorizedBody))
	unauthorizedRequest.Header.Set("X-AIOps-Environment", "test")
	unauthorizedRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: "delegated-session"})
	unauthorizedResponse := httptest.NewRecorder()
	api.ServeHTTP(unauthorizedResponse, unauthorizedRequest)
	if unauthorizedResponse.Code != 403 {
		t.Fatalf("delegated manager bound admin: %d %s", unauthorizedResponse.Code, unauthorizedResponse.Body.String())
	}
	bindInput, _ := json.Marshal(map[string]string{"user_id": "external", "subject": "provisioned-subject"})
	bindRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/bindings", bytes.NewReader(bindInput))
	bindRequest.Header.Set("X-AIOps-Environment", "test")
	bindRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: "admin-session"})
	bindResponse := httptest.NewRecorder()
	api.ServeHTTP(bindResponse, bindRequest)
	if bindResponse.Code != 201 {
		t.Fatalf("bind %d %s", bindResponse.Code, bindResponse.Body.String())
	}
	allRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/bindings", bytes.NewReader(bindInput))
	allRequest.Header.Set("X-AIOps-Environment", "all")
	allRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: "admin-session"})
	allResponse := httptest.NewRecorder()
	api.ServeHTTP(allResponse, allRequest)
	if allResponse.Code != 409 {
		t.Fatalf("all environment write %d %s", allResponse.Code, allResponse.Body.String())
	}
	duplicateRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/bindings", bytes.NewReader(bindInput))
	duplicateRequest.Header.Set("X-AIOps-Environment", "test")
	duplicateRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: "admin-session"})
	duplicateResponse := httptest.NewRecorder()
	api.ServeHTTP(duplicateResponse, duplicateRequest)
	if duplicateResponse.Code != 409 {
		t.Fatalf("duplicate binding %d %s", duplicateResponse.Code, duplicateResponse.Body.String())
	}
	conflictBody, _ := json.Marshal(map[string]string{"user_id": "delegated", "subject": "provisioned-subject"})
	conflictRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/bindings", bytes.NewReader(conflictBody))
	conflictRequest.Header.Set("X-AIOps-Environment", "test")
	conflictRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: "admin-session"})
	conflictResponse := httptest.NewRecorder()
	api.ServeHTTP(conflictResponse, conflictRequest)
	if conflictResponse.Code != 409 {
		t.Fatalf("duplicate subject %d %s", conflictResponse.Code, conflictResponse.Body.String())
	}
	state, cookie := startFlow(t, api, provider)
	if w := callback(api, state, nil); w.Code != 401 {
		t.Fatalf("unbound state accepted: %d", w.Code)
	}
	if w := callback(api, state, &http.Cookie{Name: stateCookie, Value: strings.Repeat("x", 43)}); w.Code != 401 {
		t.Fatalf("wrong browser state accepted: %d", w.Code)
	}
	w := callback(api, state, cookie)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
		t.Fatalf("callback %d %s exchanges=%d", w.Code, w.Body.String(), provider.requestCount)
	}
	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "aiops_session" {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("normal session cookie not issued")
	}
	user, err := s.Session(ctx, session.Value)
	if err != nil || user.ID != "external" {
		t.Fatalf("wrong local session: %+v %v", user, err)
	}
	protected := httptest.NewRequest("GET", "/api/v1/auth/oidc/bindings/external", nil)
	protected.Header.Set("X-AIOps-Environment", "test")
	protected.AddCookie(session)
	protectedResponse := httptest.NewRecorder()
	api.ServeHTTP(protectedResponse, protected)
	if protectedResponse.Code != http.StatusForbidden {
		t.Fatalf("OIDC login bypassed environment policy: %d", protectedResponse.Code)
	}
	if again := callback(api, state, cookie); again.Code != 401 || provider.requestCount != 1 {
		t.Fatalf("state replay: %d, exchanges=%d", again.Code, provider.requestCount)
	}
	for _, mode := range []string{"wrong_nonce", "wrong_audience", "wrong_issuer", "expired_token", "bad_signature", "unknown_subject"} {
		provider.mode = mode
		state, cookie = startFlow(t, api, provider)
		if bad := callback(api, state, cookie); bad.Code < 400 {
			t.Fatalf("%s accepted: %d", mode, bad.Code)
		}
	}
	provider.mode = ""
	state, cookie = startFlow(t, api, provider)
	provider.challenge = "tampered"
	if badCode := callback(api, state, cookie); badCode.Code != 401 {
		t.Fatalf("PKCE mismatch accepted: %d", badCode.Code)
	}
	provider.mode = ""
	state, cookie = startFlow(t, api, provider)
	if _, err = s.DB.Exec(ctx, `UPDATE v3_oidc_auth_states SET expires_at=clock_timestamp()-interval '1 second' WHERE state_hash=$1`, store.Hash(state)); err != nil {
		t.Fatal(err)
	}
	if expired := callback(api, state, cookie); expired.Code != 401 {
		t.Fatalf("expired state accepted: %d", expired.Code)
	}
	state, cookie = startFlow(t, api, provider)
	if _, err = s.DB.Exec(ctx, `UPDATE users SET disabled=true WHERE id='external'`); err != nil {
		t.Fatal(err)
	}
	if disabled := callback(api, state, cookie); disabled.Code != 403 {
		t.Fatalf("disabled identity accepted: %d", disabled.Code)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE users SET deleted_at=now() WHERE id='external'`); err != nil {
		t.Fatal(err)
	}
	state, cookie = startFlow(t, api, provider)
	if deleted := callback(api, state, cookie); deleted.Code != 403 {
		t.Fatalf("deleted identity accepted: %d", deleted.Code)
	}
	if _, err = s.DB.Exec(ctx, `UPDATE users SET deleted_at=NULL WHERE id='external'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id='external'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed callbacks created session: %d %v", count, err)
	}
	unbindRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/oidc/bindings/external", nil)
	unbindRequest.Header.Set("X-AIOps-Environment", "test")
	unbindRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: "admin-session"})
	unbindResponse := httptest.NewRecorder()
	api.ServeHTTP(unbindResponse, unbindRequest)
	if unbindResponse.Code != 204 {
		t.Fatalf("unbind %d %s", unbindResponse.Code, unbindResponse.Body.String())
	}
	if _, err = s.Session(ctx, session.Value); err == nil {
		t.Fatal("session survived unbind")
	}
	if _, err = s.DB.Exec(ctx, `UPDATE users SET disabled=false WHERE id='external'`); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.rateWindow = time.Now().Add(-time.Minute)
	service.mu.Unlock()
	state, cookie = startFlow(t, api, provider)
	if missing := callback(api, state, cookie); missing.Code != 403 {
		t.Fatalf("unbound callback %d", missing.Code)
	}
	// Callback and unbind take the same database advisory lock. Regardless of
	// which enters first, no OIDC-created session can survive the unbind.
	rebind := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/bindings", bytes.NewReader(bindInput))
	rebind.Header.Set("X-AIOps-Environment", "test")
	rebind.AddCookie(&http.Cookie{Name: "aiops_session", Value: "admin-session"})
	rebound := httptest.NewRecorder()
	api.ServeHTTP(rebound, rebind)
	if rebound.Code != 201 {
		t.Fatalf("rebind %d %s", rebound.Code, rebound.Body.String())
	}
	state, cookie = startFlow(t, api, provider)
	concurrentUnbind := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/oidc/bindings/external", nil)
	concurrentUnbind.Header.Set("X-AIOps-Environment", "test")
	concurrentUnbind.AddCookie(&http.Cookie{Name: "aiops_session", Value: "admin-session"})
	gate := make(chan struct{})
	outcomes := make(chan int, 2)
	go func() { <-gate; outcomes <- callback(api, state, cookie).Code }()
	go func() {
		<-gate
		result := httptest.NewRecorder()
		api.ServeHTTP(result, concurrentUnbind)
		outcomes <- result.Code
	}()
	close(gate)
	first, second := <-outcomes, <-outcomes
	if first != 204 && second != 204 {
		t.Fatalf("race unbind failed: %d %d", first, second)
	}
	if first != 302 && first != 403 && first != 204 || second != 302 && second != 403 && second != 204 {
		t.Fatalf("race callback unexpected: %d %d", first, second)
	}
	if err = s.DB.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id='external'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("race left session: %d %v", count, err)
	}
}

type denyEnvironment struct{}

func (denyEnvironment) AuthorizeEnvironment(context.Context, core.User, string) error {
	return store.ErrForbidden
}

type allowEnvironment struct{}

func (allowEnvironment) AuthorizeEnvironment(context.Context, core.User, string) error { return nil }

func TestOIDCRejectsUnsafeConfiguration(t *testing.T) {
	for name, cfg := range map[string]Config{
		"missing origin":  {Issuer: "http://127.0.0.1:5555", ClientID: "x", RedirectURL: "http://127.0.0.1:18081" + callbackPath, Tier: "nonprod", AllowLoopbackHTTP: true},
		"production HTTP": {Issuer: "http://127.0.0.1:5555", ClientID: "x", RedirectURL: "http://127.0.0.1:18081" + callbackPath, PublicOrigin: "http://127.0.0.1:18081", Tier: "production", AllowLoopbackHTTP: true},
		"redirect origin": {Issuer: "http://127.0.0.1:5555", ClientID: "x", RedirectURL: "http://127.0.0.1:18081" + callbackPath, PublicOrigin: "http://127.0.0.1:9999", Tier: "nonprod", AllowLoopbackHTTP: true},
	} {
		if _, err := New(context.Background(), &store.Store{}, cfg, nil); err == nil || strings.Contains(err.Error(), "database") {
			t.Fatal(fmt.Sprintf("%s accepted or skipped config validation: %v", name, err))
		}
	}
}

func TestOIDCRejectsUntrustedDiscoveryEndpoints(t *testing.T) {
	s := isolatedStore(t)
	provider, server := newFakeOIDC(t)
	defer server.Close()
	base := Config{Issuer: server.URL, ClientID: "aiops-test", RedirectURL: "http://127.0.0.1:18081" + callbackPath, PublicOrigin: "http://127.0.0.1:18081", Tier: "nonprod", AllowLoopbackHTTP: true}
	for _, endpoint := range []string{"http://example.invalid/token", "https://example.invalid/token", "http://127.0.0.1:1/token"} {
		provider.discoveryToken = endpoint
		if _, err := New(context.Background(), s, base, nil); err == nil {
			t.Fatalf("accepted untrusted token endpoint %s: %v", endpoint, err)
		}
	}
	provider.discoveryToken = ""
	provider.discoveryJWKS = "http://example.invalid/jwks"
	if _, err := New(context.Background(), s, base, nil); err == nil {
		t.Fatalf("accepted untrusted JWKS endpoint: %v", err)
	}
}

func TestOIDCStartGlobalCapWithSharedProxyAddress(t *testing.T) {
	s := &Service{}
	now := time.Now()
	// A reverse proxy presents the same RemoteAddr for all browsers. The
	// application must not lock everybody out after ten starts.
	for i := 0; i < 200; i++ {
		if allowed := s.admitStart(now); !allowed {
			t.Fatalf("start %d rejected behind shared proxy", i)
		}
	}
	if allowed := s.admitStart(now); allowed {
		t.Fatal("unbounded unauthenticated writes")
	}
	if allowed := s.admitStart(now.Add(time.Minute)); !allowed {
		t.Fatal("rate window did not recover")
	}
}

func TestOIDCStateCapacitySharedDatabase(t *testing.T) {
	s := isolatedStore(t)
	_, server := newFakeOIDC(t)
	defer server.Close()
	cfg := Config{Issuer: server.URL, ClientID: "aiops-test", RedirectURL: "http://127.0.0.1:18081" + callbackPath, PublicOrigin: "http://127.0.0.1:18081", Tier: "nonprod", AllowLoopbackHTTP: true}
	service, err := New(context.Background(), s, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondInstance, err := New(context.Background(), s, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(context.Background(), `INSERT INTO v3_oidc_auth_states(state_hash,nonce,code_verifier,expires_at)
		SELECT 'test-'||n,'nonce','verifier',now()+interval '5 minutes' FROM generate_series(1,1000) n`); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/start", nil)
	request.RemoteAddr = "127.0.0.1:7777"
	w := httptest.NewRecorder()
	service.start(w, request)
	if w.Code != 429 {
		t.Fatalf("full shared state table accepted new start: %d", w.Code)
	}
	if _, err = s.DB.Exec(context.Background(), `UPDATE v3_oidc_auth_states SET expires_at=now()-interval '1 second' WHERE state_hash='test-1'`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	secondInstance.start(w, request)
	if w.Code != 302 {
		t.Fatalf("expired capacity not recovered: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err = s.DB.QueryRow(context.Background(), `SELECT count(*) FROM v3_oidc_auth_states`).Scan(&count); err != nil || count != 1000 {
		t.Fatalf("state capacity %d %v", count, err)
	}
}
