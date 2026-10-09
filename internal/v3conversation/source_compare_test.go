package v3conversation

import (
	"sync"

	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// recordedUses collects PA-19 compare-mode notifications.
type recordedUses struct {
	mu   sync.Mutex
	uses []v3telemetry.Use
}

func (r *recordedUses) ObserveLegacy(u v3telemetry.Use) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uses = append(r.uses, u)
}

func (r *recordedUses) last() v3telemetry.Use {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.uses) == 0 {
		return v3telemetry.Use{}
	}
	return r.uses[len(r.uses)-1]
}
