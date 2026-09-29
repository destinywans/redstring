package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	maxRuleImportBytes = 128 << 10
	maxImportGroups    = 10
	maxImportRules     = 20
)

var (
	ErrUnsupportedDraftMode        = errors.New("unsupported alert rule mode")
	ErrInvalidPromQL               = errors.New("invalid PromQL syntax")
	ErrPromQLValidationUnavailable = errors.New("PromQL syntax validation unavailable")
	alertRuleNamePattern           = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	alertLabelNamePattern          = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	importDNSSubdomainPattern      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
	importDNSLabelPattern          = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

// DraftRuleSpec is an untrusted, draft-only alert rule definition. Its scope
// must be checked against the request's authorized environment and application.
type DraftRuleSpec struct {
	ApplicationID  string            `json:"application_id" yaml:"application_id"`
	EnvironmentID  string            `json:"environment_id" yaml:"environment_id"`
	Group          string            `json:"group" yaml:"group"`
	Name           string            `json:"name" yaml:"name"`
	Expr           string            `json:"expr" yaml:"expr"`
	For            string            `json:"for" yaml:"for"`
	Labels         map[string]string `json:"labels" yaml:"labels"`
	Annotations    map[string]string `json:"annotations" yaml:"annotations"`
	Mode           string            `json:"mode,omitempty" yaml:"mode,omitempty"`
	ObservedRuleID string            `json:"observed_rule_id,omitempty" yaml:"observed_rule_id,omitempty"`
}

func validateDraftSpec(spec DraftRuleSpec, env, app string) error {
	if spec.Mode != "" && spec.Mode != "draft" {
		return ErrUnsupportedDraftMode
	}
	if env == "" || app == "" || spec.EnvironmentID != env || spec.ApplicationID != app ||
		len(spec.Group) == 0 || len(spec.Group) > 128 || strings.TrimSpace(spec.Group) != spec.Group || strings.ContainsAny(spec.Group, "\x00\r\n") ||
		len(spec.Name) == 0 || len(spec.Name) > 128 || !alertRuleNamePattern.MatchString(spec.Name) ||
		len(spec.Expr) == 0 || len(spec.Expr) > 8192 || strings.TrimSpace(spec.Expr) == "" ||
		len(spec.For) == 0 || len(spec.For) > 32 {
		return errors.New("invalid alert rule draft")
	}
	duration, err := time.ParseDuration(spec.For)
	if err != nil || duration < 0 || duration > time.Hour {
		return errors.New("invalid alert rule for duration")
	}
	if len(spec.Labels) > 32 || len(spec.Annotations) > 32 ||
		spec.Labels["application"] != app || spec.Labels["environment"] != env {
		return errors.New("invalid alert rule labels")
	}
	severity := spec.Labels["severity"]
	if severity != "P1" && severity != "P2" && severity != "P3" && severity != "P4" {
		return errors.New("invalid alert rule severity")
	}
	for key, value := range spec.Labels {
		if len(key) > 128 || !alertLabelNamePattern.MatchString(key) || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("invalid alert rule label")
		}
		if severity == "P1" && isSuppressionConfigKey(key) {
			return errors.New("P1 alert rule cannot configure suppression or night downgrade")
		}
	}
	for key, value := range spec.Annotations {
		if len(key) > 128 || !alertLabelNamePattern.MatchString(key) || len(value) > 4096 || strings.ContainsRune(value, '\x00') {
			return errors.New("invalid alert rule annotation")
		}
		if severity == "P1" && isSuppressionConfigKey(key) {
			return errors.New("P1 alert rule cannot configure suppression or night downgrade")
		}
	}
	return nil
}

func isSuppressionConfigKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	return strings.Contains(key, "suppress") || strings.Contains(key, "silence") ||
		strings.Contains(key, "mute") || strings.Contains(key, "downgrade") ||
		strings.Contains(key, "night")
}

// parseRuleImportYAML accepts only the alerting-rule subset that maps without
// loss to DraftRuleSpec. It does not apply or evaluate imported rules.
func parseRuleImportYAML(raw, env, app string) ([]DraftRuleSpec, error) {
	_, rules, err := parseRuleImportYAMLWithApp(raw, env, app)
	return rules, err
}

