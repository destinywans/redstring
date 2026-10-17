package v3events

import "testing"

// PA-119: an old resolved occurrence still linked to an investigation must not
// block investigating the group's firing alert.
func TestInvestigateCountsOnlyFiringOrPendingMembers(t *testing.T) {
	for _, tc := range []struct {
		status, alert string
		want          bool
	}{
		{"open", "firing", true},
		{"investigating", "pending", true},
		{"investigating", "resolved", false},
		{"open", "resolved", false},
		{"resolved", "firing", false},
	} {
		m := bulkMember{Status: tc.status, AlertStatus: tc.alert, InvestigationID: "INV-old"}
		if got := activeInvestigationMember(m); got != tc.want {
			t.Errorf("status=%s alert=%s: counted=%t want %t", tc.status, tc.alert, got, tc.want)
		}
		if activeInvestigationMember(m) != activeClaimMember(m) {
			t.Errorf("status=%s alert=%s: investigate and claim disagree", tc.status, tc.alert)
		}
	}
}
