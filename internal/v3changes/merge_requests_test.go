package v3changes

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabMergeRequestsBoundedReadOnly(t *testing.T) {
	const secret = "synthetic-gitlab-token"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/42/merge_requests" ||
			r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "20" ||
			r.URL.Query().Get("state") != "merged" || r.URL.Query().Get("target_branch") != "main" ||
			r.Header.Get("PRIVATE-TOKEN") != secret {
			t.Errorf("unexpected GitLab request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("X-Next-Page", "3")
		_, _ = w.Write([]byte(`[{"iid":7,"project_id":42,"target_project_id":42,"state":"merged","source_branch":"codex/change","target_branch":"main","sha":"abc123","merge_commit_sha":"def456","created_at":"2026-09-28T10:00:00Z","updated_at":"2026-09-28T10:03:00Z","merged_at":"2026-09-28T10:02:00Z"}]`))
	}))
	defer server.Close()
	s := Service{Resolver: testResolver{token: secret}}
	target := target{ProjectID: "42", Endpoint: server.URL, AuthMethod: "pat", CredentialRef: "vault://isolated/gitlab#token"}
	items, next, err := s.fetchMergeRequests(t.Context(), target, 2, "merged", "main")
	if err != nil || next != 3 || len(items) != 1 || items[0].IID != 7 || items[0].MergedAt == nil || calls != 1 {
		t.Fatalf("merge requests=%+v next=%d calls=%d err=%v", items, next, calls, err)
	}
	for _, endpoint := range []string{"http://example.invalid", "https://user:pass@example.invalid", "https://example.invalid?token=x"} {
		target.Endpoint = endpoint
		if _, _, err := s.fetchMergeRequests(t.Context(), target, 1, "all", ""); !errors.Is(err, errDisabled) {
			t.Fatalf("unsafe endpoint %q: %v", endpoint, err)
		}
	}
	if calls != 1 {
		t.Fatalf("unsafe endpoint caused HTTP request: %d", calls)
	}
}

func TestGitLabMergeRequestsRejectsUntrustedResponse(t *testing.T) {
	redirectFollowed := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectFollowed = true }))
	defer destination.Close()
	mode := "redirect"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "redirect":
			http.Redirect(w, r, destination.URL, http.StatusFound)
		case "oversize":
			_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
		case "too_many":
			var rows []string
			for iid := 1; iid <= 21; iid++ {
				rows = append(rows, fmt.Sprintf(`{"iid":%d,"project_id":42,"target_project_id":42,"state":"merged","source_branch":"topic","target_branch":"main","created_at":"2026-09-28T10:00:00Z","updated_at":"2026-09-28T10:02:00Z"}`, iid))
			}
			_, _ = w.Write([]byte("[" + strings.Join(rows, ",") + "]"))
		case "invalid_next":
			w.Header().Set("X-Next-Page", "1001")
			_, _ = w.Write([]byte(`[]`))
		default:
			project, targetProject, state, source, branch := int64(42), int64(42), "merged", "topic", "main"
			created, updated, merged := "2026-09-28T10:00:00Z", "2026-09-28T10:02:00Z", "2026-09-28T10:01:00Z"
			switch mode {
			case "wrong_project":
				project = 99
			case "wrong_target_project":
				targetProject = 99
			case "wrong_state":
				state = "opened"
			case "unknown_state":
				state = "invented"
			case "wrong_branch":
				branch = "release"
			case "invalid_branch":
				source = "bad\nref"
			case "future_time":
				updated = "2999-01-01T00:00:00Z"
			case "inverted_time":
				updated = "2026-09-28T09:00:00Z"
			case "invalid_merge_time":
				merged = "2026-09-28T09:00:00Z"
			}
			fmt.Fprintf(w, `[{"iid":7,"project_id":%d,"target_project_id":%d,"state":%q,"source_branch":%q,"target_branch":%q,"created_at":%q,"updated_at":%q,"merged_at":%q}]`, project, targetProject, state, source, branch, created, updated, merged)
		}
	}))
	defer server.Close()
	s := Service{Resolver: testResolver{token: "synthetic-token"}}
	target := target{ProjectID: "42", Endpoint: server.URL, AuthMethod: "pat", CredentialRef: "vault://isolated/gitlab#token"}
	for _, selected := range []string{"redirect", "oversize", "too_many", "invalid_next", "wrong_project", "wrong_target_project", "wrong_state", "unknown_state", "wrong_branch", "invalid_branch", "future_time", "inverted_time", "invalid_merge_time"} {
		mode = selected
		if _, _, err := s.fetchMergeRequests(t.Context(), target, 1, "merged", "main"); !errors.Is(err, errUpstream) {
			t.Fatalf("%s: %v", selected, err)
		}
	}
	if redirectFollowed {
		t.Fatal("followed redirect and exposed credential")
	}
}
