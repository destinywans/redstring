package v3memory

import (
	"encoding/json"
	"strings"
	"testing"
)

// PA-166: GET /memory names each fact's and conflict side's source document,
// and says null when it has no title rather than leaving the field out.
func TestMemoryViewSourceTitleIsAlwaysPresent(t *testing.T) {
	for name, value := range map[string]any{"fact": memoryViewFact{}, "conflict side": memoryConflictSide{}} {
		raw, err := json.Marshal(value)
		if err != nil || !strings.Contains(string(raw), `"source_title":null`) {
			t.Fatalf("%s: %s (%v)", name, raw, err)
		}
	}
}
