package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const harborTestDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestHarborScopedRepositoryAndArtifacts(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("robot-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	called := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "robot$reader" || pass != "robot-token" {
			t.Error("missing or wrong basic authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/harbor/api/v2.0/projects/finance/repositories/team%252Fcheckout":
			_, _ = w.Write([]byte(`{"name":"finance/team/checkout","artifact_count":2,"pull_count":5,"description":"private upstream field"}`))
		case "/harbor/api/v2.0/projects/finance/repositories/team%252Fcheckout/artifacts":
			if got := r.URL.Query(); got.Get("page") != "1" || got.Get("page_size") != "2" || got.Get("sort") != "-push_time" || got.Get("with_tag") != "true" {
				t.Errorf("unexpected bounded query: %v", got)
			}
			w.Header().Set("X-Total-Count", "3")
			_, _ = fmt.Fprintf(w, `[{"digest":%q,"repository_name":"finance/team/checkout","size":1024,"push_time":"2026-09-28T08:00:00Z","tags":[{"name":"v1","id":9}],"scan_overview":{"secret":"hidden"}}]`, harborTestDigest)
		default:
			t.Errorf("unexpected path: %s", r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := NewHarbor(HarborConfig{
		BaseURL: server.URL + "/harbor", Repositories: map[string]HarborRepositoryRef{
			"checkout": {Project: "finance", Repository: "team/checkout"},
		}, AuthMode: "basic", Username: "robot$reader", TokenFile: tokenPath, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetHarborRepository(context.Background(), "unconfigured"); !errors.Is(err, ErrHarborRepository) {
		t.Fatalf("unconfigured alias: %v", err)
	}
	if _, err := client.ListHarborArtifacts(context.Background(), "checkout", 51); !errors.Is(err, ErrHarborLimit) {
		t.Fatalf("unbounded query: %v", err)
	}
	if called != 0 {
		t.Fatal("invalid selection caused upstream request")
	}
	repository, err := client.GetHarborRepository(context.Background(), "checkout")
	if err != nil || repository.Name != "finance/team/checkout" || repository.ArtifactCount != 2 || repository.PullCount != 5 {
		t.Fatalf("repository: %+v, %v", repository, err)
	}
	artifacts, err := client.ListHarborArtifacts(context.Background(), "checkout", 2)
	if err != nil || artifacts.Total == nil || *artifacts.Total != 3 || len(artifacts.Artifacts) != 1 ||
		artifacts.Artifacts[0].Digest != harborTestDigest || artifacts.Artifacts[0].Tags[0] != "v1" || called != 2 {
		t.Fatalf("artifacts: %+v, %v; calls=%d", artifacts, err, called)
	}
	raw, _ := json.Marshal(struct {
		Repository HarborRepository `json:"repository"`
		Artifacts  HarborArtifacts  `json:"artifacts"`
	}{repository, artifacts})
	if strings.Contains(string(raw), "robot-token") || strings.Contains(string(raw), "private upstream field") || strings.Contains(string(raw), "hidden") {
		t.Fatalf("adapter leaked credential/upstream fields: %s", raw)
	}
}

func TestHarborRejectsUnsafeConfig(t *testing.T) {
	refs := map[string]HarborRepositoryRef{"app": {Project: "finance", Repository: "team/checkout"}}
	valid := HarborConfig{BaseURL: "https://registry.example.invalid", Repositories: refs}
	cases := []HarborConfig{
		{BaseURL: "http://registry.example.invalid", Repositories: refs, AllowHTTP: true},
		{BaseURL: "http://localhost", Repositories: refs, AllowHTTP: true},
		{BaseURL: "http://127.0.0.1", Repositories: refs},
		{BaseURL: "https://user:secret@registry.example.invalid", Repositories: refs},
		{BaseURL: "https://registry.example.invalid/?token=secret", Repositories: refs},
		{BaseURL: "https://registry.example.invalid?", Repositories: refs},
		{BaseURL: "https://registry.example.invalid/%2e%2e", Repositories: refs},
		{BaseURL: valid.BaseURL, Repositories: map[string]HarborRepositoryRef{"x": {Project: "finance", Repository: "../other"}}},
		{BaseURL: valid.BaseURL, Repositories: map[string]HarborRepositoryRef{"x": {Project: "finance", Repository: "team//other"}}},
		{BaseURL: valid.BaseURL, Repositories: map[string]HarborRepositoryRef{"x": {Project: "finance", Repository: "team/%2Fother"}}},
		{BaseURL: valid.BaseURL, Repositories: refs, AuthMode: "basic", Username: "user:pass", TokenFile: "/tmp/token"},
		{BaseURL: valid.BaseURL, Repositories: refs, AuthMode: "bearer", TokenFile: "relative"},
	}
	for _, config := range cases {
		if _, err := NewHarbor(config); !errors.Is(err, ErrHarborConfig) {
			t.Errorf("accepted unsafe config: %+v, err=%v", config, err)
		}
	}
	if _, err := NewHarbor(valid); err != nil {
		t.Fatalf("valid HTTPS config: %v", err)
	}
}

func TestHarborErrorsAndInvalidResponses(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("bearer-token"), 0600); err != nil {
		t.Fatal(err)
	}
	redirectReached := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectReached = true }))
	defer other.Close()
	mode := "redirect"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bearer-token" {
			t.Error("missing bearer credential")
		}
		switch mode {
		case "redirect":
			w.Header().Set("Location", other.URL+"/steal")
			w.WriteHeader(http.StatusFound)
		case "large":
			_, _ = w.Write([]byte(strings.Repeat("x", harborMaxResponseBytes+1)))
		case "forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("bearer-token"))
		case "not-found":
			w.WriteHeader(http.StatusNotFound)
		case "bad-count":
			w.Header().Set("X-Total-Count", "0")
			_, _ = fmt.Fprintf(w, `[{"digest":%q,"size":1,"push_time":"2026-09-28T08:00:00Z"}]`, harborTestDigest)
		case "wrong-repository":
			_, _ = fmt.Fprintf(w, `[{"digest":%q,"repository_name":"finance/other","size":1,"push_time":"2026-09-28T08:00:00Z"}]`, harborTestDigest)
		case "missing-repository":
			_, _ = fmt.Fprintf(w, `[{"digest":%q,"size":1,"push_time":"2026-09-28T08:00:00Z"}]`, harborTestDigest)
		case "missing-size":
			_, _ = fmt.Fprintf(w, `[{"digest":%q,"push_time":"2026-09-28T08:00:00Z"}]`, harborTestDigest)
		case "null-list":
			_, _ = w.Write([]byte(`null`))
		case "wrong-digest":
			_, _ = w.Write([]byte(`[{"digest":"sha256:bad","size":1,"push_time":"2026-09-28T08:00:00Z"}]`))
		case "zero-time":
			_, _ = fmt.Fprintf(w, `[{"digest":%q,"size":1,"push_time":"0001-01-01T00:00:00Z"}]`, harborTestDigest)
		case "future-time":
			_, _ = fmt.Fprintf(w, `[{"digest":%q,"size":1,"push_time":"2999-01-01T00:00:00Z"}]`, harborTestDigest)
		case "bad-repository":
			_, _ = w.Write([]byte(`{"name":"finance/other","artifact_count":1,"pull_count":1}`))
		}
	}))
	defer server.Close()
	client, err := NewHarbor(HarborConfig{
		BaseURL: server.URL, Repositories: map[string]HarborRepositoryRef{
			"app": {Project: "finance", Repository: "app"},
		}, AuthMode: "bearer", TokenFile: tokenPath, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		mode string
		want error
	}{{"redirect", ErrHarborRedirect}, {"large", ErrHarborResponseTooLarge},
		{"forbidden", ErrHarborForbidden}, {"not-found", ErrHarborNotFound},
		{"bad-count", ErrHarborResponse}, {"wrong-repository", ErrHarborResponse},
		{"missing-repository", ErrHarborResponse},
		{"missing-size", ErrHarborResponse}, {"null-list", ErrHarborResponse},
		{"wrong-digest", ErrHarborResponse}, {"zero-time", ErrHarborResponse},
		{"future-time", ErrHarborResponse}}
	for _, check := range checks {
		mode = check.mode
		_, err := client.ListHarborArtifacts(context.Background(), "app", 2)
		if !errors.Is(err, check.want) || strings.Contains(err.Error(), "bearer-token") {
			t.Errorf("%s: %v", mode, err)
		}
	}
	mode = "bad-repository"
	if _, err := client.GetHarborRepository(context.Background(), "app"); !errors.Is(err, ErrHarborResponse) {
		t.Fatalf("wrong repository: %v", err)
	}
	if redirectReached {
		t.Fatal("followed credential-bearing redirect")
	}
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetHarborRepository(context.Background(), "app"); !errors.Is(err, ErrHarborCredential) {
		t.Fatalf("accepted group-readable token: %v", err)
	}
}

func TestHarborHonorsRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := NewHarbor(HarborConfig{
		BaseURL: server.URL, Repositories: map[string]HarborRepositoryRef{"app": {Project: "test", Repository: "app"}}, AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.GetHarborRepository(ctx, "app"); !errors.Is(err, ErrHarborTimeout) {
		t.Fatalf("request deadline: %v", err)
	}
}
