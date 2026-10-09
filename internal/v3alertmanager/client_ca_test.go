package v3alertmanager

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointCAFileTrustsOnlyItsEnvironment(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := New(map[string]Endpoint{
		"prod":    {URL: srv.URL, CAFile: ca},
		"staging": {URL: srv.URL},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListSilences(context.Background(), "prod"); err != nil {
		t.Fatalf("prod with its CA: %v", err)
	}
	if _, err := client.ListSilences(context.Background(), "staging"); err == nil {
		t.Fatal("staging must not inherit another environment's CA")
	}
	if _, err := New(map[string]Endpoint{"prod": {URL: "http://127.0.0.1:9093", AllowHTTP: true, CAFile: ca}}, nil); err == nil {
		t.Fatal("a CA file with plain HTTP must be rejected")
	}
	if _, err := New(map[string]Endpoint{"prod": {URL: srv.URL, CAFile: "relative.pem"}}, nil); err == nil {
		t.Fatal("a relative CA path must be rejected")
	}
}
