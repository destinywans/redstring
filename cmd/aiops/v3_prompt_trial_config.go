package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/destinywans/redstring/internal/v3agents"
)

const maxV3PromptTrialConfigSize = 64 << 10

// The model and exact allowed DNS hosts are deployment-owned inputs. Leaving
// both unset keeps paid model calls disabled at the public V3 endpoint.
func loadV3PromptTrialAdapter(path, hostList string, instance v3Instance) (v3agents.PromptTrialModelAdapter, error) {
	if path == "" && hostList == "" {
		return nil, nil
	}
	if path == "" || hostList == "" || !filepath.IsAbs(path) || len(hostList) > 4096 {
		return nil, errors.New("prompt trial model requires an absolute config path and an exact host allowlist")
	}
	hosts := strings.Split(hostList, ",")
	if len(hosts) == 0 || len(hosts) > 32 {
		return nil, errors.New("prompt trial model host allowlist is invalid")
	}
	for _, host := range hosts {
		if host == "" || strings.TrimSpace(host) != host {
			return nil, errors.New("prompt trial model host allowlist is invalid")
		}
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 1 || before.Size() > maxV3PromptTrialConfigSize {
		return nil, errors.New("prompt trial model config must be regular, mode 0600 and at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read prompt trial model config")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, errors.New("prompt trial model config changed during opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxV3PromptTrialConfigSize+1))
	if err != nil || len(content) > maxV3PromptTrialConfigSize {
		return nil, errors.New("prompt trial model config exceeds 64 KiB")
	}
	var scope struct {
		Environments map[string]json.RawMessage `json:"environments"`
	}
	if json.Unmarshal(content, &scope) != nil || len(scope.Environments) == 0 {
		return nil, errors.New("prompt trial model environments are invalid")
	}
	allowedEnvironments := make(map[string]bool, len(instance.Environments))
	for _, environment := range instance.Environments {
		allowedEnvironments[environment.ID] = true
	}
	for environment := range scope.Environments {
		if !allowedEnvironments[environment] {
			return nil, errors.New("prompt trial model environment is outside the V3 instance")
		}
	}
	return v3agents.NewPromptTrialOpenAIAdapter(content, hosts)
}
