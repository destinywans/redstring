package main

import (
	"fmt"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3passkey"
)

// Passkeys are optional. Proposal approval uses a separate proposal-bound
// assertion on this same configured service, never an account assertion.
func configureV3Passkey(st *store.Store, api *httpapi.Server, info v3Instance, enabled string) error {
	_, err := configureV3PasskeyService(st, api, info, enabled)
	return err
}

func configureV3PasskeyService(st *store.Store, api *httpapi.Server, info v3Instance, enabled string) (*v3passkey.Service, error) {
	if enabled == "" || enabled == "false" {
		return nil, nil
	}
	if enabled != "true" {
		return nil, fmt.Errorf("AIOPS_V3_PASSKEY_ENABLED must be true or false")
	}
	if st == nil || api == nil || !api.SecureCookies {
		return nil, fmt.Errorf("configure V3 passkey: secure session cookies and storage are required")
	}
	service, err := v3passkey.New(v3passkey.Config{
		DB: st.DB, InstanceID: v3MFAInstanceID(info), RPID: info.Domain,
		Origin: api.AllowedOrigin, Tier: info.Tier, DisplayName: info.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("configure V3 passkey: %w", err)
	}
	service.Register(api)
	return service, nil
}
