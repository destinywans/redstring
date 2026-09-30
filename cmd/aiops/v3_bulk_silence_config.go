package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3events"
)

const maxV3BulkGroupSilenceConfigSize = 64 << 10

var (
	v3BulkIdentityLabel    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	v3BulkIdentityRevision = regexp.MustCompile(`^[a-zA-Z0-9._/-]{1,128}$`)
	v3BulkIdentityDigest   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// loadV3BulkGroupSilenceBindings reads deployment-owned, reviewed assertions
// about a dedicated label propagated unchanged to both the authenticated
// ingress and the bound Alertmanager endpoint. The file is an explicit opt-in;
// no identity assertion is inferred from an incident fingerprint or alertname.
// This validates configuration only. The bulk domain rechecks the assertion
// and persisted group evidence when a proposal is submitted or executed.
func loadV3BulkGroupSilenceBindings(path string, instance v3Instance, client *v3alertmanager.Client,
	environmentLabels map[string]string, applicationBindings map[string]map[string]string,
	policies map[string]v3events.GroupPolicyBinding, ops operations.Config,
) (map[string]map[string]map[string]v3events.BulkGroupIdentityBinding, error) {
	if path == "" {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("bulk group silence requires a configured Alertmanager write source")
	}
	content, err := readV3RuleConfig(path, maxV3BulkGroupSilenceConfigSize)
	if err != nil {
		return nil, fmt.Errorf("bulk group silence configuration: %w", err)
	}
	var config struct {
		Bindings []struct {
			EnvironmentID              string    `json:"environment_id"`
			SourceID                   string    `json:"source_id"`
			ApplicationID              string    `json:"application_id"`
			LabelName                  string    `json:"label_name"`
			ConfigRevision             string    `json:"config_revision"`
			AttestationDigest          string    `json:"attestation_digest"`
			EndpointBindingFingerprint string    `json:"endpoint_binding_fingerprint"`
			Attestation                string    `json:"attestation"`
			ExpiresAt                  time.Time `json:"expires_at"`
		} `json:"bindings"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode bulk group silence configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || len(config.Bindings) == 0 || len(config.Bindings) > 500 {
		return nil, errors.New("bulk group silence requires one bounded bindings object")
	}
	knownEnvironments := make(map[string]bool, len(instance.Environments))
	for _, environment := range instance.Environments {
		knownEnvironments[environment.ID] = true
	}
	ingress := make(map[string]operations.Source, len(ops.Sources))
	for _, source := range ops.Sources {
		if source.ID == "" || ingress[source.ID].ID != "" {
			return nil, errors.New("bulk group silence has invalid or duplicate ingress source IDs")
		}
		ingress[source.ID] = source
	}
	bindings := make(map[string]map[string]map[string]v3events.BulkGroupIdentityBinding)
	minExpiry := time.Now().UTC().Add(time.Hour)
	for _, item := range config.Bindings {
		env, sourceID, appID := item.EnvironmentID, item.SourceID, item.ApplicationID
		policy, policyExists := policies[env]
		envLabel := environmentLabels[env]
		if !knownEnvironments[env] || !policyExists || !policy.SourceIDs[sourceID] ||
			envLabel == "" || envLabel != policy.EnvironmentLabel || appID == "" ||
			sourceID == "" || strings.TrimSpace(appID) != appID || strings.TrimSpace(sourceID) != sourceID {
			return nil, fmt.Errorf("bulk group silence binding has an unreviewed environment, source or application: %q/%q/%q", env, sourceID, appID)
		}
		appLabel := ""
		for label, boundApp := range applicationBindings[env] {
			if boundApp != appID {
				continue
			}
			if appLabel != "" {
				return nil, fmt.Errorf("bulk group silence application %q has ambiguous Alertmanager labels", appID)
			}
			appLabel = label
		}
		if appLabel == "" || policy.ApplicationBindings[appLabel] != appID {
			return nil, fmt.Errorf("bulk group silence application %q lacks an exact Alertmanager binding", appID)
		}
		source, ok := ingress[sourceID]
		if !ok || source.TokenFile == "" {
			return nil, fmt.Errorf("bulk group silence source %q is not an authenticated ingress", sourceID)
		}
		if len(item.LabelName) > 128 || !v3BulkIdentityLabel.MatchString(item.LabelName) ||
			strings.HasPrefix(item.LabelName, "__") || item.LabelName == "application" ||
			item.LabelName == "alertname" || item.LabelName == envLabel ||
			!v3BulkIdentityRevision.MatchString(item.ConfigRevision) ||
			!v3BulkIdentityDigest.MatchString(item.AttestationDigest) ||
			!v3BulkIdentityDigest.MatchString(item.EndpointBindingFingerprint) ||
			item.Attestation != "upstream-equality-unique-stable-v1" ||
			!item.ExpiresAt.After(minExpiry) {
			return nil, fmt.Errorf("bulk group silence binding has invalid or expired label attestation for %q/%q/%q", env, sourceID, appID)
		}
		matchingMappings := 0
		for _, mapping := range source.Mappings {
			if mapping.ApplicationID != appID || mapping.Labels["application"] != appLabel || mapping.Labels[envLabel] != env {
				continue
			}
			matchingMappings++
			if mapping.EnvironmentID == "" || mapping.Labels[item.LabelName] != "" {
				return nil, fmt.Errorf("bulk group silence ingress %q has an invalid or fixed identity label", sourceID)
			}
		}
		if matchingMappings != 1 {
			return nil, fmt.Errorf("bulk group silence ingress %q requires one exact application/environment mapping", sourceID)
		}
		fingerprint, err := client.CancelBindingFingerprint(env, envLabel, appLabel, appID)
		if err != nil || fingerprint != item.EndpointBindingFingerprint {
			return nil, fmt.Errorf("bulk group silence binding has a stale Alertmanager endpoint fingerprint for %q/%q/%q", env, sourceID, appID)
		}
		if bindings[env] == nil {
			bindings[env] = make(map[string]map[string]v3events.BulkGroupIdentityBinding)
		}
		if bindings[env][sourceID] == nil {
			bindings[env][sourceID] = make(map[string]v3events.BulkGroupIdentityBinding)
		}
		if _, duplicate := bindings[env][sourceID][appID]; duplicate {
			return nil, fmt.Errorf("duplicate bulk group silence binding for %q/%q/%q", env, sourceID, appID)
		}
		bindings[env][sourceID][appID] = v3events.BulkGroupIdentityBinding{
			LabelName:                  item.LabelName,
			ConfigRevision:             item.ConfigRevision,
			AttestationDigest:          item.AttestationDigest,
			EndpointBindingFingerprint: item.EndpointBindingFingerprint,
			Attestation:                item.Attestation,
			ExpiresAt:                  item.ExpiresAt.UTC().Truncate(time.Microsecond),
		}
	}
	return bindings, nil
}
