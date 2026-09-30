package aiextensions

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func catalogFixture(t *testing.T, server *mcp.Server, token string, observe func(string)) (MCPConfig, string) {
	t.Helper()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if observe != nil && r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, 32<<10))
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			var request struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &request) == nil {
				observe(request.Method)
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	root := t.TempDir()
	if token != "" {
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "token"), []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config := MCPConfig{Endpoint: remote.URL, AllowHTTP: true}
	if token != "" {
		config.CredentialRef = "token"
	}
	return config, root
}

func TestDiscoverCatalogObservesAllAdvertisedListsWithoutExecuting(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1.2.3"},
		&mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"read_a", "read_b"} {
		mcp.AddTool(server, &mcp.Tool{Name: name, Description: "read", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
				t.Error("catalog executed tool")
				return &mcp.CallToolResult{}, nil, nil
			})
	}
	for _, uri := range []string{"test://fixture/a", "test://fixture/b"} {
		server.AddResource(&mcp.Resource{URI: uri, Name: uri, Description: "observed"},
			func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				t.Error("catalog read resource")
				return &mcp.ReadResourceResult{}, nil
			})
	}
	server.AddPrompt(&mcp.Prompt{Name: "summary", Description: "observed prompt"},
		func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			t.Error("catalog retrieved prompt")
			return &mcp.GetPromptResult{}, nil
		})
	var mu sync.Mutex
	methods := map[string]int{}
	config, root := catalogFixture(t, server, "local-only", func(method string) {
		mu.Lock()
		methods[method]++
		mu.Unlock()
	})
	catalog, err := DiscoverCatalog(context.Background(), root, config)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ServerName != "fixture" || catalog.ServerVersion != "1.2.3" || catalog.ProtocolVersion == "" ||
		catalog.ToolsState != "supported" || catalog.ResourcesState != "supported" || catalog.PromptsState != "supported" ||
		len(catalog.Tools) != 2 || len(catalog.Resources) != 2 || len(catalog.Prompts) != 1 {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	if catalog.Tools[0].Name != "read_a" || catalog.Resources[0].URI != "test://fixture/a" ||
		catalog.Prompts[0].Name != "summary" {
		t.Fatalf("wrong observed items: %+v", catalog)
	}
	mu.Lock()
	defer mu.Unlock()
	if methods["tools/list"] < 2 || methods["resources/list"] < 2 || methods["prompts/list"] != 1 ||
		methods["tools/call"] != 0 || methods["resources/read"] != 0 || methods["prompts/get"] != 0 {
		t.Fatalf("unexpected MCP methods: %+v", methods)
	}
}

func TestDiscoverCatalogDistinguishesUnsupportedAndAdvertisedEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		server *mcp.Server
		state  string
	}{
		{"unsupported", mcp.NewServer(&mcp.Implementation{Name: "none", Version: "1"}, nil), "unsupported"},
		{"advertised_empty", mcp.NewServer(&mcp.Implementation{Name: "empty", Version: "1"},
			&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{}, Resources: &mcp.ResourceCapabilities{}, Prompts: &mcp.PromptCapabilities{},
			}}), "supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, root := catalogFixture(t, tc.server, "", nil)
			catalog, err := DiscoverCatalog(context.Background(), root, config)
			if err != nil {
				t.Fatal(err)
			}
			if catalog.ToolsState != tc.state || catalog.ResourcesState != tc.state || catalog.PromptsState != tc.state {
				t.Fatalf("wrong capability states: %+v", catalog)
			}
			if tc.state == "supported" && (catalog.Tools == nil || catalog.Resources == nil || catalog.Prompts == nil) {
				t.Fatalf("advertised empty list must be observed, not unknown: %+v", catalog)
			}
			if tc.state == "unsupported" && (catalog.Tools != nil || catalog.Resources != nil || catalog.Prompts != nil) {
				t.Fatalf("unsupported list must not imply zero: %+v", catalog)
			}
		})
	}
}

func TestDiscoverCatalogFailsClosedOnAdvertisedListErrorAndPaginationLimit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		makeServer  func() *mcp.Server
		breakMethod string
	}{
		{"resource_failure", func() *mcp.Server {
			s := mcp.NewServer(&mcp.Implementation{Name: "error", Version: "1"},
				&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}}})
			return s
		}, "resources/list"},
		{"too_many_pages", func() *mcp.Server {
			s := mcp.NewServer(&mcp.Implementation{Name: "pages", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
			for i := 0; i < maxCatalogPages+1; i++ {
				s.AddResource(&mcp.Resource{URI: "test://resource/" + string(rune('a'+i)), Name: "resource"}, nil)
			}
			return s
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := tc.makeServer()
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server },
				&mcp.StreamableHTTPOptions{Stateless: true})
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.breakMethod != "" && r.Body != nil {
					body, _ := io.ReadAll(io.LimitReader(r.Body, 32<<10))
					_ = r.Body.Close()
					if strings.Contains(string(body), `"method":"`+tc.breakMethod+`"`) {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					r.Body = io.NopCloser(strings.NewReader(string(body)))
				}
				handler.ServeHTTP(w, r)
			}))
			defer remote.Close()
			catalog, err := DiscoverCatalog(context.Background(), "", MCPConfig{Endpoint: remote.URL, AllowHTTP: true})
			if !errors.Is(err, ErrUnavailable) || catalog.ServerName != "" || catalog.Tools != nil ||
				catalog.Resources != nil || catalog.Prompts != nil {
				t.Fatalf("partial or unbounded discovery accepted: %+v, %v", catalog, err)
			}
		})
	}
}

func TestCatalogCursorRejectsCyclesAndInvalidValues(t *testing.T) {
	seen := map[string]bool{}
	if !catalogCursor("opaque", seen) || catalogCursor("opaque", seen) ||
		catalogCursor(strings.Repeat("x", 2049), seen) || catalogCursor("bad\n", seen) {
		t.Fatal("invalid or repeated cursor accepted")
	}
	if catalogText(string([]byte{0xff}), 100) || catalogText(strings.Repeat("x", 101), 100) ||
		catalogText("bad\ntext", 100) {
		t.Fatal("invalid catalog metadata accepted")
	}
}

func TestDiscoverCatalogRejectsCredentialBearingResourceURI(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddResource(&mcp.Resource{URI: "https://user:password@example.invalid/private", Name: "private"}, nil)
	config, root := catalogFixture(t, server, "", nil)
	catalog, err := DiscoverCatalog(context.Background(), root, config)
	if !errors.Is(err, ErrUnavailable) || catalog.ServerName != "" || catalog.Resources != nil {
		t.Fatalf("credential-bearing URI disclosed: %+v, %v", catalog, err)
	}
}
