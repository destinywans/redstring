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

// PublicWebObservationReader is the production adapter. A saved registration
// supplies the application scope before any network read. It intentionally
// has no Probe method because an unsaved test request has no application scope.
type PublicWebObservationReader struct {
	Fetcher WebFetcher
}

var _ v3sourceregistry.Reader = PublicWebObservationReader{}
var _ v3sourceregistry.ContentReader = PublicWebObservationReader{}

func (r PublicWebObservationReader) Read(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ReadResult, error) {
	result, err := r.ReadContent(ctx, spec)
	if err != nil {
		return v3sourceregistry.ReadResult{}, err
	}
	return v3sourceregistry.ReadResult{Items: []v3sourceregistry.Observation{result.Observation}, Complete: true}, nil
}

// ReadContent returns the exact bounded UTF-8 body from the same fetch that
// produced the observation hash. The registry must verify saved scope again
// before committing this body.
func (r PublicWebObservationReader) ReadContent(ctx context.Context, spec v3sourceregistry.ReadSpec) (v3sourceregistry.ContentReadResult, error) {
	if err := validPublicWebSpec(spec); err != nil {
		return v3sourceregistry.ContentReadResult{}, err
	}
	if spec.ApplicationID == "" || spec.ApplicationID == "all" {
		return v3sourceregistry.ContentReadResult{}, ErrUnsupportedWebScope
	}
	page, err := r.Fetcher.Fetch(ctx, spec.URL)
	if err != nil {
		return v3sourceregistry.ContentReadResult{}, err
	}
	return v3sourceregistry.ContentReadResult{
		Observation: v3sourceregistry.Observation{
			ExternalID: spec.URL, ContentHash: page.SHA256,
			EnvironmentID: spec.EnvironmentID, ApplicationID: spec.ApplicationID,
			TeamID: spec.TeamID, ACLVerified: true,
		},
		Body: page.Body,
	}, nil
}

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
	if spec.ApplicationID == "" || spec.ApplicationID == "all" {
		return v3sourceregistry.ReadResult{}, ErrUnsupportedWebScope
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
