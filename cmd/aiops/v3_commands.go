package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wans/aiops-canvas/internal/v3discovery"
)

// A missing path means the suggestions are unavailable, while an explicit []
// is a trusted empty catalog. Commands insert text; they execute nothing.
func loadV3TextCommands(path string, environments []string) ([]v3discovery.TextCommand, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("text command catalog requires an absolute path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read text command catalog: %w", err)
	}
	defer f.Close()
	const maxBytes = 16 << 10
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || len(raw) > maxBytes {
		return nil, fmt.Errorf("text command catalog exceeds 16 KiB")
	}
	var rows []struct {
		EnvironmentID string `json:"environment_id"`
		ID            string `json:"id"`
		Name          string `json:"name"`
		Module        string `json:"module"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rows); err != nil || rows == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid text command catalog")
	}
	known := make(map[string]bool, len(environments))
	for _, env := range environments {
		known[env] = true
	}
	items := make([]v3discovery.TextCommand, 0, len(rows))
	for _, row := range rows {
		if !known[row.EnvironmentID] {
			return nil, fmt.Errorf("text command has unknown environment")
		}
		items = append(items, v3discovery.TextCommand{
			EnvironmentID: row.EnvironmentID, ID: row.ID, Name: row.Name, Module: row.Module,
		})
	}
	return items, nil
}
