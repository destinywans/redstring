package main

import (
	"fmt"

	"github.com/destinywans/redstring/internal/v3resilience"
	"github.com/destinywans/redstring/internal/v3settings"
)

// The source is optional at startup. A configured endpoint is fixed by the
// operator and must attest complete current coverage for the requested scope.
func configureV3ResilienceReader(endpoint, tokenPath string) (v3resilience.Reader, error) {
	if endpoint == "" && tokenPath == "" {
		return nil, nil
	}
	if endpoint == "" || tokenPath == "" {
		return nil, fmt.Errorf("both HTTPS endpoint and protected token file are required")
	}
	// This loader enforces an absolute, regular 0600 file and returns no secret
	// or path in errors. Its on-call name reflects its first caller only.
	token, err := v3settings.LoadOncallTokenFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("invalid protected token file")
	}
	reader, err := v3resilience.NewHTTPSReader(endpoint, token, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid fixed HTTPS endpoint")
	}
	return reader, nil
}