// parseRuleImportYAMLWithApp also accepts the Prometheus Operator PrometheusRule
// envelope. Metadata identifies the imported source, never its deployment
// target; application and environment binding comes from rule labels.
func parseRuleImportYAMLWithApp(raw, env, app string) (string, []DraftRuleSpec, error) {
	if len(raw) == 0 || len(raw) > maxRuleImportBytes {
		return "", nil, errors.New("invalid alert rule import size")
	}
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil || len(doc.Content) != 1 {
		return "", nil, errors.New("invalid alert rule YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", nil, errors.New("alert rule import must contain one YAML document")
	}
	if err := checkImportYAMLNode(&doc); err != nil {
		return "", nil, err
	}
	groupsNode, err := importGroupsNode(doc.Content[0])
	if err != nil {
		return "", nil, err
	}
	groups, err := importSequence(groupsNode, maxImportGroups)
	if err != nil || len(groups) == 0 {
		return "", nil, errors.New("invalid alert rule groups")
	}
	result := make([]DraftRuleSpec, 0)
	for _, groupNode := range groups {
		group, err := importMapping(groupNode, "name", "rules")
		if err != nil {
			return "", nil, err
		}
		name, err := importString(group["name"])
		if err != nil {
			return "", nil, err
		}
		rules, err := importSequence(group["rules"], maxImportRules)
		if err != nil || len(rules) == 0 {
			return "", nil, errors.New("invalid alert rule group rules")
		}
		for _, ruleNode := range rules {
			if len(result) == maxImportRules {
				return "", nil, errors.New("alert rule import limit exceeded")
			}
			rule, err := importMapping(ruleNode, "alert", "expr", "for", "labels", "annotations")
			if err != nil {
				return "", nil, err
			}
			alert, err := importString(rule["alert"])
			if err != nil {
				return "", nil, err
			}
			expr, err := importString(rule["expr"])
			if err != nil {
				return "", nil, err
			}
			forText := "0s"
			if rule["for"] != nil {
				forText, err = importString(rule["for"])
				if err != nil {
					return "", nil, err
				}
			}
			labels, err := importStringMap(rule["labels"])
			if err != nil {
				return "", nil, err
			}
			annotations, err := importStringMap(rule["annotations"])
			if err != nil {
				return "", nil, err
			}
			ruleApp := labels["application"]
			if ruleApp == "" || (app != "" && ruleApp != app) {
				return "", nil, errors.New("alert rule application label does not match requested application")
			}
			if app == "" {
				app = ruleApp
			}
			spec := DraftRuleSpec{ApplicationID: app, EnvironmentID: env, Group: name, Name: alert,
				Expr: expr, For: forText, Labels: labels, Annotations: annotations, Mode: "draft"}
			if err := validateDraftSpec(spec, env, app); err != nil {
				return "", nil, err
			}
			result = append(result, spec)
		}
	}
	return app, result, nil
}

func importGroupsNode(node *yaml.Node) (*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("invalid alert rule YAML object")
	}
	// A native rule file has only groups. A CRD must carry the exact known
	// envelope; accepting either shape prevents silently discarding fields.
	crd := false
	for i := 0; i < len(node.Content); i += 2 {
		switch node.Content[i].Value {
		case "apiVersion", "kind", "metadata", "spec":
			crd = true
		}
	}
	if !crd {
		root, err := importMapping(node, "groups")
		if err != nil {
			return nil, err
		}
		return root["groups"], nil
	}
	root, err := importMapping(node, "apiVersion", "kind", "metadata", "spec")
	if err != nil {
		return nil, err
	}
	version, err := importString(root["apiVersion"])
	if err != nil || version != "monitoring.coreos.com/v1" {
		return nil, errors.New("unsupported PrometheusRule apiVersion")
	}
	kind, err := importString(root["kind"])
	if err != nil || kind != "PrometheusRule" {
		return nil, errors.New("unsupported alert rule kind")
	}
	metadata, err := importMapping(root["metadata"], "name", "namespace")
	if err != nil {
		return nil, err
	}
	name, err := importString(metadata["name"])
	if err != nil || !validImportDNSSubdomain(name) {
		return nil, errors.New("invalid PrometheusRule metadata name")
	}
	if namespace := metadata["namespace"]; namespace != nil {
		value, err := importString(namespace)
		if err != nil || len(value) > 63 || !importDNSLabelPattern.MatchString(value) {
			return nil, errors.New("invalid PrometheusRule metadata namespace")
		}
	}
	spec, err := importMapping(root["spec"], "groups")
	if err != nil {
		return nil, err
	}
	return spec["groups"], nil
}

