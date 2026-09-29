package v3configsync

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// This exercises the registered read endpoint with the real GitLab adapter
// and a persisted export record. The GitLab server and PG schema are isolated
// fixtures; no external repository or production data is touched.
func TestConfigSyncGitLabReadThroughPersistedRunbook(t *testing.T) {
	fixture := fourKindFixtures(t)[0]
	if fixture.kind != "runbook" {
		t.Fatal("expected signed runbook fixture")
	}
	var merged atomic.Bool
	var immutableReads atomic.Int32
	var base string
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("unexpected GitLab request method or token: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusForbidden)
			return
		}
		state := "opened"
		if merged.Load() {
			state = "merged"
		}
		mr := testMR(base, state)
		mr.SourceBranch = "aiops-promotion/" + fixture.id
		switch r.URL.Path {
		case "/api/v4/projects/42/merge_requests":
			if r.URL.Query().Get("state") != "merged" || r.URL.Query().Get("target_branch") != "main" ||
				r.URL.Query().Get("scope") != "all" || r.URL.Query().Get("per_page") != "100" ||
				r.URL.Query().Get("page") != "1" {
				t.Errorf("unbounded merged list: %s", r.URL.String())
			}
			respondJSON(w, 200, []MR{mr})
		case "/api/v4/projects/42/merge_requests/12":
			if r.URL.RawQuery != "" {
				t.Errorf("unexpected exact MR query: %s", r.URL.String())
			}
			respondJSON(w, 200, mr)
		case "/api/v4/projects/42/merge_requests/12/versions":
			respondJSON(w, 200, []map[string]any{{"head_commit_sha": testSHA,
				"created_at": "2026-09-28T08:00:00Z", "state": "collected"}})
		case "/api/v4/projects/42/merge_requests/12/approvals":
			respondJSON(w, 200, map[string]any{"iid": 12, "project_id": 42,
				"state": "merged", "approved": true,
				"approved_by": []any{map[string]any{"user": map[string]any{"id": 8},
					"approved_at": "2026-09-28T09:00:00Z"}}})
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
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("per_page") != "100" {
				t.Errorf("unbounded MR diff: %s", r.URL.String())
			}
			respondJSON(w, 200, []map[string]any{{"old_path": fixture.path,
				"new_path": fixture.path, "new_file": true}})
		case "/api/v4/projects/42/repository/files/" + fixture.path + "/raw":
			if r.URL.Query().Get("ref") != mergeSHA || !strings.Contains(r.URL.EscapedPath(), "%2F") {
				t.Errorf("manifest was not read at encoded immutable merge ref: %s", r.URL.String())
			}
			immutableReads.Add(1)
			w.WriteHeader(200)
			_, _ = w.Write(fixture.raw)
		default:
			t.Errorf("unexpected GitLab request %s", r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer gitlab.Close()
	base = gitlab.URL

	store := seedReadFixtures(t, []readFixture{fixture})
	if _, err := store.DB.Exec(t.Context(), `UPDATE v3_config_exports
	SET project_id=42,mr_iid=12,mr_url=$1 WHERE id=$2`,
		base+"/project/-/merge_requests/12", fixture.id); err != nil {
		t.Fatal(err)
	}
	repo := clientFor(t, base)
	nonprod := configServer(t, store, repo, "nonprod")
	production := configServer(t, store, repo, "production")

	check := func(t *testing.T, tier, token, env, direction, status, id string) {
		t.Helper()
		api := nonprod
		if tier == "production" {
			api = production
		}
		response := configRequest(api, "GET", "/api/v1/config-sync", env, token, "")
		if response.Code != 200 {
			t.Fatalf("%s GET: %d %s", tier, response.Code, response.Body.String())
		}
		list := decodeSyncList(t, response.Body.Bytes())
		if list.Direction != direction || len(list.Items) != 1 {
			t.Fatalf("%s list: %+v", tier, list)
		}
		item := list.Items[0]
		if item.ID != id || item.Kind != "runbook" || item.Title != fixture.title ||
			item.Status != status || item.MR != "!12" {
			t.Fatalf("%s item: %+v", tier, item)
		}
	}
	check(t, "nonprod", "owner-token", "test", "export", "exported", "ex-"+fixture.id)
	if immutableReads.Load() != 0 {
		t.Fatal("opened MR caused an immutable merge file read")
	}
	merged.Store(true)
	check(t, "nonprod", "owner-token", "test", "export", "merged", "ex-"+fixture.id)
	check(t, "production", "admin-token", "prod", "import", "merged", "mr-12")
	if immutableReads.Load() != 2 {
		t.Fatalf("expected source and production reads at immutable merge ref, got %d", immutableReads.Load())
	}
}
