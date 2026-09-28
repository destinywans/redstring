package v3sourceregistry

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

const MaxPublicWebURLBytes = 1000

var ErrInvalidPublicWebURL = errors.New("invalid public HTTPS Markdown URL")

// ParsePublicWebURL is shared by registration and the opt-in public Web
// reader. A saved public source must have a URL that its reader can attempt.
func ParsePublicWebURL(address string) (*url.URL, string, string, error) {
	if address == "" || len(address) > MaxPublicWebURLBytes || strings.TrimSpace(address) != address {
		return nil, "", "", ErrInvalidPublicWebURL
	}
	u, err := url.Parse(address)
	if err != nil || u == nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" ||
		u.User != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(address, "#") {
		return nil, "", "", ErrInvalidPublicWebURL
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "%\\ ") || strings.HasSuffix(host, ".") {
		return nil, "", "", ErrInvalidPublicWebURL
	}
	port := u.Port()
	if port == "" {
		port = "443"
	} else if port != "443" {
		return nil, "", "", ErrInvalidPublicWebURL
	}
	if _, err := netip.ParseAddr(host); err != nil {
		if len(host) > 253 {
			return nil, "", "", ErrInvalidPublicWebURL
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return nil, "", "", ErrInvalidPublicWebURL
			}
			for _, c := range label {
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
					return nil, "", "", ErrInvalidPublicWebURL
				}
			}
		}
	}
	u.Scheme = "https"
	return u, host, port, nil
}
