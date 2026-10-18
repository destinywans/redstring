package v3webhook

import "testing"

// PA-240: the webhook stores an alert without a severity label without one
// (""), not as "warning"; a label is stored as sent, cut to the column width.
func TestStoredSeverityKeepsAMissingLabelEmpty(t *testing.T) {
	if got := storedSeverity(map[string]string{"alertname": "NoSeverity"}); got != "" {
		t.Errorf("missing label stored as %q", got)
	}
	if got := storedSeverity(map[string]string{"severity": "critical"}); got != "critical" {
		t.Errorf("critical stored as %q", got)
	}
	long := "a-severity-value-longer-than-thirty-two-runes"
	if got := storedSeverity(map[string]string{"severity": long}); got != long[:32] {
		t.Errorf("long label stored as %q", got)
	}
}
