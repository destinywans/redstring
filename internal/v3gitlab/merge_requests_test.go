package v3gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tokenFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitlab-token")
	if err := os.WriteFile(path, []byte("first-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestClient(t *testing.T, endpoint, token string) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: endpoint, ProjectID: 42, TargetBranch: "main", TokenFile: token, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMergeRequestUsesFixedProjectAndCurrentToken(t *testing.T) {
	currentToken := "first-token"
	state := "opened"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/42/merge_requests/7" ||
			r.URL.RawQuery != "" || r.Header.Get("PRIVATE-TOKEN") != currentToken {
			t.Errorf("unexpected request: %s %s token=%q", r.Method, r.URL.String(), r.Header.Get("PRIVATE-TOKEN"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"iid":7,"project_id":42,"target_project_id":42,"state":%q,"source_branch":"codex/change","target_branch":"main","sha":"abc123","merged_at":null}`, state)
	}))
	defer server.Close()
	path := tokenFile(t)
	c := newTestClient(t, server.URL, path)
	mr, err := c.MergeRequest(context.Background(), 7)
	if err != nil || mr.Merged || mr.State != "opened" {
		t.Fatalf("opened MR: %+v, %v", mr, err)
	}
	currentToken = "rotated-token"
	if err := os.WriteFile(path, []byte(currentToken), 0600); err != nil {
		t.Fatal(err)
	}
	state = "merged"
	mr, err = c.MergeRequest(context.Background(), 7)
	if err != nil || !mr.Merged || mr.State != "merged" || mr.TargetBranch != "main" {
		t.Fatalf("merged MR: %+v, %v", mr, err)
	}
}

func TestMergeRequestErrorMappingAndBoundaries(t *testing.T) {
	status := http.StatusNotFound
	body := ""
	redirects := 0
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirects++ }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusFound {
			w.Header().Set("Location", destination.URL)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	c := newTestClient(t, server.URL, tokenFile(t))
	for _, tc := range []struct {
		status int
		want   error
	}{
		{401, ErrUnauthorized}, {403, ErrForbidden}, {404, ErrNotFound},
		{408, ErrTimeout}, {429, ErrRateLimited}, {503, ErrUpstream}, {504, ErrTimeout}, {302, ErrRedirect},
	} {
		status = tc.status
		_, err := c.MergeRequest(context.Background(), 7)
		if !errors.Is(err, tc.want) {
			t.Fatalf("status %d: got %v, want %v", status, err, tc.want)
		}
	}
	if redirects != 0 {
		t.Fatal("followed redirect and exposed credential")
	}
	status = 200
	body = `{"iid":7,"project_id":42,"target_project_id":42,"state":"merged","source_branch":"topic","target_branch":"other"}`
	if _, err := c.MergeRequest(context.Background(), 7); !errors.Is(err, ErrUnexpectedTarget) {
		t.Fatalf("unexpected target: %v", err)
	}
	body = `{"iid":7,"project_id":99,"target_project_id":42,"state":"merged","source_branch":"topic","target_branch":"main"}`
	if _, err := c.MergeRequest(context.Background(), 7); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("wrong project: %v", err)
	}
	body = strings.Repeat("x", maxResponseBytes+1)
	if _, err := c.MergeRequest(context.Background(), 7); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestMergeRequestRejectsInvalidConfigAndIID(t *testing.T) {
	path := tokenFile(t)
	for _, endpoint := range []string{"http://gitlab.example.test", "https://user:pass@gitlab.example.test", "https://gitlab.example.test?token=x"} {
		if _, err := New(Config{BaseURL: endpoint, ProjectID: 42, TargetBranch: "main", TokenFile: path, AllowHTTP: true}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("accepted endpoint %q: %v", endpoint, err)
		}
	}
	c := newTestClient(t, "http://127.0.0.1:9", path)
	if _, err := c.MergeRequest(context.Background(), 0); !errors.Is(err, ErrInvalidIID) {
		t.Fatalf("invalid IID: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MergeRequest(context.Background(), 1); !errors.Is(err, ErrCredential) {
		t.Fatalf("permissive token file: %v", err)
	}
}
