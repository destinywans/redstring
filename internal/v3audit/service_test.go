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
		got, err := resultKind(value)
		if err != nil || got != want {
			t.Fatalf("outcome %q = %q, %v", value, got, err)
		}
	}
	if _, err := resultKind("half_done"); err == nil {
		t.Fatal("unknown outcome silently mapped")
	}
}

// PA-206: a write whose result wasn't confirmed (outcome unknown: a silence
// awaiting its read-only check or a manual one, a draft whose result is
// unknown) is not a failure, and a silence that lapsed before it was sent is
// expired. The result filter selects the same outcomes.
func TestAuditUnconfirmedAndExpiredOutcomes(t *testing.T) {
	for value, want := range map[string]string{"unknown": "unconfirmed", "expired": "expired", "failed": "failed", "error": "failed"} {
		got, err := resultKind(value)
		if err != nil || got != want {
			t.Fatalf("outcome %q = %q, %v; want %q", value, got, err, want)
		}
	}
	for result, want := range map[string][]string{"unconfirmed": {"unknown"}, "expired": {"expired"}, "failed": {"failed", "failure", "error"}} {
		r := httptest.NewRequest("GET", "/api/v1/audit?result="+result, nil)
		q, err := parseAuditPage(r)
		if err != nil || strings.Join(q.outcomes, ",") != strings.Join(want, ",") {
			t.Fatalf("result=%s selects %v, %v; want %v", result, q.outcomes, err, want)
		}
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
