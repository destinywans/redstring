// Package alertseverity maps the severity an alert source recorded
// (ops_incidents.severity, copied from the alert's severity label) onto the
// V3 P1–P4 scale. Every V3 view that shows or counts alert severity uses
// this one table (PA-238, PA-239); a value outside it stays unknown instead
// of failing the page or being guessed as some level.
package alertseverity

import (
	"sort"
	"strings"
)

// Reasons for an unknown severity, in the vocabulary of the V3 unknown-value
// reasons.
const (
	// ReasonMissing: the alert carried no severity label.
	ReasonMissing = "not_filled"
	// ReasonUnrecognized: the recorded value is outside the table below.
	ReasonUnrecognized = "unclassified"
)

// levels maps a trimmed, lower-case severity value onto the P1–P4 scale.
//   - P1–P4 themselves. P0 and P5 sit one step beyond the ends of the same
//     kind of scale (Opsgenie, Datadog), so they are clamped to P1 and P4.
//   - Prometheus / kube-prometheus rules: critical, warning, info, and none,
//     which marks the Watchdog and InfoInhibitor alerts that only prove the
//     pipeline works (lowest, P4).
//   - PagerDuty Events v2: critical, error, warning, info. Error sits between
//     critical and warning; P1 stays for critical, so error shares P2.
//   - Log and syslog levels in the same order: fatal and emergency above
//     error (P1); notice between warning and info (P3, with info); debug
//     below info (P4).
//   - high / medium / low as ticketing tools use them.
//
// Words tools use with different meanings stay unknown rather than guessed:
// major and minor (ITU X.733 puts both above warning, which is already P2
// here), page and ticket (routing hints, not levels) and alert.
var levels = map[string]string{
	"p0": "P1", "p1": "P1", "critical": "P1", "fatal": "P1", "emergency": "P1",
	"p2": "P2", "high": "P2", "error": "P2", "warning": "P2", "warn": "P2",
	"p3": "P3", "medium": "P3", "info": "P3", "notice": "P3",
	"p4": "P4", "p5": "P4", "low": "P4", "none": "P4", "debug": "P4",
}

func key(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// Level maps one recorded value; ok is false when it is empty or outside the
// table.
func Level(raw string) (string, bool) {
	level, ok := levels[key(raw)]
	return level, ok
}

// Of resolves a stored severity: the level, or "" and the reason it is
// unknown.
func Of(raw string) (level, reason string) {
	if key(raw) == "" {
		return "", ReasonMissing
	}
	if level, ok := Level(raw); ok {
		return level, ""
	}
	return "", ReasonUnrecognized
}

// Ptr is Of for JSON fields: nil level and a non-nil reason when unknown.
func Ptr(raw string) (*string, *string) {
	level, reason := Of(raw)
	if level == "" {
		return nil, &reason
	}
	return &level, nil
}

// Aliases lists the trimmed lower-case values that map to level, sorted, for
// SQL filters over the raw column (lower(btrim(severity)) = ANY(...)).
func Aliases(level string) []string {
	out := []string{}
	for raw, l := range levels {
		if l == level {
			out = append(out, raw)
		}
	}
	sort.Strings(out)
	return out
}

// Rank orders levels for sorting and "highest of": P1 first, unknown last.
func Rank(level string) int {
	switch level {
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	case "P4":
		return 4
	default:
		return 5
	}
}
