package v3mfa

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTOTPRFC6238AndReplay(t *testing.T) {
	seed := []byte("12345678901234567890")
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{59, "94287082"}, {1111111109, "07081804"},
		{1111111111, "14050471"}, {1234567890, "89005924"},
		{2000000000, "69279037"}, {20000000000, "65353130"},
	} {
		if got := hotp(seed, tc.seconds/30, 8); got != tc.want {
			t.Errorf("RFC vector at %d: got %s, want %s", tc.seconds, got, tc.want)
		}
	}
	now := time.Unix(59, 0)
	code := hotp(seed, 1, 6)
	step, ok := matchTOTP(seed, code, now, -1)
	if !ok || step != 1 {
		t.Fatalf("current code: step=%d valid=%t", step, ok)
	}
	if _, ok := matchTOTP(seed, code, now, step); ok {
		t.Fatal("accepted a consumed TOTP step")
	}
	if _, ok := matchTOTP(seed, strings.Repeat("x", 6), now, -1); ok {
		t.Fatal("accepted nonnumeric TOTP")
	}
	// A six-digit code can collide across adjacent steps. A consumed current
	// step must not be accepted a second time via the look-ahead window.
	const collisionStep = int64(910737)
	const collisionCode = "911617"
	if hotp(seed, collisionStep, 6) != collisionCode || hotp(seed, collisionStep+1, 6) != collisionCode {
		t.Fatal("test fixture no longer reproduces adjacent-step collision")
	}
	collisionTime := time.Unix(collisionStep*30, 0)
	if step, ok := matchTOTP(seed, collisionCode, collisionTime, -1); !ok || step != collisionStep {
		t.Fatalf("initial collision code: step=%d valid=%t", step, ok)
	}
	if _, ok := matchTOTP(seed, collisionCode, collisionTime, collisionStep); ok {
		t.Fatal("replayed code through adjacent future-step collision")
	}
	if _, ok := matchTOTP(seed, collisionCode, collisionTime.Add(2*totpPeriod), collisionStep); ok {
		t.Fatal("replayed code while colliding adjacent step is still valid")
	}
}

func TestAEADBindingAndRecoveryHashes(t *testing.T) {
	s, err := New(nil, nil, "instance", nil)
	if !errors.Is(err, ErrUnavailable) || s != nil {
		t.Fatal("missing database/key accepted")
	}
	// Exercise the cipher directly with a fixed test key; New's storage check is
	// covered separately by the missing-dependency assertion above.
	s = &Service{keyID: "test-key"}
	copy(s.key[:], bytes.Repeat([]byte{0x71}, 32))
	seed := bytes.Repeat([]byte{0x42}, seedBytes)
	nonce, ciphertext, err := s.seal(seed, "active", "instance-a", "user-a", "generation-a")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.open("test-key", nonce, ciphertext, "active", "instance-a", "user-a", "generation-a")
	if err != nil || !bytes.Equal(plain, seed) {
		t.Fatalf("cipher round trip: %v", err)
	}
	if _, err = s.open("test-key", nonce, ciphertext, "active", "instance-a", "user-b", "generation-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("AEAD accepted a different account")
	}
	ciphertext[0] ^= 1
	if _, err = s.open("test-key", nonce, ciphertext, "active", "instance-a", "user-a", "generation-a"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("AEAD accepted modified ciphertext")
	}
	codes, salts, hashes, err := newRecoveryCodes()
	if err != nil || len(codes) != recoveryCodeCount {
		t.Fatalf("recovery generation: %v, count=%d", err, len(codes))
	}
	seen := map[string]bool{}
	for i, code := range codes {
		canonical, ok := canonicalRecovery(strings.ToLower(code))
		if !ok || len(canonical) != 32 || seen[code] || len(salts[i]) != 16 || len(hashes[i]) != 32 || !bytes.Equal(recoveryHash(salts[i], canonical), hashes[i]) {
			t.Fatalf("invalid recovery code at %d", i)
		}
		seen[code] = true
	}
	if _, ok := canonicalRecovery(strings.ReplaceAll(codes[0], "-", "")); ok {
		t.Fatal("accepted ungrouped recovery code")
	}
}

func TestLoadKeyFileRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mfa.key")
	key := bytes.Repeat([]byte{0x71}, 32)
	if err := os.WriteFile(path, key, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadKeyFile(path)
	if err != nil || !bytes.Equal(loaded, key) {
		t.Fatalf("trusted key file: %v", err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadKeyFile(path); !errors.Is(err, ErrUnavailable) {
		t.Fatal("group/world-readable key accepted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.key")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadKeyFile(link); !errors.Is(err, ErrUnavailable) {
		t.Fatal("symlink key accepted")
	}
	if _, err = LoadKeyFile("relative.key"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("relative key path accepted")
	}
}
