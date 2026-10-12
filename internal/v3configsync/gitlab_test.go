package v3configsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
	m.Author.ID = 7
	if state == "merged" {
		m.MergedAt = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	}
	return m
}

func respondJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func protectedRule(name string, push int, force bool) map[string]any {
	return map[string]any{"name": name, "allow_force_push": force,
		"push_access_levels":  []any{map[string]any{"access_level": push}},
		"merge_access_levels": []any{map[string]any{"access_level": 40}}}
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

func TestGitLabExportOverlappingCallsReconcileConflict(t *testing.T) {
	payload := []byte(`{"kind":"template"}`)
	var listCalls, mrPosts atomic.Int32
	firstLists := make(chan struct{})
	secondLists := make(chan struct{})
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/merge_requests":
			n := listCalls.Add(1)
			if n <= 2 {
				if n == 2 {
					close(firstLists)
				}
				<-firstLists
				respondJSON(w, 200, []MR{})
			} else if n <= 4 {
				if n == 4 {
					close(secondLists)
				}
				<-secondLists
				respondJSON(w, 200, []MR{})
			} else {
				respondJSON(w, 200, []MR{testMR(base, "opened")})
			}
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/branches/"+testBranch:
			respondJSON(w, 200, map[string]any{"name": testBranch})
		case r.Method == "GET" && r.URL.Path == "/api/v4/projects/42/repository/files/"+testPath+"/raw":
			w.WriteHeader(200)
			_, _ = w.Write(payload)
		case r.Method == "POST" && r.URL.Path == "/api/v4/projects/42/merge_requests":
			if mrPosts.Add(1) == 1 {
				respondJSON(w, 201, testMR(base, "opened"))
			} else {
				w.WriteHeader(409)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	type outcome struct {
		mr  MR
		err error
	}
	results := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			mr, err := c.Export(context.Background(), testBranch, testPath, payload, "Promote validated config")
			results <- outcome{mr, err}
		}()
	}
	var successes, conflicts int
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err == nil && result.mr.IID == 12 {
			successes++
		} else if errors.Is(result.err, ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected overlapping result: %+v", result)
		}
	}
	if successes != 1 || conflicts != 1 || mrPosts.Load() != 2 {
		t.Fatalf("successes=%d conflicts=%d MR POSTs=%d", successes, conflicts, mrPosts.Load())
	}
	m, err := c.Export(context.Background(), testBranch, testPath, payload, "Promote validated config")
	if err != nil || m.IID != 12 || mrPosts.Load() != 2 {
		t.Fatalf("stable retry did not reconcile: mr=%+v err=%v posts=%d", m, err, mrPosts.Load())
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
		case "/api/v4/projects/42/merge_requests/12/versions":
			respondJSON(w, 200, []map[string]any{{"head_commit_sha": testSHA, "created_at": "2026-09-28T08:00:00Z", "state": "collected"}})
		case "/api/v4/projects/42/merge_requests/12/approvals":
			respondJSON(w, 200, map[string]any{"iid": 12, "project_id": 42, "state": "merged", "approved": true, "approved_by": []any{map[string]any{"user": map[string]any{"id": 8}, "approved_at": "2026-09-28T09:00:00Z"}}})
		case "/api/v4/users/8":
			respondJSON(w, 200, map[string]any{"id": 8, "bot": false})
		case "/api/v4/projects/42/repository/branches/main":
			respondJSON(w, 200, map[string]any{"name": "main", "protected": true})
		case "/api/v4/projects/42/protected_branches/main":
			respondJSON(w, 200, protectedRule("main", 0, false))
		case "/api/v4/projects/42/protected_branches":
			w.Header().Set("X-Page", "1")
			w.Header().Set("X-Per-Page", "100")
			respondJSON(w, 200, []any{protectedRule("main", 0, false)})
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

func TestGitLabReadMergedManifestExactReviewedFile(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name, dir, path, count                     string
		extra, truncated, wrongBranch, malformedMR bool
		wantOK                                     bool
	}{
		{name: "template", dir: "templates", wantOK: true},
		{name: "runbook", dir: "runbooks", wantOK: true},
		{name: "alert rule", dir: "alert-rules", wantOK: true},
		{name: "agent version", dir: "agent-versions", wantOK: true},
		{name: "extra diff", dir: "runbooks", extra: true},
		{name: "truncated diff count", dir: "runbooks", count: "2"},
		{name: "truncated diff page", dir: "runbooks", truncated: true},
		{name: "wrong directory", dir: "secrets"},
		{name: "wrong promotion id", dir: "templates", path: "ops-config/templates/" + strings.Repeat("b", 32) + ".json"},
		{name: "wrong branch", dir: "templates", wrongBranch: true},
		{name: "malformed MR", dir: "templates", malformedMR: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = "ops-config/" + tc.dir + "/" + id + ".json"
			}
			var base string
			rawReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v4/projects/42/merge_requests/12":
					if tc.malformedMR {
						w.WriteHeader(200)
						_, _ = w.Write([]byte(`{"iid":`))
						return
					}
					m := testMR(base, "merged")
					m.SourceBranch = "aiops-promotion/" + id
					if tc.wrongBranch {
						m.SourceBranch = "unowned/" + id
					}
					if tc.count != "" {
						m.ChangesCount = tc.count
					}
					respondJSON(w, 200, m)
				case "/api/v4/projects/42/merge_requests/12/versions":
					respondJSON(w, 200, []map[string]any{{"head_commit_sha": testSHA, "created_at": "2026-09-28T08:00:00Z", "state": "collected"}})
				case "/api/v4/projects/42/merge_requests/12/approvals":
					respondJSON(w, 200, map[string]any{"iid": 12, "project_id": 42, "state": "merged", "approved": true, "approved_by": []any{map[string]any{"user": map[string]any{"id": 8}, "approved_at": "2026-09-28T09:00:00Z"}}})
				case "/api/v4/users/8":
					respondJSON(w, 200, map[string]any{"id": 8, "bot": false})
				case "/api/v4/projects/42/repository/branches/main":
					respondJSON(w, 200, map[string]any{"name": "main", "protected": true})
				case "/api/v4/projects/42/protected_branches/main":
					respondJSON(w, 200, protectedRule("main", 0, false))
				case "/api/v4/projects/42/protected_branches":
					w.Header().Set("X-Page", "1")
					w.Header().Set("X-Per-Page", "100")
					respondJSON(w, 200, []any{protectedRule("main", 0, false)})
				case "/api/v4/projects/42/merge_requests/12/diffs":
					if r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
						t.Error("diff request is not bounded")
					}
					if tc.truncated {
						w.Header().Set("X-Next-Page", "2")
					}
					diffs := []map[string]any{{"old_path": path, "new_path": path, "new_file": true}}
					if tc.extra {
						diffs = append(diffs, map[string]any{"old_path": "other.json", "new_path": "other.json", "new_file": true})
					}
					respondJSON(w, 200, diffs)
				case "/api/v4/projects/42/repository/files/" + path + "/raw":
					rawReads++
					if r.URL.Query().Get("ref") != mergeSHA {
						t.Errorf("read at %q instead of immutable merge SHA", r.URL.Query().Get("ref"))
					}
					w.WriteHeader(200)
					_, _ = w.Write([]byte(`{"kind":"` + tc.dir + `"}`))
				default:
					t.Errorf("unexpected request %s", r.URL.String())
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			base = server.URL
			c := clientFor(t, base)
			m, gotPath, data, err := c.ReadMergedManifest(context.Background(), 12)
			if tc.wantOK {
				if err != nil || m.IID != 12 || gotPath != path || string(data) != `{"kind":"`+tc.dir+`"}` || rawReads != 1 {
					t.Fatalf("mr=%+v path=%q data=%s reads=%d err=%v", m, gotPath, data, rawReads, err)
				}
			} else if err == nil || rawReads != 0 {
				t.Fatalf("untrusted MR accepted: mr=%+v path=%q reads=%d", m, gotPath, rawReads)
			}
		})
	}
}

