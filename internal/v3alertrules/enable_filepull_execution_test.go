package v3alertrules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const filePullGroupBase = `groups:
- name: g
  rules:
  - alert: ExistingRule
    expr: up{job="x"} == 0
    for: 2m
    labels:
      severity: P3
`

// filePullExecution seeds an approved version 2 enable (no draft) whose plan
// comes from a real file_pull applier against the fake rules repository (D11).
func filePullExecution(t *testing.T, expired bool, state string) (recoveryFixture, *fakeRulesRepo, *GitOpsAlertRuleApplier) {
	t.Helper()
	db := enableRecoveryDB(t)
	repo := newFakeRulesRepo(t, filePullGroupBase)
	var applier *GitOpsAlertRuleApplier
	f := seedEnableRecoveryPlan(t, db, expired, false, state, func(in *AlertRuleEnableIntent) {
		in.SchemaVersion = alertRuleEnableIntentDirect
		in.DraftOperationID, in.DraftID, in.DraftVersion, in.ManifestDigest = "", "", 0, ""
		in.ProjectID, in.MR, in.MRURL, in.Branch, in.Path = 0, 0, "", "", ""
		endpoint, _ := url.Parse(repo.server.URL)
		web, _ := url.Parse("http://gitlab.lab.invalid")
		binding := AlertRuleGitOpsBinding{EnvironmentID: "test", ApplicationID: in.ApplicationID, Group: "g", ProviderFile: testRuleFile,
			ProjectID: 6, TargetBranch: "main", YAMLPath: filePullPath, Load: LoadFilePull, ReadbackTimeoutSeconds: 300}
		applier = &GitOpsAlertRuleApplier{web: web,
			gitlab: enableGitOpsHTTP{base: endpoint, client: repo.server.Client(), gitlab: true,
				tokenSource: func(context.Context) (string, error) { return "write-token", nil }},
			bindings: map[string]AlertRuleGitOpsBinding{gitOpsBindingKey("test", in.ApplicationID, "g"): binding}}
		plan, err := applier.Plan(t.Context(), *in)
		if err != nil {
			t.Fatal(err)
		}
		in.Deployment = plan
	})
	f.s.EnableApplier = applier
	return f, repo, applier
}

type filePullRow struct {
	State, Failure, Observed string
	Readback                 enableReadback
	Steps                    []string
	Deployment               AlertRuleDeploymentReceipt
}

func readFilePullRow(t *testing.T, f recoveryFixture) filePullRow {
	t.Helper()
	var row filePullRow
	var readback, steps, deployment []byte
	if err := f.db.QueryRow(t.Context(), `SELECT state,failure_code,observed_rule_id,readback,steps,deployment
 FROM v3_alert_rule_enable_executions WHERE proposal_id=$1`, f.id).
		Scan(&row.State, &row.Failure, &row.Observed, &readback, &steps, &deployment); err != nil {
		t.Fatal(err)
	}
	var timeline []struct {
		State string    `json:"state"`
		At    time.Time `json:"at"`
	}
	if json.Unmarshal(readback, &row.Readback) != nil || json.Unmarshal(steps, &timeline) != nil || json.Unmarshal(deployment, &row.Deployment) != nil {
		t.Fatalf("row JSON: %s %s %s", readback, steps, deployment)
	}
	for _, step := range timeline {
		if step.At.IsZero() {
			t.Fatalf("step without time: %s", steps)
		}
		row.Steps = append(row.Steps, step.State)
	}
	return row
}

// loadedGroup is Prometheus after loading the merged file: both rules, the
// new one evaluated with health.
func loadedGroup(f recoveryFixture, health string, evaluated time.Time) fakeSource {
	spec := f.intent.Spec
	return fakeSource{rules: map[string][]ObservedRule{"test": {
		{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: "ExistingRule", Expression: `up{job="x"} == 0`, Duration: 120,
			Labels: map[string]string{"severity": "P3"}, Health: "ok", LastEvaluation: evaluated},
		{GroupIdentity: testRuleFile + "\x00g", Group: "g", Name: spec.Name, Expression: spec.Expr, Duration: 60,
			Labels: spec.Labels, Annotations: spec.Annotations, Health: health, LastEvaluation: evaluated,
			LastError: map[bool]string{true: "", false: "query timed out"}[health == "ok"]},
	}}}
}

