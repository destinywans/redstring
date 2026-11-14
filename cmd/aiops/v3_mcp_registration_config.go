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

	"github.com/destinywans/redstring/internal/aiextensions"
)

type v3MCPRegistrationBinding struct {
	ApplicationID string   `json:"application_id"`
	EnvironmentID string   `json:"environment_id"`
	Endpoint      string   `json:"endpoint"`
	CredentialRef string   `json:"credential_ref"`
	AllowHTTP     bool     `json:"allow_http"`
	ReadTools     []string `json:"read_tools,omitempty"`
}

type v3MCPRegistrationFile struct {
	Tier       string                     `json:"tier"`
	InstanceID string                     `json:"instance_id"`
	Bindings   []v3MCPRegistrationBinding `json:"bindings"`
}

var v3MCPBindingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type v3MCPBindingKey struct {
	applicationID, environmentID, endpoint, credentialRef string
	allowHTTP                                             bool
}

type v3MCPAdmissions struct {
	endpoint func(context.Context, string, string, aiextensions.MCPConfig) error
	readTool func(context.Context, string, string, aiextensions.MCPConfig, string) bool
}

// An empty configuration disables creation. An exact deployment-owned binding
// permits registration and catalog discovery. Read tool admission is separate.
// This loader reads no credentials and makes no network requests.
func loadV3MCPRegistrationBindings(path string, instance v3Instance) (func(context.Context, string, string, aiextensions.MCPConfig) error, error) {
	admission, err := loadV3MCPAdmissions(path, instance)
	return admission.endpoint, err
}

func loadV3MCPAdmissions(path string, instance v3Instance) (v3MCPAdmissions, error) {
	if path == "" {
		return v3MCPAdmissions{}, nil
	}
	data, err := readV3MemberPrivateFile(path, 64<<10)
	if err != nil {
		return v3MCPAdmissions{}, errors.New("MCP registration bindings require an absolute 0600 regular file of at most 64 KiB")
	}
	var config v3MCPRegistrationFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF ||
		config.Tier != instance.Tier || config.InstanceID != instance.Domain ||
		len(config.Bindings) > 64 {
		return v3MCPAdmissions{}, errors.New("invalid MCP registration bindings for this instance")
	}
	environments := map[string]bool{}
	for _, environment := range instance.Environments {
		environments[environment.ID] = true
	}
	allowed := map[v3MCPBindingKey]map[string]bool{}
	for _, binding := range config.Bindings {
		u, parseErr := url.Parse(binding.Endpoint)
		if !v3MCPBindingID.MatchString(binding.ApplicationID) || binding.ApplicationID == "all" ||
			!environments[binding.EnvironmentID] || binding.EnvironmentID == "all" ||
			binding.EnvironmentID == "*" || parseErr != nil || u.Hostname() == "" ||
			u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
			strings.TrimSpace(binding.Endpoint) != binding.Endpoint ||
			(binding.CredentialRef != "" && (!v3MCPBindingID.MatchString(binding.CredentialRef) || binding.CredentialRef == "." || binding.CredentialRef == "..")) {
			return v3MCPAdmissions{}, errors.New("invalid MCP application, environment or transport binding")
		}
		loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
		if u.Scheme != "https" && !(u.Scheme == "http" && binding.AllowHTTP && loopback) {
			return v3MCPAdmissions{}, errors.New("MCP endpoint requires HTTPS or explicitly approved loopback HTTP")
		}
		key := v3MCPBindingKey{binding.ApplicationID, binding.EnvironmentID, binding.Endpoint, binding.CredentialRef, binding.AllowHTTP}
		if _, duplicate := allowed[key]; duplicate {
			return v3MCPAdmissions{}, errors.New("duplicate MCP registration binding")
		}
		if len(binding.ReadTools) > 64 {
			return v3MCPAdmissions{}, errors.New("too many admitted MCP read tools")
		}
		tools := map[string]bool{}
		for _, tool := range binding.ReadTools {
			if !v3MCPBindingID.MatchString(tool) || len(tool) > 100 || tools[tool] {
				return v3MCPAdmissions{}, errors.New("invalid admitted MCP read tool")
			}
			tools[tool] = true
		}
		allowed[key] = tools
	}
	key := func(app, env string, transport aiextensions.MCPConfig) v3MCPBindingKey {
		return v3MCPBindingKey{app, env, transport.Endpoint, transport.CredentialRef, transport.AllowHTTP}
	}
	return v3MCPAdmissions{endpoint: func(ctx context.Context, applicationID, environmentID string, transport aiextensions.MCPConfig) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, ok := allowed[key(applicationID, environmentID, transport)]; !ok {
			return errors.New("MCP registration endpoint is not admitted")
		}
		return nil
	}, readTool: func(ctx context.Context, app, env string, transport aiextensions.MCPConfig, tool string) bool {
		return ctx.Err() == nil && allowed[key(app, env, transport)][tool]
	}}, nil
}
