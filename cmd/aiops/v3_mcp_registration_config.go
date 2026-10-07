package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/wans/aiops-canvas/internal/aiextensions"
)

type v3MCPRegistrationBinding struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
	Endpoint      string `json:"endpoint"`
	CredentialRef string `json:"credential_ref"`
	AllowHTTP     bool   `json:"allow_http"`
}

type v3MCPRegistrationFile struct {
	Tier       string                     `json:"tier"`
	InstanceID string                     `json:"instance_id"`
	Bindings   []v3MCPRegistrationBinding `json:"bindings"`
}

var v3MCPBindingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// An empty configuration disables creation. An exact deployment-owned binding
// permits registration and catalog discovery, never approval or tool execution.
// This loader reads no credentials and makes no network requests.
func loadV3MCPRegistrationBindings(path string, instance v3Instance) (func(context.Context, string, string, aiextensions.MCPConfig) error, error) {
	if path == "" {
		return nil, nil
	}
	data, err := readV3MemberPrivateFile(path, 64<<10)
	if err != nil {
		return nil, errors.New("MCP registration bindings require an absolute 0600 regular file of at most 64 KiB")
	}
	var config v3MCPRegistrationFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF ||
		config.Tier != instance.Tier || config.InstanceID != instance.Domain ||
		len(config.Bindings) > 64 {
		return nil, errors.New("invalid MCP registration bindings for this instance")
	}
	environments := map[string]bool{}
	for _, environment := range instance.Environments {
		environments[environment.ID] = true
	}
	allowed := map[v3MCPRegistrationBinding]bool{}
	for _, binding := range config.Bindings {
		u, parseErr := url.Parse(binding.Endpoint)
		if !v3MCPBindingID.MatchString(binding.ApplicationID) || binding.ApplicationID == "all" ||
			!environments[binding.EnvironmentID] || binding.EnvironmentID == "all" ||
			binding.EnvironmentID == "*" || parseErr != nil || u.Hostname() == "" ||
			u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
			strings.TrimSpace(binding.Endpoint) != binding.Endpoint ||
			(binding.CredentialRef != "" && (!v3MCPBindingID.MatchString(binding.CredentialRef) || binding.CredentialRef == "." || binding.CredentialRef == "..")) {
			return nil, errors.New("invalid MCP application, environment or transport binding")
		}
		loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
		if u.Scheme != "https" && !(u.Scheme == "http" && binding.AllowHTTP && loopback) {
			return nil, errors.New("MCP endpoint requires HTTPS or explicitly approved loopback HTTP")
		}
		if allowed[binding] {
			return nil, errors.New("duplicate MCP registration binding")
		}
		allowed[binding] = true
	}
	return func(ctx context.Context, applicationID, environmentID string, transport aiextensions.MCPConfig) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !allowed[v3MCPRegistrationBinding{ApplicationID: applicationID, EnvironmentID: environmentID,
			Endpoint: transport.Endpoint, CredentialRef: transport.CredentialRef, AllowHTTP: transport.AllowHTTP}] {
			return errors.New("MCP registration endpoint is not admitted")
		}
		return nil
	}, nil
}
