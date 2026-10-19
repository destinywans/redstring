package v3alertrules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
)

var existingObserved = ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "ExistingRule", Expression: `up{job="x"} == 0`,
	Duration: 120, Labels: map[string]string{"severity": "P3"}, Health: "ok"}

// filePullDisable seeds an approved disable of RecoveredRule, which the rule
// file and Prometheus both have, planned by a real file_pull applier (D11
// decision 7).
func filePullDisable(t *testing.T) (recoveryFixture, *fakeRulesRepo, ObservedRule) {
	t.Helper()
	db := enableRecoveryDB(t)
	repo := newFakeRulesRepo(t, filePullGroupBase)
	var applier *GitOpsAlertRuleApplier
	var rule ObservedRule
	f := seedEnableRecoveryPlan(t, db, false, false, "", func(in *AlertRuleEnableIntent) {
		app := in.ApplicationID
		repo.advance("main", filePullGroupBase+fmt.Sprintf("  - alert: RecoveredRule\n    expr: up == 0\n    for: 1m\n    labels:\n      application: %s\n"+
			"      environment: test\n      severity: P2\n    annotations:\n      summary: test\n", app))
		rule = ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "RecoveredRule", Expression: "up == 0", Duration: 60,
			Labels: in.Spec.Labels, Annotations: in.Spec.Annotations, Health: "ok", LastEvaluation: time.Now()}
		reader := mustService(t, fakeCatalog{{ID: app, EnvironmentID: "test"}}, fakeAccess{},
			fakeSource{rules: map[string][]ObservedRule{"test": {existingObserved, rule}}},
			RuleGroupBinding{EnvironmentID: "test", ApplicationID: app, File: testRuleFile, Group: "g"})
		spec := disableSpec("test", app, rule)
		groups, observed, err := NewWriteService(db, &recoveryAccess{}, reader, nil).inspectCandidates(t.Context(), "test", app, []DraftRuleSpec{spec})
		if err != nil || !observed[0].exists {
			t.Fatalf("observed: %+v %v", observed, err)
		}
		hash, _, err := specDigest(spec)
		if err != nil {
			t.Fatal(err)
		}
		in.Kind, in.SchemaVersion, in.Spec, in.SpecDigest = alertRuleDisableKind, alertRuleEnableIntentDirect, spec, hash
		in.ObservedRuleID, in.ObservedDigest, in.ProviderFileIdentityDigest = observed[0].id, observed[0].digest, fileIdentityDigest(groups[0].file)
		in.DraftOperationID, in.DraftID, in.DraftVersion, in.ManifestDigest = "", "", 0, ""
		in.ProjectID, in.MR, in.MRURL, in.Branch, in.Path = 0, 0, "", "", ""
		endpoint, _ := url.Parse(repo.server.URL)
		web, _ := url.Parse("http://gitlab.lab.invalid")
		binding := AlertRuleGitOpsBinding{EnvironmentID: "test", ApplicationID: app, Group: "g", ProviderFile: testRuleFile,
			ProjectID: 6, TargetBranch: "main", YAMLPath: filePullPath, Load: LoadFilePull, ReadbackTimeoutSeconds: 300}
		applier = &GitOpsAlertRuleApplier{web: web,
			gitlab: enableGitOpsHTTP{base: endpoint, client: repo.server.Client(), gitlab: true,
				tokenSource: func(context.Context) (string, error) { return "write-token", nil }},
			bindings: map[string]AlertRuleGitOpsBinding{gitOpsBindingKey("test", app, "g"): binding}}
		plan, err := applier.Plan(t.Context(), *in)
		if err != nil {
			t.Fatal(err)
		}
		in.Deployment = plan
	})
	f.s.EnableApplier = applier
	f.s.Reader.Source = fakeSource{rules: map[string][]ObservedRule{"test": {existingObserved, rule}}}
	return f, repo, rule
}

