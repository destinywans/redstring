package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostPolicy(t *testing.T) {
	for _, tc := range []struct {
		origin string
		allow  []string
		deny   []string
	}{
		{
			allow: []string{"127.0.0.1:8080", "127.0.0.2", "LOCALHOST:8080", "[::1]", "[::ffff:127.0.0.1]:8080"},
			deny:  []string{"", "0.0.0.0:8080", "192.168.1.10:8080", "localhost.", "localhost.attacker.test", "127.0.0.1.nip.io", "[fe80::1%lo0]", "localhost:garbage", "127.0.0.1\t", "::1", "[127.0.0.1]"},
		},
		{
			origin: "https://prod.redstring.test:18443",
			allow:  []string{"prod.redstring.test:18443", "prod.redstring.test", "PROD.redstring.test.", "127.0.0.1:18080", "localhost"},
			deny:   []string{"nonprod.redstring.test:18444", "prod.redstring.test.attacker.test", "prod.redstring.test..", "localhost."},
		},
		{
			origin: "http://192.168.1.10:8080",
			allow:  []string{"192.168.1.10", "[::ffff:192.168.1.10]:8080", "localhost"},
			deny:   []string{"192.168.1.11"},
		},
	} {
		policy, err := NewHostPolicy(tc.origin)
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range tc.allow {
			if !policy.Allowed(host) {
				t.Errorf("origin %q refused trusted host %q", tc.origin, host)
			}
		}
		for _, host := range tc.deny {
			if policy.Allowed(host) {
				t.Errorf("origin %q allowed untrusted host %q", tc.origin, host)
			}
		}
	}
}

func TestPublicOriginValidation(t *testing.T) {
	for _, origin := range []string{"", "http://127.0.0.1:5173", "https://prod.redstring.test:18443", "http://[::1]:8080", "https://xn--bcher-kva.example"} {
		if _, err := NewHostPolicy(origin); err != nil {
			t.Errorf("valid origin %q: %v", origin, err)
		}
	}
	for _, origin := range []string{
		" https://example.invalid", "HTTPS://example.invalid", "https://example.invalid/", "https://EXAMPLE.invalid",
		"https://example.invalid:443", "http://example.invalid:80", "http://example.invalid:080", "http://example.invalid:0",
		"example.invalid", "https://user:password@example.invalid", "https://example.invalid?", "https://example.invalid#",
		"ftp://example.invalid", "https://bücher.example", "https://example.invalid.", "https://[fe80::1%25lo0]:8080",
	} {
		if _, err := NewHostPolicy(origin); err == nil {
			t.Errorf("invalid origin %q was accepted", origin)
		}
	}
}

func TestHostGuardMachineExceptionsAreExact(t *testing.T) {
	policy, err := NewHostPolicy("https://prod.redstring.test:18443")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := policy.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized) // The machine auth gate still owns the decision.
	}))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/healthz", 401}, {"HEAD", "/healthz", 401},
		{"POST", "/api/v1/webhooks/alertmanager/source", 401},
		{"POST", "/api/v1/webhooks/gitlab/repo%2Fname", 401},
		{"POST", "/api/v1/events/alert", 401}, {"POST", "/api/v1/events/change", 401},
		{"POST", "/api/v1/events/incident", 401}, {"POST", "/api/v1/events/resource", 401},
		{"POST", "/api/v1/events/oncall", 401},
		{"POST", "/healthz", 421}, {"GET", "/healthz/extra", 421},
		{"GET", "/api/v1/webhooks/alertmanager/source", 421},
		{"POST", "/api/v1/webhooks/alertmanager/source/extra", 421},
		{"POST", "/api/v1/webhooks/gitlab/source/", 421},
		{"POST", "/api/v1/webhooks/gitlab/..", 421},
		{"POST", "/api/v1/events%2Falert", 421}, {"POST", "/api/v1/events/alerts", 421}, {"POST", "/api/v1/events/change/extra", 421},
		{"GET", "/api/v1/events/change", 421}, {"POST", "/api/v1/auth/login", 421},
	} {
		request := httptest.NewRequest(tc.method, "http://rebind.example.invalid"+tc.path, nil)
		request.Header.Set("X-Forwarded-Host", "prod.redstring.test")
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, request)
		if w.Code != tc.want {
			t.Errorf("%s %s: got %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
		if tc.want == 421 && (w.Header().Get("Cache-Control") != "no-store" ||
			w.Header().Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(w.Body.String(), "host_not_allowed") || strings.Contains(w.Body.String(), "prod.redstring.test")) {
			t.Errorf("%s %s: wrong host rejection response", tc.method, tc.path)
		}
	}
}

func TestRebindingHostCannotLogIn(t *testing.T) {
	api, _ := testServer(t)
	for _, tc := range []struct {
		host, origin string
		status       int
	}{
		{"rebind.example.invalid:8080", "", http.StatusMisdirectedRequest},
		{"127.0.0.1:8080", "", http.StatusOK},
		{"127.0.0.1:5173", "http://127.0.0.1:5173", http.StatusOK},
	} {
		policy, err := NewHostPolicy(tc.origin)
		if err != nil {
			t.Fatal(err)
		}
		api.AllowedOrigin = tc.origin
		r := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/api/v1/auth/login",
			strings.NewReader(`{"email":"admin@test.local","password":"test-secure-password"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://"+tc.host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		policy.Wrap(api).ServeHTTP(w, r)
		if w.Code != tc.status || (w.Header().Get("Set-Cookie") != "") != (tc.status == http.StatusOK) {
			t.Errorf("host %s: status=%d, session cookie present=%t", tc.host, w.Code, w.Header().Get("Set-Cookie") != "")
		}
		if tc.status == http.StatusMisdirectedRequest {
			var failure struct {
				Error struct{ Code string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code != "host_not_allowed" {
				t.Errorf("unexpected host error: %s", w.Body.String())
			}
		}
	}
}
