package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wans/aiops-canvas/internal/v3alertrules"
)

// Rule endpoints and exact rule groups are bound by trusted server
// configuration. A request or a Prometheus rule label cannot select ownership.
func loadV3AlertRuleSource(path string, instance v3Instance) (*v3alertrules.PrometheusSource, []v3alertrules.RuleGroupBinding, error) {
	if path == "" {
		return nil, nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	var config struct {
		Environments map[string]struct {
			URL             string `json:"url"`
			BearerTokenFile string `json:"bearer_token_file"`
			AllowHTTP       bool   `json:"allow_http"`
		} `json:"environments"`
		Bindings []struct {
			EnvironmentID string `json:"environment_id"`
			ApplicationID string `json:"application_id"`
			File          string `json:"file"`
			Group         string `json:"group"`
			// D32: the rules' "application" label value, the key the alert
			// source mappings use; empty keeps the application ID.
			ApplicationLabel string `json:"application_label"`
		} `json:"bindings"`
	}
	contents, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(contents) > 64<<10 {
		return nil, nil, fmt.Errorf("alert rule configuration exceeds 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || len(config.Environments) == 0 || len(config.Bindings) == 0 {
		return nil, nil, fmt.Errorf("alert rule configuration must contain one nonempty object")
	}
	known := make(map[string]bool, len(instance.Environments))
	for _, env := range instance.Environments {
		known[env.ID] = true
	}
	endpoints := make(map[string]v3alertrules.PrometheusEndpoint, len(config.Environments))
	for env, value := range config.Environments {
		if !known[env] || strings.TrimSpace(value.URL) == "" ||
			(value.BearerTokenFile != "" && !filepath.IsAbs(value.BearerTokenFile)) {
			return nil, nil, fmt.Errorf("invalid alert rule source for environment %q", env)
		}
		endpoints[env] = v3alertrules.PrometheusEndpoint{
			URL: value.URL, BearerTokenFile: value.BearerTokenFile, AllowHTTP: value.AllowHTTP,
		}
	}
	bindings := make([]v3alertrules.RuleGroupBinding, 0, len(config.Bindings))
	for _, value := range config.Bindings {
		if !known[value.EnvironmentID] || endpoints[value.EnvironmentID].URL == "" {
			return nil, nil, fmt.Errorf("alert rule group has no bound environment source")
		}
		bindings = append(bindings, v3alertrules.RuleGroupBinding{
			EnvironmentID: value.EnvironmentID, ApplicationID: value.ApplicationID,
			File: value.File, Group: value.Group, ApplicationLabel: value.ApplicationLabel,
		})
	}
	source, err := v3alertrules.NewPrometheusSource(endpoints, nil)
	return source, bindings, err
}
