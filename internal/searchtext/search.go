// Package searchtext implements literal, pinyin and initial text matching.
// Keep this algorithm aligned with web/src/lib/search.ts and the SQL functions
// generated into migration 019. It never parses telemetry query languages.
package searchtext

import (
	"strings"
	"unicode"

	searchdata "github.com/wans/aiops-canvas/shared/search"
)

// Match accepts plain text, not LIKE patterns. Pure Chinese queries are literal
// so unrelated words sharing initials do not match. In mixed queries, Chinese
// characters stay literal while Latin text can match pinyin or first letters.
func Match(value, query string) bool {
	value = strings.ToLower(value)
	query = strings.ToLower(strings.TrimSpace(query))
	if strings.Contains(value, query) {
		return true
	}
	if !strings.ContainsAny(query, "abcdefghijklmnopqrstuvwxyz") {
		return false
	}
	query = compact(query)
	var full, initials strings.Builder
	for _, char := range compact(value) {
		literal := string(char)
		reading, ok := searchdata.Pinyin[literal]
		if !ok || strings.ContainsRune(query, char) {
			full.WriteRune(char)
			initials.WriteRune(char)
			continue
		}
		full.WriteString(reading)
		initials.WriteRune([]rune(reading)[0])
	}
	return strings.Contains(full.String(), query) || strings.Contains(initials.String(), query)
}

func compact(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsSpace(char) {
			return -1
		}
		if char == 'ü' {
			return 'v'
		}
		return char
	}, value)
}
