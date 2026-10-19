package main

import (
	"errors"

	"github.com/destinywans/redstring/internal/v3security"
	"github.com/destinywans/redstring/internal/v3settings"
)

// The runtime controller is an explicit deployment-owned authority. Without
// both settings the security routes stay registered but return unavailable.
func loadV3SecurityAuthority(endpoint, tokenPath string) (v3security.RuntimeController, error) {
	if endpoint == "" && tokenPath == "" {
		return nil, nil
	}
	if endpoint == "" || tokenPath == "" {
		return nil, errors.New("security authority requires an HTTPS endpoint and token file")
	}
	token, err := v3settings.LoadOncallTokenFile(tokenPath)
	if err != nil {
		return nil, errors.New("invalid protected security authority token file")
	}
	authority, err := v3security.NewHTTPRuntimeController(endpoint, token)
	if err != nil {
		return nil, errors.New("invalid security authority endpoint or token")
	}
	return authority, nil
}
