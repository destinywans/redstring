package v3agents

import "testing"

// PA-236: the version the Agent detail names for a rollback is the one the
// rollback lands on.
func TestRollbackTarget(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		stable, previous, canary string
		want                     string
	}{
		{"canary returns to stable", "v2", "v1", "v3", "v2"},
		{"no canary returns to previous stable", "v2", "v1", "", "v1"},
		{"first stable has nowhere to go", "v1", "", "", ""},
		{"canary without stable has nowhere to go", "", "", "v1", ""},
	} {
		a := lifecycleAgent{agent: agent{Stable: tc.stable, Previous: tc.previous, Canary: tc.canary}}
		if got := rollbackTarget(a); got != tc.want {
			t.Errorf("%s: rollbackTarget=%q, want %q", tc.name, got, tc.want)
		}
	}
}
