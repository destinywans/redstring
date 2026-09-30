package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"

	"github.com/wans/aiops-canvas/internal/v3alertrules"
)

// The deployment adapter is opt-in. The private file selects exact, reviewed
// rule groups and GitOps targets; its credentials never enter an API response.
// Construction only checks local configuration and credentials. Remote branch,
// file, MR and Argo evidence is checked by the adapter during the workflow.
func loadV3AlertRuleEnableApplier(path string, instance v3Instance,
	ruleSource *v3alertrules.PrometheusSource, ruleBindings []v3alertrules.RuleGroupBinding,
) (*v3alertrules.GitOpsAlertRuleApplier, map[string]bool, error) {
	if path == "" {
		return nil, nil, nil
	}
	if ruleSource == nil || len(ruleBindings) == 0 {
		return nil, nil, errors.New("alert rule enable requires a trusted rule source")
	}
	content, err := readV3RuleConfig(path, 128<<10)
	if err != nil {
		return nil, nil, fmt.Errorf("alert rule enable configuration: %w", err)
	}
	var config struct {
		GitLabURL       string `json:"gitlab_url"`
		GitLabTokenFile string `json:"gitlab_token_file"`
		ArgoURL         string `json:"argo_url"`
		ArgoTokenFile   string `json:"argo_token_file"`
		Bindings        []struct {
			EnvironmentID        string `json:"environment_id"`
			ApplicationID        string `json:"application_id"`
			Group                string `json:"group"`
			ProviderFile         string `json:"provider_file"`
			ProjectID            int64  `json:"project_id"`
			TargetBranch         string `json:"target_branch"`
			YAMLPath             string `json:"yaml_path"`
			RepoURL              string `json:"repo_url"`
			ArgoApplication      string `json:"argo_application"`
			ArgoProject          string `json:"argo_project"`
			ArgoNamespace        string `json:"argo_namespace"`
			ArgoSourcePath       string `json:"argo_source_path"`
			DestinationServer    string `json:"destination_server"`
			DestinationNamespace string `json:"destination_namespace"`
		} `json:"bindings"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, nil, fmt.Errorf("decode alert rule enable configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || len(config.Bindings) == 0 || len(config.Bindings) > 1000 {
		return nil, nil, errors.New("alert rule enable requires one bounded bindings object")
	}
	if !v3AlertRuleEnableHTTPS(config.GitLabURL) || !v3AlertRuleEnableHTTPS(config.ArgoURL) ||
		!filepath.IsAbs(config.GitLabTokenFile) || !filepath.IsAbs(config.ArgoTokenFile) {
		return nil, nil, errors.New("alert rule enable requires HTTPS GitOps endpoints and absolute private token paths")
	}
	knownEnvironments := make(map[string]bool, len(instance.Environments))
	for _, env := range instance.Environments {
		knownEnvironments[env.ID] = true
	}
	trustedGroups := make(map[string]string, len(ruleBindings))
	for _, binding := range ruleBindings {
		key := v3AlertRuleEnableKey(binding.EnvironmentID, binding.ApplicationID, binding.Group)
		if binding.EnvironmentID == "" || binding.ApplicationID == "" || binding.Group == "" || binding.File == "" ||
			!knownEnvironments[binding.EnvironmentID] || trustedGroups[key] != "" {
			return nil, nil, errors.New("alert rule enable source has ambiguous group bindings")
		}
		trustedGroups[key] = binding.File
	}
	bindings := make([]v3alertrules.AlertRuleGitOpsBinding, 0, len(config.Bindings))
	enabledEnvironments := make(map[string]bool)
	for _, item := range config.Bindings {
		if !knownEnvironments[item.EnvironmentID] ||
			trustedGroups[v3AlertRuleEnableKey(item.EnvironmentID, item.ApplicationID, item.Group)] != item.ProviderFile ||
			item.ProviderFile == "" {
			return nil, nil, errors.New("alert rule enable target has no exact trusted source binding")
		}
		enabledEnvironments[item.EnvironmentID] = true
		bindings = append(bindings, v3alertrules.AlertRuleGitOpsBinding{
			EnvironmentID: item.EnvironmentID, ApplicationID: item.ApplicationID,
			Group: item.Group, ProviderFile: item.ProviderFile,
			ProjectID: item.ProjectID, TargetBranch: item.TargetBranch, YAMLPath: item.YAMLPath,
			RepoURL: item.RepoURL, ArgoApplication: item.ArgoApplication,
			ArgoProject: item.ArgoProject, ArgoNamespace: item.ArgoNamespace,
			ArgoSourcePath: item.ArgoSourcePath, DestinationServer: item.DestinationServer,
			DestinationNamespace: item.DestinationNamespace,
		})
	}
	applier, err := v3alertrules.NewGitOpsAlertRuleApplier(v3alertrules.AlertRuleGitOpsConfig{
		GitLabURL: config.GitLabURL, GitLabTokenFile: config.GitLabTokenFile,
		ArgoURL: config.ArgoURL, ArgoTokenFile: config.ArgoTokenFile,
		Bindings: bindings,
	})
	return applier, enabledEnvironments, err
}

func v3AlertRuleEnableKey(env, app, group string) string {
	return env + "\x00" + app + "\x00" + group
}

func v3AlertRuleEnableHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil &&
		u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}
