package v3telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// Clients builds the telemetry client of one registered source (PA-19 P3,
// stage 2 step 0), so the log and trace readers keep their query code while
// reading the connector, scope and credential the registry names. A client
// covers exactly one target: its endpoint, auth and the reviewed scope of
// its resource. Clients are cached by target and rebuilt when anything they
// were built from changes.
type Clients struct {
	Tier string
	// Credentials resolves a source's credential reference; it must enforce
	// the approved credential bindings (v3integrations.BoundResolver).
	Credentials v3integrations.CredentialResolver
	// Limits are the time, size and Tempo settings every client uses (the
	// file registration's, so readers behave the same after the switch).
	Limits connectors.TelemetryConfig
	// CredentialTTL caches a resolved credential; zero is 60 s. A 401 or 403
	// answer drops it at once.
	CredentialTTL time.Duration
	Now           func() time.Time

	mu          sync.Mutex
	clients     map[string]cachedClient
	credentials map[string]cachedCredential
}

type cachedClient struct {
	digest string
	client *connectors.Telemetry
}

type cachedCredential struct {
	value   string
	expires time.Time
}

const defaultCredentialTTL = 60 * time.Second

// ErrClientUnavailable means the source cannot be read as registered (an
// endpoint, auth method or scope the client cannot represent).
var ErrClientUnavailable = errors.New("registered source cannot be read")

// For returns the client of source s in environment env and the resource key
// its queries use (the target's resource ID). A source that is not ready is
// refused; the caller reports s.NotReady.
func (c *Clients) For(env string, s Source) (*connectors.Telemetry, string, error) {
	if c == nil || len(s.NotReady) > 0 || s.ResourceID == "" || !v3integrations.ValidEndpoint(s.Endpoint) {
		return nil, "", ErrClientUnavailable
	}
	scope := s.Scope
	if s.Provider == "prometheus" {
		scope = connectors.ApplicationTelemetry{PrometheusQueries: s.Queries}
	}
	endpoint := connectors.TelemetryEndpoint{URL: s.Endpoint, TenantID: s.TenantID, PostQueries: true}
	if u, err := url.Parse(s.Endpoint); err == nil && u.Scheme == "http" {
		endpoint.AllowHTTP = true // ValidEndpoint admits HTTP on loopback only
	}
	switch s.AuthMethod {
	case "none":
		if s.CredentialRef != "" {
			return nil, "", ErrClientUnavailable
		}
	case "bearer", "api_key":
		endpoint.CredentialKind = s.AuthMethod
	case "basic":
		if s.Username == "" {
			return nil, "", ErrClientUnavailable
		}
		endpoint.CredentialKind, endpoint.Username = "basic", s.Username
	default:
		return nil, "", ErrClientUnavailable
	}
	key := env + "\x00" + s.ConnectorID + "\x00" + s.ResourceID
	digest := clientDigest(env, s, scope, c.Limits)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.clients[key]; ok && cached.digest == digest {
		return cached.client, s.ResourceID, nil
	}
	if endpoint.CredentialKind != "" {
		use := v3integrations.CredentialUse{Tier: c.Tier, EnvironmentID: env, ConnectorID: s.ConnectorID,
			Provider: s.Provider, Endpoint: s.Endpoint}
		credentialKey := key + "\x00" + s.CredentialRef
		reference := s.CredentialRef
		endpoint.Credential = func(ctx context.Context) (string, error) {
			return c.credential(v3integrations.WithCredentialUse(ctx, use), credentialKey, reference)
		}
		endpoint.Unauthorized = func() { c.forget(credentialKey) }
	}
	config := c.Limits
	config.Prometheus, config.Loki, config.Elasticsearch, config.Tempo = connectors.TelemetryEndpoint{}, connectors.TelemetryEndpoint{},
		connectors.TelemetryEndpoint{}, connectors.TelemetryEndpoint{}
	switch s.Provider {
	case "prometheus":
		config.Prometheus = endpoint
	case "loki":
		config.Loki = endpoint
	case "elasticsearch":
		config.Elasticsearch = endpoint
	case "tempo":
		config.Tempo = endpoint
	default:
		return nil, "", ErrClientUnavailable
	}
	config.Applications = map[string]connectors.ApplicationTelemetry{s.ResourceID: scope}
	client, err := connectors.NewTelemetry(config)
	if err != nil {
		return nil, "", errors.Join(ErrClientUnavailable, err)
	}
	if c.clients == nil {
		c.clients = map[string]cachedClient{}
	}
	c.clients[key] = cachedClient{digest: digest, client: client}
	return client, s.ResourceID, nil
}

// clientDigest covers everything a client is built from.
func clientDigest(env string, s Source, scope connectors.ApplicationTelemetry, limits connectors.TelemetryConfig) string {
	limits.Applications = nil
	raw, _ := json.Marshal(struct {
		Env, Connector, Provider, Endpoint, Auth, Reference, Tenant, Username, Resource string
		Scope                                                                           connectors.ApplicationTelemetry
		Limits                                                                          connectors.TelemetryConfig
	}{env, s.ConnectorID, s.Provider, s.Endpoint, s.AuthMethod, s.CredentialRef, s.TenantID, s.Username, s.ResourceID, scope, limits})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (c *Clients) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// credential resolves a reference through the approved bindings, cached for
// CredentialTTL.
func (c *Clients) credential(ctx context.Context, key, reference string) (string, error) {
	now := c.now()
	c.mu.Lock()
	cached, ok := c.credentials[key]
	c.mu.Unlock()
	if ok && now.Before(cached.expires) {
		return cached.value, nil
	}
	if c.Credentials == nil {
		return "", v3integrations.ErrCredentialUnavailable
	}
	value, err := c.Credentials.Resolve(ctx, reference)
	if err != nil || value == "" {
		return "", v3integrations.ErrCredentialUnavailable
	}
	ttl := c.CredentialTTL
	if ttl <= 0 {
		ttl = defaultCredentialTTL
	}
	c.mu.Lock()
	if c.credentials == nil {
		c.credentials = map[string]cachedCredential{}
	}
	c.credentials[key] = cachedCredential{value: value, expires: now.Add(ttl)}
	c.mu.Unlock()
	return value, nil
}

func (c *Clients) forget(key string) {
	c.mu.Lock()
	delete(c.credentials, key)
	c.mu.Unlock()
}
