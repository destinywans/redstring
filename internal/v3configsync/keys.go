package v3configsync

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var errTrustKey = errors.New("configuration promotion trust key unavailable")

// LoadSigningKeyFile reads a server-owned Ed25519 private key from a mounted
// secret file. The base64url value is the 64-byte private key, not a seed.
// The caller must not log file contents or return parsing details to clients.
func LoadSigningKeyFile(path string) (ed25519.PrivateKey, error) {
	if !filepath.IsAbs(path) {
		return nil, errTrustKey
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 256 {
		return nil, errTrustKey
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errTrustKey
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nil, errTrustKey
	}
	return ed25519.PrivateKey(key), nil
}

// LoadTrustedPublicKeyFile reads one explicitly trusted non-production source
// key on the production instance. It never discovers keys from a GitLab MR.
func LoadTrustedPublicKeyFile(path string) (ed25519.PublicKey, error) {
	if !filepath.IsAbs(path) {
		return nil, errTrustKey
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() < 1 || info.Size() > 128 {
		return nil, errTrustKey
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errTrustKey
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, errTrustKey
	}
	return ed25519.PublicKey(key), nil
}
