package v3observe

import "testing"

// Trace route ownership moved to v3traces. Keep this projection helper check
// while the old sampled handler remains available for code-history review.
func TestObservedTraceHrefEncodesService(t *testing.T) {
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if href := traceHref(id, "app/team & test"); href != "/traces/"+id+"?service=app%2Fteam+%26+test" {
		t.Fatalf("trace service link is not URL encoded: %q", href)
	}
}
