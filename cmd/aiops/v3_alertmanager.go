package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/wans/aiops-canvas/internal/v3alertmanager"
)

var alertmanagerLabel = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// An Alertmanager endpoint may be shared by multiple V3 environments. The
// configured label is an additional record-level boundary: only silences with
// an exact label=value matcher for the requested environment are returned.
func loadV3AlertmanagerSource(path string, instance v3Instance) (*v3alertmanager.Client, map[string]string, error) {
	if path == "" {
		return nil, nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(contents) > 64<<10 {
		return nil, nil, fmt.Errorf("Alertmanager configuration exceeds 64 KiB")
	}
	var config struct {
		Environments map[string]struct {
			URL              string `json:"url"`
			BearerTokenFile  string `json:"bearer_token_file"`
			AllowHTTP        bool   `json:"allow_http"`
			EnvironmentLabel string `json:"environment_label"`
		} `json:"environments"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || len(config.Environments) == 0 {
		return nil, nil, fmt.Errorf("Alertmanager configuration must contain one nonempty object")
	}
	known := make(map[string]bool, len(instance.Environments))
	for _, env := range instance.Environments {
		known[env.ID] = true
	}
	endpoints := make(map[string]v3alertmanager.Endpoint, len(config.Environments))
	labels := make(map[string]string, len(config.Environments))
	for env, value := range config.Environments {
		if !known[env] || !alertmanagerLabel.MatchString(value.EnvironmentLabel) {
			return nil, nil, fmt.Errorf("invalid Alertmanager environment binding %q", env)
		}
		if instance.Tier == "production" {
			u, err := url.Parse(value.URL)
			if err != nil || u.Scheme != "https" || value.AllowHTTP {
				return nil, nil, fmt.Errorf("production Alertmanager requires HTTPS")
			}
		}
		endpoints[env] = v3alertmanager.Endpoint{URL: value.URL, BearerTokenFile: value.BearerTokenFile, AllowHTTP: value.AllowHTTP}
		labels[env] = value.EnvironmentLabel
	}
	client, err := v3alertmanager.New(endpoints, nil)
	if err != nil {
		return nil, nil, err
	}
	return client, labels, nil
}
