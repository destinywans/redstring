package httpapi

import (
	"context"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
)

// Integration is a safe configuration inventory, not a connection health check.
// Populate it before serving HTTP. Credentials, provider URLs and filesystem
// paths deliberately have no fields in this public projection.
type Integration struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Provider       string   `json:"provider"`
	Configured     bool     `json:"configured"`
	Status         string   `json:"status"`
	ApplicationIDs []string `json:"application_ids"`
	Capabilities   []string `json:"capabilities"`
	Model          string   `json:"model,omitempty"`
	// Registered bindings identify connector resources, never product application IDs.
	RegisteredBindings []core.Binding `json:"-"`
}

func (s *Server) visibleIntegrations(ctx context.Context, user core.User) ([]Integration, error) {
	ids, err := s.VisibleApplicationIDs(ctx, user)
	if err != nil {
		return nil, err
	}
	repository, ok := s.Store.(store.PlatformRepository)
	if !ok {
		return nil, store.ErrForbidden
	}
	allowed := make(map[string]bool, len(ids))
	bindings := map[core.Binding][]string{}
	for _, id := range ids {
		allowed[id] = true
		environments, err := repository.Environments(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, environment := range environments {
			for _, binding := range environment.Bindings {
				bindings[binding] = append(bindings[binding], id)
			}
		}
	}
	items := make([]Integration, 0, len(s.Integrations))
	for _, configured := range s.Integrations {
		item := configured
		item.ApplicationIDs = []string{}
		associated := map[string]bool{}
		if item.Kind == "connector" {
			for _, binding := range item.RegisteredBindings {
				for _, id := range bindings[binding] {
					associated[id] = true
				}
			}
		} else {
			for _, id := range configured.ApplicationIDs {
				associated[id] = allowed[id]
			}
		}
		// Keep the repository's stable application ordering and de-duplicate environments.
		for _, id := range ids {
			if associated[id] {
				item.ApplicationIDs = append(item.ApplicationIDs, id)
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func NewIntegration(id, name, kind, provider string, configured bool, apps, capabilities []string) Integration {
	status := "not_configured"
	if configured {
		status = "configured_unverified"
	}
	if apps == nil {
		apps = []string{}
	}
	if capabilities == nil {
		capabilities = []string{}
	}
	return Integration{ID: id, Name: name, Kind: kind, Provider: provider, Configured: configured, Status: status, ApplicationIDs: apps, Capabilities: capabilities}
}

// ReadIntegrations shares the authorized registration projection with dashboards.
func (s *Server) ReadIntegrations(ctx context.Context, user core.User) ([]Integration, error) {
	return s.visibleIntegrations(ctx, user)
}
