package v3events

import "testing"

func TestClaimDecisionUnderIncidentLock(t *testing.T) {
	for _, tc := range []struct {
		name, status, alertStatus, assignee string
		allowed, change                     bool
	}{
		{"new firing alert", "open", "firing", "", true, true},
		{"new pending alert", "investigating", "pending", "", true, true},
		{"same user's retry", "open", "firing", "alice", true, false},
		{"same user's retry after resolution", "resolved", "resolved", "alice", true, false},
		{"another user's claim", "open", "firing", "bob", false, false},
		{"resolved alert", "open", "resolved", "", false, false},
		{"closed incident", "resolved", "firing", "", false, false},
		{"unknown state", "unexpected", "firing", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, change := claimAllowed(incidentRecord{Status: tc.status, AlertStatus: tc.alertStatus, AssigneeID: tc.assignee}, "alice")
			if allowed != tc.allowed || change != tc.change {
				t.Fatalf("allowed=%t change=%t; want allowed=%t change=%t", allowed, change, tc.allowed, tc.change)
			}
		})
	}
}
