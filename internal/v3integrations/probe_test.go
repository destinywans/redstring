package v3integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeUsesVaultReferenceAndRealGitLabResponse(t *testing.T) {
	const token = "secret-test-token"
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/secret/data/aiops/gitlab" || r.Header.Get("X-Vault-Token") != "vault-test-token" {
			t.Errorf("Vault request: %s %s", r.URL.Path, r.Header.Get("X-Vault-Token"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"token": token}}})
	}))
	defer vault.Close()
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" || r.Header.Get("PRIVATE-TOKEN") != token {
			t.Errorf("GitLab check: %s %s", r.URL.Path, r.Header.Get("PRIVATE-TOKEN"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer gitlab.Close()
	resolver := NewVaultResolver(vault.URL, "vault-test-token")
	result, err := (Prober{Resolver: resolver}).Check(context.Background(), "gitlab", gitlab.URL, "pat", "vault://secret/aiops/gitlab#token")
	if err != nil || !result.OK || result.ErrorCode != "" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestProbeRejectsInvalidAuthAndDoesNotFollowRedirect(t *testing.T) {
	redirected := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/-/ready" {
			http.Redirect(w, r, "/private", http.StatusFound)
			return
		}
		redirected = true
	}))
	defer upstream.Close()
	result, err := (Prober{}).Check(context.Background(), "prometheus", upstream.URL, "none", "")
	if err != nil || result.OK || result.ErrorCode != "upstream_302" || redirected {
		t.Fatalf("result=%+v redirected=%v error=%v", result, redirected, err)
	}
	if _, err := (Prober{}).Check(context.Background(), "gitlab", upstream.URL, "pat", ""); err != ErrCredentialUnavailable {
		t.Fatalf("missing credential: %v", err)
	}
	if _, err := validEndpoint("http://example.com"); err != ErrInvalidEndpoint {
		t.Fatalf("non-local cleartext: %v", err)
	}
}

func TestVaultReferenceRejectsMalformedAndMissingFields(t *testing.T) {
	if validateReference("secret/aiops/gitlab") == nil {
		t.Fatal("accepted non-Vault reference")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"data":{"other":"value"}}}`))
	}))
	defer server.Close()
	resolver := NewVaultResolver(server.URL, "token")
	_, err := resolver.Resolve(context.Background(), "vault://secret/aiops/gitlab#token")
	if err != ErrCredentialUnavailable || strings.Contains(err.Error(), "value") {
		t.Fatalf("unexpected secret error: %v", err)
	}
}

func TestCredentialClientsRejectRedirectEvenWithInjectedHTTPClient(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected = true
	}))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer upstream.Close()
	probe := Prober{Client: &http.Client{}, Resolver: genericActivationResolver{}}
	result, err := probe.Check(context.Background(), "prometheus", upstream.URL, "bearer", "vault://isolated/prometheus#token")
	if err != nil || result.OK || result.ErrorCode != "upstream_302" || redirected {
		t.Fatalf("probe followed redirect: result=%+v redirected=%v err=%v", result, redirected, err)
	}
	vault := NewVaultResolver(upstream.URL, "test-token")
	vault.Client = &http.Client{}
	if _, err := vault.Resolve(context.Background(), "vault://secret/aiops/gitlab#token"); err != ErrCredentialUnavailable || redirected {
		t.Fatalf("Vault request followed redirect: redirected=%v err=%v", redirected, err)
	}
}

// PA-19 P3 stage 2 decision 3: Elasticsearch basic auth takes its user from
// the connector's options and only the password from Vault; without a user
// the credential is still "user:password".
func TestProbeBasicAuthUsesTheOptionsUser(t *testing.T) {
	var got []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		got = append(got, user+"|"+pass)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	prober := Prober{Resolver: staticResolver("pa:ss")}
	ctx := WithBasicUsername(context.Background(), "reader")
	if result, err := prober.Check(ctx, "elasticsearch", upstream.URL, "basic", "vault://aiops/lab/elasticsearch#password"); err != nil || !result.OK {
		t.Fatalf("options user: %+v %v", result, err)
	}
	if result, err := prober.Check(context.Background(), "elasticsearch", upstream.URL, "basic", "vault://aiops/lab/elasticsearch#password"); err != nil || !result.OK {
		t.Fatalf("user:password credential: %+v %v", result, err)
	}
	if len(got) != 2 || got[0] != "reader|pa:ss" || got[1] != "pa|ss" {
		t.Fatalf("basic auth sent: %v", got)
	}
}

type staticResolver string

func (s staticResolver) Resolve(context.Context, string) (string, error) { return string(s), nil }
