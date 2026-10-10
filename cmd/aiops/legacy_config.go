package main

import (
	"log/slog"
	"os"
)

// legacyOnlyConfig returns a variable only the legacy mode reads. A V3
// instance does not read it (PA-19 P4): a set value is logged with what
// replaces it and an empty value is returned.
func legacyOnlyConfig(v3 bool, name, replacement string) string {
	value := os.Getenv(name)
	if v3 && value != "" {
		slog.Warn(name+" is not read on a V3 instance (PA-19 P4): "+replacement, "variable", name)
		return ""
	}
	return value
}
