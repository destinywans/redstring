package v3identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

const (
	stateCookie  = "aiops_oidc_state"
	callbackPath = "/api/v1/auth/oidc/callback"
)

type Config struct {
	Issuer, ClientID, ClientSecret, RedirectURL, PublicOrigin string
	Tier                                                      string
	SecureCookies                                             bool
	// Local-only HTTP providers are useful for the isolated Keycloak lab.
	AllowLoopbackHTTP      bool
	AllowedEndpointOrigins []string
}

type Service struct {
	store         *store.Store
	client        *http.Client
	oauth         oauth2.Config
	verifier      *oidc.IDTokenVerifier
	issuerHash    string
	secureCookies bool
	mu            sync.Mutex
	rateWindow    time.Time
	rateTotal     int
}

// New verifies discovery against the configured issuer at startup. Partial or
// unsafe configurations fail startup; an unconfigured V3 instance does not
// register any OIDC routes.
func New(ctx context.Context, db *store.Store, cfg Config, client *http.Client) (*Service, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("OIDC issuer, client ID and redirect URL are required")
	}
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
		return nil, errors.New("invalid OIDC issuer")
	}
	redirect, err := url.Parse(cfg.RedirectURL)
	if err != nil || redirect.Host == "" || redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.Path != callbackPath {
		return nil, errors.New("invalid OIDC redirect URL")
	}
	if cfg.PublicOrigin == "" {
		return nil, errors.New("OIDC requires an explicit public origin")
	}
	origin, parseErr := url.Parse(cfg.PublicOrigin)
	if parseErr != nil || origin.Scheme != redirect.Scheme || origin.Host != redirect.Host || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("OIDC redirect must match public origin")
	}
	if issuer.Scheme != "https" || redirect.Scheme != "https" {
		if !cfg.AllowLoopbackHTTP || cfg.Tier == "production" || !loopbackHTTP(issuer) || !loopbackHTTP(redirect) {
			return nil, errors.New("OIDC HTTP is restricted to an explicitly enabled loopback lab")
		}
	}
	if cfg.Tier == "production" && !cfg.SecureCookies {
		return nil, errors.New("production OIDC requires secure cookies")
	}
	if redirect.Scheme == "https" && !cfg.SecureCookies {
		return nil, errors.New("HTTPS OIDC requires secure cookies")
	}
	if db == nil || db.DB == nil {
		return nil, errors.New("OIDC database is required")
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	if copyClient.Timeout == 0 || copyClient.Timeout > 10*time.Second {
		copyClient.Timeout = 5 * time.Second
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("OIDC endpoint redirect rejected") }
	client = &copyClient
	// Validate every endpoint before an authorization code or client secret can
	// leave this process. The default trust boundary is the issuer's origin.
	allowed := map[string]bool{issuer.Scheme + "://" + issuer.Host: true}
	for _, raw := range cfg.AllowedEndpointOrigins {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(cfg.AllowLoopbackHTTP && cfg.Tier != "production" && loopbackHTTP(u))) {
			return nil, errors.New("invalid OIDC endpoint origin allowlist")
		}
		allowed[u.Scheme+"://"+u.Host] = true
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(cfg.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, errors.New("invalid OIDC discovery URL")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery returned HTTP %d", response.StatusCode)
	}
	var metadata struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		JWKS          string `json:"jwks_uri"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&metadata); err != nil || metadata.Issuer != cfg.Issuer {
		return nil, errors.New("OIDC discovery issuer mismatch or malformed metadata")
	}
	for _, raw := range []string{metadata.Authorization, metadata.Token, metadata.JWKS} {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u.Host == "" || u.User != nil || u.Fragment != "" || !allowed[u.Scheme+"://"+u.Host] || (u.Scheme != "https" && !(cfg.AllowLoopbackHTTP && cfg.Tier != "production" && loopbackHTTP(u))) {
			return nil, errors.New("OIDC endpoint is outside trusted origin")
		}
	}
	ctx = oidc.ClientContext(ctx, client)
	digest := sha256.Sum256([]byte(cfg.Issuer))
	return &Service{
		store: db, client: client, issuerHash: hex.EncodeToString(digest[:]), secureCookies: cfg.SecureCookies,
		oauth: oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL,
			Endpoint: oauth2.Endpoint{AuthURL: metadata.Authorization, TokenURL: metadata.Token}, Scopes: []string{oidc.ScopeOpenID}},
		verifier: oidc.NewVerifier(cfg.Issuer, oidc.NewRemoteKeySet(ctx, metadata.JWKS), &oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: []string{"RS256"}}),
	}, nil
}

func loopbackHTTP(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func randomURLToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (s *Service) Register(api *httpapi.Server) {
	api.RegisterPublic("GET /api/v1/auth/oidc/start", s.start)
	api.RegisterPublic("GET "+callbackPath, s.callback)
	s.registerBindings(api)
}

// A local reverse proxy gives every browser the same RemoteAddr. Only a
// process-wide cap is safe here; client-aware rate limiting belongs at the
// trusted ingress, which owns the forwarded-header trust chain.
func (s *Service) admitStart(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rateWindow.IsZero() || now.Sub(s.rateWindow) >= time.Minute {
		s.rateWindow, s.rateTotal = now, 0
	}
	if s.rateTotal >= 200 {
		return false
	}
	s.rateTotal++
	return true
}

func (s *Service) start(w http.ResponseWriter, r *http.Request) {
	if !s.admitStart(time.Now()) {
		httpapi.Error(w, http.StatusTooManyRequests, "rate_limited", "Retry sign-in later")
		return
	}
	state, err := randomURLToken()
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	nonce, err := randomURLToken()
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	verifier := oauth2.GenerateVerifier()
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	defer tx.Rollback(ctx)
	// All application instances share this database advisory lock, so the
	// outstanding-state cap is a hard global bound rather than a per-process
	// estimate. Expiry cleanup is only needed when capacity is reached.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(752018997)`); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	var outstanding int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_oidc_auth_states`).Scan(&outstanding); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	if outstanding >= 1000 {
		if _, err = tx.Exec(ctx, `DELETE FROM v3_oidc_auth_states WHERE expires_at<=clock_timestamp()`); err != nil {
			httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
			return
		}
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_oidc_auth_states`).Scan(&outstanding); err != nil {
			httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
			return
		}
		if outstanding >= 1000 {
			httpapi.Error(w, 429, "rate_limited", "Too many sign-ins are in progress")
			return
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO v3_oidc_auth_states(state_hash,nonce,code_verifier,expires_at)
		VALUES($1,$2,$3,clock_timestamp()+interval '5 minutes')`, store.Hash(state), nonce, verifier); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Value: state, Path: callbackPath, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, s.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), http.StatusFound)
}

func queryOne(q url.Values, key string) string {
	values := q[key]
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	// Clear the browser binding on every callback, including failures.
	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: callbackPath, HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	state, code := queryOne(r.URL.Query(), "state"), queryOne(r.URL.Query(), "code")
	cookie, err := r.Cookie(stateCookie)
	if err != nil || state == "" || code == "" || len(state) != 43 || len(cookie.Value) != 43 || len(code) > 2048 || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		httpapi.Error(w, 401, "identity_rejected", "Sign-in could not be verified")
		return
	}
	var nonce, verifier string
	err = s.store.DB.QueryRow(r.Context(), `DELETE FROM v3_oidc_auth_states WHERE state_hash=$1 AND expires_at>clock_timestamp()
		RETURNING nonce,code_verifier`, store.Hash(state)).Scan(&nonce, &verifier)
	if err != nil {
		httpapi.Error(w, 401, "identity_rejected", "Sign-in could not be verified")
		return
	}
	ctx := oidc.ClientContext(r.Context(), s.client)
	token, err := s.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		httpapi.Error(w, 401, "identity_rejected", "Sign-in could not be verified")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		httpapi.Error(w, 401, "identity_rejected", "Sign-in could not be verified")
		return
	}
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil || idToken.Nonce != nonce || idToken.Subject == "" || len(idToken.Subject) > 200 {
		httpapi.Error(w, 401, "identity_rejected", "Sign-in could not be verified")
		return
	}
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(752018996)`); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	var userID string
	err = tx.QueryRow(ctx, `SELECT user_id FROM v3_oidc_identities WHERE issuer_hash=$1 AND subject=$2 FOR UPDATE`, s.issuerHash, idToken.Subject).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 403, "identity_not_provisioned", "Account is not enabled for this identity")
		} else {
			httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		}
		return
	}
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT NOT disabled AND deleted_at IS NULL AND v3_identity_kind='human' AND source<>'settings_demo'
		FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&enabled); err != nil || !enabled {
		httpapi.Error(w, 403, "identity_not_provisioned", "Account is not enabled for this identity")
		return
	}
	session, err := randomURLToken()
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '12 hours')`, store.Hash(session), userID); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
		VALUES($1,'account.oidc_login',$1,'success',jsonb_build_object('issuer_hash',$2::text))`, userID, s.issuerHash); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Sign-in unavailable")
		return
	}
	// An IdP-to-app top-level redirect is cross-site. Lax makes the first app
	// navigation reliable; same-origin write requests still pass Origin/CSRF.
	http.SetCookie(w, &http.Cookie{Name: "aiops_session", Value: session, Path: "/", HttpOnly: true, Secure: s.secureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 43200})
	http.Redirect(w, r, "/", http.StatusFound)
}
