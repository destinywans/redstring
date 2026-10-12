package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeV3ConfigSyncFile(t *testing.T, name string, content []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	// Exercise the requested permission case even under a restrictive umask.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func v3ConfigSyncFixture(t *testing.T, tier string) v3ConfigSyncFile {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	tokenPath := writeV3ConfigSyncFile(t, "token", []byte("test-token"), 0600)
	config := v3ConfigSyncFile{
		Tier: tier,
		GitLab: v3ConfigSyncGitLab{
			BaseURL: "https://gitlab.example", ProjectID: 42,
			TargetBranch: "main", BranchPrefix: "aiops/promotions/", TokenFile: tokenPath,
		},
	}
	if tier == "nonprod" {
		config.SourceInstanceID = "aiops-test"
		config.SigningKeyFile = writeV3ConfigSyncFile(t, "signing-key", []byte(base64.RawURLEncoding.EncodeToString(key)), 0600)
	} else {
		config.TrustedSources = map[string]string{
			"aiops-test": writeV3ConfigSyncFile(t, "public-key", []byte(base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))), 0644),
		}
	}
	return config
}

func writeV3ConfigSyncJSON(t *testing.T, config v3ConfigSyncFile) string {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return writeV3ConfigSyncFile(t, "config.json", raw, 0600)
}

func TestLoadV3ConfigSyncAbsentAndValidTiers(t *testing.T) {
	service, err := loadV3ConfigSync("", "nonprod", nil, nil)
	if err != nil || service == nil || service.Repository != nil {
		t.Fatalf("absent configuration must retain an unavailable service: service=%+v err=%v", service, err)
	}
	for _, tier := range []string{"nonprod", "production"} {
		t.Run(tier, func(t *testing.T) {
			config := v3ConfigSyncFixture(t, tier)
			service, err := loadV3ConfigSync(writeV3ConfigSyncJSON(t, config), tier, nil, nil)
			if err != nil || service == nil || service.Repository == nil {
				t.Fatalf("load valid %s configuration: %v", tier, err)
			}
			if service.Repository.ProjectID() != 42 || service.Repository.BranchPrefix() != "aiops/promotions/" {
				t.Fatalf("wrong repository binding: %+v", service.Repository)
			}
			if tier == "nonprod" && (service.SourceInstanceID != "aiops-test" || len(service.SigningKey) != ed25519.PrivateKeySize || len(service.TrustedSources) != 0) {
				t.Fatal("non-production signing trust was not bound exactly")
			}
			if tier == "production" && (service.SourceInstanceID != "" || len(service.SigningKey) != 0 || len(service.TrustedSources["aiops-test"]) != ed25519.PublicKeySize) {
				t.Fatal("production source trust was not bound exactly")
			}
		})
	}
}

func TestLoadV3ConfigSyncRejectsInvalidDocuments(t *testing.T) {
	config := v3ConfigSyncFixture(t, "nonprod")
	validPath := writeV3ConfigSyncJSON(t, config)
	for _, tc := range []struct {
		name, path, tier string
	}{
		{"relative config path", "config.json", "nonprod"},
		{"wrong instance tier", validPath, "production"},
		{"unknown instance tier", validPath, "staging"},
		{"malformed JSON", writeV3ConfigSyncFile(t, "malformed.json", []byte(`{"tier":`), 0600), "nonprod"},
		{"unknown field", writeV3ConfigSyncFile(t, "unknown.json", []byte(`{"tier":"nonprod","gitlab":{},"token":"inline-secret"}`), 0600), "nonprod"},
		{"trailing object", writeV3ConfigSyncFile(t, "trailing.json", []byte(`{} {}`), 0600), "nonprod"},
		{"oversize", writeV3ConfigSyncFile(t, "large.json", []byte(strings.Repeat("x", maxV3ConfigSyncConfigSize+1)), 0600), "nonprod"},
		{"world-readable config", writeV3ConfigSyncFile(t, "public.json", []byte(`{}`), 0644), "nonprod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, err := loadV3ConfigSync(tc.path, tc.tier, nil, nil)
			if err == nil || service != nil {
				t.Fatalf("expected rejection, got service=%+v err=%v", service, err)
			}
		})
	}
	link := filepath.Join(t.TempDir(), "config-link.json")
	if err := os.Symlink(validPath, link); err != nil {
		t.Fatal(err)
	}
	if service, err := loadV3ConfigSync(link, "nonprod", nil, nil); err == nil || service != nil {
		t.Fatalf("config symlink accepted: %v", err)
	}
}

