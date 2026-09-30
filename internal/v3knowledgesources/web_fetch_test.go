package v3knowledgesources

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type fixedWebResolver struct {
	addresses []netip.Addr
	calls     int
}

func (r *fixedWebResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.calls++
	return r.addresses, nil
}

func TestWebFetcherRejectsInvalidURLAndNonPublicAddressesBeforeDial(t *testing.T) {
	invalid := []string{
		"http://example.com/readme.md", "https://user:pass@example.com/readme.md",
		"https://example.com/readme.md?token=secret", "https://example.com/readme.md?",
		"https://example.com/readme.md#fragment", "https://example.com/readme.md#",
		"https://example.com:8443/readme.md", "https://example.com./readme.md",
		"https://example.com:bad/readme.md", "https://exa_mple.com/readme.md",
	}
	for _, address := range invalid {
		t.Run(address, func(t *testing.T) {
			resolver := &fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
			fetcher := WebFetcher{Resolver: resolver, DialContext: func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("invalid URL reached dial")
				return nil, nil
			}}
			if _, err := fetcher.Fetch(context.Background(), address); !errors.Is(err, ErrInvalidWebURL) {
				t.Fatalf("expected invalid URL, got %v", err)
			}
			if resolver.calls != 0 {
				t.Fatalf("invalid URL reached resolver: %d calls", resolver.calls)
			}
		})
	}
	for _, address := range []string{
		"https://127.0.0.1/readme.md", "https://10.0.0.1/readme.md",
		"https://169.254.169.254/readme.md", "https://100.64.0.1/readme.md",
		"https://[::1]/readme.md", "https://[::ffff:127.0.0.1]/readme.md",
		"https://[fe80::1]/readme.md", "https://[2001:db8::1]/readme.md",
		"https://[2001:20::1]/readme.md", "https://[2002:0a00:0001::]/readme.md",
		"https://[3fff::1]/readme.md", "https://[3000::1]/readme.md",
	} {
		t.Run(address, func(t *testing.T) {
			if _, err := (WebFetcher{}).Fetch(context.Background(), address); !errors.Is(err, ErrUnsafeWebAddress) {
				t.Fatalf("expected unsafe address, got %v", err)
			}
		})
	}
	resolver := &fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}}
	fetcher := WebFetcher{Resolver: resolver, DialContext: func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("mixed DNS result reached dial")
		return nil, nil
	}}
	if _, err := fetcher.Fetch(context.Background(), "https://example.com/readme.md"); !errors.Is(err, ErrUnsafeWebAddress) {
		t.Fatalf("mixed DNS answers must fail closed, got %v", err)
	}
	resolver.addresses = []netip.Addr{netip.MustParseAddr("::ffff:127.0.0.1")}
	if _, err := fetcher.Fetch(context.Background(), "https://example.com/readme.md"); !errors.Is(err, ErrUnsafeWebAddress) {
		t.Fatalf("IPv4-mapped private DNS answer must fail closed, got %v", err)
	}
	for _, ip := range []string{"3fff::1", "3000::1"} {
		resolver.addresses = []netip.Addr{netip.MustParseAddr(ip)}
		if _, err := fetcher.Fetch(context.Background(), "https://example.com/readme.md"); !errors.Is(err, ErrUnsafeWebAddress) {
			t.Fatalf("reserved IPv6 DNS answer %s must fail closed, got %v", ip, err)
		}
	}
}

func TestWebFetcherPublicAddressPolicyAllowsAllocatedIPv6(t *testing.T) {
	for _, ip := range []string{"2001:4860:4860::8888", "2404:6800::1", "2606:4700::1111", "2a00:1450::1", "2c0f:f248::1"} {
		if !publicWebIP(netip.MustParseAddr(ip)) {
			t.Fatalf("allocated public IPv6 address %s was rejected", ip)
		}
	}
}

func TestWebFetcherPinsPublicIPAndVerifiesOriginalTLSHostname(t *testing.T) {
	const body = "# Runbook\n\nCheck status.\n"
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" || r.URL.Path != "/guide.md" {
			t.Errorf("unexpected request host/path: %q %q", r.Host, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	var dialed string
	resolver := &fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	fetcher := webTestFetcher(server, roots, resolver, &dialed)
	page, err := fetcher.Fetch(context.Background(), "https://example.com/guide.md")
	if err != nil {
		t.Fatal(err)
	}
	if dialed != "8.8.8.8:443" || resolver.calls != 1 {
		t.Fatalf("dial was not pinned to validated DNS answer: %q (%d lookups)", dialed, resolver.calls)
	}
	hash := sha256.Sum256([]byte(body))
	if page.Body != body || page.SHA256 != hex.EncodeToString(hash[:]) || page.ContentType != "text/plain" {
		t.Fatalf("body/hash/content type changed: %+v", page)
	}
	if _, err := fetcher.Fetch(context.Background(), "https://other.example/guide.md"); !errors.Is(err, ErrWebFetchFailed) {
		t.Fatalf("TLS hostname mismatch must fail, got %v", err)
	}
}

func TestWebFetcherRejectsRedirectOversizeAndNonMarkdown(t *testing.T) {
	var redirectTargetHits int
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect.md":
			http.Redirect(w, r, "/target.md", http.StatusFound)
		case "/target.md":
			redirectTargetHits++
		case "/huge.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte(strings.Repeat("a", maxWebMarkdownBytes+1)))
		case "/html.md":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<h1>Markdown?</h1>"))
		case "/plain.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("not a Markdown path"))
		case "/invalid.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte{0xff, 0xfe})
		case "/empty.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("  \n\t"))
		case "/compressed.md":
			w.Header().Set("Content-Type", "text/markdown")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte("not gzip"))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	fetcher := webTestFetcher(server, roots, &fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)
	for path, want := range map[string]error{
		"/redirect.md":   ErrWebFetchFailed,
		"/huge.md":       ErrWebContent,
		"/html.md":       ErrWebContent,
		"/plain.txt":     ErrWebContent,
		"/invalid.md":    ErrWebContent,
		"/empty.md":      ErrWebContent,
		"/compressed.md": ErrWebContent,
	} {
		t.Run(path, func(t *testing.T) {
			if _, err := fetcher.Fetch(context.Background(), "https://example.com"+path); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
	if redirectTargetHits != 0 {
		t.Fatalf("redirect target was followed %d times", redirectTargetHits)
	}
}

func webTestFetcher(server *httptest.Server, roots *x509.CertPool, resolver WebIPResolver, dialed *string) WebFetcher {
	return WebFetcher{
		Resolver: resolver,
		RootCAs:  roots,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if dialed != nil {
				*dialed = address
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
}

func webTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"},
		DNSNames: []string{"example.com"}, NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	return server, roots
}
