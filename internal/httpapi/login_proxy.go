package httpapi

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// SetTrustedProxyCIDRs must be called before serving requests. The direct peer
// must be trusted before any forwarded address is considered.
func (s *Server) SetTrustedProxyCIDRs(value string) error {
	var prefixes []netip.Prefix
	if strings.TrimSpace(value) != "" {
		for _, item := range strings.Split(value, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(item))
			if err != nil {
				return fmt.Errorf("invalid trusted proxy CIDR: %w", err)
			}
			prefixes = append(prefixes, prefix.Masked())
		}
	}
	s.trustedProxyPrefixes = prefixes
	return nil
}

func (s *Server) trustedProxy(ip netip.Addr) bool {
	for _, prefix := range s.trustedProxyPrefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) loginClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	fallback := peer.String()
	if !s.trustedProxy(peer) {
		return fallback
	}
	values := r.Header.Values("X-Forwarded-For")
	header := strings.Join(values, ",")
	if len(header) == 0 || len(header) > 4096 {
		return fallback
	}
	parts := strings.Split(header, ",")
	if len(parts) > 32 {
		return fallback
	}
	// Walk from the nearest hop toward the client. Stop at the first untrusted
	// hop; addresses further left can have been supplied by that client.
	current := peer
	for i := len(parts) - 1; i >= 0; i-- {
		if !s.trustedProxy(current) {
			return current.String()
		}
		next, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return fallback
		}
		current = next.Unmap()
	}
	return current.String()
}