func TestLoadV3ConfigSyncRejectsInvalidTrustAndTokenPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tier   string
		mutate func(*v3ConfigSyncFile)
	}{
		{"relative token", "nonprod", func(c *v3ConfigSyncFile) { c.GitLab.TokenFile = "token" }},
		{"missing token", "nonprod", func(c *v3ConfigSyncFile) { c.GitLab.TokenFile = "/missing/config-sync-token" }},
		{"relative signing key", "nonprod", func(c *v3ConfigSyncFile) { c.SigningKeyFile = "key" }},
		{"production trust on nonprod", "nonprod", func(c *v3ConfigSyncFile) { c.TrustedSources = map[string]string{"aiops-test": "/tmp/public-key"} }},
		{"source identifier reserved", "nonprod", func(c *v3ConfigSyncFile) { c.SourceInstanceID = "prod" }},
		{"signing key on production", "production", func(c *v3ConfigSyncFile) { c.SigningKeyFile = "/tmp/signing-key" }},
		{"missing trusted source", "production", func(c *v3ConfigSyncFile) { c.TrustedSources = nil }},
		{"relative public key", "production", func(c *v3ConfigSyncFile) { c.TrustedSources["aiops-test"] = "key" }},
		{"invalid source identifier", "production", func(c *v3ConfigSyncFile) { c.TrustedSources = map[string]string{"Prod": "/tmp/key"} }},
		{"missing public key", "production", func(c *v3ConfigSyncFile) { c.TrustedSources["aiops-test"] = "/missing/config-sync-public-key" }},
		{"relative GitLab CA file", "nonprod", func(c *v3ConfigSyncFile) { c.GitLab.CAFile = "gitlab-ca.pem" }},
		{"missing GitLab CA file", "production", func(c *v3ConfigSyncFile) { c.GitLab.CAFile = "/missing/gitlab-ca.pem" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := v3ConfigSyncFixture(t, tc.tier)
			tc.mutate(&config)
			service, err := loadV3ConfigSync(writeV3ConfigSyncJSON(t, config), tc.tier, nil, nil)
			if err == nil || service != nil {
				t.Fatalf("expected rejection, got service=%+v err=%v", service, err)
			}
		})
	}
	for _, tier := range []string{"nonprod", "production"} {
		t.Run(tier+" weak token mode", func(t *testing.T) {
			config := v3ConfigSyncFixture(t, tier)
			config.GitLab.TokenFile = writeV3ConfigSyncFile(t, "public-token", []byte("test-token"), 0644)
			if service, err := loadV3ConfigSync(writeV3ConfigSyncJSON(t, config), tier, nil, nil); err == nil || service != nil {
				t.Fatalf("world-readable token accepted: %v", err)
			}
		})
	}
	t.Run("malformed signing key", func(t *testing.T) {
		config := v3ConfigSyncFixture(t, "nonprod")
		config.SigningKeyFile = writeV3ConfigSyncFile(t, "malformed-key", []byte("not-a-key"), 0600)
		if service, err := loadV3ConfigSync(writeV3ConfigSyncJSON(t, config), "nonprod", nil, nil); err == nil || service != nil {
			t.Fatalf("malformed signing key accepted: %v", err)
		}
	})
	t.Run("writable production public key", func(t *testing.T) {
		config := v3ConfigSyncFixture(t, "production")
		keyPath := config.TrustedSources["aiops-test"]
		if err := os.Chmod(keyPath, 0666); err != nil {
			t.Fatal(err)
		}
		if service, err := loadV3ConfigSync(writeV3ConfigSyncJSON(t, config), "production", nil, nil); err == nil || service != nil {
			t.Fatalf("writable trusted key accepted: %v", err)
		}
	})
}

// PA-91: gitlab.ca_file binds a PEM CA for a GitLab behind an internal CA;
// a CA file others can write is refused.
func TestLoadV3ConfigSyncGitLabCAFile(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gitlab internal test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	config := v3ConfigSyncFixture(t, "nonprod")
	config.GitLab.BaseURL = "https://127.0.0.1:18446"
	config.GitLab.CAFile = writeV3ConfigSyncFile(t, "gitlab-ca.pem", ca, 0644)
	if service, err := loadV3ConfigSync(writeV3ConfigSyncJSON(t, config), "nonprod", nil, nil); err != nil || service.Repository == nil {
		t.Fatalf("valid GitLab CA file refused: %v", err)
	}
	config.GitLab.CAFile = writeV3ConfigSyncFile(t, "writable-ca.pem", ca, 0666)
	if service, err := loadV3ConfigSync(writeV3ConfigSyncJSON(t, config), "nonprod", nil, nil); err == nil || service != nil {
		t.Fatalf("writable GitLab CA file accepted: %v", err)
	}
}
