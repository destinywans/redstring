package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadV3PushSources(t *testing.T) {
	if cfg, err := loadV3PushSources(""); err != nil || len(cfg.Sources) != 0 || cfg.PublicHost != "" {
		t.Fatalf("unset Push config = %#v, %v", cfg, err)
	}
	for _, tc := range []struct {
		name      string
		json      string
		wantError bool
	}{
		{"valid", `{"public_host":"aiops.example.invalid","sources":[]}`, false},
		{"unknown field", `{"sources":[],"token":"unexpected"}`, true},
		{"null", `null`, true},
		{"trailing object", `{} {}`, true},
		{"oversized", `{"sources":[],"padding":"` + strings.Repeat("x", 64<<10) + `"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "push.json")
			if err := os.WriteFile(path, []byte(tc.json), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := loadV3PushSources(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("config = %#v, error = %v, wantError = %v", cfg, err, tc.wantError)
			}
			if !tc.wantError && cfg.PublicHost != "aiops.example.invalid" {
				t.Fatalf("public host = %q", cfg.PublicHost)
			}
		})
	}
}
