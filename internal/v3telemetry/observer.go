package v3telemetry

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
)

// Mode selects how readers use the registry (AIOPS_V3_SOURCE_REGISTRY).
type Mode string

const (
	// ModeUnified: every reader reads the registry. The default since PA-19
	// P3 stage 2 step 8; AIOPS_V3_UNIFIED_READERS is then ignored.
	ModeUnified Mode = "unified"
	// ModeLegacy: the readers named in AIOPS_V3_UNIFIED_READERS read the
	// registry, the others the file configuration. Kept until P4 as the way
	// back.
	ModeLegacy Mode = "legacy"
	// ModeCompare is ModeLegacy that also checks, in the background, each
	// legacy read against the registry; differences go to the log.
	ModeCompare Mode = "compare"
)

// ParseMode accepts unified (or empty), legacy and compare.
func ParseMode(value string) (Mode, error) {
	switch value {
	case "", string(ModeUnified):
		return ModeUnified, nil
	case string(ModeLegacy):
		return ModeLegacy, nil
	case string(ModeCompare):
		return ModeCompare, nil
	}
	return "", errors.New("AIOPS_V3_SOURCE_REGISTRY must be unified, legacy or compare")
}

// Use is one read of a file-configured binding by a current reader.
type Use struct {
	ApplicationID string
	EnvironmentID string
	Provider      string
	Resource      string
}

// Observer is told about legacy reads. ObserveLegacy must never block or
// change the reader's result.
type Observer interface {
	ObserveLegacy(Use)
}

const (
	compareQueue    = 256
	compareTimeout  = 5 * time.Second
	compareRecheck  = 10 * time.Minute
	compareSummary  = 15 * time.Minute
	compareMaxCache = 10000
)

type compareEntry struct {
	result Result
	at     time.Time
}

// Comparer runs compare mode. Reads are queued without blocking (a full
// queue drops and counts), deduplicated for ten minutes, resolved in both
// registries and logged when the outcome changes. Logs carry only IDs and
// reason codes, never endpoints, selectors or expressions.
type Comparer struct {
	db        Querier
	resolver  *Resolver
	telemetry connectors.TelemetryConfig
	logger    *slog.Logger
	queue     chan Use
	now       func() time.Time

	mu      sync.Mutex
	last    map[string]compareEntry
	counts  map[string]int
	dropped int
}

func NewComparer(db Querier, resolver *Resolver, telemetry connectors.TelemetryConfig, logger *slog.Logger) *Comparer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Comparer{db: db, resolver: resolver, telemetry: telemetry, logger: logger,
		queue: make(chan Use, compareQueue), now: time.Now, last: map[string]compareEntry{}, counts: map[string]int{}}
}

// ObserveLegacy queues a read for comparison.
func (c *Comparer) ObserveLegacy(u Use) {
	if c == nil {
		return
	}
	select {
	case c.queue <- u:
	default:
		c.mu.Lock()
		c.dropped++
		c.mu.Unlock()
	}
}

// Run processes the queue until ctx ends and logs a periodic summary.
func (c *Comparer) Run(ctx context.Context) {
	ticker := time.NewTicker(compareSummary)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case u := <-c.queue:
			c.check(ctx, u)
		case <-ticker.C:
			counts, dropped := c.Counts()
			if len(counts) > 0 || dropped > 0 {
				c.logger.Info("source registry compare summary", "counts", counts, "dropped", dropped)
			}
		}
	}
}

// Counts returns how many checks ended in each state and how many reads
// were dropped because the queue was full.
func (c *Comparer) Counts() (map[string]int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.counts))
	for state, n := range c.counts {
		out[state] = n
	}
	return out, c.dropped
}

func (c *Comparer) check(ctx context.Context, u Use) {
	surface, ok := SurfaceOf(u.Provider)
	if !ok {
		return
	}
	key := u.ApplicationID + "\x00" + u.EnvironmentID + "\x00" + u.Provider + "\x00" + u.Resource
	now := c.now()
	c.mu.Lock()
	previous, seen := c.last[key]
	c.mu.Unlock()
	if seen && now.Sub(previous.at) < compareRecheck {
		return
	}
	result := c.Compare(ctx, u)
	c.mu.Lock()
	if len(c.last) >= compareMaxCache {
		c.last = map[string]compareEntry{}
	}
	c.last[key] = compareEntry{result: result, at: now}
	c.counts[result.State]++
	c.mu.Unlock()
	if seen && previous.result.State == result.State && slices.Equal(previous.result.Reasons, result.Reasons) {
		return
	}
	level := slog.LevelInfo
	switch result.State {
	case StateDiffers, StateMissing, StateAmbiguous, StateUnavailable:
		level = slog.LevelWarn
	}
	c.logger.Log(ctx, level, "source registry compare", "surface", string(surface),
		"application_id", u.ApplicationID, "environment_id", u.EnvironmentID, "legacy_provider", u.Provider,
		"state", result.State, "reasons", result.Reasons)
}

// Compare resolves one legacy read in both registries now.
func (c *Comparer) Compare(ctx context.Context, u Use) Result {
	surface, ok := SurfaceOf(u.Provider)
	if !ok {
		return Result{State: StateLegacyUnregistered}
	}
	ctx, cancel := context.WithTimeout(ctx, compareTimeout)
	defer cancel()
	legacy, legacyOK := LegacySource(c.telemetry, u.Provider, u.Resource)
	unified, err := c.resolver.Resolve(ctx, c.db, u.ApplicationID, u.EnvironmentID, surface)
	return Compare(legacy, legacyOK, unified, err)
}
