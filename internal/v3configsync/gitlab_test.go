package v3configsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testBranch = "aiops-promotion/p-123"
	testPath   = "ops-config/templates/p-123.json"
	testSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mergeSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func tokenFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func clientFor(t *testing.T, base string) *GitLabClient {
	t.Helper()
	c, err := NewGitLabClient(Config{BaseURL: base, ProjectID: 42, TargetBranch: "main", BranchPrefix: "aiops-promotion/", TokenFile: tokenFile(t), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testMR(base, state string) MR {
	m := MR{IID: 12, Title: "Promote validated config", URL: base + "/project/-/merge_requests/12", State: state, SourceBranch: testBranch, TargetBranch: "main", SourceProjectID: 42, TargetProjectID: 42, SourceCommitSHA: testSHA}
	if state == "merged" {
		m.MergeCommitSHA = mergeSHA
		m.ChangesCount = "1"
	}
	m.Author.Name = "Config reviewer"
	return m
}

func respondJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func TestGitLabExportReconcilesStableIdentity(t *testing.T) {
	payload := []byte(`{"kind":"runbook","name":"validated"}`)
	var branchCreated, fileCreated bool
	var mrCreated, writes int
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Error("token missing")
		}
		if strings.Contains(r.URL.Path, "/repository/files/") && !strings.Contains(r.URL.EscapedPath(), "%2F") {
			t.Error("file path was not encoded")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/merge_requests":
			if r.URL.Query().Get("source_branch") != testBranch || r.URL.Query().Get("target_branch") != "main" {
				t.Error("MR lookup is not bounded")
			}
			if mrCreated > 0 {
				respondJSON(w, 200, []MR{testMR(base, "opened")})
			} else {
				respondJSON(w, 200, []MR{})
			}
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/branches/"+testBranch:
			if !branchCreated {
				w.WriteHeader(404)
			} else {
				respondJSON(w, 200, map[string]string{"name": testBranch})
			}
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/repository/branches":
			if r.URL.Query().Get("branch") != testBranch || r.URL.Query().Get("ref") != "main" {
				t.Error("branch created from untrusted ref")
			}
			branchCreated, writes = true, writes+1
			respondJSON(w, 201, map[string]string{"name": testBranch})
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/files/"+testPath+"/raw":
			if !fileCreated {
				w.WriteHeader(404)
				return
			}
			if ref := r.URL.Query().Get("ref"); ref != testBranch && ref != testSHA {
				t.Errorf("unexpected file ref %q", ref)
			}
			w.WriteHeader(200)
			_, _ = w.Write(payload)
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/repository/files/"+testPath:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["branch"] != testBranch || body["content"] != string(payload) {
				t.Error("wrong file content or branch")
			}
			fileCreated, writes = true, writes+1
			respondJSON(w, 201, map[string]string{"file_path": testPath})
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/merge_requests":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["source_branch"] != testBranch || body["target_branch"] != "main" || body["labels"] != promotionLabel {
				t.Error("wrong MR branch")
			}
			mrCreated++
			writes++
			respondJSON(w, 201, testMR(base, "opened"))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	for i := 0; i < 2; i++ {
		m, err := c.Export(context.Background(), testBranch, testPath, payload, "Promote validated config")
		if err != nil || m.IID != 12 {
			t.Fatalf("export %d: mr=%+v err=%v", i, m, err)
		}
	}
	if writes != 3 || mrCreated != 1 {
		t.Fatalf("writes=%d mrCreated=%d", writes, mrCreated)
	}
	if _, err := c.Export(context.Background(), testBranch, testPath, []byte(`{"different":true}`), "Promote validated config"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflicting payload, got %v", err)
	}
}

func TestGitLabReadMergedChecksDiffAndExactMergeSHA(t *testing.T) {
	var base string
	var diffs any = []map[string]any{{"old_path": testPath, "new_path": testPath, "new_file": true}}
	changesCount := "1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/42/merge_requests/12":
			m := testMR(base, "merged")
			m.ChangesCount = changesCount
			respondJSON(w, 200, m)
		case "/api/v4/projects/42/merge_requests/12/diffs":
			respondJSON(w, 200, diffs)
		case "/api/v4/projects/42/repository/files/" + testPath + "/raw":
			if r.URL.Query().Get("ref") != mergeSHA {
				t.Errorf("read at %q instead of merge SHA", r.URL.Query().Get("ref"))
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"kind":"runbook"}`))
		default:
			t.Errorf("unexpected request %s", r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	m, data, err := c.ReadMerged(context.Background(), 12, testPath)
	if err != nil || m.MergeCommitSHA != mergeSHA || string(data) != `{"kind":"runbook"}` {
		t.Fatalf("mr=%+v data=%s err=%v", m, data, err)
	}
	diffs = []map[string]any{{"old_path": testPath, "new_path": testPath, "new_file": true, "too_large": true}}
	if _, _, err := c.ReadMerged(context.Background(), 12, testPath); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("too-large diff accepted: %v", err)
	}
	diffs = []map[string]any{{"old_path": "other.json", "new_path": "other.json", "new_file": true}}
	if _, _, err := c.ReadMerged(context.Background(), 12, testPath); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("unrelated MR accepted: %v", err)
	}
	diffs = []map[string]any{{"old_path": testPath, "new_path": testPath, "new_file": true}}
	changesCount = "2" // /diffs can omit a second file under GitLab diff limits.
	if _, _, err := c.ReadMerged(context.Background(), 12, testPath); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("truncated diff count accepted: %v", err)
	}
}

func TestGitLabRejectsUnsafeConfigurationAndRedirect(t *testing.T) {
	token := tokenFile(t)
	cfg := Config{BaseURL: "http://example.com", ProjectID: 42, TargetBranch: "main", BranchPrefix: "aiops-promotion/", TokenFile: token, AllowLoopbackHTTP: true}
	if _, err := NewGitLabClient(cfg); err == nil {
		t.Fatal("external HTTP accepted")
	}
	cfg.BaseURL = "http://127.0.0.1:12345"
	if err := os.Chmod(token, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewGitLabClient(cfg); err == nil {
		t.Fatal("weak token permissions accepted")
	}
	if err := os.Chmod(token, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example/api", http.StatusFound)
	}))
	defer server.Close()
	cfg.BaseURL = server.URL
	c, err := NewGitLabClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListMerged(context.Background(), 10); err == nil {
		t.Fatal("redirect accepted")
	}
	if validPath("../secret.json") || validPath("ops-config/%2Fsecret.json") || validBranch("refs/../main") {
		t.Fatal("unsafe path or branch accepted")
	}
}

func TestGitLabMergedListRejectsIncompletePage(t *testing.T) {
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labels") != promotionLabel || r.URL.Query().Get("state") != "merged" ||
			r.URL.Query().Get("target_branch") != "main" {
			t.Errorf("list filter: %s", r.URL.String())
		}
		w.Header().Set("X-Next-Page", "2")
		respondJSON(w, 200, []MR{testMR(base, "merged")})
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	if _, err := c.ListMerged(context.Background(), 100); !errors.Is(err, ErrListTooLarge) {
		t.Fatalf("incomplete merged list accepted: %v", err)
	}
}
