package alertseverity

import (
	"reflect"
	"testing"
)

// PA-239: one table for every view; an unknown value is unknown with a
// reason, never an error and never P4.
func TestOfMapsKnownValuesAndKeepsTheRestUnknown(t *testing.T) {
	for _, tc := range []struct{ raw, level, reason string }{
		{"critical", "P1", ""}, {" Fatal ", "P1", ""}, {"P1", "P1", ""}, {"p0", "P1", ""},
		{"warning", "P2", ""}, {"warn", "P2", ""}, {"high", "P2", ""}, {"error", "P2", ""},
		{"info", "P3", ""}, {"medium", "P3", ""}, {"P3", "P3", ""},
		{"p4", "P4", ""}, {"low", "P4", ""}, {"none", "P4", ""}, {"P5", "P4", ""},
		{"", "", ReasonMissing}, {"  ", "", ReasonMissing},
		{"major", "", ReasonUnrecognized}, {"page", "", ReasonUnrecognized}, {"not-a-severity", "", ReasonUnrecognized},
	} {
		level, reason := Of(tc.raw)
		if level != tc.level || reason != tc.reason {
			t.Errorf("Of(%q) = %q,%q; want %q,%q", tc.raw, level, reason, tc.level, tc.reason)
		}
	}
	if level, reason := Ptr("major"); level != nil || reason == nil || *reason != ReasonUnrecognized {
		t.Errorf("Ptr(major) = %v,%v", level, reason)
	}
	if level, reason := Ptr("critical"); level == nil || *level != "P1" || reason != nil {
		t.Errorf("Ptr(critical) = %v,%v", level, reason)
	}
}

func TestAliasesAndRank(t *testing.T) {
	if got := Aliases("P1"); !reflect.DeepEqual(got, []string{"critical", "emergency", "fatal", "p0", "p1"}) {
		t.Errorf("P1 aliases = %v", got)
	}
	if Rank("P1") >= Rank("P4") || Rank("P4") >= Rank("") {
		t.Error("unknown must sort after P4")
	}
}
