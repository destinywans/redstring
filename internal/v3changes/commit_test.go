package v3changes

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabCommitMetadataBoundedReadOnly(t *testing.T) {
	const secret = "synthetic-gitlab-token"
	sha := strings.Repeat("a", 40)
	parent := strings.Repeat("b", 40)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/42/repository/commits/"+sha ||
			r.URL.RawQuery != "stats=false" || r.Header.Get("PRIVATE-TOKEN") != secret {
			t.Errorf("unexpected GitLab request: %s %s", r.Method, r.URL.String())
		}
		fmt.Fprintf(w, `{"id":%q,"short_id":"aaaaaaaa","title":"Correct cache key","author_email":"hidden@example.invalid","message":"secret body","authored_date":"2026-09-28T10:00:00Z","committed_date":"2026-09-28T10:01:00Z","parent_ids":[%q],"stats":{"additions":99}}`, sha, parent)
	}))
	defer server.Close()
	s := Service{Resolver: testResolver{token: secret}}
	target := target{ProjectID: "42", Endpoint: server.URL, AuthMethod: "pat", CredentialRef: "vault://isolated/gitlab#token"}
	item, err := s.fetchCommit(t.Context(), target, strings.ToUpper(sha))
	if err != nil || item.ID != sha || item.Title != "Correct cache key" || len(item.ParentIDs) != 1 || item.ParentIDs[0] != parent || calls != 1 {
		t.Fatalf("commit=%+v calls=%d err=%v", item, calls, err)
	}
	for _, endpoint := range []string{"http://example.invalid", "https://user:pass@example.invalid", "https://example.invalid?token=x"} {
		target.Endpoint = endpoint
		if _, err := s.fetchCommit(t.Context(), target, sha); !errors.Is(err, errDisabled) {
			t.Fatalf("unsafe endpoint %q: %v", endpoint, err)
		}
	}
	if calls != 1 {
		t.Fatalf("unsafe endpoint caused HTTP request: %d", calls)
	}
}

func TestGitLabCommitRejectsUntrustedResponse(t *testing.T) {
	sha := strings.Repeat("a", 40)
	parent := strings.Repeat("b", 40)
	redirectFollowed := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectFollowed = true }))
	defer destination.Close()
	mode := "redirect"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "redirect":
			http.Redirect(w, r, destination.URL, http.StatusFound)
		case "missing":
			w.WriteHeader(http.StatusNotFound)
		case "oversize":
			_, _ = w.Write([]byte(strings.Repeat("x", maxCommitResponseBytes+1)))
		default:
			id, title, authored, committed, parents := sha, "Title", "2026-09-28T10:00:00Z", "2026-09-28T10:01:00Z", fmt.Sprintf("[%q]", parent)
			switch mode {
			case "wrong_sha":
				id = strings.Repeat("c", 40)
			case "bad_title":
				title = "bad\ntitle"
			case "missing_authored":
				authored = ""
			case "future_committed":
				committed = "2999-01-01T00:00:00Z"
			case "bad_parent":
				parents = `["not-a-sha"]`
			case "too_many_parents":
				values := make([]string, 17)
				for i := range values {
					values[i] = fmt.Sprintf("%q", parent)
				}
				parents = "[" + strings.Join(values, ",") + "]"
			case "missing_parents":
				parents = "null"
			}
			fmt.Fprintf(w, `{"id":%q,"title":%q,"authored_date":%q,"committed_date":%q,"parent_ids":%s}`, id, title, authored, committed, parents)
		}
	}))
	defer server.Close()
	s := Service{Resolver: testResolver{token: "synthetic-token"}}
	target := target{ProjectID: "42", Endpoint: server.URL, AuthMethod: "pat", CredentialRef: "vault://isolated/gitlab#token"}
	for _, selected := range []string{"redirect", "oversize", "wrong_sha", "bad_title", "missing_authored", "future_committed", "bad_parent", "too_many_parents", "missing_parents"} {
		mode = selected
		if _, err := s.fetchCommit(t.Context(), target, sha); !errors.Is(err, errUpstream) {
			t.Fatalf("%s: %v", selected, err)
		}
	}
	mode = "missing"
	if _, err := s.fetchCommit(t.Context(), target, sha); !errors.Is(err, errCommitUnavailable) {
		t.Fatalf("missing: %v", err)
	}
	if redirectFollowed {
		t.Fatal("followed redirect and exposed credential")
	}
}

func TestCommitRequiresFullHexSHA(t *testing.T) {
	for _, value := range []string{"main", "deadbeef", strings.Repeat("g", 40), strings.Repeat("a", 39), strings.Repeat("a", 65)} {
		if validFullSHA(value) {
			t.Fatalf("accepted non-full SHA %q", value)
		}
	}
	if !validFullSHA(strings.Repeat("A", 40)) || !validFullSHA(strings.Repeat("b", 64)) {
		t.Fatal("rejected full hexadecimal SHA")
	}
}