func TestFilePullExecutionMergesThenReadsBack(t *testing.T) {
	f, repo, _ := filePullExecution(t, false, "")
	ctx := t.Context()
	base := repo.branches["main"]
	result, err := f.s.ExecuteApproved(ctx, f.id)
	if err != nil || result.Status != "merged" || result.Deployment == nil || result.Deployment.State != "merged" {
		t.Fatalf("execute: %+v %v", result, err)
	}
	if head := repo.branches["main"]; head == base || head != result.Deployment.MergeRevision || repo.parents[head] != base {
		t.Fatalf("main was not fast-forwarded to the approved head: base=%s head=%s receipt=%+v", base, head, result.Deployment)
	}
	// Prometheus has not loaded it yet.
	if result, err = f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "awaiting_provider" || result.ObservedRuleID != "" {
		t.Fatalf("before load: %+v %v", result, err)
	}
	row := readFilePullRow(t, f)
	if row.Readback.State != "pending" || row.Readback.CheckedAt == nil || row.Readback.FirstSeenAt != nil || row.Failure != "" {
		t.Fatalf("pending readback: %+v", row)
	}
	// Loaded, but the rule was not evaluated since the merge.
	f.s.Reader.Source = loadedGroup(f, "ok", result.Deployment.MergedAt.Add(-time.Second))
	if result, err = f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "awaiting_provider" {
		t.Fatalf("evaluated before the merge: %+v %v", result, err)
	}
	if row = readFilePullRow(t, f); row.Readback.FirstSeenAt == nil || row.Readback.State != "pending" {
		t.Fatalf("first seen: %+v", row)
	}
	firstSeen := *row.Readback.FirstSeenAt
	// A different group (someone else's later change) is a mismatch.
	other := loadedGroup(f, "ok", time.Now())
	other.rules["test"][0].Labels = map[string]string{"severity": "P1"}
	f.s.Reader.Source = other
	if result, err = f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "awaiting_provider" {
		t.Fatalf("group mismatch: %+v %v", result, err)
	}
	if row = readFilePullRow(t, f); row.Readback.State != "mismatch" {
		t.Fatalf("mismatch readback: %+v", row)
	}
	f.s.Reader.Source = loadedGroup(f, "ok", time.Now())
	if result, err = f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "enabled" || result.ObservedRuleID == "" {
		t.Fatalf("loaded: %+v %v", result, err)
	}
	row = readFilePullRow(t, f)
	if row.State != "enabled" || row.Readback.State != "loaded" || row.Readback.Health != "ok" || row.Readback.LastEvaluationAt == nil ||
		!row.Readback.FirstSeenAt.Equal(firstSeen) || row.Failure != "" ||
		strings.Join(row.Steps, ",") != "reserved,committing,merging,merged,loading,enabled" {
		t.Fatalf("enabled row: %+v", row)
	}
	// The ledger is settled: the worker and a retry write nothing.
	writes := repo.writes
	if again, err := f.s.ExecuteApproved(ctx, f.id); err != nil || again.Status != "enabled" || repo.writes != writes {
		t.Fatalf("retry after enabled: %+v %v writes=%d->%d", again, err, writes, repo.writes)
	}
	var audits int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action='v3.alert_rule.enable.observed'`, f.id).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("observation audits (state changes only): %d %v", audits, err)
	}
}

func TestFilePullExecutionFailuresNeedAttention(t *testing.T) {
	ctx := t.Context()
	t.Run("rule_health_error_is_failed", func(t *testing.T) {
		f, _, _ := filePullExecution(t, false, "")
		result, err := f.s.ExecuteApproved(ctx, f.id)
		if err != nil || result.Status != "merged" {
			t.Fatalf("execute: %+v %v", result, err)
		}
		f.s.Reader.Source = loadedGroup(f, "err", time.Now())
		if result, err = f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "failed" {
			t.Fatalf("health error: %+v %v", result, err)
		}
		row := readFilePullRow(t, f)
		if row.Failure != "rule_health_error" || row.Readback.State != "error" || row.Readback.LastError != "query timed out" {
			t.Fatalf("failed row: %+v", row)
		}
		// failed is settled: no more observation or writes.
		if _, err := f.s.ObserveEnabled(ctx, f.id); err == nil {
			t.Fatal("a failed execution was observed again")
		}
	})
	t.Run("load_timeout_then_late_load", func(t *testing.T) {
		f, _, _ := filePullExecution(t, false, "")
		if result, err := f.s.ExecuteApproved(ctx, f.id); err != nil || result.Status != "merged" {
			t.Fatalf("execute: %+v %v", result, err)
		}
		// GitLab reported the merge ten minutes ago; the timeout is five.
		if _, err := f.db.Exec(ctx, `UPDATE v3_alert_rule_enable_executions SET deployment=jsonb_set(deployment,'{merged_at}',
 to_jsonb(to_char(clock_timestamp() AT TIME ZONE 'UTC' - interval '10 minutes','YYYY-MM-DD"T"HH24:MI:SS"Z"'))) WHERE proposal_id=$1`, f.id); err != nil {
			t.Fatal(err)
		}
		if result, err := f.s.ObserveEnabled(ctx, f.id); err != nil || result.Status != "awaiting_provider" {
			t.Fatalf("timeout: %+v %v", result, err)
		}
		if row := readFilePullRow(t, f); row.Failure != "load_timeout" || row.Readback.State != "timeout" {
			t.Fatalf("timeout row: %+v", row)
		}
		// The worker keeps reading it back (within 24 hours of the merge).
		if _, err := f.db.Exec(ctx, `UPDATE v3_alert_rule_enable_executions SET updated_at=clock_timestamp()-interval '1 minute' WHERE proposal_id=$1`, f.id); err != nil {
			t.Fatal(err)
		}
		f.s.Reader.Source = loadedGroup(f, "ok", time.Now())
		if _, err := f.s.enableWorkerPass(ctx, ""); err != nil {
			t.Fatal(err)
		}
		if row := readFilePullRow(t, f); row.State != "enabled" || row.Failure != "" || row.Readback.State != "loaded" {
			t.Fatalf("late load: %+v", row)
		}
	})
	t.Run("merge_refused_keeps_the_mr", func(t *testing.T) {
		f, repo, _ := filePullExecution(t, false, "")
		base := repo.branches["main"]
		repo.failMerge = http.StatusMethodNotAllowed
		if _, err := f.s.ExecuteApproved(ctx, f.id); err == nil {
			t.Fatal("a refused merge succeeded")
		}
		row := readFilePullRow(t, f)
		if row.State != "stale_remote" || row.Failure != "merge_refused" || row.Deployment.State != "opened" || row.Deployment.MR != 1 ||
			repo.branches["main"] != base || strings.Join(row.Steps, ",") != "reserved,committing,merging,needs_attention" {
			t.Fatalf("refused row: %+v", row)
		}
	})
	t.Run("lost_merge_response_is_found_on_retry", func(t *testing.T) {
		f, repo, _ := filePullExecution(t, false, "")
		repo.failMerge = http.StatusBadGateway
		if _, err := f.s.ExecuteApproved(ctx, f.id); err == nil {
			t.Fatal("a lost merge response succeeded")
		}
		if row := readFilePullRow(t, f); row.State != "uncertain" || row.Deployment.State != "opened" {
			t.Fatalf("uncertain row: %+v", row)
		}
		repo.failMerge = 0
		writes := repo.writes
		result, err := f.s.ExecuteApproved(ctx, f.id)
		if err != nil || result.Status != "merged" || repo.writes != writes {
			t.Fatalf("retry: %+v %v writes=%d->%d", result, err, writes, repo.writes)
		}
	})
	t.Run("target_moved_before_merge", func(t *testing.T) {
		f, repo, _ := filePullExecution(t, false, "")
		repo.advance("main", filePullGroupBase+"# unrelated\n")
		moved := repo.branches["main"]
		if _, err := f.s.ExecuteApproved(ctx, f.id); err == nil {
			t.Fatal("executed on a moved target")
		}
		if row := readFilePullRow(t, f); row.State != "stale_remote" || row.Failure != "plan_changed" || repo.branches["main"] != moved || len(repo.mrs) != 0 {
			t.Fatalf("moved target: %+v mrs=%d", row, len(repo.mrs))
		}
	})
}

// Without a current approval, recovery records the executor's own merge but
// never merges an MR it finds open.
func TestFilePullRecoveryWithoutApproval(t *testing.T) {
	ctx := t.Context()
	t.Run("merged", func(t *testing.T) {
		f, repo, _ := filePullExecution(t, false, "")
		repo.failMerge = http.StatusBadGateway
		if _, err := f.s.ExecuteApproved(ctx, f.id); err == nil {
			t.Fatal("a lost merge response succeeded")
		}
		if _, err := f.db.Exec(ctx, `INSERT INTO v3_proposal_approval_revocations(proposal_id,revoked_by,reason) VALUES($1,$2,'Test revocation')`,
			f.id, f.authority.approval.ApproverID); err != nil {
			t.Fatal(err)
		}
		result, err := f.s.ExecuteApproved(ctx, f.id)
		if err != nil || result.Status != "merged" {
			t.Fatalf("recovered merge: %+v %v", result, err)
		}
	})
	t.Run("opened", func(t *testing.T) {
		f, repo, _ := filePullExecution(t, false, "")
		repo.failMerge = http.StatusInternalServerError
		if _, err := f.s.ExecuteApproved(ctx, f.id); err == nil {
			t.Fatal("a failed merge succeeded")
		}
		if _, err := f.db.Exec(ctx, `INSERT INTO v3_proposal_approval_revocations(proposal_id,revoked_by,reason) VALUES($1,$2,'Test revocation')`,
			f.id, f.authority.approval.ApproverID); err != nil {
			t.Fatal(err)
		}
		repo.failMerge = 0
		writes := repo.writes
		result, err := f.s.ExecuteApproved(ctx, f.id)
		if err != nil || result.Status != "stale_remote" || repo.writes != writes {
			t.Fatalf("opened MR without approval: %+v %v writes=%d->%d", result, err, writes, repo.writes)
		}
		if row := readFilePullRow(t, f); row.Failure != "approval_invalid" || row.Deployment.State != "opened" {
			t.Fatalf("row: %+v", row)
		}
	})
}
