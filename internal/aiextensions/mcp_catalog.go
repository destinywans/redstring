package aiextensions

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPCatalog is one bounded observation from a single initialized MCP session.
// A missing capability is unsupported, while an advertised empty list is
// supported with an empty (non-nil) slice. Neither state grants tool use.
type MCPCatalog struct {
	ServerName      string         `json:"server_name"`
	ServerVersion   string         `json:"server_version"`
	ProtocolVersion string         `json:"protocol_version"`
	Tools           []ToolInfo     `json:"tools"`
	Resources       []ResourceInfo `json:"resources"`
	Prompts         []PromptInfo   `json:"prompts"`
	ToolsState      string         `json:"tools_state"`
	ResourcesState  string         `json:"resources_state"`
	PromptsState    string         `json:"prompts_state"`
}

type ResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type PromptInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

const (
	maxCatalogPages = 8
	maxCatalogItems = 128
	maxCatalogBytes = 512 << 10
)

func catalogText(s string, max int) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func catalogCursor(next string, seen map[string]bool) bool {
	if next == "" {
		return true
	}
	if !catalogText(next, 2048) || seen[next] {
		return false
	}
	seen[next] = true
	return true
}

func catalogTools(ctx context.Context, session *mcp.ClientSession) ([]ToolInfo, error) {
	items := make([]ToolInfo, 0)
	seenCursor := map[string]bool{}
	seenName := map[string]bool{}
	cursor := ""
	for page := 0; page < maxCatalogPages; page++ {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil || result == nil {
			return nil, ErrUnavailable
		}
		for _, tool := range result.Tools {
			if tool == nil || len(items) >= maxCatalogItems || tool.Name == "" ||
				!catalogText(tool.Name, 100) || !catalogText(tool.Description, 2000) || seenName[tool.Name] {
				return nil, ErrUnavailable
			}
			seenName[tool.Name] = true
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil || len(schema) < 2 || len(schema) > 16<<10 || schema[0] != '{' {
				return nil, ErrUnavailable
			}
			items = append(items, ToolInfo{Name: tool.Name, Description: tool.Description,
				ReadOnlyHint: tool.Annotations != nil && tool.Annotations.ReadOnlyHint,
				InputSchema:  json.RawMessage(schema)})
		}
		if !catalogCursor(result.NextCursor, seenCursor) {
			return nil, ErrUnavailable
		}
		if result.NextCursor == "" {
			return items, nil
		}
		cursor = result.NextCursor
	}
	return nil, ErrUnavailable
}

func catalogResources(ctx context.Context, session *mcp.ClientSession) ([]ResourceInfo, error) {
	items := make([]ResourceInfo, 0)
	seenCursor := map[string]bool{}
	seenURI := map[string]bool{}
	cursor := ""
	for page := 0; page < maxCatalogPages; page++ {
		result, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil || result == nil {
			return nil, ErrUnavailable
		}
		for _, resource := range result.Resources {
			if resource == nil || len(items) >= maxCatalogItems || !catalogText(resource.URI, 2048) ||
				!catalogText(resource.Name, 100) || !catalogText(resource.Description, 2000) ||
				resource.URI == "" || seenURI[resource.URI] {
				return nil, ErrUnavailable
			}
			// Remote URIs are displayed only. Reject common credential-bearing
			// forms; never dereference them during discovery.
			u, err := url.Parse(resource.URI)
			if err != nil || u.Scheme == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
				strings.Contains(resource.URI, "\\") {
				return nil, ErrUnavailable
			}
			seenURI[resource.URI] = true
			items = append(items, ResourceInfo{URI: resource.URI, Name: resource.Name,
				Description: resource.Description})
		}
		if !catalogCursor(result.NextCursor, seenCursor) {
			return nil, ErrUnavailable
		}
		if result.NextCursor == "" {
			return items, nil
		}
		cursor = result.NextCursor
	}
	return nil, ErrUnavailable
}

func catalogPrompts(ctx context.Context, session *mcp.ClientSession) ([]PromptInfo, error) {
	items := make([]PromptInfo, 0)
	seenCursor := map[string]bool{}
	seenName := map[string]bool{}
	cursor := ""
	for page := 0; page < maxCatalogPages; page++ {
		result, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil || result == nil {
			return nil, ErrUnavailable
		}
		for _, prompt := range result.Prompts {
			if prompt == nil || len(items) >= maxCatalogItems || prompt.Name == "" ||
				!catalogText(prompt.Name, 100) || !catalogText(prompt.Description, 2000) ||
				seenName[prompt.Name] {
				return nil, ErrUnavailable
			}
			seenName[prompt.Name] = true
			items = append(items, PromptInfo{Name: prompt.Name, Description: prompt.Description})
		}
		if !catalogCursor(result.NextCursor, seenCursor) {
			return nil, ErrUnavailable
		}
		if result.NextCursor == "" {
			return items, nil
		}
		cursor = result.NextCursor
	}
	return nil, ErrUnavailable
}

// DiscoverCatalog initializes one fixed-endpoint Streamable HTTP session and
// reads only advertised list capabilities. It does not execute tools, fetch
// resources, retrieve prompt bodies, or follow remote links.
func DiscoverCatalog(ctx context.Context, credentialRoot string, config MCPConfig) (MCPCatalog, error) {
	var catalog MCPCatalog
	if err := validateMCPTransport(config); err != nil {
		return catalog, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := (&Service{CredentialRoot: credentialRoot}).connect(ctx, config)
	if err != nil {
		return catalog, err
	}
	defer session.Close()
	initial := session.InitializeResult()
	if initial == nil || initial.ServerInfo == nil || initial.Capabilities == nil ||
		initial.ServerInfo.Name == "" || initial.ServerInfo.Version == "" ||
		initial.ProtocolVersion == "" || !catalogText(initial.ServerInfo.Name, 100) ||
		!catalogText(initial.ServerInfo.Version, 100) || !catalogText(initial.ProtocolVersion, 100) {
		return MCPCatalog{}, ErrUnavailable
	}
	catalog.ServerName = initial.ServerInfo.Name
	catalog.ServerVersion = initial.ServerInfo.Version
	catalog.ProtocolVersion = initial.ProtocolVersion
	catalog.ToolsState, catalog.ResourcesState, catalog.PromptsState = "unsupported", "unsupported", "unsupported"
	if initial.Capabilities.Tools != nil {
		catalog.Tools, err = catalogTools(ctx, session)
		if err != nil {
			return MCPCatalog{}, err
		}
		catalog.ToolsState = "supported"
	}
	if initial.Capabilities.Resources != nil {
		catalog.Resources, err = catalogResources(ctx, session)
		if err != nil {
			return MCPCatalog{}, err
		}
		catalog.ResourcesState = "supported"
	}
	if initial.Capabilities.Prompts != nil {
		catalog.Prompts, err = catalogPrompts(ctx, session)
		if err != nil {
			return MCPCatalog{}, err
		}
		catalog.PromptsState = "supported"
	}
	encoded, err := json.Marshal(catalog)
	if err != nil || len(encoded) > maxCatalogBytes {
		return MCPCatalog{}, ErrUnavailable
	}
	return catalog, nil
}
