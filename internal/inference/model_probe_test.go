package inference

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestModelProbeLocalMetadataOnly(t *testing.T) {
	for _, tc := range []struct {
		name, kind, api, path, response, wantStatus, wantError string
		code                                                   int
	}{
		{"openai", "openai", "chat_completions", "/v1/models/local-model", `{"id":"local-model"}`, "verified", "", 200},
		{"claude", "claude", "messages", "/v1/models/local-model", `{"id":"resolved-model"}`, "verified", "", 200},
		{"ollama", "ollama", "ollama", "/api/show", `{"details":{"family":"test"}}`, "verified", "", 200},
		{"ollama empty metadata", "ollama", "ollama", "/api/show", `{"details":null}`, "error", "invalid_metadata", 200},
		{"invalid metadata", "openai", "responses", "/v1/models/local-model", `{"id":"wrong-model"}`, "error", "invalid_metadata", 200},
		{"unsupported endpoint", "openai", "responses", "/v1/models/local-model", `{}`, "unsupported", "metadata_endpoint_unsupported", 405},
		{"authentication needed", "openai", "responses", "/v1/models/local-model", `{}`, "error", "authentication_required", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != tc.path {
					t.Errorf("path=%q want %q", r.URL.Path, tc.path)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
					t.Error("probe sent a credential")
				}
				if tc.kind == "claude" && r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Errorf("missing Claude protocol version: %q", r.Header.Get("anthropic-version"))
				}
				if tc.api == "ollama" {
					if r.Method != http.MethodPost {
						t.Errorf("method=%s", r.Method)
					}
					var body struct {
						Model string `json:"model"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model != "local-model" {
						t.Errorf("body=%+v err=%v", body, err)
					}
				} else if r.Method != http.MethodGet {
					t.Errorf("method=%s", r.Method)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			base := server.URL + "/v1"
			if tc.api == "ollama" {
				base = server.URL
			}
			runtime, err := New(Config{Providers: []Provider{{ID: "model:local", Kind: tc.kind, API: tc.api, Model: "local-model", BaseURL: base}}, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			got, err := runtime.ProbeModel(t.Context(), "model:local")
			if err != nil || got.Model != "model:local" || got.Status != tc.wantStatus || got.ErrorCode != tc.wantError || calls.Load() != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", got, err, calls.Load())
			}
		})
	}
}

func TestModelProbeNeverContactsRemoteOrCredentialedEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("credentialed local model was contacted")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	runtime, err := New(Config{Providers: []Provider{
		{ID: "remote", Kind: "openai", Model: "remote", BaseURL: "https://example.invalid/v1", APIKey: "private-test-key"},
		{ID: "credentialed-local", Kind: "openai", Model: "local", BaseURL: server.URL + "/v1", APIKey: "private-test-key"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"remote", "credentialed-local"} {
		got, err := runtime.ProbeModel(t.Context(), id)
		if err != nil || got.Status != "unsupported" || got.ErrorCode != "local_credential_free_only" {
			t.Fatalf("%s: result=%+v err=%v", id, got, err)
		}
	}
	if _, err := runtime.ProbeModel(context.Background(), "absent"); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("unknown model error=%v", err)
	}
}

func TestModelProbeDoesNotFollowRedirect(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	runtime, err := New(Config{Providers: []Provider{{ID: "local", Kind: "openai", Model: "local", BaseURL: source.URL + "/v1"}}, HTTPClient: source.Client()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runtime.ProbeModel(t.Context(), "local")
	if err != nil || got.Status != "error" || got.ErrorCode != "upstream_failed" || destinationCalls.Load() != 0 {
		t.Fatalf("result=%+v err=%v redirected=%d", got, err, destinationCalls.Load())
	}
}

func TestModelProbeDoesNotForwardInjectedClientCookies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("probe forwarded injected cookie: %q", got)
		}
		_, _ = w.Write([]byte(`{"id":"local"}`))
	}))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(endpoint, []*http.Cookie{{Name: "session", Value: "private-test-cookie"}})
	client := server.Client()
	client.Jar = jar
	client.Transport = failingProbeTransport{}
	runtime, err := New(Config{Providers: []Provider{{ID: "local", Kind: "openai", Model: "local", BaseURL: server.URL + "/v1"}}, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	got, err := runtime.ProbeModel(t.Context(), "local")
	if err != nil || got.Status != "verified" {
		t.Fatalf("result=%+v err=%v", got, err)
	}
}

type failingProbeTransport struct{}

func (failingProbeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("injected credential-bearing transport was used")
}
