// Package v3ingestpush implements the generic, source-bound V3 Push ingress.
// It deliberately does not interpret vendor-specific Alertmanager payloads.
package v3ingestpush

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/delivery"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
)

var ErrInvalidConfiguration = errors.New("invalid V3 Push configuration")

type Source struct {
	ID             string   `json:"id"`
	Provider       string   `json:"provider"`
	ApplicationID  string   `json:"application_id"`
	EnvironmentID  string   `json:"environment_id"`
	TokenFile      string   `json:"token_file"`
	HMACSecretFile string   `json:"hmac_secret_file"`
	Types          []string `json:"types"`
	RatePerMinute  int      `json:"rate_per_minute,omitempty"`
}

type Config struct {
	PublicHost string   `json:"public_host"`
	Sources    []Source `json:"sources"`
}

type Service struct {
	db            *pgxpool.Pool
	policy        *rbac.V3Policy
	sources       map[string]Source
	envs          map[string]bool
	publicHost    string
	now           func() time.Time
	nativeAlerts  []operations.Source
	nativeGitLabs []delivery.GitLabConfig
}

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validPublicHost(host string) bool {
	if host == "" {
		return true
	}
	if strings.ContainsAny(host, "/?#@ \\\t\r\n") || len(host) > 253 || strings.ToLower(host) != host {
		return false
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" {
		return false
	}
	name := u.Hostname()
	if net.ParseIP(name) != nil {
		return true
	}
	return regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`).MatchString(name)
}

// New validates the static source-to-application/environment mapping. Secret
// files are read at request time so rotation does not require a restart.
func New(db *pgxpool.Pool, instance httpapi.V3InstanceConfig, cfg Config, policy *rbac.V3Policy) (*Service, error) {
	if db == nil || policy == nil || (instance.Tier != "production" && instance.Tier != "nonprod") || len(instance.EnvironmentIDs) == 0 || !validPublicHost(cfg.PublicHost) || len(cfg.Sources) > 50 {
		return nil, ErrInvalidConfiguration
	}
	s := &Service{db: db, policy: policy, sources: map[string]Source{}, envs: map[string]bool{}, publicHost: cfg.PublicHost, now: time.Now}
	for _, id := range instance.EnvironmentIDs {
		if id == "" || id == "all" || strings.TrimSpace(id) != id || s.envs[id] || (instance.Tier == "production" && id != "prod") || (instance.Tier == "nonprod" && id == "prod") {
			return nil, ErrInvalidConfiguration
		}
		s.envs[id] = true
	}
	for _, source := range cfg.Sources {
		// Only the canonical generic protocol is implemented. A vendor label
		// must not imply that its native payload can be ingested here.
		if !sourceIDPattern.MatchString(source.ID) || source.Provider != "generic" || source.ApplicationID == "" || source.EnvironmentID == "" || !s.envs[source.EnvironmentID] || !filepath.IsAbs(source.TokenFile) || !filepath.IsAbs(source.HMACSecretFile) || source.TokenFile == source.HMACSecretFile || len(source.Types) == 0 || len(source.Types) > 5 || source.RatePerMinute < 0 || source.RatePerMinute > 600 {
			return nil, ErrInvalidConfiguration
		}
		if source.RatePerMinute == 0 {
			source.RatePerMinute = 120
		}
		if _, exists := s.sources[source.ID]; exists {
			return nil, ErrInvalidConfiguration
		}
		seen := map[string]bool{}
		for _, typ := range source.Types {
			if !eventTypes[typ] || seen[typ] {
				return nil, ErrInvalidConfiguration
			}
			seen[typ] = true
		}
		s.sources[source.ID] = source
	}
	if _, _, err := s.loadSecrets(""); err != nil {
		return nil, ErrInvalidConfiguration
	}
	return s, nil
}

// loadSecrets checks every source on each request. This detects rotations
// that accidentally converge on another source's credential, even across
// instances sharing the same mounted secret files.
func (s *Service) loadSecrets(selected string) ([]byte, []byte, error) {
	ids := make([]string, 0, len(s.sources))
	for id := range s.sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	paths := map[string]bool{}
	contents := map[string]bool{}
	var token, secret []byte
	for _, id := range ids {
		source := s.sources[id]
		for i, path := range []string{source.TokenFile, source.HMACSecretFile} {
			if paths[path] {
				return nil, nil, ErrInvalidConfiguration
			}
			paths[path] = true
			min := 24
			if i == 1 {
				min = 32
			}
			value, err := readSecret(path, min)
			if err != nil {
				if id == selected || selected == "" {
					return nil, nil, err
				}
				// An unrelated source with an unreadable file cannot accept
				// requests. Check equality again when that file recovers.
				continue
			}
			fingerprint := hashKey(string(value))
			if contents[fingerprint] {
				return nil, nil, ErrInvalidConfiguration
			}
			contents[fingerprint] = true
			if id == selected {
				if i == 0 {
					token = value
				} else {
					secret = value
				}
			}
		}
	}
	return token, secret, nil
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/ingest/push", "viewer", s.view)
	api.MarkV3ObjectRoute("GET /api/v1/ingest/push", "connectors", "view")
	api.Register("GET /api/v1/webhooks", "viewer", s.webhooks)
	api.MarkV3ObjectRoute("GET /api/v1/webhooks", "connectors", "view")
	api.Register("POST /api/v1/ingest/push/test", "viewer", s.test)
	api.MarkV3ObjectRoute("POST /api/v1/ingest/push/test", "connectors", "manage")
}

// RegisterPublic requires the shared V3 route gate and environment middleware
// to exempt exactly these five source-authenticated routes. WAN-117 owns that
// shared wiring; without it the public handlers intentionally remain blocked.
func (s *Service) RegisterPublic(api *httpapi.Server) {
	api.RegisterPublic("POST /api/v1/events/alert", s.ingest)
	api.RegisterPublic("POST /api/v1/events/change", s.ingest)
	api.RegisterPublic("POST /api/v1/events/incident", s.ingest)
	api.RegisterPublic("POST /api/v1/events/resource", s.ingest)
	api.RegisterPublic("POST /api/v1/events/oncall", s.ingest)
}
