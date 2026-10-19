package v3events

import "testing"

// PA-166: the drafted severity hint follows the request language.
func TestIncidentDraftSeverityHintFollowsAcceptLanguage(t *testing.T) {
	if got := textFor("zh-CN").severityHint(0, 1); got != "所选告警中的最高级别" {
		t.Fatalf("zh: %q", got)
	}
	if got := textFor("").severityHint(0, 1); got != "所选告警中的最高级别" {
		t.Fatalf("default: %q", got)
	}
	if got := textFor("en-US,en;q=0.9").severityHint(0, 1); got != "Highest severity among selected persisted alerts" {
		t.Fatalf("en: %q", got)
	}
}

// PA-246: with unknown severities among the selected alerts the hint says
// so instead of calling the pre-filled level the highest.
func TestIncidentDraftSeverityHintNamesUnknownAlerts(t *testing.T) {
	for _, tc := range []struct {
		lang           string
		unknown, total int
		want           string
	}{
		{"zh-CN", 2, 2, "所选告警的级别未知，请选择级别"},
		{"zh-CN", 1, 3, "已知级别中的最高级；另有 1 条告警级别未知，请确认"},
		{"zh-CN", 2, 3, "已知级别中的最高级；另有 2 条告警级别未知，请确认"},
		{"en-US", 2, 2, "The selected alerts have an unknown severity; choose one"},
		{"en-US", 1, 3, "Highest known severity; 1 selected alert has an unknown severity, so confirm it"},
		{"en-US", 2, 3, "Highest known severity; 2 selected alerts have an unknown severity, so confirm it"},
	} {
		if got := textFor(tc.lang).severityHint(tc.unknown, tc.total); got != tc.want {
			t.Errorf("%s %d/%d: %q; want %q", tc.lang, tc.unknown, tc.total, got, tc.want)
		}
	}
}
