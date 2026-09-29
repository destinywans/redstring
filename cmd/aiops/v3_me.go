package main

import (
	"fmt"

	"github.com/wans/aiops-canvas/internal/v3settings"
)

// configureV3MeEvidence binds account registration queries to this instance.
// A current on-call assertion is optional at startup, but /me reports it as
// unavailable until an authoritative source has been configured.
func configureV3MeEvidence(info v3Instance, endpoint, tokenPath string) (v3settings.MeEvidence, error) {
	evidence := v3settings.MeEvidence{
		MFAInstanceID: v3MFAInstanceID(info),
		PasskeyRPID:   info.Domain,
	}
	if endpoint == "" && tokenPath == "" {
		return evidence, nil
	}
	if endpoint == "" || tokenPath == "" {
		return v3settings.MeEvidence{}, fmt.Errorf("configure V3 on-call evidence: both endpoint and token file are required")
	}
	token, err := v3settings.LoadOncallTokenFile(tokenPath)
	if err != nil {
		return v3settings.MeEvidence{}, fmt.Errorf("configure V3 on-call evidence: %w", err)
	}
	source, err := v3settings.NewOncallHTTPSource(endpoint, token, nil)
	if err != nil {
		return v3settings.MeEvidence{}, fmt.Errorf("configure V3 on-call evidence: %w", err)
	}
	evidence.Oncall = source
	return evidence, nil
}
