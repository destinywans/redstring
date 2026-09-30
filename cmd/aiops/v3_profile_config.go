package main

import (
	"errors"

	"github.com/wans/aiops-canvas/internal/v3settings"
)

// Profile evidence is an explicit deployment-owned authority. A partial pair
// fails startup; an absent pair leaves the full profile routes unavailable.
func loadV3ProfileEvidence(endpoint, tokenPath string) (v3settings.ProfileEvidenceSource, error) {
	if endpoint == "" && tokenPath == "" {
		return nil, nil
	}
	if endpoint == "" || tokenPath == "" {
		return nil, errors.New("profile evidence requires an HTTPS endpoint and token file")
	}
	token, err := v3settings.LoadOncallTokenFile(tokenPath)
	if err != nil {
		return nil, errors.New("invalid protected profile evidence token file")
	}
	source, err := v3settings.NewHTTPProfileEvidenceSource(endpoint, token)
	if err != nil {
		return nil, errors.New("invalid profile evidence endpoint or token")
	}
	return source, nil
}
