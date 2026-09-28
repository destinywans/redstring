package v3identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// Opt-in, disposable local Keycloak protocol smoke. The caller supplies a
// dedicated test realm, public client with exact redirect URI
// http://127.0.0.1:18089/api/v1/auth/oidc/callback, and test account; no real
// user or model credentials are used. No normal application DB is touched.
func TestOIDCKeycloakLocalSmoke(t *testing.T) {
	issuer, username, password := os.Getenv("AIOPS_OIDC_TEST_ISSUER"), os.Getenv("AIOPS_OIDC_TEST_USERNAME"), os.Getenv("AIOPS_OIDC_TEST_PASSWORD")
	if issuer == "" {
		t.Skip("AIOPS_OIDC_TEST_ISSUER required")
	}
	if username == "" || password == "" {
		t.Fatal("dedicated Keycloak test account required")
	}
	s := isolatedStore(t)
	api := httpapi.New(s, nil, true)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		denyEnvironment{}, s.Session, func(context.Context, core.User, string, string, string) error { return store.ErrForbidden }); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewUnstartedServer(api)
	listener, err := net.Listen("tcp", "127.0.0.1:18089")
	if err != nil {
		t.Fatalf("dedicated callback port unavailable: %v", err)
	}
	app.Listener.Close()
	app.Listener = listener
	app.Start()
	defer app.Close()
	service, err := New(context.Background(), s, Config{Issuer: issuer, ClientID: "aiops-oidc-smoke", RedirectURL: app.URL + callbackPath,
		PublicOrigin: app.URL, Tier: "nonprod", AllowLoopbackHTTP: true}, nil)
	if err != nil {
		t.Fatalf("Keycloak discovery: %v", err)
	}
	service.Register(api)

	// Obtain only this disposable account's subject for exact pre-provisioning.
	form := url.Values{"grant_type": {"password"}, "client_id": {"aiops-oidc-smoke"}, "scope": {"openid"}, "username": {username}, "password": {password}}
	resp, err := http.PostForm(issuer+"/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var token map[string]any
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token) != nil {
		t.Fatalf("dedicated test account token response: HTTP %d", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("dedicated test account token request: HTTP %d %v %v", resp.StatusCode, token["error"], token["error_description"])
	}
	parts := strings.SplitN(token["id_token"].(string), ".", 3)
	if len(parts) != 3 {
		t.Fatal("Keycloak did not return ID token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil || claims.Subject == "" {
		t.Fatal("Keycloak subject unavailable")
	}
	if _, err = s.DB.Exec(context.Background(), `INSERT INTO users(id,email,name,role,password_hash,source,external_id)
		VALUES('keycloak-smoke','keycloak-smoke@example.invalid','Keycloak smoke','viewer',$1,'local','')`, store.PasswordHash("disposable-local-password")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(context.Background(), `INSERT INTO v3_oidc_identities(issuer_hash,subject,user_id,created_by)
		VALUES($1,$2,'keycloak-smoke','keycloak-smoke')`, service.issuerHash, claims.Subject); err != nil {
		t.Fatal(err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Scheme+"://"+req.URL.Host == app.URL && req.URL.Path == "/" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	loginPage, err := client.Get(app.URL + "/api/v1/auth/oidc/start")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(io.LimitReader(loginPage.Body, 2<<20))
	loginPage.Body.Close()
	if loginPage.StatusCode != 200 {
		t.Fatalf("Keycloak login page: HTTP %d", loginPage.StatusCode)
	}
	action := regexp.MustCompile(`(?s)<form[^>]*action="([^"]+)"[^>]*>`).FindSubmatch(page)
	if len(action) != 2 {
		t.Fatal("Keycloak login form unavailable")
	}
	postURL := html.UnescapeString(string(action[1]))
	credentials := url.Values{"username": {username}, "password": {password}}
	authResponse, err := client.PostForm(postURL, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer authResponse.Body.Close()
	if authResponse.StatusCode != http.StatusFound || authResponse.Header.Get("Location") != "/" {
		t.Fatalf("OIDC callback did not complete: HTTP %d", authResponse.StatusCode)
	}
	appURL, _ := url.Parse(app.URL)
	var session string
	for _, cookie := range jar.Cookies(appURL) {
		if cookie.Name == "aiops_session" {
			session = cookie.Value
		}
	}
	if session == "" {
		t.Fatal("OIDC callback did not set local session")
	}
	u, err := s.Session(context.Background(), session)
	if err != nil || u.ID != "keycloak-smoke" {
		t.Fatalf("OIDC session not persisted: %v", err)
	}
	var auditCount int
	if err = s.DB.QueryRow(context.Background(), `SELECT count(*) FROM management_audit WHERE action='account.oidc_login' AND actor_id='keycloak-smoke'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("OIDC login audit missing: %d %v", auditCount, err)
	}
}
