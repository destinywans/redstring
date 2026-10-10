package v3integrations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrInvalidEndpoint = errors.New("invalid connector endpoint")

type ProbeResult struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	ErrorCode string `json:"-"`
}

type Prober struct {
	Client   *http.Client
	Resolver CredentialResolver
}

func validEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, ErrInvalidEndpoint
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, ErrInvalidEndpoint
		}
	}
	return u, nil
}

func (p Prober) Check(ctx context.Context, provider, rawURL, authMethod, credentialRef string) (ProbeResult, error) {
	d, ok := descriptors[provider]
	if !ok || !knownMethod(d, authMethod) {
		return ProbeResult{}, ErrInvalidEndpoint
	}
	base, err := validEndpoint(rawURL)
	if err != nil {
		return ProbeResult{}, err
	}
	probe, _ := url.Parse(d.Probe)
	endpoint := *base
	endpoint.Path = strings.TrimSuffix(base.Path, "/") + probe.Path
	endpoint.RawQuery = probe.RawQuery
	var credential string
	if authMethod != "none" {
		if err := validateReference(credentialRef); err != nil {
			return ProbeResult{}, ErrCredentialUnavailable
		}
		if p.Resolver == nil {
			return ProbeResult{}, ErrCredentialUnavailable
		}
		credential, err = p.Resolver.Resolve(ctx, credentialRef)
		if err != nil || credential == "" || len(credential) > 4096 || strings.ContainsAny(credential, "\r\n\x00") {
			return ProbeResult{}, ErrCredentialUnavailable
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return ProbeResult{}, ErrInvalidEndpoint
	}
	switch authMethod {
	case "pat":
		request.Header.Set("PRIVATE-TOKEN", credential)
	case "basic":
		user, pass, ok := strings.Cut(credential, ":")
		if !ok {
			return ProbeResult{}, ErrCredentialUnavailable
		}
		request.SetBasicAuth(user, pass)
	case "api_key":
		request.Header.Set("Authorization", "ApiKey "+credential)
	case "oauth", "bearer":
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	client := p.Client
	if client == nil {
		client = &http.Client{}
	}
	// A cluster API usually has a private CA, kept next to the token in the
	// same approved secret (field "ca").
	if provider == "kubernetes" && base.Scheme == "https" {
		if siblings, ok := p.Resolver.(SiblingResolver); ok {
			if pem, err := siblings.ResolveSibling(ctx, credentialRef, "ca"); err == nil && pem != "" {
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM([]byte(pem)) {
					return ProbeResult{}, ErrCredentialUnavailable
				}
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
				client = &http.Client{Transport: transport, Timeout: client.Timeout}
			}
		}
	}
	// Preserve a caller's transport for controlled tests, but never let it
	// redirect a credentialed probe to an endpoint outside its binding.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 5*time.Second {
		copyClient.Timeout = 5 * time.Second
	}
	start := time.Now()
	response, err := copyClient.Do(request)
	result := ProbeResult{LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		result.ErrorCode = "connection_failed"
		return result, nil
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		result.OK = true
	} else if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		result.ErrorCode = "authentication_failed"
	} else {
		result.ErrorCode = fmt.Sprintf("upstream_%d", response.StatusCode)
	}
	return result, nil
}