func TestGitLabListMergedRejectsMalformedOwnedMR(t *testing.T) {
	var base string
	var owned, valid, malformedSuffix bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/42/merge_requests" {
			t.Errorf("unexpected request %s", r.URL.String())
			w.WriteHeader(404)
			return
		}
		m := testMR(base, "merged")
		if !owned {
			m.SourceBranch = "unrelated/branch"
		} else if malformedSuffix {
			m.SourceBranch = "aiops-promotion/bad-id"
		} else if valid {
			m.SourceBranch = "aiops-promotion/" + strings.Repeat("a", 32)
		} else {
			m.MergeCommitSHA = ""
		}
		respondJSON(w, 200, []MR{m})
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	if list, err := c.ListMerged(context.Background(), 10); err != nil || len(list) != 0 {
		t.Fatalf("unrelated labeled MR should be skipped: list=%+v err=%v", list, err)
	}
	owned = true
	if _, err := c.ListMerged(context.Background(), 10); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("malformed owned MR disappeared: %v", err)
	}
	malformedSuffix = true
	if _, err := c.ListMerged(context.Background(), 10); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("malformed owned promotion ID disappeared: %v", err)
	}
	malformedSuffix = false
	valid = true
	if list, err := c.ListMerged(context.Background(), 10); err != nil || len(list) != 1 {
		t.Fatalf("valid owned MR absent: list=%+v err=%v", list, err)
	}
}

