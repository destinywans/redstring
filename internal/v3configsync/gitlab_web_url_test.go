package v3configsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PA-98: behind an API proxy GitLab's links are on its external_url. A merge
// request link is checked against gitlab.web_url when set, as strictly as
// against base_url, and a mismatch says so.
func TestGitLabWebURLChecksMergeRequestLinks(t *testing.T) {
	const web = "http://127.0.0.1:28080"
	payload := []byte(`{"kind":"agent_version","name":"validated"}`)
	var mrCreated int
	var branchCreated, fileCreated bool
	linkBase := web
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/merge_requests":
			if mrCreated > 0 {
				respondJSON(w, 200, []MR{testMR(linkBase, "opened")})
			} else {
				respondJSON(w, 200, []MR{})
			}
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/branches/"+testBranch:
			if !branchCreated {
				w.WriteHeader(404)
				return
			}
			respondJSON(w, 200, map[string]string{"name": testBranch})
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/repository/branches":
			branchCreated = true
			respondJSON(w, 201, map[string]string{"name": testBranch})
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/files/"+testPath+"/raw":
			if !fileCreated {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write(payload)
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/repository/files/"+testPath:
			fileCreated = true
			respondJSON(w, 201, map[string]string{"file_path": testPath})
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/merge_requests":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mrCreated++
			respondJSON(w, 201, testMR(linkBase, "opened"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	config := func(webURL string) Config {
		return Config{BaseURL: server.URL, ProjectID: 42, TargetBranch: "main", BranchPrefix: "aiops-promotion/",
			TokenFile: tokenFile(t), AllowLoopbackHTTP: true, WebURL: webURL}
	}
	// Without web_url the MR GitLab created links to its external_url: the
	// export cannot trust it and says why.
	plain, err := NewGitLabClient(config(""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plain.Export(context.Background(), testBranch, testPath, payload, "Promote"); !errors.Is(err, ErrMRLinkMismatch) || !errors.Is(err, ErrUntrusted) {
		t.Fatalf("link on another host: %v", err)
	}
	// With web_url the retry reconciles the same branch and reuses the MR.
	proxied, err := NewGitLabClient(config(web))
	if err != nil {
		t.Fatal(err)
	}
	m, err := proxied.Export(context.Background(), testBranch, testPath, payload, "Promote")
	if err != nil || m.IID != 12 || mrCreated != 1 {
		t.Fatalf("retry: mr=%+v err=%v created=%d", m, err, mrCreated)
	}
	// A configured web_url is as strict as base_url: another host, a
	// fragment or user information is refused.
	other, err := NewGitLabClient(config("http://127.0.0.1:38080"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.Export(context.Background(), testBranch, testPath, payload, "Promote"); !errors.Is(err, ErrMRLinkMismatch) {
		t.Fatalf("link off the configured web address: %v", err)
	}
	linkBase = web + "/x#frag"
	if _, err = proxied.Export(context.Background(), testBranch, testPath, payload, "Promote"); !errors.Is(err, ErrUntrusted) || errors.Is(err, ErrMRLinkMismatch) {
		t.Fatalf("link with a fragment: %v", err)
	}
	linkBase = "http://user@127.0.0.1:28080"
	if _, err = proxied.Export(context.Background(), testBranch, testPath, payload, "Promote"); !errors.Is(err, ErrUntrusted) || errors.Is(err, ErrMRLinkMismatch) {
		t.Fatalf("link with user information: %v", err)
	}
	for _, bad := range []string{"ftp://gitlab.example", "https://user@gitlab.example", "https://gitlab.example/path",
		"https://gitlab.example?x=1", "https://gitlab.example#x", "https://"} {
		if _, err := NewGitLabClient(config(bad)); err == nil || !strings.Contains(err.Error(), "web URL") {
			t.Fatalf("web_url %q accepted: %v", bad, err)
		}
	}
}

// mismatchOnceRepository creates the MR like fakeRepository, but the first
// time reports its link off the web address, as a missing web_url did.
type mismatchOnceRepository struct {
	*fakeRepository
	reported bool
}

func (m *mismatchOnceRepository) Export(ctx context.Context, branch, path string, payload []byte, title string) (MR, error) {
	mr, err := m.fakeRepository.Export(ctx, branch, path, payload, title)
	if !m.reported {
		m.reported = true
		return MR{}, ErrMRLinkMismatch
	}
	return mr, err
}

// PA-98: the mismatch is reported as such, the export stays uncertain, and
// the retry after the fix reconciles the same branch instead of another MR.
func TestPromotionWebURLMismatchThenRetryReusesExport(t *testing.T) {
	s := isolatedConfigStore(t)
	repo := &mismatchOnceRepository{fakeRepository: &fakeRepository{}}
	api := configServer(t, s, repo, "nonprod")
	first := configRequest(api, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"CT-source"}`)
	if first.Code != 503 || !strings.Contains(first.Body.String(), `"code":"gitlab_web_url_mismatch"`) {
		t.Fatalf("mismatch: %d %s", first.Code, first.Body.String())
	}
	var state string
	var unknown int
	if err := s.DB.QueryRow(t.Context(), `SELECT state,(SELECT count(*) FROM management_audit WHERE action='v3.config_export.result_unknown')
 FROM v3_config_exports`).Scan(&state, &unknown); err != nil || state != "uncertain" || unknown != 1 {
		t.Fatalf("ledger after mismatch: %q unknown audits=%d %v", state, unknown, err)
	}
	branch := repo.lastBranch
	second := configRequest(api, "POST", "/api/v1/promotions", "test", "owner-token", `{"kind":"templates","id":"CT-source"}`)
	if second.Code != 200 || !strings.Contains(second.Body.String(), `"mr":"!42"`) || repo.lastBranch != branch || repo.calls != 2 {
		t.Fatalf("retry: %d %s branch %q->%q calls=%d", second.Code, second.Body.String(), branch, repo.lastBranch, repo.calls)
	}
	var exports int
	if err := s.DB.QueryRow(t.Context(), `SELECT count(*),max(state) FROM v3_config_exports`).Scan(&exports, &state); err != nil || exports != 1 || state != "exported" {
		t.Fatalf("ledger after retry: %d %q %v", exports, state, err)
	}
}

// PA-100: a merged MR whose changes_count GitLab has not computed yet is
// not verified, and says it is not ready rather than untrustworthy.
func TestGitLabMergedNotPrepared(t *testing.T) {
	var base string
	count := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := testMR(base, "merged")
		m.ChangesCount = count
		m.Author.ID = 7
		respondJSON(w, 200, m)
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	if _, err := c.Merged(context.Background(), 12); !errors.Is(err, ErrMRNotPrepared) || !errors.Is(err, ErrUntrusted) {
		t.Fatalf("unprepared merge: %v", err)
	}
	count = "2"
	if _, err := c.Merged(context.Background(), 12); !errors.Is(err, ErrUntrusted) || errors.Is(err, ErrMRNotPrepared) {
		t.Fatalf("two changed files: %v", err)
	}
	count = "1"
	if _, err := c.Merged(context.Background(), 12); err != nil {
		t.Fatalf("prepared merge: %v", err)
	}
}
