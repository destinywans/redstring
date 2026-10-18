package operations

import (
	"context"
	"testing"
	"time"
)

// PA-240: an alert without a severity label is stored without one ("") so
// readers can say the severity is unknown; a label is stored as sent.
func TestIngestStoresNoSeverityWhenTheLabelIsMissing(t *testing.T) {
	s := isolatedAutomation(t)
	ctx := context.Background()
	source := Source{ID: "prom", Mappings: []Mapping{{Labels: map[string]string{"service": "one"}, ApplicationID: "a", EnvironmentID: "e1"}}}
	start := time.Now().UTC().Add(-time.Minute)
	for fingerprint, labels := range map[string]map[string]string{
		"unlabelled": {"service": "one", "alertname": "NoSeverity"},
		"labelled":   {"service": "one", "alertname": "Info", "severity": "info"},
	} {
		if err := s.Ingest(ctx, source, Alert{Status: "firing", Fingerprint: fingerprint, StartsAt: start, Labels: labels}); err != nil {
			t.Fatal(err)
		}
	}
	for fingerprint, want := range map[string]string{"unlabelled": "", "labelled": "info"} {
		var got string
		if err := s.DB.QueryRow(ctx, `SELECT severity FROM ops_incidents WHERE fingerprint=$1`, fingerprint).Scan(&got); err != nil || got != want {
			t.Errorf("%s: severity=%q err=%v; want %q", fingerprint, got, err, want)
		}
	}
}