func validImportDNSSubdomain(value string) bool {
	if len(value) == 0 || len(value) > 253 || !importDNSSubdomainPattern.MatchString(value) {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || !importDNSLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func checkImportYAMLNode(node *yaml.Node) error {
	count := 0
	return checkImportYAMLNodeBounded(node, 0, &count)
}

func checkImportYAMLNodeBounded(node *yaml.Node, depth int, count *int) error {
	*count = *count + 1
	if depth > 16 || *count > 2000 {
		return errors.New("alert rule YAML nesting or node limit exceeded")
	}
	if node == nil || node.Kind == yaml.AliasNode || node.Anchor != "" ||
		(node.Tag != "" && node.Tag != "!!map" && node.Tag != "!!seq" && node.Tag != "!!str" && node.Tag != "!!int" && node.Tag != "!!float" && node.Tag != "!!bool" && node.Tag != "!!null") {
		return errors.New("unsupported alert rule YAML alias or tag")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return errors.New("duplicate or invalid alert rule YAML key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := checkImportYAMLNodeBounded(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}

func importMapping(node *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("invalid alert rule YAML object")
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		valid := false
		for _, candidate := range allowed {
			if key == candidate {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("unknown alert rule YAML field %q", key)
		}
		fields[key] = node.Content[i+1]
	}
	return fields, nil
}

func importSequence(node *yaml.Node, limit int) ([]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) > limit {
		return nil, errors.New("invalid alert rule YAML sequence")
	}
	return node.Content, nil
}

func importString(node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", errors.New("invalid alert rule YAML string")
	}
	return node.Value, nil
}

func importStringMap(node *yaml.Node) (map[string]string, error) {
	if node == nil {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode || len(node.Content) > 64 {
		return nil, errors.New("invalid alert rule YAML labels or annotations")
	}
	result := make(map[string]string, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		value, err := importString(node.Content[i+1])
		if err != nil {
			return nil, err
		}
		result[node.Content[i].Value] = value
	}
	return result, nil
}

// ValidatePromQL asks the configured Prometheus parser to format an expression.
// This API parses syntax without evaluating the query against metric data.
func (s *PrometheusSource) ValidatePromQL(ctx context.Context, environment, expression string) error {
	if len(expression) == 0 || len(expression) > 8192 || strings.TrimSpace(expression) == "" {
		return ErrInvalidPromQL
	}
	if s == nil {
		return ErrPromQLValidationUnavailable
	}
	endpoint, ok := s.endpoints[environment]
	if !ok || s.client == nil {
		return ErrPromQLValidationUnavailable
	}
	form := url.Values{"query": {expression}}.Encode()
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost,
		strings.TrimRight(endpoint.URL, "/")+"/api/v1/format_query", strings.NewReader(form))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPromQLValidationUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if endpoint.BearerTokenFile != "" {
		b, err := os.ReadFile(endpoint.BearerTokenFile)
		if err != nil {
			return fmt.Errorf("%w: token unavailable", ErrPromQLValidationUnavailable)
		}
		token := strings.TrimSpace(string(b))
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return ErrPromQLValidationUnavailable
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPromQLValidationUnavailable, err)
	}
	defer resp.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(contents) > 64<<10 {
		return ErrPromQLValidationUnavailable
	}
	var envelope struct {
		Status    string          `json:"status"`
		Data      json.RawMessage `json:"data"`
		ErrorType string          `json:"errorType"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrPromQLValidationUnavailable
	}
	if resp.StatusCode == http.StatusBadRequest && envelope.Status == "error" && envelope.ErrorType == "bad_data" {
		return ErrInvalidPromQL
	}
	if resp.StatusCode != http.StatusOK || envelope.Status != "success" {
		return ErrPromQLValidationUnavailable
	}
	var formatted string
	if json.Unmarshal(envelope.Data, &formatted) != nil || formatted == "" {
		return ErrPromQLValidationUnavailable
	}
	return nil
}
