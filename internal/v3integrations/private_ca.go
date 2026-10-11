package v3integrations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
)

// ErrPrivateCAInvalid is a "ca" field that holds no PEM certificate.
var ErrPrivateCAInvalid = errors.New("private CA is not a PEM certificate")

// PrivateCAClient returns a copy of client for an HTTPS endpoint reached with
// reference's credential. When the same approved Vault secret has a "ca"
// field (a self-hosted GitLab or a cluster API, PA-08/D11), that CA is the
// only root; without one the client keeps its roots, so an untrusted
// certificate still fails. The copy never follows redirects. ctx must carry
// the credential use a bound resolver checks.
func PrivateCAClient(ctx context.Context, resolver CredentialResolver, reference string, base *url.URL, client *http.Client) (*http.Client, error) {
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if base == nil || base.Scheme != "https" {
		return &copyClient, nil
	}
	siblings, ok := resolver.(SiblingResolver)
	if !ok {
		return &copyClient, nil
	}
	pem, err := siblings.ResolveSibling(ctx, reference, "ca")
	if err != nil || pem == "" {
		return &copyClient, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, ErrPrivateCAInvalid
	}
	// Keep a caller's transport settings (timeouts, no proxy) and replace
	// only its roots. Each client is short-lived, so keep no idle connections.
	transport, ok := copyClient.Transport.(*http.Transport)
	if ok {
		transport = transport.Clone()
	} else {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.TLSClientConfig != nil {
		config = transport.TLSClientConfig.Clone()
	}
	config.RootCAs = pool
	if config.MinVersion < tls.VersionTLS12 {
		config.MinVersion = tls.VersionTLS12
	}
	transport.TLSClientConfig = config
	transport.DisableKeepAlives = true
	copyClient.Transport = transport
	return &copyClient, nil
}
