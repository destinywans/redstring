package connectors

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLokiCountTimestampPrecision(t *testing.T) {
	// Loki's vector timestamps have millisecond precision. The request uses
	// end-1ns for the [start,end) count. The returned time is 999999ns earlier,
	// although subtracting epoch float64 seconds produces 0.0010001659393310547.
	at := time.UnixMilli(1790759000011).Add(-time.Nanosecond)
	cases := []struct {
		name, timestamp string
		valid           bool
	}{
		{"real millisecond truncation", "1790759000.010", true},
		{"scientific notation preserves same instant", "1.790759000010e9", true},
		{"exact lower one millisecond boundary", "1790759000.009999999", true},
		{"exact upper one millisecond boundary", "1790759000.011999999", true},
		{"lower boundary plus one nanosecond stale", "1790759000.009999998", false},
		{"upper boundary plus one nanosecond future", "1790759000.012000000", false},
		{"actual stale two millisecond vector", "1790759000.009", false},
		{"quoted timestamp violates vector contract", `"1790759000.010"`, false},
		{"unbounded number exponent", "1e999999999", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			value, err := lokiIntegerValue([]json.RawMessage{json.RawMessage(c.timestamp), json.RawMessage(`"7"`)}, at)
			if c.valid && (err != nil || value != 7) {
				t.Fatalf("valid <=1ms evaluation timestamp rejected: %v", err)
			}
			if !c.valid && err == nil {
				t.Fatal("invalid or genuinely >1ms vector evaluation timestamp accepted")
			}
		})
	}
}
