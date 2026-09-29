package main

import (
	"fmt"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3passkey"
)

// Passkeys are an optional self-account capability. Their account assertion
// cannot be used as proposal approval proof; no proposal route is registered.
func configureV3Passkey(st *store.Store, api *httpapi.Server, info v3Instance, enabled string) error {
	if enabled == "" || enabled == "false" {
		return nil
	}
	if enabled != "true" {
		return fmt.Errorf("AIOPS_V3_PASSKEY_ENABLED must be true or false")
	}
	if st == nil || api == nil || !api.SecureCookies {
		return fmt.Errorf("configure V3 passkey: secure session cookies and storage are required")
	}
	service, err := v3passkey.New(v3passkey.Config{
		DB: st.DB, InstanceID: v3MFAInstanceID(info), RPID: info.Domain,
		Origin: api.AllowedOrigin, Tier: info.Tier, DisplayName: info.Name,
	})
	if err != nil {
		return fmt.Errorf("configure V3 passkey: %w", err)
	}
	service.Register(api)
	return nil
}
