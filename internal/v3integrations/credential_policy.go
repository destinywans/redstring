package v3integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CredentialUse identifies the connector operation allowed to read a credential.
// It is supplied by the server after resolving the connector's environment and
// configuration, never from a client-supplied credential request.
type CredentialUse struct {
	Tier          string
	EnvironmentID string
	ConnectorID   string
	Provider      string
	Endpoint      string
}

type credentialUseKey struct{}

func WithCredentialUse(ctx context.Context, use CredentialUse) context.Context {
	return context.WithValue(ctx, credentialUseKey{}, use)
}

// CredentialUseFrom returns the use a context carries.
func CredentialUseFrom(ctx context.Context) (CredentialUse, bool) {
	use, ok := ctx.Value(credentialUseKey{}).(CredentialUse)
	return use, ok
}

// CredentialBinding authorizes one exact connector configuration to resolve one
// Vault reference. The reference and any resolved secret must never be logged.
type CredentialBinding struct {
	Tier          string `json:"tier"`
	EnvironmentID string `json:"environment_id"`
	ConnectorID   string `json:"connector_id"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	CredentialRef string `json:"credential_ref"`
}

const maxCredentialBindings = 200

// ParseCredentialBindings accepts an optional JSON array of exact bindings.
// Unknown fields, invalid rows and duplicates fail startup configuration.
func ParseCredentialBindings(raw string) ([]CredentialBinding, error) {
	if strings.TrimSpace(raw) == "" {
		return []CredentialBinding{}, nil
	}
	var bindings []CredentialBinding
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bindings); err != nil || bindings == nil {
		return nil, errors.New("invalid credential bindings JSON array")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid trailing credential bindings data")
	}
	if len(bindings) > maxCredentialBindings {
		return nil, fmt.Errorf("credential bindings exceed limit of %d", maxCredentialBindings)
	}
	if duplicateCredentialBindingKey(raw) {
		return nil, errors.New("duplicate credential binding field")
	}
	seen := make(map[CredentialBinding]struct{}, len(bindings))
	for i, binding := range bindings {
		canonical, ok := validCredentialBinding(binding)
		if !ok {
			return nil, fmt.Errorf("invalid credential binding at index %d", i)
		}
		if _, duplicate := seen[canonical]; duplicate {
			return nil, fmt.Errorf("duplicate credential binding at index %d", i)
		}
		bindings[i] = canonical
		seen[canonical] = struct{}{}
	}
	return bindings, nil
}

func duplicateCredentialBindingKey(raw string) bool {
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return false // The strict decoder above reports malformed JSON.
	}
	for _, row := range rows {
		decoder := json.NewDecoder(bytes.NewReader(row))
		start, err := decoder.Token()
		if err != nil || start != json.Delim('{') {
			continue // Non-object entries fail binding validation below.
		}
		seen := make(map[string]struct{})
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				return false
			}
			if _, duplicate := seen[key]; duplicate {
				return true
			}
			seen[key] = struct{}{}
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				return false
			}
		}
	}
	return false
}

func canonicalCredentialEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

func validCredentialBinding(binding CredentialBinding) (CredentialBinding, bool) {
	if binding.Tier != "production" && binding.Tier != "nonprod" {
		return CredentialBinding{}, false
	}
	if binding.EnvironmentID == "" || binding.EnvironmentID != strings.TrimSpace(binding.EnvironmentID) {
		return CredentialBinding{}, false
	}
	if binding.ConnectorID == "" || binding.ConnectorID != strings.TrimSpace(binding.ConnectorID) {
		return CredentialBinding{}, false
	}
	if _, known := descriptors[binding.Provider]; !known {
		// These providers are used only by the bounded knowledge-source
		// readers. They do not have a generic integration probe and must not
		// be advertised in the integration catalog just to bind Vault use.
		if binding.Provider != "confluence" && binding.Provider != "notion" && binding.Provider != "feishu" {
			return CredentialBinding{}, false
		}
	}
	binding.Endpoint = canonicalCredentialEndpoint(binding.Endpoint)
	if _, err := validEndpoint(binding.Endpoint); err != nil {
		return CredentialBinding{}, false
	}
	if err := validateReference(binding.CredentialRef); err != nil {
		return CredentialBinding{}, false
	}
	return binding, true
}

type BoundResolver struct {
	inner    CredentialResolver
	bindings map[CredentialBinding]struct{}
}

// NewBoundResolver refuses every resolution unless the current operation and
// reference match an explicitly configured binding. Invalid direct bindings
// are ignored, so bypassing the parser cannot broaden access.
func NewBoundResolver(inner CredentialResolver, bindings []CredentialBinding) *BoundResolver {
	allowed := make(map[CredentialBinding]struct{}, len(bindings))
	for _, binding := range bindings {
		if canonical, ok := validCredentialBinding(binding); ok {
			allowed[canonical] = struct{}{}
		}
	}
	return &BoundResolver{inner: inner, bindings: allowed}
}

// CredentialAllowed reports whether a connector configuration has an exact
// server-side binding without contacting the underlying credential store.
func (resolver *BoundResolver) CredentialAllowed(use CredentialUse, reference string) bool {
	if resolver == nil || resolver.inner == nil {
		return false
	}
	binding := CredentialBinding{
		Tier:          use.Tier,
		EnvironmentID: use.EnvironmentID,
		ConnectorID:   use.ConnectorID,
		Provider:      use.Provider,
		Endpoint:      canonicalCredentialEndpoint(use.Endpoint),
		CredentialRef: reference,
	}
	_, allowed := resolver.bindings[binding]
	return allowed
}

// ResolveSibling reads another field of an approved credential's secret. The
// approval covers the secret path, so the sibling needs no binding of its own.
func (resolver *BoundResolver) ResolveSibling(ctx context.Context, reference, field string) (string, error) {
	if resolver == nil || ctx == nil {
		return "", ErrCredentialUnavailable
	}
	use, ok := ctx.Value(credentialUseKey{}).(CredentialUse)
	sibling, valid := SiblingReference(reference, field)
	if !ok || !valid || !resolver.CredentialAllowed(use, reference) {
		return "", ErrCredentialUnavailable
	}
	return resolver.inner.Resolve(ctx, sibling)
}

func (resolver *BoundResolver) Resolve(ctx context.Context, reference string) (string, error) {
	if resolver == nil || ctx == nil {
		return "", ErrCredentialUnavailable
	}
	use, ok := ctx.Value(credentialUseKey{}).(CredentialUse)
	if !ok || !resolver.CredentialAllowed(use, reference) {
		return "", ErrCredentialUnavailable
	}
	return resolver.inner.Resolve(ctx, reference)
}
