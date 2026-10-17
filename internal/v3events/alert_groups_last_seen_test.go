package v3events

import (
	"testing"
	"time"
)

// PA-174: a group carries when it last fired, and groups of one severity
// list the most recent first instead of by their hashed ID.
func TestAlertGroupsSortByLastFiring(t *testing.T) {
	day := time.Date(2026, 10, 12, 1, 36, 29, 0, time.UTC)
	member := func(id, fingerprint string, start time.Time) incidentRecord {
		return incidentRecord{ID: id, SourceID: "am", Fingerprint: fingerprint, ApplicationID: "app-1",
			EnvironmentID: "env-1", EnvironmentKey: "prod", Title: "LabHigh5xxRate", Severity: "critical",
			AlertStatus: "resolved", StartsAt: start}
	}
	groups, err := aggregateAlertGroups([]incidentRecord{
		member("old", "fp-old", day),
		member("again-1", "fp-again", day),
		member("again-2", "fp-again", day.Add(5*24*time.Hour+8*time.Hour+20*time.Minute)),
		member("mid", "fp-mid", day.Add(2*24*time.Hour)),
	}, day.Add(6*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	items := []alertGroup{}
	for _, g := range groups {
		items = append(items, g.group)
	}
	sortAlertGroups(items)
	if len(items) != 3 || items[0].Fingerprint != "fp-again" || items[1].Fingerprint != "fp-mid" || items[2].Fingerprint != "fp-old" {
		t.Fatalf("order: %+v", items)
	}
	again := items[0]
	if again.Since != day.Format(time.RFC3339Nano) || again.LastSeen != "2026-10-17T09:56:29Z" {
		t.Fatalf("since=%s last_seen=%s", again.Since, again.LastSeen)
	}
}
