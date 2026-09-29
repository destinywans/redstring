package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3events"
)

const maxV3InhibitionHistoryConfigSize = 64 << 10

// loadV3InhibitionHistory binds a complete-history provider to an already
// validated Alertmanager webhook source in each configured environment.
func loadV3InhibitionHistory(path string, instance v3Instance, policies map[string]v3events.GroupPolicyBinding) (v3alertmanager.InhibitionHistorySource, error) {
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("inhibition history config path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() < 1 || before.Size() > maxV3InhibitionHistoryConfigSize {
		return nil, errors.New("inhibition history config must be regular, mode 0600 and at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read inhibition history config")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, errors.New("inhibition history config changed during opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxV3InhibitionHistoryConfigSize+1))
	if err != nil || len(content) > maxV3InhibitionHistoryConfigSize {
		return nil, errors.New("inhibition history config exceeds 64 KiB")
	}
	var config struct {
		Environments map[string]struct {
			URL             string `json:"url"`
			BearerTokenFile string `json:"bearer_token_file"`
			AllowHTTP       bool   `json:"allow_http"`
			SourceID        string `json:"source_id"`
		} `json:"environments"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, errors.New("invalid inhibition history config")
	}
	if decoder.Decode(new(any)) != io.EOF || len(config.Environments) == 0 || len(config.Environments) > 32 {
		return nil, errors.New("inhibition history config requires one object with 1 to 32 environments")
	}
	known := make(map[string]bool, len(instance.Environments))
	for _, environment := range instance.Environments {
		known[environment.ID] = true
	}
	endpoints := make(map[string]v3alertmanager.HistoryEndpoint, len(config.Environments))
	seenSources := make(map[string]bool, len(config.Environments))
	for environment, value := range config.Environments {
		// The history response attests one ingestion stream. A multi-source
		// environment cannot claim complete environment-wide coverage here.
		if !known[environment] || len(policies[environment].SourceIDs) != 1 ||
			!policies[environment].SourceIDs[value.SourceID] || seenSources[value.SourceID] {
			return nil, errors.New("inhibition history source is outside validated environment policy")
		}
		seenSources[value.SourceID] = true
		if instance.Tier == "production" {
			u, err := url.Parse(value.URL)
			if err != nil || u.Scheme != "https" || value.AllowHTTP {
				return nil, errors.New("production inhibition history requires HTTPS")
			}
		}
		endpoints[environment] = v3alertmanager.HistoryEndpoint{
			Endpoint: v3alertmanager.Endpoint{URL: value.URL, BearerTokenFile: value.BearerTokenFile, AllowHTTP: value.AllowHTTP},
			SourceID: value.SourceID,
		}
	}
	return v3alertmanager.NewInhibitionHistoryClient(endpoints, nil)
}
