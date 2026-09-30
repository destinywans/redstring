package v3alertrules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitOpsEnableRecoveryMergedLookupThenObserveIsReadOnly(t *testing.T) {
	// Both GitLab and Argo are this in-process TLS server, with fake tokens.
	// Reject and count every non-GET, including branch, MR and sync writes.
	var writes, gets atomic.Int32
	var synced, wrongContent atomic.Bool
	content := []byte("groups:\n- name: g\n  rules:\n  - alert: RecoveredRule\n    expr: up == 0\n")
	base, source, execution := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 32)
	binding := AlertRuleGitOpsBinding{EnvironmentID: "test", ApplicationID: "app", Group: "g", ProviderFile: testRuleFile,
		ProjectID: 42, TargetBranch: "main", YAMLPath: "rules/alerts.yml", RepoURL: "https://gitlab.example.invalid/ops/rules.git",
		ArgoApplication: "prometheus", ArgoProject: "ops", ArgoNamespace: "argocd", ArgoSourcePath: "rules",
		DestinationServer: "https://kubernetes.example.invalid", DestinationNamespace: "monitoring"}
	intent := AlertRuleEnableIntent{EnvironmentID: "test", ApplicationID: "app", ProviderFileIdentityDigest: fileIdentityDigest(testRuleFile),
		Spec: DraftRuleSpec{ApplicationID: "app", EnvironmentID: "test", Group: "g", Name: "RecoveredRule", Expr: "up == 0", For: "1m", Mode: "draft",
			Labels: map[string]string{"application": "app", "environment": "test", "severity": "P2"}},
		Deployment: AlertRuleDeploymentPlan{ProjectID: 42, TargetBranch: "main", Path: "rules/alerts.yml", BaseRevision: base,
			ContentDigest: hashGitOpsBytes(content), ScopeFingerprint: deploymentScope(binding)}}
	mergedAt := time.Now().UTC().Add(-time.Minute)
	mr := enableGitMRWire{IID: 7, State: "merged", SourceBranch: "aiops-alert-rule-enable/" + execution, TargetBranch: "main",
		SourceProjectID: 42, TargetProjectID: 42, SHA: source, MergeCommitSHA: source, ChangesCount: "1", MergedAt: mergedAt}
	mr.Author.ID, mr.MergeUser.ID = 1, 2
	mr.DiffRefs.StartSHA, mr.DiffRefs.HeadSHA = base, source
	protection := map[string]any{"name": "main", "allow_force_push": false, "push_access_levels": []any{map[string]any{"access_level": 0}}, "merge_access_levels": []any{map[string]any{"access_level": 40}}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		gets.Add(1)
		var response any
		root := "/api/v4/projects/42"
		switch r.URL.Path {
		case root + "/merge_requests":
			if r.URL.Query().Get("source_branch") != mr.SourceBranch || r.URL.Query().Get("state") != "all" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			response = []any{mr}
		case root + "/merge_requests/7":
			response = mr
		case root + "/merge_requests/7/diffs":
			response = []any{map[string]any{"old_path": binding.YAMLPath, "new_path": binding.YAMLPath}}
		case root + "/repository/commits/" + source:
			response = map[string]any{"id": source, "parent_ids": []string{base}}
		case root + "/repository/files/" + binding.YAMLPath + "/raw":
			if r.URL.Query().Get("ref") != source {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if wrongContent.Load() {
				_, _ = w.Write([]byte("different bytes"))
			} else {
				_, _ = w.Write(content)
			}
			return
		case root + "/merge_requests/7/versions":
			response = []any{map[string]any{"id": 1, "head_commit_sha": source, "start_commit_sha": base, "created_at": mergedAt.Add(-2 * time.Minute), "state": "collected"}}
		case root + "/merge_requests/7/approvals":
			response = map[string]any{"approved": true, "approved_by": []any{map[string]any{"approved_at": mergedAt.Add(-time.Minute), "user": map[string]any{"id": 2, "state": "active"}}}}
		case "/api/v4/users/2":
			response = map[string]any{"id": 2, "state": "active", "bot": false}
		case root + "/repository/branches/main":
			response = map[string]any{"name": "main", "protected": true, "commit": map[string]any{"id": source}}
		case root + "/protected_branches/main":
			response = protection
		case root + "/protected_branches":
			w.Header().Set("X-Page", "1")
			w.Header().Set("X-Per-Page", "100")
			response = []any{protection}
		case "/api/v1/applications/prometheus":
			status, phase, health := "OutOfSync", "Running", "Progressing"
			if synced.Load() {
				status, phase, health = "Synced", "Succeeded", "Healthy"
			}
			response = map[string]any{"metadata": map[string]any{"name": binding.ArgoApplication, "namespace": binding.ArgoNamespace, "uid": "test-argo"},
				"spec": map[string]any{"project": binding.ArgoProject, "source": map[string]any{"repoURL": binding.RepoURL, "path": binding.ArgoSourcePath, "targetRevision": binding.TargetBranch},
					"destination": map[string]any{"server": binding.DestinationServer, "namespace": binding.DestinationNamespace}},
				"status": map[string]any{"sync": map[string]any{"status": status, "revision": source}, "health": map[string]any{"status": health},
					"operationState": map[string]any{"phase": phase, "syncResult": map[string]any{"revision": source}}}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	mr.URL = server.URL + "/ops/rules/-/merge_requests/7"
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	applier := &GitOpsAlertRuleApplier{
		gitlab:   enableGitOpsHTTP{base: endpoint, client: server.Client(), token: "fake-gitlab", gitlab: true},
		argo:     enableGitOpsHTTP{base: endpoint, client: server.Client(), token: "fake-argo"},
		bindings: map[string]AlertRuleGitOpsBinding{gitOpsBindingKey("test", "app", "g"): binding},
	}
	receipt, found, err := applier.LookupForReconciliation(t.Context(), intent, execution)
	if err != nil || !found || receipt.State != "merged" || !validRecoveryReceipt(intent.Deployment, execution, receipt) {
		t.Fatalf("merged recovery receipt=%+v found=%v err=%v", receipt, found, err)
	}
	if _, found, err := applier.Lookup(t.Context(), intent, execution); err == nil || found {
		t.Fatal("Prepare's normal lookup was loosened to accept merged MRs")
	}
	// ObserveEnabled passes an opened copy for the adapter's historical
	// creation-receipt contract; Observe still reads the actual merged MR.
	creationReceipt := receipt
	creationReceipt.State = "opened"
	observation, err := applier.Observe(t.Context(), intent, creationReceipt)
	if err != nil || observation.State != "waiting_sync" {
		t.Fatalf("unsynced merge observation=%+v err=%v", observation, err)
	}
	synced.Store(true)
	observation, err = applier.Observe(t.Context(), intent, creationReceipt)
	if err != nil || observation.State != "synced" || observation.MergeRevision != source || observation.SyncedRevision != source || observation.ContentDigest != intent.Deployment.ContentDigest {
		t.Fatalf("exact merged sync observation=%+v err=%v", observation, err)
	}
	wrongContent.Store(true)
	if _, found, err := applier.LookupForReconciliation(t.Context(), intent, execution); err == nil || found {
		t.Fatal("recovery accepted changed approved bytes")
	}
	if writes.Load() != 0 || gets.Load() == 0 {
		t.Fatalf("GET=%d writes=%d", gets.Load(), writes.Load())
	}
}
