package v3telemetry

import (
	"fmt"
	"sort"
	"strings"
)

// Readers that switch to the registry one at a time during stage 2 of PA-19
// P3 (design §10.3), named in AIOPS_V3_UNIFIED_READERS.
const (
	ReaderCoverage      = "coverage"
	ReaderMappings      = "mappings"
	ReaderLogs          = "logs"
	ReaderTraces        = "traces"
	ReaderAIMetrics     = "ai_metrics"
	ReaderMappingWrites = "mapping_writes"
	ReaderRegistrations = "registrations"
)

// availableReaders are the readers whose registry path is implemented; a
// reader that is not here yet cannot be named, so a configuration never
// claims a switch the build cannot make.
var availableReaders = map[string]bool{ReaderCoverage: true, ReaderMappings: true, ReaderLogs: true, ReaderTraces: true,
	ReaderAIMetrics: true, ReaderMappingWrites: true, ReaderRegistrations: true}

// readerRequires lists the readers a reader cannot run without. Mapping
// writes change targets, so the mapping page and every reader of the edited
// systems must already read targets (design §10.3), or an edit would not
// show where it was made.
var readerRequires = map[string][]string{
	ReaderMappingWrites: {ReaderMappings, ReaderLogs, ReaderTraces, ReaderAIMetrics},
}

// Readers is the set of readers that read the registry.
type Readers map[string]bool

// ParseReaders reads AIOPS_V3_UNIFIED_READERS: comma-separated reader names.
// Unknown, unavailable or repeated names reject the configuration.
func ParseReaders(raw string) (Readers, error) {
	readers := Readers{}
	if strings.TrimSpace(raw) == "" {
		return readers, nil
	}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		switch {
		case !availableReaders[name]:
			return nil, fmt.Errorf("AIOPS_V3_UNIFIED_READERS: %q is not an available reader (available: %s)", name, availableReaderList())
		case readers[name]:
			return nil, fmt.Errorf("AIOPS_V3_UNIFIED_READERS: %q is listed twice", name)
		}
		readers[name] = true
	}
	for name := range readers {
		for _, required := range readerRequires[name] {
			if !readers[required] {
				return nil, fmt.Errorf("AIOPS_V3_UNIFIED_READERS: %q requires %q", name, required)
			}
		}
	}
	return readers, nil
}

func availableReaderList() string {
	names := make([]string, 0, len(availableReaders))
	for name := range availableReaders {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "none yet"
	}
	return strings.Join(names, ", ")
}

// EffectiveReaders is the set of readers that read the registry in mode:
// every available reader in unified mode, where a listed set is ignored
// (ignored reports it), and the listed set otherwise.
func EffectiveReaders(mode Mode, listed Readers) (readers Readers, ignored bool) {
	if mode != ModeUnified {
		return listed, false
	}
	all := Readers{}
	for name := range availableReaders {
		all[name] = true
	}
	return all, len(listed) > 0
}

// Unified reports whether reader reads the registry.
func (r Readers) Unified(reader string) bool { return r[reader] }

// Names lists the switched readers, sorted, for the startup log.
func (r Readers) Names() []string {
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
