package v3knowledgesources

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/v3sourceregistry"
)

func TestPublicWebObservationProbeReadsOnePublicMarkdownPage(t *testing.T) {
	var fetches atomic.Int32
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.Method != http.MethodGet || r.Host != "example.com" || r.URL.Path != "/guide.md" {
			t.Errorf("unexpected probe request: %s %s %s", r.Method, r.Host, r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Public guide\n\nCheck status.\n"))
	}))
	defer server.Close()
	reader := PublicWebObservationReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	spec := v3sourceregistry.ReadSpec{SourceType: "web", URL: "https://example.com/guide.md", EnvironmentID: "dev"}
	if err := reader.Probe(context.Background(), spec); err != nil {
		t.Fatalf("readable Markdown probe failed: %v", err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("probe fetched %d times, want one", fetches.Load())
	}
	for name, mutate := range map[string]func(*v3sourceregistry.ReadSpec){
		"other type":       func(v *v3sourceregistry.ReadSpec) { v.SourceType = "git" },
		"credentialed web": func(v *v3sourceregistry.ReadSpec) { v.SecretRef = "vault:kv/web" },
		"spaces":           func(v *v3sourceregistry.ReadSpec) { v.Spaces = []string{"private"} },
		"application":      func(v *v3sourceregistry.ReadSpec) { v.ApplicationID = "app" },
		"team":             func(v *v3sourceregistry.ReadSpec) { v.TeamID = "team" },
		"all environments": func(v *v3sourceregistry.ReadSpec) { v.EnvironmentID = "all" },
		"http":             func(v *v3sourceregistry.ReadSpec) { v.URL = "http://example.com/guide.md" },
	} {
		t.Run(name, func(t *testing.T) {
			rejected := spec
			mutate(&rejected)
			if err := reader.Probe(context.Background(), rejected); !errors.Is(err, ErrUnsupportedWebScope) {
				t.Fatalf("got %v, want unsupported scope", err)
			}
		})
	}
	if fetches.Load() != 1 {
		t.Fatalf("rejected probes reached source: %d fetches", fetches.Load())
	}
}

func TestPublicWebObservationProbeFailsClosedOnUnreadablePage(t *testing.T) {
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing.md":
			http.NotFound(w, r)
		case "/html.md":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<h1>not Markdown</h1>"))
		case "/empty.md":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("  \n"))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	reader := PublicWebObservationReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	for path, want := range map[string]error{
		"/missing.md": ErrWebFetchFailed,
		"/html.md":    ErrWebContent,
		"/empty.md":   ErrWebContent,
	} {
		t.Run(path, func(t *testing.T) {
			err := reader.Probe(context.Background(), v3sourceregistry.ReadSpec{
				SourceType: "web", URL: "https://example.com" + path, EnvironmentID: "dev",
			})
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}

func TestPublicWebObservationProbeHonorsTimeoutAndCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	server, roots := webTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	reader := PublicWebObservationReader{Fetcher: webTestFetcher(server, roots,
		&fixedWebResolver{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}, nil)}
	spec := v3sourceregistry.ReadSpec{SourceType: "web", URL: "https://example.com/slow.md", EnvironmentID: "dev"}
	deadlineCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := reader.Probe(deadlineCtx, spec); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("deadline probe did not reach local TLS server")
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- reader.Probe(cancelCtx, spec) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("cancellation probe did not reach local TLS server")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe ignored cancellation")
	}
}
