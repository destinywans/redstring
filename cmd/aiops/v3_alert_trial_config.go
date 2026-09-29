package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3alertrules"
)

// An unset private file leaves dry_run disabled. A configured trial must have
// an exact application/environment rule binding and a separate HTTPS query
// credential. The attestation is an operator claim about upstream data-plane
// isolation; this composition cannot prove it from a URL or a label filter.
func loadV3AlertTrialRuntime(path string, cfg httpapi.V3InstanceConfig,
	ruleSource *v3alertrules.PrometheusSource, bindings []v3alertrules.RuleGroupBinding,
	writer *v3alertrules.WriteService) (*v3alertrules.PrometheusTrialRuntime, error) {
	if path == "" {
		return nil, nil
	}
	if writer == nil || ruleSource == nil || len(bindings) == 0 ||
		(cfg.Tier != "production" && cfg.Tier != "nonprod") {
		return nil, errors.New("trial requires an active rule source and V3 instance")
	}
	contents, err := readV3RuleConfig(path, 64<<10)
	if err != nil {
		return nil, fmt.Errorf("trial private config: %w", err)
	}
	endpoints, err := v3alertrules.ParsePrometheusTrialEndpoints(contents)
	if err != nil {
		return nil, err
	}
	knownEnvs := make(map[string]bool, len(cfg.EnvironmentIDs))
	for _, env := range cfg.EnvironmentIDs {
		knownEnvs[env] = true
	}
	bound := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		bound[binding.EnvironmentID+"\x00"+binding.ApplicationID] = true
	}
	for _, endpoint := range endpoints {
		if !knownEnvs[endpoint.EnvironmentID] ||
			!bound[endpoint.EnvironmentID+"\x00"+endpoint.ApplicationID] ||
			endpoint.AllowHTTP || endpoint.BearerTokenFile == "" ||
			ruleSource.TrialSourceConflicts(endpoint) {
			return nil, errors.New("trial endpoint lacks an exact HTTPS rule binding and private credential")
		}
		token, err := readV3RuleConfig(endpoint.BearerTokenFile, 16<<10)
		if err != nil || strings.TrimSpace(string(token)) == "" ||
			strings.ContainsAny(strings.TrimSpace(string(token)), "\r\n") {
			return nil, errors.New("trial credential must be a readable private file")
		}
	}
	return v3alertrules.NewPrometheusTrialRuntime(writer, endpoints, nil)
}
