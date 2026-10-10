package v3integrations

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
)

type mapResolver map[string]string

func (m mapResolver) Resolve(_ context.Context, reference string) (string, error) {
	if v, ok := m[reference]; ok {
		return v, nil
	}
	return "", ErrCredentialUnavailable
}

// PA-08: a Kubernetes connector is probed with the CA kept next to its
// token in the same approved secret; the sibling needs no extra binding but
// an unapproved token reference reads nothing.
func TestKubernetesProbeUsesSiblingCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" || r.Header.Get("Authorization") != "Bearer reader-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"gitVersion":"v1.30.0"}`))
	}))
	defer server.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	const ref = "vault://kv/lab/k8s-reader#token"
	use := CredentialUse{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "k8s", Provider: "kubernetes", Endpoint: server.URL}
	binding := CredentialBinding{Tier: use.Tier, EnvironmentID: use.EnvironmentID, ConnectorID: use.ConnectorID,
		Provider: use.Provider, Endpoint: use.Endpoint, CredentialRef: ref}
	ctx := WithCredentialUse(context.Background(), use)
	store := mapResolver{ref: "reader-token", "vault://kv/lab/k8s-reader#ca": caPEM}

	result, err := Prober{Resolver: NewBoundResolver(store, []CredentialBinding{binding})}.Check(ctx, "kubernetes", server.URL, "bearer", ref)
	if err != nil || !result.OK {
		t.Fatalf("probe with sibling CA: %+v %v", result, err)
	}
	delete(store, "vault://kv/lab/k8s-reader#ca")
	if result, err = (Prober{Resolver: NewBoundResolver(store, []CredentialBinding{binding})}).Check(ctx, "kubernetes", server.URL, "bearer", ref); err != nil || result.OK || result.ErrorCode != "connection_failed" {
		t.Fatalf("probe without CA must fail TLS: %+v %v", result, err)
	}
	if _, err = (Prober{Resolver: NewBoundResolver(store, nil)}).Check(ctx, "kubernetes", server.URL, "bearer", ref); err != ErrCredentialUnavailable {
		t.Fatalf("unapproved token was read: %v", err)
	}
	if _, err = NewBoundResolver(store, nil).ResolveSibling(ctx, ref, "ca"); err != ErrCredentialUnavailable {
		t.Fatalf("unapproved sibling was read: %v", err)
	}
	if sibling, ok := SiblingReference(ref, "ca"); !ok || sibling != "vault://kv/lab/k8s-reader#ca" {
		t.Fatalf("sibling reference: %q %v", sibling, ok)
	}
	if _, ok := SiblingReference(ref, "../x"); ok {
		t.Fatal("invalid sibling field accepted")
	}
}
