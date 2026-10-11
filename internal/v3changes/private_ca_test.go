package v3changes

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// siblingResolver serves the token and, when set, the "ca" field next to it.
type siblingResolver struct{ token, ca string }

func (r siblingResolver) Resolve(_ context.Context, ref string) (string, error) {
	if ref != "vault://isolated/gitlab#token" {
		return "", errors.New("missing credential")
	}
	return r.token, nil
}

func (r siblingResolver) ResolveSibling(_ context.Context, ref, field string) (string, error) {
	if ref != "vault://isolated/gitlab#token" || field != "ca" || r.ca == "" {
		return "", errors.New("missing sibling")
	}
	return r.ca, nil
}

// D11: an HTTPS GitLab with a private CA is trusted through the "ca" field
// next to the read token, and only through it.
func TestReadServiceChangesTrustsTheConnectorCA(t *testing.T) {
	f := newChangesFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(f.gitlab.serve))
	t.Cleanup(server.Close)
	if _, err := f.db.Exec(t.Context(), `UPDATE v3_connectors SET endpoint=$1 WHERE id='gitlab-dev'`, server.URL); err != nil {
		t.Fatal(err)
	}
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	f.service.Client = nil
	f.service.Resolver = siblingResolver{token: fakeSecret}
	if _, err := f.read(t); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an untrusted certificate was accepted: %v", err)
	}
	f.service.Resolver = siblingResolver{token: fakeSecret, ca: ca}
	if got, err := f.read(t); err != nil || len(got.Deployments) != 2 {
		t.Fatalf("private CA: %+v %v", got, err)
	}
	f.service.Resolver = siblingResolver{token: fakeSecret, ca: "not a certificate"}
	if _, err := f.read(t); !errors.Is(err, ErrCredential) {
		t.Fatalf("a malformed CA: %v", err)
	}
}
