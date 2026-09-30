package v3configsync

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestPromotionTrustKeyFiles(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	dir := t.TempDir()
	privateFile := filepath.Join(dir, "private")
	publicFile := filepath.Join(dir, "public")
	if err := os.WriteFile(privateFile, []byte(base64.RawURLEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(publicFile, []byte(base64.RawURLEncoding.EncodeToString(public)), 0644); err != nil {
		t.Fatal(err)
	}
	gotPrivate, err := LoadSigningKeyFile(privateFile)
	if err != nil || !bytes.Equal(gotPrivate, private) {
		t.Fatalf("private key: %v", err)
	}
	gotPublic, err := LoadTrustedPublicKeyFile(publicFile)
	if err != nil || !bytes.Equal(gotPublic, public) {
		t.Fatalf("public key: %v", err)
	}
	if err := os.Chmod(privateFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigningKeyFile(privateFile); err == nil {
		t.Fatal("world-readable private key accepted")
	}
	link := filepath.Join(dir, "linked-public")
	if err := os.Symlink(publicFile, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrustedPublicKeyFile(link); err == nil {
		t.Fatal("symlinked trust key accepted")
	}
}
