package v3events

import "testing"

// PA-167: the alert detail names the same local group GET /alert-groups
// lists it in, so the page can start that group's investigation.
func TestAlertDetailGroupIDMatchesAlertGroups(t *testing.T) {
	v := incidentRecord{ID: "i", SourceID: "am", Fingerprint: "f1", ApplicationID: "app", EnvironmentID: "app-dev"}
	if got := alertDetailGroupID(v); got == nil || *got != localAlertGroupID(v) {
		t.Fatalf("group_id=%v want %s", got, localAlertGroupID(v))
	}
	v.Fingerprint = ""
	if got := alertDetailGroupID(v); got != nil {
		t.Fatalf("group_id without fingerprint=%v", *got)
	}
}