func TestDisableExecutionRemovesTheRuleAndReadsItBack(t *testing.T) {
	ctx := t.Context()
	f, repo, rule := filePullDisable(t)
	result, err := f.s.ExecuteApproved(ctx, f.id)
	if err != nil || result.Status != "merged" {
		t.Fatalf("execute: %+v %v", result, err)
	}
	head := repo.branches["main"]
	if strings.Contains(repo.files[head][filePullPath], "RecoveredRule") || !strings.Contains(repo.files[head][filePullPath], "ExistingRule") ||
		repo.mrs[0]["iid"] != int64(1) {
		t.Fatalf("merged file: %s", repo.files[head][filePullPath])
	}
	// Prometheus still evaluates it: not disabled yet.
	if result, err = f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "awaiting_provider" {
		t.Fatalf("still loaded: %+v %v", result, err)
	}
	if row := readFilePullRow(t, f); row.Readback.State != "pending" {
		t.Fatalf("pending readback: %+v", row)
	}
	f.s.Reader.Source = fakeSource{rules: map[string][]ObservedRule{"test": {existingObserved}}}
	if result, err = f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "disabled" {
		t.Fatalf("gone: %+v %v", result, err)
	}
	row := readFilePullRow(t, f)
	if row.Readback.State != "absent" || row.Failure != "" || strings.Join(row.Steps, ",") != "reserved,committing,merging,merged,loading,disabled" {
		t.Fatalf("disabled row: %+v", row)
	}
	var actor core.User
	if err := f.db.QueryRow(ctx, `SELECT creator_id FROM v3_proposal_actions WHERE proposal_id=$1`, f.id).Scan(&actor.ID); err != nil {
		t.Fatal(err)
	}
	// A recheck keeps it disabled while the rule stays gone, and reports a
	// rule that comes back.
	if got, err := f.s.recheckExecution(ctx, "test", actor, f.execution); err != nil || got.Status != "disabled" {
		t.Fatalf("recheck disabled: %+v %v", got, err)
	}
	f.s.Reader.Source = fakeSource{rules: map[string][]ObservedRule{"test": {existingObserved, rule}}}
	if _, err := f.s.recheckExecution(ctx, "test", actor, f.execution); err == nil {
		t.Fatal("a rule that came back stayed disabled")
	}
}

// The request takes the rule as Prometheus reports it and proposes its
// removal; it needs an executor-merged binding and replays by key.
func TestDisableRequest(t *testing.T) {
	f := newEnableIDFixture(t)
	rule := ObservedRule{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "ActiveRule", Expression: "up == 0", Duration: 7200,
		Labels: map[string]string{"application": "allowed", "environment": "test", "severity": "info"}, State: "inactive"}
	f.writer.Reader.Source = disableSource{enableIDSource: f.source, rules: []ObservedRule{rule}}
	ruleID := toDetail("test", "allowed", rule).ID
	request := func(id, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/alert-rules/"+id+"/disable", nil)
		w := httptest.NewRecorder()
		f.writer.handleDisable(w, r, "test", f.actor, key, id, disableRequest{InvestigationID: f.prefixed})
		return w
	}
	// An Argo CD binding cannot be disabled through the product.
	if w := request(ruleID, "disable-"+store.ID()); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "disable_unavailable") {
		t.Fatalf("argo binding: %d %s", w.Code, w.Body.String())
	}
	f.applier.intent.Deployment.Load, f.applier.intent.Deployment.Merge, f.applier.intent.Deployment.ReadbackTimeoutSeconds = LoadFilePull, MergeByExecutor, 300
	key := "disable-" + store.ID()
	w := request(ruleID, key)
	var body map[string]any
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["status"] != "pending_approval" || body["proposal_id"] == nil {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if _, ok := body["draft_id"]; ok {
		t.Fatalf("a disable reports draft fields: %s", w.Body.String())
	}
	var kind, review string
	var action json.RawMessage
	if err := f.db.QueryRow(t.Context(), `SELECT a.action_kind,a.action,p.content::text FROM v3_proposal_actions a JOIN v3_proposals p ON p.id=a.proposal_id
 WHERE a.proposal_id=$1`, body["proposal_id"]).Scan(&kind, &action, &review); err != nil {
		t.Fatal(err)
	}
	var intent AlertRuleEnableIntent
	if err := json.Unmarshal(action, &intent); err != nil || kind != alertRuleDisableKind || intent.Kind != kind || intent.SchemaVersion != 2 ||
		intent.ObservedRuleID != ruleID || intent.Spec.Name != "ActiveRule" || intent.Spec.For != "2h0m0s" ||
		!strings.Contains(review, "Disable alert rule ActiveRule") || !strings.Contains(review, "Scope labels: environment=test application=allowed") {
		t.Fatalf("stored disable: %s %+v %v", kind, intent, review)
	}
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAlertRuleEnableIntentWithTx(t.Context(), tx, body["proposal_id"].(string))
	_ = tx.Rollback(t.Context())
	if err != nil || loaded.Kind != alertRuleDisableKind {
		t.Fatalf("load: %+v %v", loaded, err)
	}
	if again := request(ruleID, key); again.Code != http.StatusAccepted || !strings.Contains(again.Body.String(), body["proposal_id"].(string)) {
		t.Fatalf("replay: %d %s", again.Code, again.Body.String())
	}
	// The same key cannot enable, and an unknown rule is 404.
	if w := f.requestWithKey(t, f.prefixed, "OtherRule", key); w.Code != http.StatusConflict {
		t.Fatalf("enable with a disable key: %d %s", w.Code, w.Body.String())
	}
	if w := request("prom-"+strings.Repeat("0", 24), "disable-"+store.ID()); w.Code != http.StatusNotFound {
		t.Fatalf("unknown rule: %d %s", w.Code, w.Body.String())
	}
}

type disableSource struct {
	*enableIDSource
	rules []ObservedRule
}

func (s disableSource) List(context.Context, string) ([]ObservedRule, error) { return s.rules, nil }
