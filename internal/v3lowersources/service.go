// Package v3lowersources exposes explicitly configured, read-only source
// metadata on a production instance. It does not resolve credentials, probe
// endpoints, or accept source addresses from a request.
package v3lowersources

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/wans/aiops-canvas/internal/httpapi"
)

type Localized struct {
	ZH string `json:"zh"`
	EN string `json:"en"`
}

type Environment struct {
	ID    string
	Label Localized
}

// Source contains server-side connection metadata. Endpoint and VaultRef are
// validated at startup and never serialized into a response.
type Source struct {
	ID            string
	Brand         string
	EnvironmentID string
	Name          Localized
	Purpose       Localized
	Endpoint      string
	VaultRef      string
}

type Config struct {
	Tier         string
	Environments []Environment
	Sources      []Source
}

type Service struct{ cfg Config }

var ErrInvalidConfig = errors.New("invalid lower-source configuration")
var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var vaultField = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validText(v Localized) bool {
	return strings.TrimSpace(v.ZH) != "" && strings.TrimSpace(v.EN) != "" &&
		v.ZH == strings.TrimSpace(v.ZH) && v.EN == strings.TrimSpace(v.EN) &&
		len(v.ZH) <= 240 && len(v.EN) <= 240
}

func validEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" &&
		strings.TrimSpace(raw) == raw
}

func validVaultRef(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "vault" && u.Hostname() != "" &&
		strings.Trim(u.Path, "/") != "" && vaultField.MatchString(u.Fragment) &&
		u.User == nil && u.RawQuery == "" && u.Opaque == "" &&
		strings.TrimSpace(raw) == raw
}

func New(cfg Config) (*Service, error) {
	if cfg.Tier != "production" && cfg.Tier != "nonprod" {
		return nil, ErrInvalidConfig
	}
	if cfg.Tier == "nonprod" && (len(cfg.Environments) != 0 || len(cfg.Sources) != 0) {
		return nil, ErrInvalidConfig
	}
	envs := make(map[string]Localized, len(cfg.Environments))
	for _, env := range cfg.Environments {
		if !identifier.MatchString(env.ID) || env.ID == "prod" || env.ID == "all" || !validText(env.Label) {
			return nil, ErrInvalidConfig
		}
		if _, exists := envs[env.ID]; exists {
			return nil, ErrInvalidConfig
		}
		envs[env.ID] = env.Label
	}
	seen := make(map[string]bool, len(cfg.Sources))
	for _, source := range cfg.Sources {
		if !identifier.MatchString(source.ID) || !identifier.MatchString(source.Brand) ||
			!validText(source.Name) || !validText(source.Purpose) ||
			!validEndpoint(source.Endpoint) || !validVaultRef(source.VaultRef) || seen[source.ID] {
			return nil, ErrInvalidConfig
		}
		if _, exists := envs[source.EnvironmentID]; !exists {
			return nil, ErrInvalidConfig
		}
		seen[source.ID] = true
	}
	// The caller owns cfg. Keep an independent snapshot so later mutation of
	// startup objects cannot silently change the exposed source list.
	copyCfg := Config{Tier: cfg.Tier, Environments: append([]Environment(nil), cfg.Environments...), Sources: append([]Source(nil), cfg.Sources...)}
	return &Service{cfg: copyCfg}, nil
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/integrations/lower-sources", "viewer", s.Handle)
	api.MarkV3Route("GET /api/v1/integrations/lower-sources", "connectors", "view")
}

func (s *Service) Handle(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.cfg.Tier == "" {
		httpapi.Error(w, http.StatusServiceUnavailable, "lower_sources_unavailable", "Lower-source configuration is unavailable")
		return
	}
	if s.cfg.Tier != "production" {
		httpapi.Error(w, http.StatusNotFound, "not_available", "Lower sources are only available on a production instance")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.Tier != "production" || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] != "prod" {
		httpapi.Error(w, http.StatusServiceUnavailable, "environment_scope_unavailable", "Production environment scope is unavailable")
		return
	}
	envLabels := make(map[string]Localized, len(s.cfg.Environments))
	for _, env := range s.cfg.Environments {
		envLabels[env.ID] = env.Label
	}
	english := strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
	selectText := func(v Localized) string {
		if english {
			return v.EN
		}
		return v.ZH
	}
	credential := "生产 Vault 引用已配置；连接未验证"
	if english {
		credential = "Production Vault reference configured; connection unverified"
	}
	items := make([]map[string]string, 0, len(s.cfg.Sources))
	for _, source := range s.cfg.Sources {
		items = append(items, map[string]string{
			"id": source.ID, "name": selectText(source.Name), "brand": source.Brand,
			"env": source.EnvironmentID, "env_label": selectText(envLabels[source.EnvironmentID]),
			"purpose": selectText(source.Purpose), "credential": credential,
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
