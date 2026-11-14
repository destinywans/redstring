package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/apicontract"
	"github.com/destinywans/redstring/internal/delivery"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/v3ingestpush"
	"github.com/destinywans/redstring/internal/v3webhook"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStartupRejectsPlaceholderDatabasePassword(t *testing.T) {
	t.Setenv("PGPASSFILE", t.TempDir()+"/missing")
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGPASSWORD", "")
	for _, dsn := range []string{
		"postgresql://aiops:replace-with-random-value@127.0.0.1/aiops?sslmode=disable",
		"postgresql://aiops:%72eplace-with-random-value@127.0.0.1/aiops?sslmode=disable",
		"user=aiops host=127.0.0.1 password=' replace-with-random-value ' dbname=aiops sslmode=disable",
		"user=aiops host=127.0.0.1 password='REPLACE-WITH-LOCAL' dbname=aiops sslmode=disable",
	} {
		err := validateStartupDatabaseSecret(dsn)
		if err == nil || strings.Contains(err.Error(), "replace-with-random-value") || strings.Contains(err.Error(), "REPLACE-WITH-LOCAL") {
			t.Fatal("startup did not reject the example database secret safely")
		}
	}
	for _, dsn := range []string{
		"postgresql://aiops:synthetic-db-password@127.0.0.1/aiops?sslmode=disable",
		"user=aiops host=127.0.0.1 dbname=aiops sslmode=disable",
	} {
		if err := validateStartupDatabaseSecret(dsn); err != nil {
			t.Errorf("legitimate database configuration refused: %v", err)
		}
	}
	t.Setenv("PGPASSWORD", "replace-with-random-value")
	if err := validateStartupDatabaseSecret("postgresql://aiops@127.0.0.1/aiops?sslmode=disable"); err == nil {
		t.Fatal("effective placeholder password from PGPASSWORD was accepted")
	}
	if err := validateStartupDatabaseSecret("postgresql://aiops:synthetic-db-password@127.0.0.1/aiops?sslmode=disable"); err != nil {
		t.Errorf("legitimate password overriding PGPASSWORD refused: %v", err)
	}
	err := validateStartupDatabaseSecret("postgresql://aiops:synthetic-secret-leak-check@127.0.0.1:bad/aiops")
	if err == nil || strings.Contains(err.Error(), "synthetic-secret-leak-check") {
		t.Fatal("invalid database configuration did not fail with a sanitized error")
	}
}

func TestEnvExampleContainsNoActiveCredentials(t *testing.T) {
	data, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"POSTGRES_PASSWORD", "DATABASE_URL", "AIOPS_ADMIN_PASSWORD"} {
		if !strings.Contains(string(data), "\n"+name+"=\n") {
			t.Errorf("example must leave %s empty", name)
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(name, "#") || value == "" {
			continue
		}
		for _, suffix := range []string{"_PASSWORD", "_SECRET", "_TOKEN", "_KEY"} {
			if strings.HasSuffix(name, suffix) {
				t.Errorf("example contains an active value for %s", name)
			}
		}
	}
}

func TestRootHandlerHostBoundary(t *testing.T) {
	policy, err := httpapi.NewHostPolicy("https://prod.redstring.test:18443")
	if err != nil {
		t.Fatal(err)
	}
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	web := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) })
	handler := rootHandler(api, web, policy)
	for _, tc := range []struct {
		host, method, path string
		status             int
	}{
		{"rebind.example.invalid", "GET", "/api/v1/instance", 421},
		{"rebind.example.invalid", "POST", "/api/v1/auth/login", 421},
		{"rebind.example.invalid", "GET", "/openapi.json", 421},
		{"rebind.example.invalid", "GET", "/", 421},
		{"rebind.example.invalid", "GET", "/assets/index.js", 421},
		{"rebind.example.invalid", "GET", "/healthz", 204},
		{"127.0.0.1:8080", "GET", "/api/v1/instance", 204},
		{"localhost:8080", "GET", "/", 202},
		{"prod.redstring.test", "GET", "/openapi.json", 200},
	} {
		request := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != tc.status {
			t.Errorf("%s %s %s: got %d, want %d", tc.host, tc.method, tc.path, w.Code, tc.status)
		}
		if tc.status == 200 && w.Body.String() != string(apicontract.OpenAPI) {
			t.Error("trusted host did not receive the embedded OpenAPI")
		}
	}
}

func TestMachineIngressRetainsSourceAuthentication(t *testing.T) {
	// Only unauthenticated requests are sent. The registered receivers reject
	// them before persistence or provider calls.
	api := httpapi.New(nil, nil)
	api.AllowedOrigin = "https://prod.redstring.test:18443"
	instance := httpapi.V3InstanceConfig{Tier: "production", EnvironmentIDs: []string{"prod"}}
	pool := new(pgxpool.Pool)
	tokenFile := t.TempDir() + "/source-token"
	if err := os.WriteFile(tokenFile, []byte("synthetic-source-auth-token-only"), 0600); err != nil {
		t.Fatal(err)
	}
	alerts, err := v3webhook.New(pool, instance, operations.Config{Sources: []operations.Source{{
		ID: "synthetic-source", TokenFile: tokenFile, Mappings: []operations.Mapping{{
			Labels: map[string]string{"application": "synthetic-app"}, ApplicationID: "synthetic-app", EnvironmentID: "synthetic-prod",
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	api.RegisterPublic(v3webhook.Pattern, alerts.Handler)
	(&delivery.Service{}).Register(api)
	push, err := v3ingestpush.New(pool, instance, v3ingestpush.Config{}, &rbac.V3Policy{})
	if err != nil {
		t.Fatal(err)
	}
	push.RegisterPublic(api)
	policy, err := httpapi.NewHostPolicy(api.AllowedOrigin)
	if err != nil {
		t.Fatal(err)
	}
	handler := rootHandler(api, nil, policy)
	for _, path := range []string{
		"/api/v1/webhooks/alertmanager/synthetic-source", "/api/v1/webhooks/gitlab/synthetic-source",
		"/api/v1/events/alert", "/api/v1/events/change", "/api/v1/events/incident", "/api/v1/events/resource", "/api/v1/events/oncall",
	} {
		r := httptest.NewRequest(http.MethodPost, "http://machine-ingress.example.invalid"+path, strings.NewReader("{}"))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "synthetic-browser-session"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: source auth got %d: %s", path, w.Code, w.Body.String())
		}
	}
}
