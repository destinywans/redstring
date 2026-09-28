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
