package aiextensions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type memoryRepository struct{ records map[string][]Record }

func (p *memoryRepository) List(_ context.Context, app, kind string) ([]Record, error) {
	out := []Record{}
	for _, rs := range p.records {
		r := rs[len(rs)-1]
		if r.ApplicationID == app && r.Kind == kind {
			out = append(out, r)
		}
	}
	return out, nil
}
func (p *memoryRepository) Get(_ context.Context, id string, v int) (Record, error) {
	rs := p.records[id]
	if len(rs) == 0 {
		return Record{}, store.ErrNotFound
	}
	if v == 0 {
		v = len(rs)
	}
	if v > len(rs) {
		return Record{}, store.ErrNotFound
	}
	return rs[v-1], nil
}
func (p *memoryRepository) Save(_ context.Context, r Record, expected int) (Record, error) {
	if len(p.records[r.ID]) != expected {
		return Record{}, store.ErrConflict
	}
	p.records[r.ID] = append(p.records[r.ID], r)
	return r, nil
}
func serviceForTest() (*Service, context.Context) {
	return &Service{Repository: &memoryRepository{records: map[string][]Record{}}, Authorize: func(_ context.Context, u core.User, app, permission string) error {
		if u.ID != "user" || app != "app-a" {
			return store.ErrForbidden
		}
		return nil
	}}, store.WithPrincipal(context.Background(), core.User{ID: "user", Role: "admin"})
}
func TestVersionCompositionAndApplicationIsolation(t *testing.T) {
	s, ctx := serviceForTest()
	r, e := s.Save(ctx, Record{ApplicationID: "app-a", Kind: "skill", Title: "SRE", Body: "old fact", Maintainer: "owner", Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	old := r
	r.Body = "new fact"
	r, e = s.Save(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Save(ctx, old); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stale save was accepted", e)
	}
	composed, e := s.Compose(ctx, Composition{ApplicationID: "app-a", References: []Reference{{ID: old.ID, Version: 1}}, Question: "why?"})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(composed.Content, "version=1 sha256="+old.ContentHash) || !strings.Contains(composed.Content, "old fact") || strings.Contains(composed.Content, "new fact") {
		t.Fatal(composed)
	}
	if _, e = s.Compose(ctx, Composition{ApplicationID: "app-b", References: []Reference{{ID: r.ID, Version: 1}}}); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("cross-app composition", e)
	}
	r.Enabled = false
	if _, e = s.Save(ctx, r); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Compose(ctx, Composition{ApplicationID: "app-a", References: []Reference{{ID: r.ID, Version: 1}}}); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("disabled source composed", e)
	}
}

func TestMCPRealHTTPProtocolFixedScopeRevocationAndReadOnly(t *testing.T) {
	s, ctx := serviceForTest()
	var calls atomic.Int32
	var sawAuth atomic.Bool
	server := mcp.NewServer(&mcp.Implementation{Name: "isolated-test", Version: "1"}, nil)
	type Input struct {
		Application string `json:"application"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "snapshot", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *mcp.CallToolRequest, in Input) (*mcp.CallToolResult, any, error) {
		calls.Add(1)
		if in.Application != "app-a" {
			t.Errorf("scope override: %s", in.Application)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "observed app-a"}}}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "write", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(context.Context, *mcp.CallToolRequest, Input) (*mcp.CallToolResult, any, error) {
		calls.Add(100)
		return &mcp.CallToolResult{}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-test-token" {
			w.WriteHeader(401)
			return
		}
		sawAuth.Store(true)
		handler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	s.CredentialRoot = t.TempDir()
	if e := os.Chmod(s.CredentialRoot, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(s.CredentialRoot, "token"), []byte("secret-test-token"), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := s.Save(ctx, Record{ApplicationID: "app-a", Kind: "mcp", Title: "Lab", Maintainer: "owner", Enabled: true, MCP: &MCPConfig{Endpoint: remote.URL, AllowHTTP: true, CredentialRef: "token", Bindings: []Binding{{Name: "snapshot", Tool: "snapshot", ReadOnlyConfirmed: true, Arguments: map[string]any{"application": "app-a"}}}}})
	if e != nil {
		t.Fatal(e)
	}
	discovery, e := s.Discover(ctx, r.ID)
	if e != nil || len(discovery) != 2 || !sawAuth.Load() {
		t.Fatal(discovery, e)
	}
	tools, e := s.Tools(ctx, "app-a")
	if e != nil || len(tools) != 1 {
		t.Fatal(tools, e)
	}
	ev, e := tools[0].Read(ctx, "")
	if e != nil || !strings.Contains(ev.Content, "observed app-a") {
		t.Fatal(ev, e)
	}
	if _, e = tools[0].Read(ctx, `{"application":"app-b"}`); !errors.Is(e, ErrInvalid) {
		t.Fatal("query override", e)
	}
	if _, e = s.Tools(ctx, "app-b"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("cross-app tool", e)
	}
	viewer := store.WithPrincipal(ctx, core.User{ID: "user", Role: "viewer"})
	if _, e = s.Get(viewer, r.ID, 0); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("viewer discovered credentials config", e)
	}
	r.MCP.Bindings[0].Tool = "write"
	r, e = s.Save(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tools[0].Read(ctx, ""); !errors.Is(e, store.ErrConflict) {
		t.Fatal("stale allowlist accepted", e)
	}
	if _, e = s.Test(ctx, r.ID, "snapshot"); !errors.Is(e, store.ErrForbidden) {
		t.Fatal("write hint allowed", e)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected remote calls", calls.Load())
	}
}

func TestCredentialAndEndpointBoundaries(t *testing.T) {
	s, _ := serviceForTest()
	s.CredentialRoot = t.TempDir()
	if e := os.Chmod(s.CredentialRoot, 0700); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(s.CredentialRoot, "secret")
	_ = os.WriteFile(p, []byte("sensitive"), 0644)
	if _, e := s.token("secret"); e == nil {
		t.Fatal("public credential file accepted")
	}
	_ = os.Chmod(p, 0600)
	_ = os.Symlink(p, filepath.Join(s.CredentialRoot, "link"))
	if _, e := s.token("link"); e == nil {
		t.Fatal("symlink accepted")
	}
	if _, e := s.token("../secret"); e == nil {
		t.Fatal("path traversal accepted")
	}
	for _, endpoint := range []string{"http://example.com/mcp", "https://user:pass@example.com/mcp", "https://example.com/mcp?token=secret"} {
		r := Record{ApplicationID: "a", Kind: "mcp", Title: "m", Maintainer: "o", MCP: &MCPConfig{Endpoint: endpoint, AllowHTTP: true}}
		if validate(r) == nil {
			t.Fatal(endpoint)
		}
	}
	raw, _ := json.Marshal(Examples())
	if strings.Contains(string(raw), "secret") {
		t.Fatal("example contains secrets")
	}
}
