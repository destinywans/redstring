package v3admin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSMTPInvitationSenderCAFile(t *testing.T) {
	base := SMTPInvitationConfig{Host: "smtp.example.invalid", Port: 465, From: "ops@example.invalid", Username: "u", Password: "p"}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "internal ca"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "internal ca"}}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "ca.pem")
	bad := filepath.Join(dir, "bad.pem")
	if os.WriteFile(good, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600) != nil || os.WriteFile(bad, []byte("not a cert"), 0o600) != nil {
		t.Fatal("write fixtures")
	}
	cfg := base
	cfg.CAFile = good
	if s, err := NewSMTPInvitationSender(cfg); err != nil || s.roots == nil {
		t.Fatalf("valid CA file: sender=%v err=%v", s, err)
	}
	for _, path := range []string{bad, "relative.pem", filepath.Join(dir, "missing.pem")} {
		cfg.CAFile = path
		if _, err := NewSMTPInvitationSender(cfg); err == nil {
			t.Fatalf("CA file %q must be rejected", path)
		}
	}
	if s, err := NewSMTPInvitationSender(base); err != nil || s.roots != nil {
		t.Fatalf("no CA file keeps system roots: sender=%v err=%v", s, err)
	}
}
