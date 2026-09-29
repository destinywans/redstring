package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3events"
)

var alertmanagerLabel = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// An Alertmanager endpoint may be shared by multiple V3 environments. The
// configured label is an additional record-level boundary: only silences with
// an exact label=value matcher for the requested environment are returned.
func loadV3AlertmanagerSource(path string, instance v3Instance) (*v3alertmanager.Client, map[string]string, map[string]map[string]string, error) {
	client, labels, bindings, _, err := loadV3AlertmanagerSourceWithPolicies(path, instance, operations.Config{}, nil)
	return client, labels, bindings, err
}

// loadV3AlertmanagerSourceWithPolicies reads one configuration file and
// returns the source plus only the explicitly reviewed webhook bindings.
// source_ids are never inferred from an endpoint URL or ingress labels alone.
func loadV3AlertmanagerSourceWithPolicies(path string, instance v3Instance, ops operations.Config, db *pgxpool.Pool) (*v3alertmanager.Client, map[string]string, map[string]map[string]string, map[string]v3events.GroupPolicyBinding, error) {
	if path == "" {
		return nil, nil, nil, nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(contents) > 64<<10 {
		return nil, nil, nil, nil, fmt.Errorf("Alertmanager configuration exceeds 64 KiB")
	}
	return parseV3AlertmanagerSourceWithPolicies(contents, instance, ops, db, false)
}

// loadV3AlertmanagerWriteSource is an explicit production write opt-in. It
// uses one verified private configuration snapshot and never reads or returns
// token bytes. The existing read-only loader retains its permissive behavior.
func loadV3AlertmanagerWriteSource(path string, instance v3Instance, ops operations.Config, db *pgxpool.Pool) (*v3alertmanager.Client, map[string]string, map[string]map[string]string, map[string]v3events.GroupPolicyBinding, error) {
	if path == "" {
		return nil, nil, nil, nil, nil
	}
	contents, err := readV3RuleConfig(path, 64<<10)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Alertmanager write configuration: %w", err)
	}
	return parseV3AlertmanagerSourceWithPolicies(contents, instance, ops, db, true)
}

