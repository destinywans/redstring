package v3events

import "testing"

// PA-166: the drafted severity hint follows the request language.
func TestIncidentDraftSeverityHintFollowsAcceptLanguage(t *testing.T) {
	if got := textFor("zh-CN").severityHint(); got != "所选告警中的最高级别" {
		t.Fatalf("zh: %q", got)
	}
	if got := textFor("").severityHint(); got != "所选告警中的最高级别" {
		t.Fatalf("default: %q", got)
	}
	if got := textFor("en-US,en;q=0.9").severityHint(); got != "Highest severity among selected persisted alerts" {
		t.Fatalf("en: %q", got)
	}
}
