package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/wans/aiops-canvas/internal/v3alertrules"
	"github.com/wans/aiops-canvas/internal/v3integrations"
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
		// D11: the write token is a Vault reference read on every request;
		// gitlab_ca_field names the private CA kept next to it.
		GitLabTokenRef string `json:"gitlab_token_ref"`
		GitLabCAField  string `json:"gitlab_ca_field"`
		GitLabWebURL   string `json:"gitlab_web_url"`
		ArgoURL        string `json:"argo_url"`
		ArgoTokenFile  string `json:"argo_token_file"`
		Bindings       []struct {
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
			Load                 string `json:"load"`
			ReadbackTimeout      int    `json:"readback_timeout_seconds"`
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
	argo := false
	for _, item := range config.Bindings {
		if item.Load == "" || item.Load == "argo_cd" {
			argo = true
		}
	}
	if !v3AlertRuleEnableHTTPS(config.GitLabURL) || (config.GitLabTokenFile == "") == (config.GitLabTokenRef == "") ||
		(config.GitLabTokenFile != "" && !filepath.IsAbs(config.GitLabTokenFile)) ||
		(config.GitLabCAField != "" && config.GitLabTokenRef == "") {
		return nil, nil, errors.New("alert rule enable requires an HTTPS GitLab endpoint and one token: an absolute private file or a Vault reference")
	}
	if argo && (!v3AlertRuleEnableHTTPS(config.ArgoURL) || !filepath.IsAbs(config.ArgoTokenFile)) {
		return nil, nil, errors.New("an argo_cd alert rule binding requires an HTTPS Argo CD endpoint and an absolute private token path")
	}
	var token, ca func(context.Context) (string, error)
	if config.GitLabTokenRef != "" {
		// The process Vault token reads only this reviewed reference and its
		// CA sibling, never a connector request's reference.
		ref := config.GitLabTokenRef
		vault := v3integrations.NewVaultResolver(os.Getenv("AIOPS_VAULT_URL"), os.Getenv("AIOPS_VAULT_TOKEN"))
		if _, ok := v3integrations.SiblingReference(ref, "ca"); !ok || vault.Address == "" || vault.Token == "" {
			return nil, nil, errors.New("alert rule enable token reference requires vault://mount/path#field and the process Vault settings")
		}
		token = func(ctx context.Context) (string, error) { return vault.Resolve(ctx, ref) }
		if config.GitLabCAField != "" {
			sibling, ok := v3integrations.SiblingReference(ref, config.GitLabCAField)
			if !ok {
				return nil, nil, errors.New("alert rule enable CA field is invalid")
			}
			ca = func(ctx context.Context) (string, error) { return vault.Resolve(ctx, sibling) }
		}
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
		load := item.Load
		if load == "argo_cd" {
			load = ""
		}
		bindings = append(bindings, v3alertrules.AlertRuleGitOpsBinding{
			EnvironmentID: item.EnvironmentID, ApplicationID: item.ApplicationID,
			Group: item.Group, ProviderFile: item.ProviderFile,
			ProjectID: item.ProjectID, TargetBranch: item.TargetBranch, YAMLPath: item.YAMLPath,
			RepoURL: item.RepoURL, ArgoApplication: item.ArgoApplication,
			ArgoProject: item.ArgoProject, ArgoNamespace: item.ArgoNamespace,
			ArgoSourcePath: item.ArgoSourcePath, DestinationServer: item.DestinationServer,
			DestinationNamespace: item.DestinationNamespace,
			Load:                 load, ReadbackTimeoutSeconds: item.ReadbackTimeout,
		})
	}
	applier, err := v3alertrules.NewGitOpsAlertRuleApplier(v3alertrules.AlertRuleGitOpsConfig{
		GitLabURL: config.GitLabURL, GitLabTokenFile: config.GitLabTokenFile,
		GitLabToken: token, GitLabCA: ca, GitLabWebURL: config.GitLabWebURL,
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
