package aiextensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type ToolInfo struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	ReadOnlyHint bool   `json:"read_only_hint"`
	InputSchema  any    `json:"input_schema"`
}
type boundedBody struct {
	io.Reader
	io.Closer
}
type endpointTransport struct {
	endpoint, token string
	next            http.RoundTripper
}

func (t endpointTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	// SDK and remote metadata cannot send this credential to another URL.
	if r.URL.String() != t.endpoint {
		return nil, ErrUnavailable
	}
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	}
	response, e := t.next.RoundTrip(r)
	if e != nil {
		return nil, ErrUnavailable
	}
	response.Body = boundedBody{Reader: io.LimitReader(response.Body, 2<<20), Closer: response.Body}
	return response, nil
}
func (s *Service) token(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	if s.CredentialRoot == "" || !ident.MatchString(ref) || ref == "." || ref == ".." {
		return "", ErrInvalid
	}
	root, e := filepath.EvalSymlinks(s.CredentialRoot)
	if e != nil {
		return "", ErrUnavailable
	}
	root, e = filepath.Abs(root)
	if e != nil {
		return "", ErrUnavailable
	}
	rootInfo, e := os.Stat(root)
	if e != nil || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0077 != 0 {
		return "", ErrUnavailable
	}
	path := filepath.Join(root, ref)
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return "", ErrUnavailable
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return "", ErrUnavailable
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", ErrUnavailable
	}
	return token, nil
}
func (s *Service) connect(ctx context.Context, c MCPConfig) (*mcp.ClientSession, error) {
	token, e := s.token(c.CredentialRef)
	if e != nil {
		return nil, e
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// Sessions are deliberately short-lived; don't retain a new idle connection
	// pool after every evidence read.
	transport.DisableKeepAlives = true
	client := &http.Client{Transport: endpointTransport{c.Endpoint, token, transport}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	sdk := mcp.NewClient(&mcp.Implementation{Name: "aiops-canvas", Version: "1"}, nil)
	session, e := sdk.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: c.Endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if e != nil {
		transport.CloseIdleConnections()
		return nil, ErrUnavailable
	}
	return session, nil
}
func list(ctx context.Context, session *mcp.ClientSession) ([]ToolInfo, error) {
	result := []ToolInfo{}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 8; page++ {
		r, e := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if e != nil || r == nil {
			return nil, ErrUnavailable
		}
		for _, t := range r.Tools {
			if len(result) >= 128 || len(t.Name) > 100 || len(t.Description) > 8000 {
				return nil, ErrUnavailable
			}
			result = append(result, ToolInfo{Name: t.Name, Description: t.Description, ReadOnlyHint: t.Annotations != nil && t.Annotations.ReadOnlyHint, InputSchema: t.InputSchema})
		}
		if r.NextCursor == "" {
			return result, nil
		}
		if seen[r.NextCursor] {
			return nil, ErrUnavailable
		}
		seen[r.NextCursor] = true
		cursor = r.NextCursor
	}
	return nil, ErrUnavailable
}
func (s *Service) Discover(ctx context.Context, id string) ([]ToolInfo, error) {
	r, e := s.Get(ctx, id, 0)
	if e != nil {
		return nil, e
	}
	if r.Kind != "mcp" || r.MCP == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, e := s.connect(ctx, *r.MCP)
	if e != nil {
		return nil, e
	}
	defer session.Close()
	return list(ctx, session)
}
func toolName(id, binding string) string {
	h := sha256.Sum256([]byte(id + ":" + binding))
	return "mcp_" + hex.EncodeToString(h[:8])
}

// Tools builds only scope-authorized, fixed-argument snapshots. A malicious model
// cannot substitute app IDs, targets, URLs or free-form queries into MCP calls.
// Server hints are checked in addition to the administrator's explicit allowlist;
// neither hints nor descriptions independently grant permission.
func (s *Service) Tools(ctx context.Context, app string) ([]inference.EvidenceTool, error) {
	if e := s.authorize(ctx, app, "operate"); e != nil {
		return nil, e
	}
	records, e := s.Repository.List(ctx, app, "mcp")
	if e != nil {
		return nil, e
	}
	out := []inference.EvidenceTool{}
	for _, r := range records {
		if !r.Enabled || r.MCP == nil {
			continue
		}
		for _, b := range r.MCP.Bindings {
			if len(out) >= 8 {
				return nil, ErrInvalid
			}
			out = append(out, inference.EvidenceTool{Name: toolName(r.ID, b.Name), Description: fmt.Sprintf("Read-only registered snapshot %s: %s. Application and arguments are fixed by an administrator. query must be empty; do not pass targets or instructions.", b.Name, b.Description), Read: func(callCtx context.Context, q string) (inference.Evidence, error) {
				if strings.TrimSpace(q) != "" && strings.TrimSpace(q) != "{}" {
					return inference.Evidence{}, ErrInvalid
				}
				return s.read(callCtx, app, r.ID, r.Version, b.Name)
			}})
		}
	}
	return out, nil
}
func (s *Service) read(ctx context.Context, app, id string, version int, name string) (inference.Evidence, error) {
	if e := s.authorize(ctx, app, "operate"); e != nil {
		return inference.Evidence{}, e
	}
	r, e := s.Repository.Get(ctx, id, 0)
	if e != nil {
		return inference.Evidence{}, e
	}
	if r.ApplicationID != app || r.Version != version || !r.Enabled || r.MCP == nil {
		return inference.Evidence{}, store.ErrConflict
	}
	var binding *Binding
	for _, b := range r.MCP.Bindings {
		if b.Name == name {
			copy := b
			binding = &copy
		}
	}
	if binding == nil || !binding.ReadOnlyConfirmed {
		return inference.Evidence{}, store.ErrForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, e := s.connect(ctx, *r.MCP)
	if e != nil {
		return inference.Evidence{}, e
	}
	defer session.Close()
	tools, e := list(ctx, session)
	if e != nil {
		return inference.Evidence{}, e
	}
	allowed := false
	for _, t := range tools {
		if t.Name == binding.Tool && t.ReadOnlyHint {
			allowed = true
		}
	}
	if !allowed {
		return inference.Evidence{}, store.ErrForbidden
	}
	// Recheck ACL/config immediately before the remote operation, not only discovery.
	if e = s.authorize(ctx, app, "operate"); e != nil {
		return inference.Evidence{}, e
	}
	current, e := s.Repository.Get(ctx, id, 0)
	if e != nil {
		return inference.Evidence{}, e
	}
	if current.Version != version || !current.Enabled {
		return inference.Evidence{}, store.ErrConflict
	}
	result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: binding.Tool, Arguments: binding.Arguments})
	if e != nil || result == nil || result.IsError {
		return inference.Evidence{}, ErrUnavailable
	}
	// No resource-link dereferencing, image fetching, prompts, sampling or elicitation.
	texts := []string{}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	raw, e := json.Marshal(map[string]any{"text": texts, "structured_content": result.StructuredContent, "captured_at": time.Now().UTC(), "extension_id": id, "extension_version": version, "binding": name})
	if e != nil || len(raw) > 65536 {
		return inference.Evidence{}, ErrUnavailable
	}
	return inference.Evidence{Kind: "mcp", Title: r.Title + " · " + name, Content: string(raw), Source: fmt.Sprintf("mcp:%s@%d/%s", id, version, name)}, nil
}
func (s *Service) Test(ctx context.Context, id, binding string) (inference.Evidence, error) {
	r, e := s.Get(ctx, id, 0)
	if e != nil {
		return inference.Evidence{}, e
	}
	if store.Principal(ctx).Role != "admin" {
		return inference.Evidence{}, store.ErrForbidden
	}
	return s.read(ctx, r.ApplicationID, id, r.Version, binding)
}
