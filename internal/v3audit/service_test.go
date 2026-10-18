package v3audit

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditFilterAndOutcomeMapping(t *testing.T) {
	for _, input := range []query{{}, {Filter: "user", Q: "   中文   "}, {Filter: "agent"}, {Filter: "system"}} {
		if _, err := parseQuery(input); err != nil {
			t.Fatalf("valid filter %q rejected: %v", input.Filter, err)
		}
	}
	for _, input := range []query{{Filter: "root"}, {Q: strings.Repeat("中", 201)}} {
		if _, err := parseQuery(input); err == nil {
			t.Fatalf("invalid filter %#v accepted", input)
		}
	}
	for value, want := range map[string]string{
		"success": "ok", "succeeded": "ok", "completed": "ok", "accepted": "ok",
		"recorded": "ok", "pending_manual_cleanup": "ok", "test_accepted": "ok",
		"denied": "blocked", "duplicate": "blocked", "invalid_event": "blocked", "failed": "failed",
		"pending": "pending", "no_change": "ok",
	} {
		got, err := resultKind("check.action", value)
		if err != nil || got != want {
			t.Fatalf("outcome %q = %q, %v", value, got, err)
		}
	}
	if _, err := resultKind("check.action", "half_done"); err == nil {
		t.Fatal("unknown outcome silently mapped")
	}
}

// PA-206: a write whose result wasn't confirmed (outcome unknown: a silence
// awaiting its read-only check or a manual one, a draft whose result is
// unknown) is not a failure, and a silence that lapsed before it was sent is
// expired. The result filter takes results, not outcomes.
func TestAuditUnconfirmedAndExpiredOutcomes(t *testing.T) {
	for value, want := range map[string]string{"unknown": "unconfirmed", "expired": "expired", "failed": "failed", "error": "failed"} {
		got, err := resultKind("check.action", value)
		if err != nil || got != want {
			t.Fatalf("outcome %q = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, result := range []string{"ok", "pending", "blocked", "failed", "unconfirmed", "expired", "needs_attention"} {
		r := httptest.NewRequest("GET", "/api/v1/audit?result="+result, nil)
		q, err := parseAuditPage(r)
		if err != nil || strings.Join(q.results, ",") != result {
			t.Fatalf("result=%s selects %v, %v", result, q.results, err)
		}
	}
	for _, result := range []string{"unknown", "success", "needs-attention"} {
		if _, err := parseAuditPage(httptest.NewRequest("GET", "/api/v1/audit?result="+result, nil)); err == nil {
			t.Fatalf("result=%s accepted", result)
		}
	}
}

// PA-230: a rollout records needs_attention for its progress steps too, so
// those read by action; the same outcome on the step that stopped, or on an
// alert rule MR whose remote changed, needs attention.
func TestAuditRolloutStepsReadByAction(t *testing.T) {
	for _, c := range []struct{ action, outcome, want string }{
		{"v3.rollout.dispatching", "needs_attention", "pending"},
		{"v3.rollout.verifying", "needs_attention", "pending"},
		{"v3.rollout.unknown", "needs_attention", "unconfirmed"},
		{"v3.rollout.needs_attention", "needs_attention", "needs_attention"},
		{"v3.rollout.claimed", "needs_attention", "needs_attention"},
		{"v3.rollout.dispatching", "failed", "failed"},
		{"v3.alert_rule.enable.execution_state", "stale_remote", "needs_attention"},
		{"v3.alert_rule.enable.execution_state", "Uncertain", "unconfirmed"},
	} {
		if got, err := resultKind(c.action, c.outcome); err != nil || got != c.want {
			t.Fatalf("%s %s = %q, %v; want %q", c.action, c.outcome, got, err, c.want)
		}
	}
	// The SQL holds only the fixed vocabulary, so it can be spliced into a query.
	for _, table := range []map[string]string{resultKinds, rolloutStepKinds} {
		for key, kind := range table {
			if strings.ContainsAny(key+kind, "'\\") {
				t.Fatalf("%q or result %q can't be quoted", key, kind)
			}
		}
	}
	if !strings.Contains(resultKindSQL, "WHEN action='v3.rollout.unknown' AND lower(outcome)='needs_attention' THEN 'unconfirmed'") {
		t.Fatalf("rollout steps missing from %s", resultKindSQL)
	}
}

func TestExportScopeMustMatch(t *testing.T) {
	if !sameScope([]string{"dev", "test"}, []string{"test", "dev"}) {
		t.Fatal("same environment set rejected")
	}
	if sameScope([]string{"dev", "test"}, []string{"dev"}) || sameScope([]string{"dev"}, []string{"prod"}) || sameScope([]string{"dev", "test"}, []string{"dev", "dev"}) {
		t.Fatal("changed export environment accepted")
	}
}

func TestCSVFormulaIsLiteral(t *testing.T) {
	if got := csvSafe(" =HYPERLINK(\"https://example.invalid\")"); got[0] != '\'' {
		t.Fatalf("formula value not escaped: %q", got)
	}
	if got := csvSafe("ordinary-target"); got != "ordinary-target" {
		t.Fatalf("ordinary value changed: %q", got)
	}
}
