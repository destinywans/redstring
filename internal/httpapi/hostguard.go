package httpapi

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// HostPolicy restricts the outer HTTP server, including static and API paths.
type HostPolicy struct {
	publicHost string
}

func NewHostPolicy(publicOrigin string) (HostPolicy, error) {
	if publicOrigin == "" {
		return HostPolicy{}, nil
	}
	invalid := errors.New("AIOPS_PUBLIC_ORIGIN must be a canonical http or https origin without a path, user info, query, fragment or default port")
	origin, err := url.Parse(publicOrigin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") ||
		origin.User != nil || publicOrigin != origin.Scheme+"://"+origin.Host {
		return HostPolicy{}, invalid
	}
	host, port, ok := hostAuthority(origin.Host)
	if !ok || origin.Host != strings.ToLower(origin.Host) || strings.HasSuffix(host, ".") ||
		(origin.Scheme == "http" && port == "80") || (origin.Scheme == "https" && port == "443") {
		return HostPolicy{}, invalid
	}
	return HostPolicy{publicHost: canonicalHost(host)}, nil
}

func hostAuthority(authority string) (host, port string, ok bool) {
	host = authority
	if strings.Contains(authority, ":") {
		if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
			host = authority[1 : len(authority)-1]
		} else {
			var err error
			host, port, err = net.SplitHostPort(authority)
			if err != nil {
				return "", "", false
			}
			number, err := strconv.ParseUint(port, 10, 16)
			if err != nil || number == 0 || strconv.FormatUint(number, 10) != port {
				return "", "", false
			}
		}
	}
	ip, err := netip.ParseAddr(host)
	if strings.HasPrefix(authority, "[") {
		return host, port, err == nil && ip.Is6() && ip.Zone() == ""
	}
	if err == nil {
		return host, port, ip.Is4()
	}
	return host, port, validDNSName(host)
}

func validDNSName(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func canonicalHost(host string) string {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func (p HostPolicy) Allowed(authority string) bool {
	host, _, ok := hostAuthority(authority)
	if !ok {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Unmap().IsLoopback() {
		return true
	}
	return p.publicHost != "" && p.publicHost == canonicalHost(host) &&
		!(p.publicHost == "localhost" && strings.HasSuffix(host, "."))
}

// Exempt only the probe and the machine-authenticated routes. Their handlers
// enforce their own token or signature and never use a browser session.
func hostExempt(r *http.Request) bool {
	if r.URL.EscapedPath() == "/healthz" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return true
	}
	return nativeWebhookRoute(r) || (r.URL.EscapedPath() == r.URL.Path && v3SignedPushRoute(r))
}

func (p HostPolicy) Wrap(next http.Handler) http.Handler {
	var mu sync.Mutex
	logged := make(map[string]bool)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.Allowed(r.Host) || hostExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if len(host) > 255 {
			host = host[:255]
		}
		mu.Lock()
		first := len(logged) < 64 && !logged[host]
		if first {
			logged[host] = true
		}
		mu.Unlock()
		if first {
			slog.Warn("host_not_allowed", "host", host)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		Error(w, http.StatusMisdirectedRequest, "host_not_allowed", "This server does not answer for this host name")
	})
}