func TestGitLabListMergedIncludesPromotionAfterLabelRemoval(t *testing.T) {
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects/42/merge_requests" || r.URL.Query().Get("labels") != "" || r.URL.Query().Get("scope") != "all" ||
			r.URL.Query().Get("state") != "merged" || r.URL.Query().Get("target_branch") != "main" ||
			r.URL.Query().Get("per_page") != "10" || r.URL.Query().Get("page") != "1" {
			t.Errorf("list query misses unlabeled promotion MRs: %s", r.URL.String())
			w.WriteHeader(400)
			return
		}
		unrelated := testMR(base, "merged")
		unrelated.SourceBranch = "feature/unrelated"
		promotion := testMR(base, "merged")
		promotion.SourceBranch = "aiops-promotion/" + strings.Repeat("a", 32)
		respondJSON(w, 200, []MR{unrelated, promotion})
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	list, err := c.ListMerged(context.Background(), 10)
	if err != nil || len(list) != 1 || list[0].SourceBranch != "aiops-promotion/"+strings.Repeat("a", 32) {
		t.Fatalf("unlabeled promotion disappeared or unrelated MR leaked: list=%+v err=%v", list, err)
	}
}

func TestGitLabListMergedFullPageWithoutPaginationFailsClosed(t *testing.T) {
	var base string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unrelated := testMR(base, "merged")
		unrelated.SourceBranch = "feature/unrelated"
		respondJSON(w, 200, []MR{unrelated})
	}))
	defer server.Close()
	base = server.URL
	c := clientFor(t, base)
	if _, err := c.ListMerged(context.Background(), 1); !errors.Is(err, ErrListTooLarge) {
		t.Fatalf("ambiguous full page accepted: %v", err)
	}
}

func TestGitLabLookupPromotionMRExactOwnedStatus(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name, state                                          string
		wrongBranch, wrongProject, wrongURL, missingMergeSHA bool
		wantOK                                               bool
	}{
		{name: "opened export", state: "opened", wantOK: true},
		{name: "merged export", state: "merged", wantOK: true},
		{name: "closed is not exported or merged", state: "closed"},
		{name: "wrong branch", state: "merged", wrongBranch: true},
		{name: "wrong project", state: "merged", wrongProject: true},
		{name: "wrong MR URL", state: "merged", wrongURL: true},
		{name: "merged without immutable SHA", state: "merged", missingMergeSHA: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var base string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v4/projects/42/merge_requests/12" || r.URL.RawQuery != "" {
					t.Errorf("unexpected lookup %s %s", r.Method, r.URL.String())
					w.WriteHeader(404)
					return
				}
				m := testMR(base, tc.state)
				m.SourceBranch = "aiops-promotion/" + id
				if tc.wrongBranch {
					m.SourceBranch = "other/" + id
				}
				if tc.wrongProject {
					m.SourceProjectID = 43
				}
				if tc.wrongURL {
					m.URL = base + "/project/-/merge_requests/13"
				}
				if tc.missingMergeSHA {
					m.MergeCommitSHA = ""
				}
				respondJSON(w, 200, m)
			}))
			defer server.Close()
			base = server.URL
			c := clientFor(t, base)
			m, err := c.LookupPromotionMR(context.Background(), 12)
			if tc.wantOK {
				if err != nil || m.IID != 12 || m.State != tc.state {
					t.Fatalf("owned MR rejected: mr=%+v err=%v", m, err)
				}
			} else if !errors.Is(err, ErrUntrusted) {
				t.Fatalf("untrusted MR accepted: mr=%+v err=%v", m, err)
			}
		})
	}
}

func TestGitLabReadMergedReviewProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, approvedAt, versionSHA                                                                                                                                           string
		approverID                                                                                                                                                             int64
		bot                                                                                                                                                                    bool
		missingApproval, unsatisfiedRules, missingBotField, laterVersion, permissivePush, noProtection, unavailableApproval, unavailableVersions, unavailablePolicy, forcePush bool
		wildcardPush, wildcardForce, safeWildcard, twoPage, listUnavailable, listHeadersMissing, listOverflow                                                                  bool
		approvalNoIDs, approvalWrongIID, approvalWrongProject, approvalWrongState                                                                                              bool
		wantOK                                                                                                                                                                 bool
	}{
		{name: "valid independent review", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, wantOK: true},
		{name: "GitLab CE approval response without MR identity", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, approvalNoIDs: true, wantOK: true},
		{name: "approval response wrong MR IID", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, approvalWrongIID: true},
		{name: "approval response wrong project", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, approvalWrongProject: true},
		{name: "approval response wrong state", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, approvalWrongState: true},
		{name: "author approved", approverID: 7, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA},
		{name: "bot approved", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, bot: true},
		{name: "approval missing", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, missingApproval: true},
		{name: "approval rules unsatisfied", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, unsatisfiedRules: true},
		{name: "approval before final diff", approverID: 8, approvedAt: "2026-09-28T07:00:00Z", versionSHA: testSHA},
		{name: "later commit invalidates approval", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, laterVersion: true},
		{name: "approval after merge", approverID: 8, approvedAt: "2026-09-28T11:00:00Z", versionSHA: testSHA},
		{name: "approval at merge instant", approverID: 8, approvedAt: "2026-09-28T10:00:00Z", versionSHA: testSHA},
		{name: "final head mismatch", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: mergeSHA},
		{name: "bot field missing", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, missingBotField: true},
		{name: "direct push allowed", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, permissivePush: true},
		{name: "target unprotected", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, noProtection: true},
		{name: "approval API unavailable", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, unavailableApproval: true},
		{name: "versions API unavailable", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, unavailableVersions: true},
		{name: "policy API unavailable", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, unavailablePolicy: true},
		{name: "force push allowed", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, forcePush: true},
		{name: "permissive wildcard defeats exact rule", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, wildcardPush: true},
		{name: "force push wildcard defeats exact rule", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, wildcardForce: true},
		{name: "safe wildcard allowed", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, safeWildcard: true, wantOK: true},
		{name: "safe second page allowed", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, twoPage: true, wantOK: true},
		{name: "protected branch list unavailable", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, listUnavailable: true},
		{name: "protected branch pagination unavailable", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, listHeadersMissing: true},
		{name: "protected branch list overflow", approverID: 8, approvedAt: "2026-09-28T09:00:00Z", versionSHA: testSHA, listOverflow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var base string
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls > 13 {
					t.Error("unbounded GitLab requests")
				}
				switch r.URL.Path {
				case "/api/v4/projects/42/merge_requests/12":
					respondJSON(w, 200, testMR(base, "merged"))
				case "/api/v4/projects/42/merge_requests/12/versions":
					if tc.unavailableVersions {
						w.WriteHeader(503)
						return
					}
					versions := []map[string]any{{"head_commit_sha": tc.versionSHA, "created_at": "2026-09-28T08:00:00Z", "state": "collected"}}
					if tc.laterVersion {
						versions = append(versions, map[string]any{"head_commit_sha": testSHA, "created_at": "2026-09-28T09:30:00Z", "state": "collected"})
					}
					respondJSON(w, 200, versions)
				case "/api/v4/projects/42/merge_requests/12/approvals":
					if tc.unavailableApproval {
						w.WriteHeader(503)
						return
					}
					approved := []any{}
					if !tc.missingApproval {
						approved = append(approved, map[string]any{"user": map[string]any{"id": tc.approverID}, "approved_at": tc.approvedAt})
					}
					response := map[string]any{"approved": !tc.unsatisfiedRules, "approved_by": approved}
					if !tc.approvalNoIDs {
						response["iid"] = 12
						response["project_id"] = 42
						response["state"] = "merged"
					}
					if tc.approvalWrongIID {
						response["iid"] = 13
					}
					if tc.approvalWrongProject {
						response["project_id"] = 43
					}
					if tc.approvalWrongState {
						response["state"] = "opened"
					}
					respondJSON(w, 200, response)
				case "/api/v4/users/8":
					user := map[string]any{"id": 8}
					if !tc.missingBotField {
						user["bot"] = tc.bot
					}
					respondJSON(w, 200, user)
				case "/api/v4/projects/42/repository/branches/main":
					respondJSON(w, 200, map[string]any{"name": "main", "protected": !tc.noProtection})
				case "/api/v4/projects/42/protected_branches/main":
					if tc.unavailablePolicy {
						w.WriteHeader(503)
						return
					}
					push := 0
					if tc.permissivePush {
						push = 40
					}
					respondJSON(w, 200, protectedRule("main", push, tc.forcePush))
				case "/api/v4/projects/42/protected_branches":
					if tc.listUnavailable {
						w.WriteHeader(503)
						return
					}
					if r.URL.Query().Get("per_page") != "100" {
						t.Error("unbounded policy list")
					}
					page, err := strconv.Atoi(r.URL.Query().Get("page"))
					if err != nil || page < 1 || page > 5 {
						t.Error("invalid policy page")
					}
					if !tc.listHeadersMissing {
						w.Header().Set("X-Page", strconv.Itoa(page))
						w.Header().Set("X-Per-Page", "100")
					}
					rules := []any{protectedRule("main", 0, false)}
					if tc.twoPage && page == 1 {
						w.Header().Set("X-Next-Page", "2")
					}
					if tc.twoPage && page == 2 {
						rules = []any{protectedRule("*", 0, false)}
					}
					if tc.listOverflow {
						w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
					}
					if tc.wildcardPush {
						rules = append(rules, protectedRule("*", 40, false))
					}
					if tc.wildcardForce {
						rules = append(rules, protectedRule("m*", 0, true))
					}
					if tc.safeWildcard {
						rules = append(rules, protectedRule("*", 0, false))
					}
					respondJSON(w, 200, rules)
				case "/api/v4/projects/42/merge_requests/12/diffs":
					respondJSON(w, 200, []map[string]any{{"old_path": testPath, "new_path": testPath, "new_file": true}})
				case "/api/v4/projects/42/repository/files/" + testPath + "/raw":
					if r.URL.Query().Get("ref") != mergeSHA {
						t.Error("file read did not use merge SHA")
					}
					w.WriteHeader(200)
					_, _ = w.Write([]byte(`{"kind":"runbook"}`))
				default:
					t.Errorf("unexpected GitLab request: %s", r.URL.String())
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			base = server.URL
			c := clientFor(t, base)
			_, _, err := c.ReadMerged(context.Background(), 12, testPath)
			if tc.wantOK && err != nil {
				t.Fatalf("valid reviewed MR rejected: %v", err)
			}
			// PA-103: GitLab failing a read is unavailable, not untrusted;
			// either way nothing is accepted.
			unavailable := tc.unavailableApproval || tc.unavailableVersions || tc.unavailablePolicy || tc.listUnavailable
			if !tc.wantOK && unavailable && (!errors.Is(err, ErrGitLabUnavailable) || errors.Is(err, ErrUntrusted)) {
				t.Fatalf("GitLab outage not reported as unavailable: %v", err)
			}
			if !tc.wantOK && !unavailable && !errors.Is(err, ErrUntrusted) {
				t.Fatalf("untrusted MR accepted: %v", err)
			}
		})
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
		if r.URL.Query().Get("labels") != "" || r.URL.Query().Get("scope") != "all" || r.URL.Query().Get("state") != "merged" ||
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
