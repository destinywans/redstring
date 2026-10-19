package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/destinywans/redstring/internal/v3ruleassistant"
)

const (
	maxV3RulePreviewConfigSize = 128 << 10
	maxV3RuleModelConfigSize   = 64 << 10
)

// Both adapters are deployment-owned and default off. A model binding needs a
// reviewed preview catalog in the same environment; neither adapter calls its
// upstream source during startup.
func loadV3RuleAssistantSources(previewPath, previewHosts, modelPath, modelHosts string, instance v3Instance) (v3ruleassistant.PreviewReader, v3ruleassistant.GenerateModel, error) {
	if previewPath == "" && previewHosts == "" && modelPath == "" && modelHosts == "" {
		return nil, nil, nil
	}
	if (previewPath == "") != (previewHosts == "") || (modelPath == "") != (modelHosts == "") {
		return nil, nil, errors.New("rule assistant config requires each path and exact host allowlist together")
	}
	if modelPath != "" && previewPath == "" {
		return nil, nil, errors.New("rule draft model requires a reviewed preview catalog")
	}
	known := make(map[string]bool, len(instance.Environments))
	for _, environment := range instance.Environments {
		known[environment.ID] = true
	}
	var preview v3ruleassistant.PreviewReader
	if previewPath != "" {
		content, err := readV3RuleConfig(previewPath, maxV3RulePreviewConfigSize)
		if err != nil {
			return nil, nil, fmt.Errorf("rule preview config: %w", err)
		}
		hosts, err := v3RuleAllowedHosts(previewHosts, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("rule preview hosts: %w", err)
		}
		var scope struct {
			Sources []struct {
				EnvironmentID string `json:"environment_id"`
				ApplicationID string `json:"application_id"`
			} `json:"sources"`
		}
		if err := decodeV3RuleScope(content, &scope); err != nil || len(scope.Sources) == 0 || len(scope.Sources) > 64 {
			return nil, nil, errors.New("rule preview source bindings are invalid")
		}
		for _, source := range scope.Sources {
			if !known[source.EnvironmentID] || source.ApplicationID == "" {
				return nil, nil, errors.New("rule preview source is outside the V3 instance")
			}
		}
		preview, err = v3ruleassistant.NewPrometheusPreviewReader(content, hosts)
		if err != nil {
			return nil, nil, fmt.Errorf("rule preview source: %w", err)
		}
	}
	if modelPath == "" {
		return preview, nil, nil
	}
	content, err := readV3RuleConfig(modelPath, maxV3RuleModelConfigSize)
	if err != nil {
		return nil, nil, fmt.Errorf("rule draft model config: %w", err)
	}
	hosts, err := v3RuleAllowedHosts(modelHosts, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("rule draft model hosts: %w", err)
	}
	var scope struct {
		Environments map[string]json.RawMessage `json:"environments"`
	}
	if err := decodeV3RuleScope(content, &scope); err != nil || len(scope.Environments) == 0 || len(scope.Environments) > 32 {
		return nil, nil, errors.New("rule draft model environment bindings are invalid")
	}
	for environment := range scope.Environments {
		if !known[environment] || len(preview.Bindings(environment)) == 0 {
			return nil, nil, errors.New("rule draft model environment lacks a reviewed preview catalog")
		}
	}
	model, err := v3ruleassistant.NewRuleDraftOpenAIAdapter(content, hosts)
	if err != nil {
		return nil, nil, fmt.Errorf("rule draft model: %w", err)
	}
	return preview, model, nil
}

func readV3RuleConfig(path string, maxSize int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 1 || before.Size() > maxSize {
		return nil, errors.New("file must be regular, mode 0600 and within its size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, errors.New("file changed during opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil || len(content) == 0 || int64(len(content)) > maxSize {
		return nil, errors.New("file exceeds its size limit")
	}
	return content, nil
}

func v3RuleAllowedHosts(list string, max int) ([]string, error) {
	if len(list) > 4096 {
		return nil, errors.New("host allowlist is too long")
	}
	hosts := strings.Split(list, ",")
	if len(hosts) == 0 || len(hosts) > max {
		return nil, errors.New("host allowlist size is invalid")
	}
	seen := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		if host == "" || strings.TrimSpace(host) != host || host != strings.ToLower(host) || strings.ContainsAny(host, "/:@* \\\r\n\x00") || seen[host] {
			return nil, errors.New("host allowlist requires distinct exact DNS names")
		}
		seen[host] = true
	}
	return hosts, nil
}

// Keep this file self-contained so JSON scope checks reject trailing objects
// before constructing adapters, whose own strict decoders validate all fields.
func decodeV3RuleScope(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
