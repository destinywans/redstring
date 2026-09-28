package v3lowersources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

func configured() Config {
	return Config{
		Tier:         "production",
		Environments: []Environment{{ID: "staging", Label: Localized{ZH: "预发", EN: "Staging"}}},
		Sources: []Source{{ID: "prometheus-staging", Brand: "prometheus", EnvironmentID: "staging",
			Name:     Localized{ZH: "预发指标", EN: "Staging metrics"},
			Purpose:  Localized{ZH: "对比指标", EN: "Compare metrics"},
			Endpoint: "https://metrics.internal.example/api", VaultRef: "vault://ops/staging/prometheus#token"}},
	}
}

func TestNewRejectsInvalidSourceConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"unknown tier", func(c *Config) { c.Tier = "other" }},
		{"nonprod source", func(c *Config) { c.Tier = "nonprod" }},
		{"duplicate environment", func(c *Config) { c.Environments = append(c.Environments, c.Environments[0]) }},
		{"production as lower environment", func(c *Config) { c.Environments[0].ID = "prod"; c.Sources[0].EnvironmentID = "prod" }},
		{"duplicate source", func(c *Config) { c.Sources = append(c.Sources, c.Sources[0]) }},
		{"unknown environment", func(c *Config) { c.Sources[0].EnvironmentID = "test" }},
		{"missing translated label", func(c *Config) { c.Sources[0].Purpose.EN = "" }},
		{"credential in endpoint URL", func(c *Config) { c.Sources[0].Endpoint = "https://secret@metrics.example/api" }},
		{"client query in endpoint URL", func(c *Config) { c.Sources[0].Endpoint += "?token=secret" }},
		{"raw credential", func(c *Config) { c.Sources[0].VaultRef = "secret" }},
		{"vault query", func(c *Config) { c.Sources[0].VaultRef += "?token=secret" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configured()
			tc.change(&cfg)
			if _, err := New(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New error=%v, want invalid config", err)
			}
		})
	}
}

type allowEnv struct{}

func (allowEnv) AuthorizeEnvironment(context.Context, core.User, string) error { return nil }

func productionHandler(t *testing.T, s *Service) http.Handler {
	t.Helper()
	h, err := httpapi.NewV3EnvironmentHandler(
		httpapi.V3InstanceConfig{Tier: "production", DefaultEnv: "prod", EnvironmentIDs: []string{"prod"}},
		allowEnv{}, func(context.Context, string) (core.User, error) { return core.User{ID: "operator"}, nil },
		http.HandlerFunc(s.Handle))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestProductionResponseUsesServerConfigurationAndDoesNotRevealConnectionData(t *testing.T) {
	cfg := configured()
	// Multiple sources may intentionally refer to one lower environment.
	second := cfg.Sources[0]
	second.ID, second.Brand = "gitlab-staging", "gitlab"
	cfg.Sources = append(cfg.Sources, second)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sources[0].Name.EN = "changed after startup"
	for _, tc := range []struct{ language, name, label, purpose string }{
		{"zh-CN", "预发指标", "预发", "对比指标"},
		{"en-US", "Staging metrics", "Staging", "Compare metrics"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/lower-sources?endpoint=https://attacker.example", nil)
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
			r.Header.Set("Accept-Language", tc.language)
			w := httptest.NewRecorder()
			productionHandler(t, s).ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var body struct {
				Items []struct {
					ID         string `json:"id"`
					Name       string `json:"name"`
					Brand      string `json:"brand"`
					Env        string `json:"env"`
					EnvLabel   string `json:"env_label"`
					Purpose    string `json:"purpose"`
					Credential string `json:"credential"`
				} `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Items) != 2 || body.Items[0].ID != "prometheus-staging" || body.Items[0].Name != tc.name || body.Items[0].EnvLabel != tc.label || body.Items[0].Purpose != tc.purpose || body.Items[0].Credential == "" {
				t.Fatalf("unexpected response: %+v", body)
			}
			for _, forbidden := range []string{"metrics.internal.example", "vault://", "#token", "attacker.example", "changed after startup"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("response disclosed %q: %s", forbidden, w.Body.String())
				}
			}
		})
	}
}

func TestNoSourcesReturnsEmptyArrayAndNonprodReturns404(t *testing.T) {
	prod, err := New(Config{Tier: "production"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/lower-sources", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "session"})
	w := httptest.NewRecorder()
	productionHandler(t, prod).ServeHTTP(w, r)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty production source response: %d %s", w.Code, w.Body.String())
	}
	nonprod, err := New(Config{Tier: "nonprod"})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	nonprod.Handle(w, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/lower-sources", nil))
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_available"`) {
		t.Fatalf("non-production response: %d %s", w.Code, w.Body.String())
	}
}

func TestProductionRejectsMissingEnvironmentScope(t *testing.T) {
	s, err := New(Config{Tier: "production"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handle(w, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/lower-sources", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"environment_scope_unavailable"`) {
		t.Fatalf("missing scope response: %d %s", w.Code, w.Body.String())
	}
}
