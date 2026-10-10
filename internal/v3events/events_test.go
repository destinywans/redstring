package v3events

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIncidentStatusPreservesAlertAndWorkState(t *testing.T) {
	cases := []struct {
		name   string
		record incidentRecord
		want   string
	}{
		{"active alert", incidentRecord{Status: "open", AlertStatus: "firing"}, "active"},
		{"investigating resolved alert", incidentRecord{Status: "investigating", AlertStatus: "resolved"}, "mitigated"},
		{"work resolved", incidentRecord{Status: "resolved", AlertStatus: "resolved"}, "resolved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := incidentStatus(tc.record); got != tc.want {
				t.Fatalf("status=%q want %q", got, tc.want)
			}
		})
	}
}

func TestInboxFilterUsesDocumentedParameterAndRejectsConflicts(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  string
		ok    bool
	}{
		{"", "all", true},
		{"filter=mine", "mine", true},
		{"tab=mentions", "mentions", true},
		{"filter=subscribed&tab=subscribed", "subscribed", true},
		{"filter=mine&tab=all", "", false},
		{"filter=unknown", "", false},
		{"filter=mine&filter=all", "", false},
	} {
		query, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := inboxFilter(query)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: got %q,%t want %q,%t", tc.query, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAlertSeveritySupportsRepeatedValuesAndFiltersGroupSeverity(t *testing.T) {
	for _, tc := range []struct {
		query  string
		wantP1 bool
		wantP2 bool
		wantP3 bool
		valid  bool
	}{
		{"", true, true, true, true},
		{"severity=all", true, true, true, true},
		{"severity=P1&severity=P3", true, false, true, true},
		{"severity=p2", false, true, false, true},
		{"severity=P5", false, false, false, false},
		{"severity=all&severity=P1", false, false, false, false},
	} {
		query, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		selected, ok := alertSeverities(query)
		if ok != tc.valid {
			t.Errorf("%q: valid=%t want %t", tc.query, ok, tc.valid)
			continue
		}
		if !ok {
			continue
		}
		if matchesAlertSeverity(selected, "P1") != tc.wantP1 || matchesAlertSeverity(selected, "P2") != tc.wantP2 || matchesAlertSeverity(selected, "P3") != tc.wantP3 {
			t.Errorf("%q: severity selection=%v", tc.query, selected)
		}
	}
}

func TestInvalidFiltersReturn422BeforeDatabaseAccess(t *testing.T) {
	service := &Service{}
	for _, tc := range []struct {
		path    string
		handler http.HandlerFunc
		code    string
	}{
		{"/inbox?filter=mine&tab=mentions", service.Inbox, "invalid_filter"},
		{"/alert-groups?severity=P5", service.AlertGroups, "invalid_severity"},
		{"/incidents?status=active&status=resolved", service.Incidents, "invalid_status"},
	} {
		response := httptest.NewRecorder()
		tc.handler(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), tc.code) {
			t.Errorf("%s: status=%d body=%s", tc.path, response.Code, response.Body.String())
		}
	}
}

func TestSummaryUsesActualAssigneeAndElapsedTime(t *testing.T) {
	since := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	got := summarize(incidentRecord{ID: "i", Severity: "critical", Status: "open", AlertStatus: "firing", Title: "Disk full", Commander: "Li", StartsAt: since}, since.Add(90*time.Minute))
	if got.Severity != "P1" || got.Status != "active" || got.Duration != "1h 30m" || got.Commander != "Li" {
		t.Fatalf("unexpected summary: %+v", got)
	}
}

func TestIncidentFilterDefaultsToActiveAndRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  string
		ok    bool
	}{
		{"", "active", true},
		{"status=active", "active", true},
		{"status=resolved", "resolved", true},
		{"status=all", "all", true},
		{"status=", "", false},
		{"status=other", "", false},
		{"status=active&status=resolved", "", false},
	} {
		query, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := incidentFilter(query)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: got %q,%t want %q,%t", tc.query, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIncidentListCountsUnfilteredVisibleRecords(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	values := []incidentRecord{
		{ID: "open", Status: "open", AlertStatus: "firing", StartsAt: now.Add(-time.Hour)},
		{ID: "mitigated", Status: "investigating", AlertStatus: "resolved", StartsAt: now.Add(-time.Hour)},
		{ID: "resolved", Status: "resolved", AlertStatus: "resolved", StartsAt: now.Add(-time.Hour)},
	}
	for _, tc := range []struct {
		filter string
		ids    []string
	}{
		{"active", []string{"open", "mitigated"}},
		{"resolved", []string{"resolved"}},
		{"all", []string{"open", "mitigated", "resolved"}},
	} {
		items, counts := incidentList(values, tc.filter, now)
		if len(items) != len(tc.ids) || counts["active"] != 2 || counts["resolved"] != 1 || counts["all"] != 3 {
			t.Errorf("%s: items=%+v counts=%+v", tc.filter, items, counts)
			continue
		}
		for i, id := range tc.ids {
			if items[i].ID != id {
				t.Errorf("%s: item[%d]=%q want %q", tc.filter, i, items[i].ID, id)
			}
		}
	}
}

func TestEventTextsFollowAcceptLanguage(t *testing.T) {
	en, zh := textFor("en-US,en;q=0.8"), textFor("zh-CN")
	sent := "sent"
	if en.notify(nil) == zh.notify(nil) || en.notification(sent) != "Accepted by the notification transport; receipt not confirmed" ||
		zh.notify(&sent) != "平台通知传输接口已接受；未确认接收人已读" || en.unrecorded() != "Not recorded" || zh.unrecorded() != "未记录" {
		t.Fatal("event texts do not follow the request language")
	}
	if label, help := en.timing("group_wait"); label != "Group wait" || help == "" {
		t.Fatalf("timing label: %q %q", label, help)
	}
	since := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	resolved := incidentRecord{ID: "r", Severity: "critical", Status: "resolved", AlertStatus: "resolved", StartsAt: since}
	if got := summarize(resolved, since.Add(time.Hour), en); got.Duration != "Not recorded" {
		t.Fatalf("resolved without close event: %+v", got)
	}
	if got := summarize(resolved, since.Add(time.Hour)); got.Duration != "未记录" {
		t.Fatalf("default language changed: %+v", got)
	}
}

// PA-84: an assignment links to the assigned object, not back to the inbox,
// and its description follows the request language.
func TestAssignmentInboxItemLinksAndText(t *testing.T) {
	for kind, want := range map[string]string{"alert": "/incidents/a%2Fb", "incident": "/incidents/a%2Fb", "proposal": "/review/a%2Fb"} {
		if got := assignedHref(kind, "a/b"); got != want {
			t.Errorf("%s: %q, want %q", kind, got, want)
		}
	}
	if textFor("en-US").assignedSub("proposal") != "Proposal assigned to you" || textFor("").assignedSub("incident") != "分配给你的事件" {
		t.Fatal("assignment text not localized")
	}
}
