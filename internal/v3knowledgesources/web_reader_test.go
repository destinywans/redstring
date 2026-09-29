package v3knowledgesources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
)

func TestPublicWebReaderObservesOnlyAnActuallyReadablePage(t *testing.T) {
	const body = "# Public guide\n\nRead the signal before acting.\n"
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	reader := PublicWebReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	spec := v3sourceregistry.ReadSpec{SourceType: "web", URL: "https://example.com/guide.md", EnvironmentID: "dev", ApplicationID: "app"}
	if err := reader.Probe(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	result, err := reader.Read(context.Background(), spec)
	if err != nil || !result.Complete || len(result.Items) != 1 {
		t.Fatalf("read result: %+v %v", result, err)
	}
	want := sha256.Sum256([]byte(body))
	got := result.Items[0]
	if got.ExternalID != spec.URL || got.ContentHash != hex.EncodeToString(want[:]) ||
		got.EnvironmentID != "dev" || got.ApplicationID != "app" || !got.ACLVerified {
		t.Fatalf("observation lacks verified lineage: %+v", got)
	}
	for _, rejected := range []v3sourceregistry.ReadSpec{
		{SourceType: "git", URL: spec.URL, EnvironmentID: "dev"},
		{SourceType: "web", URL: spec.URL, SecretRef: "vault:kv/read", EnvironmentID: "dev"},
		{SourceType: "web", URL: spec.URL, Spaces: []string{"SRE"}, EnvironmentID: "dev"},
		{SourceType: "web", URL: spec.URL, EnvironmentID: "all"},
		{SourceType: "web", URL: "https://example.com/" + strings.Repeat("a", 1000), EnvironmentID: "dev"},
	} {
		if _, err := reader.Read(context.Background(), rejected); !errors.Is(err, ErrUnsupportedWebScope) {
			t.Fatalf("unsupported scope reached fetch: %+v %v", rejected, err)
		}
	}
}

func TestPublicWebObservationReaderReturnsExactContentAndLineage(t *testing.T) {
	const body = "# Public guide\n\nUTF-8: 巡检\n"
	var fetches atomic.Int32
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	reader := PublicWebObservationReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	spec := v3sourceregistry.ReadSpec{SourceType: "web", URL: "https://example.com/guide.md",
		EnvironmentID: "dev", ApplicationID: "app", TeamID: "sre"}
	result, err := reader.ReadContent(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(body))
	if result.Body != body || result.Observation.ExternalID != spec.URL ||
		result.Observation.ContentHash != hex.EncodeToString(want[:]) ||
		result.Observation.EnvironmentID != spec.EnvironmentID ||
		result.Observation.ApplicationID != spec.ApplicationID ||
		result.Observation.TeamID != spec.TeamID || !result.Observation.ACLVerified {
		t.Fatalf("content/lineage mismatch: %+v", result)
	}
	if fetches.Load() != 1 {
		t.Fatalf("ReadContent fetched %d times, want one", fetches.Load())
	}
	observation, err := reader.Read(context.Background(), spec)
	if err != nil || !observation.Complete || len(observation.Items) != 1 ||
		observation.Items[0] != result.Observation {
		t.Fatalf("observation read mismatch: %+v %v", observation, err)
	}
	if fetches.Load() != 2 {
		t.Fatalf("Read fetched %d total times, want two", fetches.Load())
	}

	for _, rejected := range []v3sourceregistry.ReadSpec{
		{SourceType: "web", URL: spec.URL, EnvironmentID: "dev"},
		{SourceType: "web", URL: spec.URL, EnvironmentID: "dev", ApplicationID: "all"},
		{SourceType: "web", URL: spec.URL, ApplicationID: "app"},
		{SourceType: "web", URL: spec.URL, EnvironmentID: "all", ApplicationID: "app"},
		{SourceType: "web", URL: spec.URL, EnvironmentID: "dev", ApplicationID: "app", SecretRef: "vault:secret"},
	} {
		if _, err := reader.ReadContent(context.Background(), rejected); !errors.Is(err, ErrUnsupportedWebScope) {
			t.Fatalf("unsupported scope reached fetch: %+v %v", rejected, err)
		}
	}
	if fetches.Load() != 2 {
		t.Fatalf("invalid scope fetched %d total times, want two", fetches.Load())
	}
}
