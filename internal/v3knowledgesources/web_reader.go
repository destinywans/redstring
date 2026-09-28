package v3knowledgesources

import (
	"context"
	"errors"

	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

var ErrUnsupportedWebScope = errors.New("public Web Markdown reader requires one page, no credential or spaces")

// PublicWebReader is an opt-in adapter for the source registry's read-only
// probe and observation routes. It proves only one publicly readable Markdown
// page and its exact content hash. It does not import or index the body.
type PublicWebReader struct {
	Fetcher WebFetcher
}

var _ v3sourceregistry.Reader = PublicWebReader{}
var _ v3sourceregistry.ConnectionTester = PublicWebReader{}

func (r PublicWebReader) Probe(ctx context.Context, spec v3sourceregistry.ReadSpec) error {
	if err := validPublicWebSpec(spec); err != nil {
		return err
	}
	_, err := r.Fetcher.Fetch(ctx, spec.URL)
	return err
}

func (r PublicWebReader) Read(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ReadResult, error) {
	if err := validPublicWebSpec(spec); err != nil {
		return v3sourceregistry.ReadResult{}, err
	}
	page, err := r.Fetcher.Fetch(ctx, spec.URL)
	if err != nil {
		return v3sourceregistry.ReadResult{}, err
	}
	return v3sourceregistry.ReadResult{
		Items: []v3sourceregistry.Observation{{
			ExternalID: spec.URL, ContentHash: page.SHA256,
			EnvironmentID: spec.EnvironmentID, ApplicationID: spec.ApplicationID,
			TeamID: spec.TeamID, ACLVerified: true,
		}},
		Complete: true,
	}, nil
}

func validPublicWebSpec(spec v3sourceregistry.ReadSpec) error {
	if spec.SourceType != "web" || spec.SecretRef != "" || len(spec.Spaces) != 0 ||
		spec.EnvironmentID == "" || spec.EnvironmentID == "all" {
		return ErrUnsupportedWebScope
	}
	if _, _, _, err := v3sourceregistry.ParsePublicWebURL(spec.URL); err != nil {
		return ErrUnsupportedWebScope
	}
	return nil
}
