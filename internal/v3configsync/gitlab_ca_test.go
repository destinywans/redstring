package v3configsync

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testCA is an internal CA that signs one server certificate per test server.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gitlab internal test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// server starts a GitLab answering an empty merged list, with a certificate
// for the given names only.
func (ca testCA) server(t *testing.T, ips []net.IP, dns []string) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "gitlab"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: ips, DNSNames: dns, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, 200, []MR{})
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are expected
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func caFile(t *testing.T, content []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gitlab-ca.pem")
	if err := os.WriteFile(p, content, mode); err != nil {
		t.Fatal(err)
	}
	// Exercise the requested permission case even under a restrictive umask.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func caConfig(t *testing.T, base, ca string) Config {
	return Config{BaseURL: base, ProjectID: 42, TargetBranch: "main", BranchPrefix: "aiops-promotion/", TokenFile: tokenFile(t), CAFile: ca}
}

// PA-91: a GitLab behind an internal CA is reachable only with ca_file, and
// the host name is still verified against that CA's certificate.
func TestGitLabCAFileTrustsInternalCAAndVerifiesHostname(t *testing.T) {
	ca := newTestCA(t)
	loopback := ca.server(t, []net.IP{net.IPv4(127, 0, 0, 1)}, nil)
	// 0644 is how the acceptance certificate is stored: readable, not writable.
	trusted := caFile(t, ca.pem, 0o644)

	c, err := NewGitLabClient(caConfig(t, loopback.URL, trusted))
	if err != nil {
		t.Fatal(err)
	}
	if list, err := c.ListMerged(context.Background(), 10); err != nil || len(list) != 0 {
		t.Fatalf("GitLab with the configured CA refused: list=%+v err=%v", list, err)
	}
	drafts, err := c.ForAlertRuleDrafts("rules", "aiops-alert-rule-drafts/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drafts.ListMerged(context.Background(), 10); err != nil {
		t.Fatalf("derived draft client lost the CA: %v", err)
	}

	plain, err := NewGitLabClient(caConfig(t, loopback.URL, ""))
	if err != nil {
		t.Fatal(err)
	}
	// The platform verifier reports an untrusted root differently per OS.
	var untrusted *tls.CertificateVerificationError
	if _, err := plain.ListMerged(context.Background(), 10); !errors.As(err, &untrusted) {
		t.Fatalf("internal CA trusted without ca_file: %v", err)
	}

	other := ca.server(t, nil, []string{"gitlab.internal.test"})
	c, err = NewGitLabClient(caConfig(t, other.URL, trusted))
	if err != nil {
		t.Fatal(err)
	}
	var mismatch x509.HostnameError
	if _, err := c.ListMerged(context.Background(), 10); !errors.As(err, &mismatch) {
		t.Fatalf("certificate for another host accepted: %v", err)
	}

	// The CA is this client's only trust: it does not leak into the shared
	// default transport.
	if transport := http.DefaultTransport.(*http.Transport); transport.TLSClientConfig != nil && transport.TLSClientConfig.RootCAs != nil {
		t.Fatal("ca_file changed the process default transport")
	}
	resp, err := http.Get(loopback.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("default HTTP client trusts the configured CA")
	}
}

func TestGitLabCAFileRejectsUnsafeFiles(t *testing.T) {
	ca := newTestCA(t)
	valid := caFile(t, ca.pem, 0o644)
	link := filepath.Join(t.TempDir(), "ca-link.pem")
	if err := os.Symlink(valid, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, base, ca string
	}{
		{"relative path", "https://127.0.0.1:18446", "gitlab-ca.pem"},
		{"missing file", "https://127.0.0.1:18446", filepath.Join(t.TempDir(), "missing.pem")},
		{"directory", "https://127.0.0.1:18446", t.TempDir()},
		{"symlink", "https://127.0.0.1:18446", link},
		{"group writable", "https://127.0.0.1:18446", caFile(t, ca.pem, 0o664)},
		{"others writable", "https://127.0.0.1:18446", caFile(t, ca.pem, 0o646)},
		{"empty", "https://127.0.0.1:18446", caFile(t, nil, 0o600)},
		{"not PEM", "https://127.0.0.1:18446", caFile(t, []byte("not a certificate\n"), 0o600)},
		{"private key only", "https://127.0.0.1:18446", caFile(t, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}}), 0o600)},
		{"loopback HTTP", "http://127.0.0.1:18446", valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := caConfig(t, tc.base, tc.ca)
			config.AllowLoopbackHTTP = true
			if c, err := NewGitLabClient(config); err == nil || c != nil {
				t.Fatalf("unsafe CA file accepted: %v", err)
			}
		})
	}
}
