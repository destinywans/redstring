package v3mfa

import (
	"io"
	"os"
	"path/filepath"
)

// LoadKeyFile reads an exactly 32-byte AES key from a server-owned regular
// file. It rejects relative paths, symlinks and group/world-readable files.
// The caller must inject the key at startup; the service never falls back to
// plaintext or generates a new key that would orphan existing factors.
func LoadKeyFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 32 {
		return nil, ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 {
		return nil, ErrUnavailable
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		return nil, ErrUnavailable
	}
	return key, nil
}
