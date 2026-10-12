package v3alertrules

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRulesRepo is an in-memory GitLab project 6 with real branch, commit,
// file, MR and fast-forward merge semantics for the file_pull executor (D11).
type fakeRulesRepo struct {
	mu        sync.Mutex
	branches  map[string]string            // branch -> commit
	parents   map[string]string            // commit -> parent
	files     map[string]map[string]string // commit -> path -> content
	mrs       []map[string]any
	protected bool
	failMerge int // HTTP status to answer PUT merge with (merging anyway when 502)
	// unpreparedReads is how many reads of a new MR see it unprepared, as
	// GitLab prepares the diff asynchronously: changes_count and diff_refs
	// null, no prepared_at and no diffs yet.
	unpreparedReads int
	mrReads         int
	mergeSHA        string
	writes          int
	requests        int
	server          *httptest.Server
	seq             int
}

const filePullPath = "rules/test/app.yml"

func newFakeRulesRepo(t *testing.T, content string) *fakeRulesRepo {
	t.Helper()
	f := &fakeRulesRepo{branches: map[string]string{}, parents: map[string]string{}, files: map[string]map[string]string{}, protected: true}
	base := f.commit("", map[string]string{filePullPath: content})
	f.branches["main"] = base
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeRulesRepo) commit(parent string, files map[string]string) string {
	f.seq++
	sum := sha1.Sum([]byte(fmt.Sprintf("%s-%d", parent, f.seq)))
	id := hex.EncodeToString(sum[:])
	f.parents[id], f.files[id] = parent, files
	return id
}

func (f *fakeRulesRepo) advance(branch, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head := f.branches[branch]
	files := map[string]string{}
	for k, v := range f.files[head] {
		files[k] = v
	}
	files[filePullPath] = content
	f.branches[branch] = f.commit(head, files)
}

func (f *fakeRulesRepo) mr(iid int64) map[string]any {
	for _, m := range f.mrs {
		if m["iid"].(int64) == iid {
			return m
		}
	}
	return nil
}