func parseV3AlertmanagerSourceWithPolicies(contents []byte, instance v3Instance, ops operations.Config,
	db *pgxpool.Pool, strictWrite bool) (*v3alertmanager.Client, map[string]string, map[string]map[string]string, map[string]v3events.GroupPolicyBinding, error) {
	var config struct {
		Environments map[string]struct {
			URL                 string            `json:"url"`
			BearerTokenFile     string            `json:"bearer_token_file"`
			AllowHTTP           bool              `json:"allow_http"`
			EnvironmentLabel    string            `json:"environment_label"`
			ApplicationBindings map[string]string `json:"application_bindings"`
			SourceIDs           []string          `json:"source_ids"`
		} `json:"environments"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, nil, nil, nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || len(config.Environments) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("Alertmanager configuration must contain one nonempty object")
	}
	known := make(map[string]bool, len(instance.Environments))
	for _, env := range instance.Environments {
		known[env.ID] = true
	}
	endpoints := make(map[string]v3alertmanager.Endpoint, len(config.Environments))
	labels := make(map[string]string, len(config.Environments))
	bindings := make(map[string]map[string]string, len(config.Environments))
	policies := make(map[string]v3events.GroupPolicyBinding)
	sources := make(map[string]operations.Source, len(ops.Sources))
	for _, source := range ops.Sources {
		if source.ID == "" || sources[source.ID].ID != "" {
			return nil, nil, nil, nil, fmt.Errorf("invalid or duplicate Alertmanager webhook source ID")
		}
		sources[source.ID] = source
	}
	validationCtx, cancelValidation := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelValidation()
	seenSourceIDs := make(map[string]bool)
	for env, value := range config.Environments {
		if strictWrite {
			u, err := url.Parse(value.URL)
			if err != nil || u.Scheme != "https" || value.AllowHTTP ||
				len(value.SourceIDs) == 0 || len(value.ApplicationBindings) == 0 ||
				!privateAlertmanagerTokenFile(value.BearerTokenFile) {
				return nil, nil, nil, nil, fmt.Errorf("Alertmanager write source for %q requires HTTPS, private token and complete bindings", env)
			}
		}
		if !known[env] || !alertmanagerLabel.MatchString(value.EnvironmentLabel) || value.EnvironmentLabel == "application" {
			return nil, nil, nil, nil, fmt.Errorf("invalid Alertmanager environment binding %q", env)
		}
		if len(value.ApplicationBindings) > 500 {
			return nil, nil, nil, nil, fmt.Errorf("Alertmanager application bindings exceed limit for %q", env)
		}
		for label, appID := range value.ApplicationBindings {
			if label == "" || len(label) > 256 || appID == "" || len(appID) > 256 || strings.TrimSpace(label) != label || strings.TrimSpace(appID) != appID {
				return nil, nil, nil, nil, fmt.Errorf("invalid Alertmanager application binding for %q", env)
			}
		}
		if len(value.SourceIDs) > 100 {
			return nil, nil, nil, nil, fmt.Errorf("Alertmanager source IDs exceed limit for %q", env)
		}
		if len(value.SourceIDs) > 0 {
			if db == nil || len(value.ApplicationBindings) == 0 {
				return nil, nil, nil, nil, fmt.Errorf("Alertmanager policy bindings require a database and application bindings for %q", env)
			}
			policy := v3events.GroupPolicyBinding{
				SourceIDs:           make(map[string]bool, len(value.SourceIDs)),
				EnvironmentLabel:    value.EnvironmentLabel,
				ApplicationBindings: value.ApplicationBindings,
			}
			for _, id := range value.SourceIDs {
				if id == "" || len(id) > 256 || strings.TrimSpace(id) != id || seenSourceIDs[id] {
					return nil, nil, nil, nil, fmt.Errorf("invalid or duplicate Alertmanager policy source ID %q", id)
				}
				seenSourceIDs[id] = true
				source, ok := sources[id]
				if !ok || len(source.Mappings) == 0 || len(source.Mappings) > 500 {
					return nil, nil, nil, nil, fmt.Errorf("Alertmanager policy source %q has no bounded registered mappings", id)
				}
				for _, mapping := range source.Mappings {
					appLabel := mapping.Labels["application"]
					if mapping.Labels[value.EnvironmentLabel] != env || appLabel == "" || value.ApplicationBindings[appLabel] != mapping.ApplicationID || mapping.EnvironmentID == "" {
						return nil, nil, nil, nil, fmt.Errorf("Alertmanager policy source %q crosses the configured application or environment", id)
					}
					var active bool
					err := db.QueryRow(validationCtx, `SELECT EXISTS(SELECT 1 FROM application_environments e
						JOIN applications a ON a.id=e.application_id AND NOT a.archived
						JOIN v3_application_environments ve ON ve.application_id=e.application_id AND ve.environment_id=e.key
						WHERE e.id=$1 AND e.application_id=$2 AND e.key=$3 AND NOT e.archived)`,
						mapping.EnvironmentID, mapping.ApplicationID, env).Scan(&active)
					if err != nil {
						return nil, nil, nil, nil, fmt.Errorf("validate Alertmanager policy source %q mapping: %w", id, err)
					}
					if !active {
						return nil, nil, nil, nil, fmt.Errorf("Alertmanager policy source %q has an inactive or mismatched application/environment mapping", id)
					}
				}
				policy.SourceIDs[id] = true
			}
			policies[env] = policy
		}
		if instance.Tier == "production" {
			u, err := url.Parse(value.URL)
			if err != nil || u.Scheme != "https" || value.AllowHTTP {
				return nil, nil, nil, nil, fmt.Errorf("production Alertmanager requires HTTPS")
			}
		}
		endpoints[env] = v3alertmanager.Endpoint{URL: value.URL, BearerTokenFile: value.BearerTokenFile, AllowHTTP: value.AllowHTTP}
		labels[env] = value.EnvironmentLabel
		bindings[env] = value.ApplicationBindings
	}
	client, err := v3alertmanager.New(endpoints, nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return client, labels, bindings, policies, nil
}

func privateAlertmanagerTokenFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 ||
		before.Size() < 1 || before.Size() > 16<<10 {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	opened, err := file.Stat()
	return err == nil && os.SameFile(before, opened) && opened.Mode().IsRegular() &&
		opened.Mode().Perm() == 0600 && opened.Size() > 0 && opened.Size() <= 16<<10
}
