package v3events

import (
	"testing"
	"time"
)

// PA-130: a group names its alert when every member agrees, and says null otherwise.
func TestAlertGroupsNameTheAlertWhenMembersAgree(t *testing.T) {
	start := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)
	member := func(id, fingerprint, title string, labels map[string]string) incidentRecord {
		return incidentRecord{ID: id, SourceID: "lab-alertmanager", Fingerprint: fingerprint, ApplicationID: "app-1",
			EnvironmentID: "env-1", EnvironmentKey: "prod", Title: title, Severity: "critical", AlertStatus: "firing",
			Labels: labels, StartsAt: start}
	}
	one := func(groups map[string]*alertGroupAccumulator) alertGroup {
		t.Helper()
		if len(groups) != 1 {
			t.Fatalf("groups=%d want 1", len(groups))
		}
		for _, g := range groups {
			g.settleAlertName()
			return g.group
		}
		return alertGroup{}
	}

	same, err := aggregateAlertGroups([]incidentRecord{
		member("i1", "fp-1", "LabHigh5xxRate", map[string]string{"alertname": "LabHigh5xxRate"}),
		member("i2", "fp-1", "LabHigh5xxRate", nil),
	}, start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if g := one(same); g.AlertName == nil || *g.AlertName != "LabHigh5xxRate" {
		t.Fatalf("alert_name=%v want LabHigh5xxRate", g.AlertName)
	}

	mixed, err := aggregateAlertGroups([]incidentRecord{
		member("i1", "fp-1", "LabHigh5xxRate", map[string]string{"alertname": "LabHigh5xxRate", "service": "app-1"}),
		member("i2", "fp-2", "LabAPIDown", map[string]string{"alertname": "LabAPIDown", "service": "app-1"}),
	}, start.Add(time.Minute), "service")
	if err != nil {
		t.Fatal(err)
	}
	if g := one(mixed); g.AlertName != nil {
		t.Fatalf("alert_name=%q want null for mixed members", *g.AlertName)
	}
}
