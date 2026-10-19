package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3mfa"
)

// The instance tier and domain are already bound to this database. Their
// digest is a stable, opaque MFA namespace that also meets the factor store's
// restricted identifier syntax without exposing the public domain in records.
func v3MFAInstanceID(info v3Instance) string {
	digest := sha256.Sum256([]byte(info.Tier + "\x00" + info.Domain))
	return "i-" + hex.EncodeToString(digest[:16])
}

func configureV3MFA(st *store.Store, api *httpapi.Server, info v3Instance, keyPath string) error {
	if keyPath == "" {
		return nil
	}
	key, err := v3mfa.LoadKeyFile(keyPath)
	if err != nil {
		return fmt.Errorf("load V3 MFA key file: %w", err)
	}
	defer clear(key)
	service, err := v3mfa.New(st.DB, key, v3MFAInstanceID(info), nil)
	if err != nil {
		return fmt.Errorf("configure V3 MFA: %w", err)
	}
	service.Register(api)
	return nil
}