func (f *fakeRulesRepo) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if r.Header.Get("PRIVATE-TOKEN") != "write-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	root := "/api/v4/projects/6"
	p := r.URL.Path
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if r.Method != http.MethodGet {
		f.writes++
	}
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(p, root+"/repository/branches/"):
		name := strings.TrimPrefix(p, root+"/repository/branches/")
		head, ok := f.branches[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply(200, map[string]any{"name": name, "protected": name == "main" && f.protected, "commit": map[string]any{"id": head}})
	case r.Method == http.MethodPost && p == root+"/repository/branches":
		name, ref := r.URL.Query().Get("branch"), r.URL.Query().Get("ref")
		f.branches[name] = ref
		reply(201, map[string]any{"name": name, "commit": map[string]any{"id": ref}})
	case p == root+"/protected_branches/main" || p == root+"/protected_branches":
		rule := map[string]any{"name": "main", "allow_force_push": false,
			"push_access_levels": []any{map[string]any{"access_level": 0}}, "merge_access_levels": []any{map[string]any{"access_level": 40}}}
		if p == root+"/protected_branches" {
			w.Header().Set("X-Page", "1")
			w.Header().Set("X-Per-Page", "100")
			reply(200, []any{rule})
			return
		}
		reply(200, rule)
	case r.Method == http.MethodGet && p == root+"/repository/files/"+filePullPath+"/raw":
		ref := r.URL.Query().Get("ref")
		if head, ok := f.branches[ref]; ok {
			ref = head
		}
		content, ok := f.files[ref][filePullPath]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(content))
	case r.Method == http.MethodGet && p == root+"/repository/files/"+filePullPath:
		reply(200, map[string]any{"last_commit_id": f.branches[r.URL.Query().Get("ref")]})
	case r.Method == http.MethodPut && p == root+"/repository/files/"+filePullPath:
		branch := body["branch"].(string)
		head := f.branches[branch]
		if body["last_commit_id"] != head {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		files := map[string]string{}
		for k, v := range f.files[head] {
			files[k] = v
		}
		files[filePullPath] = body["content"].(string)
		f.branches[branch] = f.commit(head, files)
		reply(200, map[string]any{"file_path": filePullPath, "branch": branch})
	case r.Method == http.MethodGet && strings.HasPrefix(p, root+"/repository/commits/"):
		id := strings.TrimPrefix(p, root+"/repository/commits/")
		parent, ok := f.parents[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reply(200, map[string]any{"id": id, "parent_ids": []string{parent}})
	case r.Method == http.MethodGet && p == root+"/merge_requests":
		list := []any{}
		for _, m := range f.mrs {
			if m["source_branch"] == r.URL.Query().Get("source_branch") {
				list = append(list, f.view(m))
			}
		}
		reply(200, list)
	case r.Method == http.MethodPost && p == root+"/merge_requests":
		iid := int64(len(f.mrs) + 1)
		m := map[string]any{"iid": iid, "state": "opened", "source_branch": body["source_branch"], "target_branch": body["target_branch"],
			"unprepared": f.unpreparedReads}
		f.mrs = append(f.mrs, m)
		reply(201, f.view(m))
	case strings.HasPrefix(p, root+"/merge_requests/"):
		rest := strings.Split(strings.TrimPrefix(p, root+"/merge_requests/"), "/")
		iid, _ := strconv.ParseInt(rest[0], 10, 64)
		m := f.mr(iid)
		if m == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case len(rest) == 1:
			f.mrReads++
			reply(200, f.view(m))
			if n, _ := m["unprepared"].(int); n > 0 {
				m["unprepared"] = n - 1
			}
		case rest[1] == "diffs":
			if n, _ := m["unprepared"].(int); n > 0 {
				reply(200, []any{})
				return
			}
			reply(200, []any{map[string]any{"old_path": filePullPath, "new_path": filePullPath}})
		case rest[1] == "merge" && r.Method == http.MethodPut:
			head := f.branches[m["source_branch"].(string)]
			f.mergeSHA, _ = body["sha"].(string)
			if f.failMerge != 0 && f.failMerge != http.StatusBadGateway {
				w.WriteHeader(f.failMerge)
				return
			}
			if body["sha"] != head || f.parents[head] != f.branches["main"] {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			f.branches["main"] = head
			m["state"], m["merged_at"] = "merged", time.Now().UTC()
			if f.failMerge == http.StatusBadGateway {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			reply(200, f.view(m))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeRulesRepo) view(m map[string]any) map[string]any {
	head := f.branches[m["source_branch"].(string)]
	out := map[string]any{"iid": m["iid"], "state": m["state"], "source_branch": m["source_branch"], "target_branch": m["target_branch"],
		"web_url":           "http://gitlab.lab.invalid/aiops-lab/prometheus-rules/-/merge_requests/" + fmt.Sprint(m["iid"]),
		"source_project_id": 6, "target_project_id": 6, "sha": head, "changes_count": "1",
		"author": map[string]any{"id": 9}, "merge_user": map[string]any{"id": 9},
		"diff_refs": map[string]any{"start_sha": f.parents[head], "head_sha": head}}
	if at, ok := m["merged_at"]; ok {
		out["merged_at"] = at
	}
	if n, _ := m["unprepared"].(int); n > 0 {
		out["changes_count"], out["diff_refs"] = nil, nil
	} else {
		out["prepared_at"] = time.Now().UTC()
	}
	return out
}

const filePullBase = `groups:
- name: redstring-test-app
  rules:
  - alert: ExistingRule
    expr: up == 0
    for: 1m
    labels:
      application: app
      environment: test
      severity: P2
`

func filePullIntent(t *testing.T, a *GitOpsAlertRuleApplier, name string) AlertRuleEnableIntent {
	t.Helper()
	in := AlertRuleEnableIntent{EnvironmentID: "test", ApplicationID: "app", ProviderFileIdentityDigest: fileIdentityDigest(testRuleFile),
		Spec: DraftRuleSpec{ApplicationID: "app", EnvironmentID: "test", Group: "redstring-test-app", Name: name,
			Expr: "rate(errors_total[5m]) > 1", For: "5m", Mode: "draft",
			Labels:      map[string]string{"application": "app", "environment": "test", "severity": "P2"},
			Annotations: map[string]string{"summary": "Errors"}}}
	plan, err := a.Plan(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Deployment = plan
	return in
}

func newFilePullApplier(t *testing.T, repo *fakeRulesRepo) *GitOpsAlertRuleApplier {
	t.Helper()
	endpoint, _ := url.Parse(repo.server.URL)
	web, _ := url.Parse("http://gitlab.lab.invalid")
	binding := AlertRuleGitOpsBinding{EnvironmentID: "test", ApplicationID: "app", Group: "redstring-test-app", ProviderFile: testRuleFile,
		ProjectID: 6, TargetBranch: "main", YAMLPath: filePullPath, Load: LoadFilePull, ReadbackTimeoutSeconds: 300}
	return &GitOpsAlertRuleApplier{web: web,
		gitlab: enableGitOpsHTTP{base: endpoint, client: repo.server.Client(), gitlab: true,
			tokenSource: func(context.Context) (string, error) { return "write-token", nil }},
		bindings: map[string]AlertRuleGitOpsBinding{gitOpsBindingKey("test", "app", "redstring-test-app"): binding}}
}

var filePullApproval = AlertRuleApproval{ID: strings.Repeat("a", 32), ActionDigest: strings.Repeat("b", 64), Digest: strings.Repeat("c", 64)}

// D11: an executor-merged file_pull deployment: plan, MR, fast-forward merge
// guarded by the head SHA, and an observation of the bytes on the target.
func TestFilePullExecutorMergesTheApprovedMR(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRulesRepo(t, filePullBase)
	a := newFilePullApplier(t, repo)
	in := filePullIntent(t, a, "CheckoutErrors")
	if in.Deployment.Load != LoadFilePull || !in.Deployment.ExecutorMerges() || in.Deployment.BaseRevision != repo.branches["main"] ||
		validateDeploymentPlan(in.Deployment) != nil {
		t.Fatalf("plan: %+v", in.Deployment)
	}
	execution := strings.Repeat("1", 32)
	receipt, err := a.Prepare(ctx, in, filePullApproval, execution)
	if err != nil || receipt.State != "opened" || receipt.SourceRevision == "" || !validDeploymentReceipt(in.Deployment, execution, receipt) {
		t.Fatalf("prepare: %+v %v", receipt, err)
	}
	merged, err := a.Merge(ctx, in, receipt, execution)
	if err != nil || merged.State != "merged" || merged.MergeRevision != receipt.SourceRevision || merged.MergedAt == nil ||
		repo.branches["main"] != receipt.SourceRevision || repo.mergeSHA != receipt.SourceRevision ||
		!validDeploymentReceipt(in.Deployment, execution, merged) {
		t.Fatalf("merge: %+v %v main=%s sha=%s", merged, err, repo.branches["main"], repo.mergeSHA)
	}
	// A retry finds the executor's merge instead of writing again.
	writes := repo.writes
	again, found, err := a.Lookup(ctx, in, execution)
	if err != nil || !found || again.State != "merged" || again.MergeRevision != merged.MergeRevision {
		t.Fatalf("lookup after merge: %+v %v %v", again, found, err)
	}
	if again, err = a.Merge(ctx, in, receipt, execution); err != nil || again.MergeRevision != merged.MergeRevision || repo.writes != writes {
		t.Fatalf("merge retry wrote again: %+v %v writes=%d->%d", again, err, writes, repo.writes)
	}
	observation, err := a.Observe(ctx, in, merged)
	if err != nil || observation.State != "synced" || observation.MergeRevision != merged.MergeRevision ||
		len(observation.GroupRules) != 2 || observation.GroupRules[0].Name != "CheckoutErrors" || observation.GroupRules[0].ForSeconds != 300 ||
		observation.GroupRules[1].Name != "ExistingRule" || observation.GroupRules[1].ForSeconds != 60 ||
		observation.GroupRules[1].Labels["severity"] != "P2" {
		t.Fatalf("observe: %+v %v", observation, err)
	}
	// Someone changes the file afterwards: the deployment no longer holds.
	repo.advance("main", filePullBase)
	if _, err := a.Observe(ctx, in, merged); err == nil {
		t.Fatal("drift after the merge was not detected")
	}
}

func TestFilePullMergeRefusalsAndUncertainty(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRulesRepo(t, filePullBase)
	a := newFilePullApplier(t, repo)

	// The target moved after the approval: no merge, stale_base.
	in := filePullIntent(t, a, "StaleRule")
	execution := strings.Repeat("2", 32)
	receipt, err := a.Prepare(ctx, in, filePullApproval, execution)
	if err != nil {
		t.Fatal(err)
	}
	repo.advance("main", filePullBase+"# unrelated change\n")
	moved := repo.branches["main"]
	if _, err := a.Merge(ctx, in, receipt, execution); err == nil {
		t.Fatal("merged onto a moved target")
	} else if code, ok := MergeRefusal(err); !ok || code != "stale_base" || repo.branches["main"] != moved {
		t.Fatalf("stale base: %v", err)
	}

	// GitLab refuses the merge: a definite refusal.
	in = filePullIntent(t, a, "RefusedRule")
	execution = strings.Repeat("3", 32)
	if receipt, err = a.Prepare(ctx, in, filePullApproval, execution); err != nil {
		t.Fatal(err)
	}
	repo.failMerge = http.StatusMethodNotAllowed
	if code, ok := MergeRefusal(func() error { _, err := a.Merge(ctx, in, receipt, execution); return err }()); !ok || code != "merge_refused" {
		t.Fatalf("refused merge: %s %v", code, ok)
	}

	// The merge happened but the response was lost: unknown, and a lookup
	// finds the merge.
	repo.failMerge = http.StatusBadGateway
	_, err = a.Merge(ctx, in, receipt, execution)
	if _, refused := MergeRefusal(err); err == nil || refused {
		t.Fatalf("lost merge response: %v", err)
	}
	found, ok, err := a.Lookup(ctx, in, execution)
	if err != nil || !ok || found.State != "merged" {
		t.Fatalf("lookup after a lost merge response: %+v %v %v", found, ok, err)
	}
	if recovered, ok, err := a.LookupForReconciliation(ctx, in, execution); err != nil || !ok || recovered.MergeRevision != found.MergeRevision {
		t.Fatalf("reconciliation lookup: %+v %v %v", recovered, ok, err)
	}

	// A file_pull binding does not move to another load kind's plan.
	argoPlan := in
	argoPlan.Deployment.Load, argoPlan.Deployment.Merge = "", ""
	if _, err := a.Merge(ctx, argoPlan, receipt, execution); err == nil {
		t.Fatal("merged a plan without executor merge")
	}
}

// The constructor reads the token through a source and trusts the CA kept
// next to it; a file_pull binding has no Argo settings.
func TestFilePullConfiguration(t *testing.T) {
	repo := newFakeRulesRepo(t, filePullBase)
	token := func(context.Context) (string, error) { return "write-token", nil }
	binding := AlertRuleGitOpsBinding{EnvironmentID: "test", ApplicationID: "app", Group: "redstring-test-app", ProviderFile: testRuleFile,
		ProjectID: 6, TargetBranch: "main", YAMLPath: filePullPath, Load: LoadFilePull}
	if _, err := NewGitOpsAlertRuleApplier(AlertRuleGitOpsConfig{GitLabURL: repo.server.URL, GitLabToken: token,
		GitLabCAPEM: "not a certificate", Bindings: []AlertRuleGitOpsBinding{binding}}); err == nil {
		t.Fatal("an invalid CA was accepted")
	}
	withArgo := binding
	withArgo.ArgoApplication = "prometheus"
	if _, err := NewGitOpsAlertRuleApplier(AlertRuleGitOpsConfig{GitLabURL: repo.server.URL, GitLabToken: token,
		Bindings: []AlertRuleGitOpsBinding{withArgo}}); err == nil {
		t.Fatal("a file_pull binding with Argo fields was accepted")
	}
	if _, err := NewGitOpsAlertRuleApplier(AlertRuleGitOpsConfig{GitLabURL: repo.server.URL, GitLabToken: token,
		ArgoURL: "https://argo.example.invalid", Bindings: []AlertRuleGitOpsBinding{binding}}); err == nil {
		t.Fatal("Argo settings without an argo_cd binding were accepted")
	}
	a, err := NewGitOpsAlertRuleApplier(AlertRuleGitOpsConfig{GitLabURL: repo.server.URL, GitLabToken: token,
		GitLabWebURL: "http://gitlab.lab.invalid", Bindings: []AlertRuleGitOpsBinding{binding}})
	if err != nil {
		t.Fatal(err)
	}
	// The private CA is not set here, so the TLS test server is untrusted.
	if _, err := a.Plan(context.Background(), AlertRuleEnableIntent{EnvironmentID: "test", ApplicationID: "app",
		ProviderFileIdentityDigest: fileIdentityDigest(testRuleFile), Spec: DraftRuleSpec{ApplicationID: "app", EnvironmentID: "test",
			Group: "redstring-test-app", Name: "X", Expr: "up == 0", For: "1m", Mode: "draft",
			Labels: map[string]string{"application": "app", "environment": "test", "severity": "P2"}}}); err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
	// The CA kept next to the token makes the private certificate trusted.
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: repo.server.Certificate().Raw}))
	if a, err = NewGitOpsAlertRuleApplier(AlertRuleGitOpsConfig{GitLabURL: repo.server.URL, GitLabToken: token, GitLabCAPEM: ca,
		GitLabWebURL: "http://gitlab.lab.invalid", Bindings: []AlertRuleGitOpsBinding{binding}}); err != nil {
		t.Fatal(err)
	}
	in := filePullIntent(t, a, "Trusted")
	if in.Deployment.BaseRevision != repo.branches["main"] {
		t.Fatalf("plan through the private CA: %+v", in.Deployment)
	}
	// A token that cannot be read sends nothing.
	failing := a.gitlab
	failing.tokenSource = func(context.Context) (string, error) { return "", errors.New("vault sealed") }
	before := repo.requests
	if _, _, _, err := failing.request(context.Background(), http.MethodGet, "/api/v4/projects/6", nil, nil); !errors.Is(err, errGitOpsCredential) ||
		repo.requests != before {
		t.Fatalf("unreadable token: %v requests=%d->%d", err, before, repo.requests)
	}
	failing.tokenSource = func(context.Context) (string, error) { return "two words", nil }
	if _, _, _, err := failing.request(context.Background(), http.MethodGet, "/api/v4/projects/6", nil, nil); !errors.Is(err, errGitOpsCredential) {
		t.Fatalf("malformed token: %v", err)
	}
}

// GitLab prepares a new MR's diff asynchronously. Prepare waits for it
// (bounded) instead of reporting an uncertain result, and still checks the
// exact diff; only a timeout is uncertain, and the same MR is found later.
func TestFilePullPrepareWaitsForTheMRDiff(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRulesRepo(t, filePullBase)
	a := newFilePullApplier(t, repo)
	a.mrPreparePoll = 10 * time.Millisecond
	in := filePullIntent(t, a, "PreparedLater")
	repo.unpreparedReads = 3
	execution := strings.Repeat("4", 32)
	receipt, err := a.Prepare(ctx, in, filePullApproval, execution)
	if err != nil || receipt.State != "opened" || receipt.MR != 1 || repo.mrReads < 4 {
		t.Fatalf("prepare across the diff preparation: %+v %v reads=%d", receipt, err, repo.mrReads)
	}
	// Not prepared within the wait: uncertain, nothing else written, and the
	// same MR is found once GitLab is done.
	in = filePullIntent(t, a, "PreparedTooLate")
	repo.unpreparedReads = 1 << 20
	a.mrPrepareWait = 80 * time.Millisecond
	execution = strings.Repeat("5", 32)
	if _, err := a.Prepare(ctx, in, filePullApproval, execution); err == nil {
		t.Fatal("an unprepared MR was accepted")
	} else if _, refused := MergeRefusal(err); refused {
		t.Fatalf("a slow preparation is a refusal: %v", err)
	}
	repo.mrs[1]["unprepared"] = 0
	writes := repo.writes
	found, ok, err := a.Lookup(ctx, in, execution)
	if err != nil || !ok || found.MR != 2 || repo.writes != writes {
		t.Fatalf("lookup after the preparation: %+v %v %v", found, ok, err)
	}
}
