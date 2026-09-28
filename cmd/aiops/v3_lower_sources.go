package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wans/aiops-canvas/internal/v3lowersources"
)

// Lower-environment source metadata is a trusted production-instance config.
// It contains only Vault references; requests cannot select a source address.
func loadV3LowerSources(path, tier string) (*v3lowersources.Service, error) {
	config := v3lowersources.Config{Tier: tier}
	if path == "" {
		return v3lowersources.New(config)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(content) > 64<<10 {
		return nil, fmt.Errorf("lower-source configuration exceeds 64 KiB")
	}
	var input struct {
		Environments []struct {
			ID    string                   `json:"id"`
			Label v3lowersources.Localized `json:"label"`
		} `json:"environments"`
		Sources []struct {
			ID            string                   `json:"id"`
			Brand         string                   `json:"brand"`
			EnvironmentID string                   `json:"environment_id"`
			Name          v3lowersources.Localized `json:"name"`
			Purpose       v3lowersources.Localized `json:"purpose"`
			Endpoint      string                   `json:"endpoint"`
			VaultRef      string                   `json:"vault_ref"`
		} `json:"sources"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("lower-source configuration must contain one valid JSON object")
	}
	for _, env := range input.Environments {
		config.Environments = append(config.Environments, v3lowersources.Environment{ID: env.ID, Label: env.Label})
	}
	for _, source := range input.Sources {
		config.Sources = append(config.Sources, v3lowersources.Source{
			ID: source.ID, Brand: source.Brand, EnvironmentID: source.EnvironmentID,
			Name: source.Name, Purpose: source.Purpose, Endpoint: source.Endpoint, VaultRef: source.VaultRef,
		})
	}
	return v3lowersources.New(config)
}
