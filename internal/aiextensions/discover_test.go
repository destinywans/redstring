package aiextensions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoverToolsUsesBoundedProtocolWithoutCallingTools(t *testing.T) {
	var calls atomic.Int32
	var requests atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "local-discovery", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "snapshot", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			calls.Add(1)
			return &mcp.CallToolResult{}, nil, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer local-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "token"), []byte("local-token"), 0600); err != nil {
		t.Fatal(err)
	}
	config := MCPConfig{Endpoint: remote.URL, AllowHTTP: true, CredentialRef: "token"}
	tools, err := DiscoverTools(context.Background(), root, config)
	if err != nil || len(tools) != 1 || tools[0].Name != "snapshot" || !tools[0].ReadOnlyHint {
		t.Fatalf("discovery: tools=%+v err=%v", tools, err)
	}
	if calls.Load() != 0 || requests.Load() == 0 {
		t.Fatalf("unexpected protocol operations: requests=%d calls=%d", requests.Load(), calls.Load())
	}

	before := requests.Load()
	for _, invalid := range []MCPConfig{
		{Endpoint: "http://example.invalid/mcp", AllowHTTP: true},
		{Endpoint: remote.URL + "?token=secret", AllowHTTP: true},
		{Endpoint: remote.URL, AllowHTTP: true, CredentialRef: "../token"},
	} {
		if _, err := DiscoverTools(context.Background(), root, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid configuration accepted: %+v err=%v", invalid, err)
		}
	}
	if requests.Load() != before {
		t.Fatal("invalid configuration contacted remote MCP")
	}
	if err := os.Chmod(filepath.Join(root, "token"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverTools(context.Background(), root, config); !errors.Is(err, ErrUnavailable) || requests.Load() != before {
		t.Fatalf("insecure credential accepted: err=%v requests=%d", err, requests.Load())
	}
}

func TestDiscoverToolsDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := DiscoverTools(context.Background(), "", MCPConfig{Endpoint: source.URL, AllowHTTP: true})
	if !errors.Is(err, ErrUnavailable) || redirected.Load() != 0 {
		t.Fatalf("redirect followed: err=%v sink_requests=%d", err, redirected.Load())
	}
}
