package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3proposals"
)

func TestFileChange(t *testing.T) {
	ops := func(lines []v3proposals.ProposalAlertRuleChangeLine) string {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(l.Op + l.Text + "|")
		}
		return b.String()
	}
	before := []byte("a\nb\nc\nd\n")
	if got := ops(fileChange(before, []byte("a\nb\nx\nc\nd\n"))); got != " a| b|+x| c| d|" {
		t.Fatalf("insert: %s", got)
	}
	if got := ops(fileChange(before, []byte("a\nd\n"))); got != " a|-b|-c| d|" {
		t.Fatalf("remove: %s", got)
	}
	if got := ops(fileChange(before, []byte("a\nB\nc\nd\ne\n"))); got != " a|-b|+B| c| d|+e|" {
		t.Fatalf("replace: %s", got)
	}
	if got := ops(fileChange(nil, []byte("a\n"))); got != "+a|" {
		t.Fatalf("new: %s", got)
	}
}

// D11 §11.4: a request through the real file_pull applier records the
// whole-file difference, and the proposal view shows the rule, the target
// and that difference.
func TestProposalAlertRuleView(t *testing.T) {
	f := newEnableIDFixture(t)
	const base = "# Managed by Redstring.\ngroups:\n  - name: g\n    rules:\n      - alert: Existing\n        expr: up == 0\n        labels:\n          severity: P3\n"
	repo := newFakeRulesRepo(t, base)
	endpoint, _ := url.Parse(repo.server.URL)
	web, _ := url.Parse("http://gitlab.lab.invalid")
	binding := AlertRuleGitOpsBinding{EnvironmentID: "test", ApplicationID: "allowed", Group: "g", ProviderFile: testRuleFile,
		ProjectID: 6, TargetBranch: "main", YAMLPath: filePullPath, Load: LoadFilePull, ReadbackTimeoutSeconds: 300,
		ProjectPath: "aiops-lab/prometheus-rules", ConnectorID: "gitlab-test"}
	f.writer.EnableApplier = &GitOpsAlertRuleApplier{web: web,
		gitlab: enableGitOpsHTTP{base: endpoint, client: repo.server.Client(), gitlab: true,
			tokenSource: func(context.Context) (string, error) { return "write-token", nil }},
		bindings: map[string]AlertRuleGitOpsBinding{gitOpsBindingKey("test", "allowed", "g"): binding}}
	w := f.requestWithKey(t, f.prefixed, "NewRule", "view-"+store.ID())
	var body map[string]any
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("request: %d %s", w.Code, w.Body.String())
	}
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	view, err := f.writer.ProposalAlertRule(t.Context(), tx, body["proposal_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if view.Operation != "enable" || view.Rule.Name != "NewRule" || view.Rule.Severity != "P2" || view.Rule.Labels["environment"] != "test" ||
		view.Repository.ProjectID != "6" || *view.Repository.ProjectPath != "aiops-lab/prometheus-rules" ||
		*view.Repository.WebURL != "http://gitlab.lab.invalid/aiops-lab/prometheus-rules" || *view.Repository.ConnectorID != "gitlab-test" ||
		view.Repository.BaseSHA != repo.branches["main"] || view.Repository.Path != filePullPath || view.Repository.TargetBranch != "main" ||
		view.Prometheus.File != testRuleFile || view.Prometheus.ReadbackTimeoutSeconds != 300 ||
		view.Merge.Method != "fast_forward" || view.Merge.By != "executor" || view.Change == nil {
		t.Fatalf("view: %+v %+v", view, view.Repository)
	}
	// The existing two-space file changes only where the rule is added.
	added, kept := 0, 0
	for _, line := range view.Change.Lines {
		switch line.Op {
		case "+":
			added++
		case " ":
			kept++
		default:
			t.Fatalf("removed line in an enable: %+v", line)
		}
	}
	if kept != 8 || added < 5 || view.Change.Lines[0].Text != "# Managed by Redstring." ||
		!strings.Contains(view.Change.Lines[8].Text, "- alert: NewRule") {
		t.Fatalf("change: kept=%d added=%d %+v", kept, added, view.Change.Lines)
	}
	intent, err := loadAlertRuleEnableIntentWithTx(t.Context(), tx, body["proposal_id"].(string), false)
	if err != nil || view.Change.ContentSHA256 != intent.Deployment.ContentDigest {
		t.Fatalf("digest: %v", err)
	}
}

// D11 §11.5: the execution view follows the ledger.
func TestExecutionView(t *testing.T) {
	ctx := t.Context()
	f, _, _ := filePullExecution(t, false, "")
	var actor core.User
	if err := f.db.QueryRow(ctx, `SELECT creator_id FROM v3_proposal_actions WHERE proposal_id=$1`, f.id).Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, handled := f.s.executionView(ctx, []string{"test"}, actor, f.execution); handled {
		t.Fatal("an execution that has not started was handled")
	}
	if result, err := f.s.ExecuteApproved(ctx, f.id); err != nil || result.Status != "merged" {
		t.Fatalf("execute: %+v %v", result, err)
	}
	if _, err := f.s.ObserveEnabled(ctx, f.id); err != nil {
		t.Fatal(err)
	}
	status, body, handled := f.s.executionView(ctx, []string{"test"}, actor, f.execution)
	if !handled || status != http.StatusOK {
		t.Fatalf("view: %d %v", status, handled)
	}
	raw, _ := json.Marshal(body)
	var view struct {
		Kind      string `json:"kind"`
		Approvers []struct {
			Name string `json:"name"`
		} `json:"approvers"`
		AlertRule struct {
			Operation  string                   `json:"operation"`
			State      string                   `json:"state"`
			Steps      []struct{ State string } `json:"steps"`
			Repository struct {
				SourceBranch string  `json:"source_branch"`
				CommitSHA    *string `json:"commit_sha"`
				MRIID        *int64  `json:"mr_iid"`
				MergedSHA    *string `json:"merged_sha"`
				MergedAt     *string `json:"merged_at"`
				ProjectPath  *string `json:"project_path"`
			} `json:"repository"`
			Readback struct {
				State     string  `json:"state"`
				CheckedAt *string `json:"checked_at"`
			} `json:"readback"`
			Failure *struct{ Code string } `json:"failure"`
		} `json:"alert_rule"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	ar := view.AlertRule
	if view.Kind != alertRuleEnableKind || len(view.Approvers) != 2 || ar.Operation != "enable" || ar.State != "loading" ||
		len(ar.Steps) != 5 || ar.Repository.SourceBranch != "aiops-alert-rule-enable/"+f.execution || ar.Repository.CommitSHA == nil ||
		ar.Repository.MRIID == nil || *ar.Repository.MRIID != 1 || ar.Repository.MergedSHA == nil || ar.Repository.MergedAt == nil ||
		ar.Repository.ProjectPath != nil || ar.Readback.State != "pending" || ar.Readback.CheckedAt == nil || ar.Failure != nil {
		t.Fatalf("view: %s", raw)
	}
	// Another environment, or no proposals.view, hides it.
	if status, _, _ := f.s.executionView(ctx, []string{"staging"}, actor, f.execution); status != http.StatusNotFound {
		t.Fatalf("other environment: %d", status)
	}
	f.access.denied = true
	if status, _, _ := f.s.executionView(ctx, []string{"test"}, actor, f.execution); status != http.StatusNotFound {
		t.Fatalf("without permission: %d", status)
	}
	for state, failure := range map[string]string{"exporting": "", "pending_review": "", "awaiting_provider": "load_timeout", "stale_remote": "stale_base"} {
		want := map[string]string{"exporting": "committing", "pending_review": "merging", "awaiting_provider": "needs_attention", "stale_remote": "needs_attention"}[state]
		if got := executionViewState(state, failure, AlertRuleDeploymentReceipt{}); got != want {
			t.Errorf("%s: %s", state, got)
		}
	}
	if executionViewState("exporting", "", AlertRuleDeploymentReceipt{MR: 3}) != "merging" {
		t.Error("an opened MR is merging")
	}
}

// D11 §11.6: an approved disable marks its rule disabling and is listed as
// pending until it takes effect; the viewer sees only proposals it may view.
func TestRuleLifecycle(t *testing.T) {
	ctx := t.Context()
	f, _, rule := filePullDisable(t)
	ruleID := toDetail("test", f.intent.ApplicationID, rule).ID
	user := core.User{ID: "viewer"}
	lifecycle, pending, err := f.s.RuleLifecycle(ctx, user, []string{"test"}, []string{ruleID, "prom-other"})
	if err != nil {
		t.Fatal(err)
	}
	life, ok := lifecycle[ruleID]
	if !ok || life.State != "disabling" || *life.ProposalID != f.id || life.ExecutionID != nil || len(lifecycle) != 1 {
		t.Fatalf("lifecycle: %+v", lifecycle)
	}
	if len(pending) != 1 || pending[0].Operation != "disable" || pending[0].State != "executing" || pending[0].RuleName != "RecoveredRule" ||
		pending[0].Href != "/review/"+f.id || pending[0].Group != "g" {
		t.Fatalf("pending: %+v", pending)
	}
	if result, err := f.s.ExecuteApproved(ctx, f.id); err != nil || result.Status != "merged" {
		t.Fatalf("execute: %+v %v", result, err)
	}
	if lifecycle, pending, err = f.s.RuleLifecycle(ctx, user, []string{"test"}, []string{ruleID}); err != nil ||
		*lifecycle[ruleID].ExecutionID != f.execution || pending[0].Href != "/executions/"+f.execution {
		t.Fatalf("running: %+v %+v %v", lifecycle, pending, err)
	}
	f.s.Reader.Source = fakeSource{rules: map[string][]ObservedRule{"test": {existingObserved}}}
	if result, err := f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "disabled" {
		t.Fatalf("observe: %+v %v", result, err)
	}
	if lifecycle, pending, err = f.s.RuleLifecycle(ctx, user, []string{"test"}, []string{ruleID}); err != nil || len(lifecycle) != 0 || len(pending) != 0 {
		t.Fatalf("in effect: %+v %+v %v", lifecycle, pending, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE v3_alert_rule_enable_executions SET state='stale_remote',failure_code='stale_base' WHERE proposal_id=$1`, f.id); err != nil {
		t.Fatal(err)
	}
	if lifecycle, pending, err = f.s.RuleLifecycle(ctx, user, []string{"test"}, []string{ruleID}); err != nil ||
		lifecycle[ruleID].State != "needs_attention" || pending[0].State != "needs_attention" {
		t.Fatalf("needs attention: %+v %+v %v", lifecycle, pending, err)
	}
	f.access.denied = true
	if lifecycle, pending, err = f.s.RuleLifecycle(ctx, user, []string{"test"}, []string{ruleID}); err != nil || len(lifecycle) != 0 || len(pending) != 0 {
		t.Fatalf("hidden: %+v %+v %v", lifecycle, pending, err)
	}
}

// The main list carries the lifecycle and pending changes, and says when
// they are unknown.
func TestMainListLifecycle(t *testing.T) {
	rule := ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "Active", Expression: "up", State: "inactive",
		Labels: map[string]string{"environment": "test", "application": "app"}}
	s := mustService(t, fakeCatalog{{ID: "app", EnvironmentID: "test"}}, fakeAccess{}, fakeSource{rules: map[string][]ObservedRule{"test": {rule}}},
		RuleGroupBinding{EnvironmentID: "test", ApplicationID: "app", File: testRuleFile, Group: "g"})
	id := toDetail("test", "app", rule).ID
	proposal := "p1"
	s.Lifecycle = func(_ context.Context, _ core.User, envs []string, ids []string) (map[string]RuleLifecycle, []PendingRuleChange, error) {
		if len(envs) != 1 || envs[0] != "test" || len(ids) != 1 || ids[0] != id {
			t.Errorf("lifecycle call: %v %v", envs, ids)
		}
		return map[string]RuleLifecycle{id: {State: "disable_pending_approval", ProposalID: &proposal}},
			[]PendingRuleChange{{ProposalID: "p2", Operation: "enable", State: "pending_approval", Href: "/review/p2"}}, nil
	}
	w := call(t, scopedHandler(t, http.HandlerFunc(s.MainList)), "GET", "/api/v1/alert-rules", "test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"lifecycle":{"state":"disable_pending_approval","proposal_id":"p1","execution_id":null}`) ||
		!strings.Contains(w.Body.String(), `"pending":[{"proposal_id":"p2"`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	s.Lifecycle = func(context.Context, core.User, []string, []string) (map[string]RuleLifecycle, []PendingRuleChange, error) {
		return nil, nil, errors.New("down")
	}
	w = call(t, scopedHandler(t, http.HandlerFunc(s.MainList)), "GET", "/api/v1/alert-rules", "test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"pending":null`) || !strings.Contains(w.Body.String(), `"items[].lifecycle":"unavailable"`) ||
		strings.Contains(w.Body.String(), `"lifecycle":{`) {
		t.Fatalf("unavailable: %d %s", w.Code, w.Body.String())
	}
}
