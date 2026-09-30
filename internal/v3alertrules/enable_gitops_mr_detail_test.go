package v3alertrules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// Native GitLab list responses omit changes_count/diff_refs. The canonical
// single-MR response is required before the existing exact receipt checks.
type nativeMRDetailFixture struct {
	applier      *GitOpsAlertRuleApplier
	intent       AlertRuleEnableIntent
	execution    string
	detail       enableGitMRWire
	list         []map[string]any
	detailStatus int
	nextPage     string
	detailGETs   atomic.Int32
	writes       atomic.Int32
}

func newNativeMRDetailFixture(t *testing.T, state string) *nativeMRDetailFixture {
	t.Helper()
	f := &nativeMRDetailFixture{execution: strings.Repeat("c", 32), detailStatus: 200}
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	content := []byte("groups:\n- name: g\n  rules:\n  - alert: NativeListRule\n    expr: up == 0\n")
	binding := AlertRuleGitOpsBinding{EnvironmentID: "test", ApplicationID: "app", Group: "g", ProviderFile: testRuleFile,
		ProjectID: 42, TargetBranch: "main", YAMLPath: "rules/alerts.yml"}
	f.intent = AlertRuleEnableIntent{EnvironmentID: "test", ApplicationID: "app", ProviderFileIdentityDigest: fileIdentityDigest(testRuleFile),
		Spec: DraftRuleSpec{ApplicationID: "app", EnvironmentID: "test", Group: "g", Name: "NativeListRule", Expr: "up == 0", For: "1m", Mode: "draft",
			Labels: map[string]string{"application": "app", "environment": "test", "severity": "P2"}},
		Deployment: AlertRuleDeploymentPlan{ProjectID: 42, TargetBranch: "main", Path: binding.YAMLPath, BaseRevision: base,
			ContentDigest: hashGitOpsBytes(content), ScopeFingerprint: deploymentScope(binding)}}
	f.detail = enableGitMRWire{IID: 7, State: state, SourceBranch: "aiops-alert-rule-enable/" + f.execution,
		TargetBranch: "main", SourceProjectID: 42, TargetProjectID: 42, SHA: head, ChangesCount: "1"}
	f.detail.Author.ID = 1
	f.detail.DiffRefs.StartSHA = base
	f.detail.DiffRefs.HeadSHA = head
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			f.writes.Add(1)
			w.WriteHeader(405)
			return
		}
		root := "/api/v4/projects/42"
		var out any
		switch r.URL.Path {
		case root + "/merge_requests":
			if r.URL.Query().Get("source_branch") != "aiops-alert-rule-enable/"+f.execution || r.URL.Query().Get("state") != "all" {
				w.WriteHeader(400)
				return
			}
			w.Header().Set("X-Next-Page", f.nextPage)
			out = f.list
		case root + "/merge_requests/7":
			f.detailGETs.Add(1)
			if f.detailStatus != 200 {
				w.WriteHeader(f.detailStatus)
				return
			}
			out = f.detail
		case root + "/merge_requests/7/diffs":
			out = []any{map[string]any{"old_path": binding.YAMLPath, "new_path": binding.YAMLPath}}
		case root + "/repository/commits/" + head:
			out = map[string]any{"id": head, "parent_ids": []string{base}}
		case root + "/repository/files/" + binding.YAMLPath + "/raw":
			if r.URL.Query().Get("ref") != head {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write(content)
			return
		default:
			w.WriteHeader(404)
			return
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	f.detail.URL = server.URL + "/ops/rules/-/merge_requests/7"
	f.list = []map[string]any{{"iid": 7, "state": state, "source_branch": f.detail.SourceBranch, "target_branch": "main",
		"source_project_id": 42, "target_project_id": 42, "sha": head, "web_url": f.detail.URL, "author": map[string]any{"id": 1}}}
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.applier = &GitOpsAlertRuleApplier{gitlab: enableGitOpsHTTP{base: u, client: server.Client(), token: "synthetic", gitlab: true}, bindings: map[string]AlertRuleGitOpsBinding{gitOpsBindingKey("test", "app", "g"): binding}}
	return f
}

func TestGitOpsNativeMRListHydratesDetail(t *testing.T) {
	f := newNativeMRDetailFixture(t, "opened")
	receipt, found, err := f.applier.Lookup(t.Context(), f.intent, f.execution)
	if err != nil || !found || receipt.MR != 7 || receipt.State != "opened" || f.detailGETs.Load() != 1 || f.writes.Load() != 0 {
		t.Fatalf("native thin list receipt=%+v found=%v err=%v detail GETs=%d writes=%d", receipt, found, err, f.detailGETs.Load(), f.writes.Load())
	}
}

func TestGitOpsCanonicalDetailFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*nativeMRDetailFixture)
	}{
		{"wrong_iid", func(f *nativeMRDetailFixture) { f.detail.IID = 8 }},
		{"wrong_branch", func(f *nativeMRDetailFixture) { f.detail.SourceBranch = "other" }},
		{"wrong_source_project", func(f *nativeMRDetailFixture) { f.detail.SourceProjectID = 43 }},
		{"wrong_target_project", func(f *nativeMRDetailFixture) { f.detail.TargetProjectID = 43 }},
		{"closed", func(f *nativeMRDetailFixture) { f.detail.State = "closed" }},
		{"detail_outage", func(f *nativeMRDetailFixture) { f.detailStatus = 503 }},
		{"detail_gone", func(f *nativeMRDetailFixture) { f.detailStatus = 404 }},
		{"unknown_file_count", func(f *nativeMRDetailFixture) { f.detail.ChangesCount = "unknown" }},
		{"missing_sha", func(f *nativeMRDetailFixture) { f.detail.SHA = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeMRDetailFixture(t, "opened")
			tc.mutate(f)
			if _, found, err := f.applier.Lookup(t.Context(), f.intent, f.execution); err == nil || found {
				t.Fatal("untrusted canonical detail accepted")
			}
			if f.detailGETs.Load() != 1 || f.writes.Load() != 0 {
				t.Fatal("canonical detail was not checked read-only")
			}
		})
	}
}

func TestGitOpsMRListUniquenessAndIdentityRemainRequired(t *testing.T) {
	for _, kind := range []string{"multiple", "next_page", "wrong_list_project"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeMRDetailFixture(t, "opened")
			switch kind {
			case "multiple":
				f.list = append(f.list, f.list[0])
			case "next_page":
				f.nextPage = "2"
			case "wrong_list_project":
				f.list[0]["target_project_id"] = 43
			}
			if _, found, err := f.applier.Lookup(t.Context(), f.intent, f.execution); err == nil || found {
				t.Fatal("ambiguous or untrusted list accepted")
			}
			if f.detailGETs.Load() != 0 || f.writes.Load() != 0 {
				t.Fatal("invalid list reached detail or writes")
			}
		})
	}
}

func TestGitOpsNativeThinListMergedRecovery(t *testing.T) {
	f := newNativeMRDetailFixture(t, "merged")
	receipt, found, err := f.applier.LookupForReconciliation(t.Context(), f.intent, f.execution)
	if err != nil || !found || receipt.State != "merged" || f.detailGETs.Load() != 1 || f.writes.Load() != 0 {
		t.Fatalf("merged receipt=%+v found=%v err=%v", receipt, found, err)
	}
	if _, found, err := f.applier.Lookup(t.Context(), f.intent, f.execution); err == nil || found {
		t.Fatal("normal Prepare lookup accepted merged MR")
	}
}
