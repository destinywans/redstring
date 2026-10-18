package v3events

import (
	"reflect"
	"testing"
)

// PA-189: the incident detail lists the investigation and alerts it has
// validated instead of an empty links array.
func TestIncidentLinksListValidatedInvestigationAndAlerts(t *testing.T) {
	got := incidentLinks("INV-1", "Checkout errors", []caseAlert{{ID: "alert-a", Title: "LabHigh5xxRate"}, {ID: "alert b", Title: "  "}})
	want := []incidentDetailLink{
		{Kind: "investigation", Label: "Checkout errors", Href: "/canvas/INV-1"},
		{Kind: "alert", Label: "LabHigh5xxRate", Href: "/incidents/alert-a"},
		// A blank title falls back to the ID; the path segment is escaped.
		{Kind: "alert", Label: "alert b", Href: "/incidents/alert%20b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("links: %#v", got)
	}
	// No investigation and no case alerts: an empty array, never null.
	if none := incidentLinks("", "", nil); none == nil || len(none) != 0 {
		t.Fatalf("empty links: %#v", none)
	}
}
