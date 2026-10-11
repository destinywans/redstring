package v3integrations

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestPrivateCAClient(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	binding := CredentialBinding{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "lab", Provider: "gitlab",
		Endpoint: server.URL, CredentialRef: "vault://isolated/gitlab#token"}
	ctx := WithCredentialUse(context.Background(), CredentialUse{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "lab",
		Provider: "gitlab", Endpoint: server.URL})
	base, _ := url.Parse(server.URL)
	caller := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 2 * time.Second}}
	get := func(resolver CredentialResolver, ctx context.Context) error {
		client, err := PrivateCAClient(ctx, resolver, binding.CredentialRef, base, caller)
		if err != nil {
			return err
		}
		if client.Timeout != caller.Timeout || client.CheckRedirect == nil ||
			client.Transport.(*http.Transport).ResponseHeaderTimeout != 2*time.Second {
			t.Fatalf("caller settings lost: %+v", client)
		}
		response, err := client.Get(server.URL)
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	}
	if err := get(NewBoundResolver(vaultEntry{"token": "t", "ca": ca}, []CredentialBinding{binding}), ctx); err != nil {
		t.Fatalf("CA next to the token: %v", err)
	}
	if err := get(NewBoundResolver(vaultEntry{"token": "t"}, []CredentialBinding{binding}), ctx); err == nil {
		t.Fatal("no CA: a private certificate was trusted")
	}
	// The bound resolver reads the sibling only for the approved use.
	if err := get(NewBoundResolver(vaultEntry{"token": "t", "ca": ca}, []CredentialBinding{binding}), context.Background()); err == nil {
		t.Fatal("a CA was read without the credential use")
	}
	if _, err := PrivateCAClient(ctx, NewBoundResolver(vaultEntry{"token": "t", "ca": "junk"}, []CredentialBinding{binding}),
		binding.CredentialRef, base, caller); !errors.Is(err, ErrPrivateCAInvalid) {
		t.Fatalf("invalid CA: %v", err)
	}
	// Plain HTTP (loopback labs) needs no CA and keeps the caller's transport.
	plain, _ := url.Parse("http://127.0.0.1:1")
	if client, err := PrivateCAClient(ctx, NewBoundResolver(vaultEntry{"token": "t", "ca": "junk"}, []CredentialBinding{binding}),
		binding.CredentialRef, plain, caller); err != nil || client.Transport != caller.Transport {
		t.Fatalf("http endpoint: %v", err)
	}
}
