package v3knowledgesources

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

const maxWebMarkdownBytes = 128 << 10

var (
	ErrInvalidWebURL    = errors.New("invalid HTTPS Markdown URL")
	ErrUnsafeWebAddress = errors.New("web address is not public")
	ErrWebFetchFailed   = errors.New("web Markdown fetch failed")
	ErrWebContent       = errors.New("web response is not bounded UTF-8 Markdown")
)

// WebPage contains the exact fetched Markdown bytes represented as a UTF-8
// string. SHA256 is the lowercase hex digest of those bytes, with no trimming
// or newline normalization.
type WebPage struct {
	Body        string
	SHA256      string
	ContentType string
}

type WebIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// WebFetcher has injection points for isolated TLS protocol tests. In normal
// use, leave all fields nil to use the system resolver, dialer, and trust roots.
// DialContext is given only a previously checked numeric IP and port.
type WebFetcher struct {
	Resolver    WebIPResolver
	DialContext func(context.Context, string, string) (net.Conn, error)
	RootCAs     *x509.CertPool
}

func (f WebFetcher) Fetch(ctx context.Context, address string) (WebPage, error) {
	u, host, port, err := v3sourceregistry.ParsePublicWebURL(address)
	if err != nil {
		return WebPage{}, ErrInvalidWebURL
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	addresses, err := f.resolve(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return WebPage{}, ctx.Err()
		}
		return WebPage{}, err
	}

	dial := f.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: -1}).DialContext
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DisableKeepAlives:     true,
		DisableCompression:    true,
		ResponseHeaderTimeout: 5 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		TLSClientConfig:       &tls.Config{RootCAs: f.RootCAs, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, destination string) (net.Conn, error) {
			requestedHost, requestedPort, splitErr := net.SplitHostPort(destination)
			if splitErr != nil || !strings.EqualFold(requestedHost, host) || requestedPort != port {
				return nil, ErrUnsafeWebAddress
			}
			for _, ip := range addresses {
				conn, dialErr := dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
			return nil, ErrWebFetchFailed
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrWebFetchFailed
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return WebPage{}, ErrInvalidWebURL
	}
	req.Header.Set("Accept", "text/markdown, text/plain;q=0.9")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return WebPage{}, ctx.Err()
		}
		return WebPage{}, ErrWebFetchFailed
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return WebPage{}, ErrWebFetchFailed
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return WebPage{}, ErrWebContent
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !acceptedMarkdownType(mediaType, u.Path) || resp.ContentLength > maxWebMarkdownBytes {
		return WebPage{}, ErrWebContent
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebMarkdownBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return WebPage{}, ctx.Err()
		}
		return WebPage{}, ErrWebFetchFailed
	}
	if len(body) > maxWebMarkdownBytes || !utf8.Valid(body) || len(strings.TrimSpace(string(body))) == 0 {
		return WebPage{}, ErrWebContent
	}
	hash := sha256.Sum256(body)
	return WebPage{Body: string(body), SHA256: hex.EncodeToString(hash[:]), ContentType: mediaType}, nil
}

func acceptedMarkdownType(mediaType, path string) bool {
	if strings.EqualFold(mediaType, "text/markdown") {
		return true
	}
	path = strings.ToLower(path)
	return strings.EqualFold(mediaType, "text/plain") && (strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".markdown"))
}

func (f WebFetcher) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicWebIP(ip) {
			return nil, ErrUnsafeWebAddress
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	resolver := f.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrWebFetchFailed
	}
	for _, ip := range addresses {
		if !publicWebIP(ip) {
			return nil, ErrUnsafeWebAddress
		}
	}
	return addresses, nil
}

var reservedIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// IANA IPv6 Global Unicast Address Space, allocated public RIR ranges as of
// 2025-10-10. 2001::/23 (IETF assignments) and 2002::/16 (6to4) are omitted
// deliberately. Everything else, including future allocations, fails closed
// until this list is reviewed against the registry.
var allocatedPublicIPv6 = []netip.Prefix{
	netip.MustParsePrefix("2001:200::/23"),
	netip.MustParsePrefix("2001:400::/23"),
	netip.MustParsePrefix("2001:600::/23"),
	netip.MustParsePrefix("2001:800::/22"),
	netip.MustParsePrefix("2001:c00::/23"),
	netip.MustParsePrefix("2001:e00::/23"),
	netip.MustParsePrefix("2001:1200::/23"),
	netip.MustParsePrefix("2001:1400::/22"),
	netip.MustParsePrefix("2001:1800::/23"),
	netip.MustParsePrefix("2001:1a00::/23"),
	netip.MustParsePrefix("2001:1c00::/22"),
	netip.MustParsePrefix("2001:2000::/19"),
	netip.MustParsePrefix("2001:4000::/23"),
	netip.MustParsePrefix("2001:4200::/23"),
	netip.MustParsePrefix("2001:4400::/23"),
	netip.MustParsePrefix("2001:4600::/23"),
	netip.MustParsePrefix("2001:4800::/23"),
	netip.MustParsePrefix("2001:4a00::/23"),
	netip.MustParsePrefix("2001:4c00::/23"),
	netip.MustParsePrefix("2001:5000::/20"),
	netip.MustParsePrefix("2001:8000::/19"),
	netip.MustParsePrefix("2001:a000::/20"),
	netip.MustParsePrefix("2001:b000::/20"),
	netip.MustParsePrefix("2003::/18"),
	netip.MustParsePrefix("2400::/12"),
	netip.MustParsePrefix("2410::/12"),
	netip.MustParsePrefix("2600::/12"),
	netip.MustParsePrefix("2610::/23"),
	netip.MustParsePrefix("2620::/23"),
	netip.MustParsePrefix("2630::/12"),
	netip.MustParsePrefix("2800::/12"),
	netip.MustParsePrefix("2a00::/12"),
	netip.MustParsePrefix("2a10::/12"),
	netip.MustParsePrefix("2c00::/12"),
}

func publicWebIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip.Is4() {
		for _, prefix := range reservedIPv4 {
			if prefix.Contains(ip) {
				return false
			}
		}
		return true
	}
	// Documentation is an exception inside the otherwise allocated
	// 2001:c00::/23 range.
	if netip.MustParsePrefix("2001:db8::/32").Contains(ip) {
		return false
	}
	for _, prefix := range allocatedPublicIPv6 {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
