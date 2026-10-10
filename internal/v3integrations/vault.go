package v3integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrCredentialUnavailable = errors.New("credential resolver unavailable")
var vaultReference = regexp.MustCompile(`^vault://([A-Za-z0-9_-]+)/([A-Za-z0-9_./-]+)#([A-Za-z0-9_-]+)$`)

type CredentialResolver interface {
	Resolve(context.Context, string) (string, error)
}

// VaultResolver reads a single field from HashiCorp Vault KV v2. The Vault
// token must come from process configuration, never a connector request.
type VaultResolver struct {
	Address string
	Token   string
	Client  *http.Client
}

func NewVaultResolver(address, token string) *VaultResolver {
	return &VaultResolver{Address: address, Token: token, Client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (v *VaultResolver) Resolve(ctx context.Context, reference string) (string, error) {
	m := vaultReference.FindStringSubmatch(reference)
	if m == nil {
		return "", ErrCredentialUnavailable
	}
	base, err := url.Parse(v.Address)
	if err != nil || base == nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || v.Token == "" {
		return "", ErrCredentialUnavailable
	}
	path := "/v1/" + m[1] + "/data/" + strings.Trim(m[2], "/")
	endpoint := base.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", ErrCredentialUnavailable
	}
	req.Header.Set("X-Vault-Token", v.Token)
	client := v.Client
	if client == nil {
		client = NewVaultResolver(v.Address, v.Token).Client
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copyClient.Timeout == 0 || copyClient.Timeout > 5*time.Second {
		copyClient.Timeout = 5 * time.Second
	}
	resp, err := copyClient.Do(req)
	if err != nil {
		return "", ErrCredentialUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ErrCredentialUnavailable
	}
	var body struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", ErrCredentialUnavailable
	}
	value, ok := body.Data.Data[m[3]].(string)
	if !ok || value == "" {
		return "", ErrCredentialUnavailable
	}
	return value, nil
}

// SiblingReference names another field of the same Vault secret, such as the
// cluster CA stored next to a Kubernetes token (PA-08).
func SiblingReference(reference, field string) (string, bool) {
	m := vaultReference.FindStringSubmatch(reference)
	if m == nil || !vaultField.MatchString(field) {
		return "", false
	}
	return "vault://" + m[1] + "/" + m[2] + "#" + field, true
}

var vaultField = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// SiblingResolver reads a field next to an approved credential.
type SiblingResolver interface {
	ResolveSibling(ctx context.Context, reference, field string) (string, error)
}

func validateReference(reference string) error {
	if vaultReference.MatchString(reference) {
		return nil
	}
	return fmt.Errorf("credential_ref must use vault://mount/path#field")
}
