package v3telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/connectors"
)

// Querier is satisfied by a pool, a connection and a transaction, so callers
// can resolve inside the transaction that holds their own locks.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

var (
	// ErrNotConfigured means no target serves this surface of the service.
	ErrNotConfigured = errors.New("source not configured")
	// ErrAmbiguous means more than one target serves it; readers fail closed.
	ErrAmbiguous = errors.New("source ambiguous")
)

// Source is what the registry says one surface of a service reads. NotReady
// lists why a matching source could not be read yet (connector disabled,
// tool off, credential not approved, scope missing); it is empty when the
// source is complete.
type Source struct {
	TargetID    string
	ConnectorID string
	Provider    string
	Endpoint    string
	AuthMethod  string
	ResourceID  string
	TenantID    string
	// CredentialRef is the connector's Vault reference ("" without auth);
	// a reader resolves it through the approved credential bindings.
	CredentialRef string
	// Scope is the provider's reviewed scope; empty for Prometheus.
	Scope connectors.ApplicationTelemetry
	// Queries are Prometheus's reviewed expressions by query ID.
	Queries  map[string]string
	NotReady []string
}

// Resolver resolves the single registered source of one service surface.
type Resolver struct {
	Tier      string
	Catalog   Catalog
	Approvals Approvals
}

// Resolve returns the one target serving surface for the application and
// environment, joined to its connector and catalog scope. Zero targets is
// ErrNotConfigured and two or more is ErrAmbiguous; authorization stays with
// the caller.
func (r *Resolver) Resolve(ctx context.Context, q Querier, applicationID, environmentID string, surface Surface) (Source, error) {
	rows, err := q.Query(ctx, `SELECT t.id,t.connector_id,c.provider,c.endpoint,c.auth_method,c.credential_ref,
 c.enabled,c.tools,c.options,t.external_id
 FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 JOIN applications a ON a.id=t.application_id AND NOT a.archived
 JOIN application_environments e ON e.application_id=t.application_id AND e.key=t.environment_id AND NOT e.archived
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.target_kind||':'||c.provider=ANY($3::text[])
 ORDER BY t.id LIMIT 2`, applicationID, environmentID, surfacePairs(surface))
	if err != nil {
		return Source{}, err
	}
	var found []Source
	for rows.Next() {
		var s Source
		var credentialRef string
		var enabled bool
		var tools, options []byte
		if err = rows.Scan(&s.TargetID, &s.ConnectorID, &s.Provider, &s.Endpoint, &s.AuthMethod, &credentialRef,
			&enabled, &tools, &options, &s.ResourceID); err != nil {
			break
		}
		s.CredentialRef = credentialRef
		s.NotReady = r.readiness(&s, environmentID, credentialRef, enabled, tools, options)
		found = append(found, s)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return Source{}, err
	}
	switch len(found) {
	case 0:
		return Source{}, ErrNotConfigured
	case 1:
	default:
		return Source{}, ErrAmbiguous
	}
	s := found[0]
	key := ScopeKey{ConnectorID: s.ConnectorID, ApplicationID: applicationID, EnvironmentID: environmentID, ResourceID: s.ResourceID}
	if s.Provider == "prometheus" {
		s.Queries = r.Catalog.QueriesFor(key)
		if len(s.Queries) == 0 {
			s.NotReady = append(s.NotReady, "scope_missing")
		}
	} else if scope, ok := r.Catalog.Scopes[key]; ok && scope.Provider == s.Provider {
		s.Scope = scope.ApplicationTelemetry
	} else {
		s.NotReady = append(s.NotReady, "scope_missing")
	}
	return s, nil
}

func (r *Resolver) readiness(s *Source, environmentID, credentialRef string, enabled bool, tools, options []byte) []string {
	var notReady []string
	if !enabled {
		notReady = append(notReady, "connector_disabled")
	}
	if !toolEnabled(tools, telemetryProviders[s.Provider].tool) {
		notReady = append(notReady, "tool_disabled")
	}
	tenant, ok := parseOptions(options)
	if !ok {
		notReady = append(notReady, "options_invalid")
	}
	s.TenantID = tenant
	if s.AuthMethod != "none" {
		if credentialRef == "" {
			notReady = append(notReady, "credential_missing")
		} else if !r.Approvals.Allowed(r.Tier, environmentID, s.ConnectorID, s.Provider, s.Endpoint, credentialRef) {
			notReady = append(notReady, "credential_not_approved")
		}
	}
	return notReady
}

// Options are the non-secret connector attributes the registry accepts.
type Options struct {
	TenantID string `json:"tenant_id,omitempty"`
}

func parseOptions(raw []byte) (string, bool) {
	var options Options
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&options) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", false
	}
	return options.TenantID, true
}

func toolEnabled(raw []byte, name string) bool {
	var tools []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		if tool.Name == name && tool.Enabled {
			return true
		}
	}
	return false
}
