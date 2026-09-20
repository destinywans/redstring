package httpapi

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
