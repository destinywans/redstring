// Package v3pagequery parses bounded page query parameters without performing
// authorization or interpreting display strings as resource identities.
package v3pagequery

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid page query")
var durations = map[string]time.Duration{"15m": 15 * time.Minute, "30m": 30 * time.Minute, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// LongestRange is the longest range value no longer than within ("" when
// even the shortest is longer).
func LongestRange(within time.Duration) string {
	best, longest := "", time.Duration(0)
	for name, d := range durations {
		if d <= within && d > longest {
			best, longest = name, d
		}
	}
	return best
}

func ParseRange(q url.Values, fallback time.Duration) (time.Duration, error) {
	values, has := q["range"]
	if !has {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, ErrInvalid
	}
	d, ok := durations[values[0]]
	if !ok {
		return 0, ErrInvalid
	}
	return d, nil
}
func Values(q url.Values, key string) ([]string, error) {
	values := q[key]
	if len(values) > 32 {
		return nil, ErrInvalid
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		if v == "" || len(v) > 256 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\x00\r\n") {
			return nil, ErrInvalid
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}
func Matches(values []string, value string) bool {
	if len(values) == 0 {
		return true
	}
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func Single(q url.Values, key string, allowed ...string) (string, error) {
	values := q[key]
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 {
		return "", ErrInvalid
	}
	for _, v := range allowed {
		if values[0] == v {
			return v, nil
		}
	}
	return "", ErrInvalid
}
