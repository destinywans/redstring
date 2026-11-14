package aiextensions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const MaxReviewedMCPReadBytes = 16 << 10

// ReviewedMCPRead is a bounded, no-argument MCP call on a deployment-admitted
// transport. verify compares current remote metadata with the approved
// declaration and checks local revocation before the SDK issues tools/call.
func ReviewedMCPRead(ctx context.Context, root string, config MCPConfig, name string, verify func(context.Context, MCPCatalog) error) (string, error) {
	if err := validateMCPTransport(config); err != nil || !ident.MatchString(name) || verify == nil {
		return "", ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := (&Service{CredentialRoot: root}).token(config.CredentialRef)
	if err != nil {
		return "", ErrUnavailable
	}
	session, err := connectMCP(ctx, config.Endpoint, token)
	if err != nil {
		return "", err
	}
	defer session.Close()
	init := session.InitializeResult()
	if init == nil || init.ServerInfo == nil || init.Capabilities == nil || init.Capabilities.Tools == nil {
		return "", ErrUnavailable
	}
	tools, err := catalogTools(ctx, session)
	if err != nil {
		return "", err
	}
	catalog := MCPCatalog{ServerName: init.ServerInfo.Name, ServerVersion: init.ServerInfo.Version,
		ProtocolVersion: init.ProtocolVersion, Tools: tools, ToolsState: "supported"}
	if err = verify(ctx, catalog); err != nil {
		return "", err
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
	if err != nil || result == nil || result.IsError {
		return "", ErrUnavailable
	}
	return reviewedMCPResponse(result, token)
}

// Accept text and structured data only. Prompts, resource dereferencing and
// other returned content cannot initiate requests. An echoed transport secret
// fails closed before evidence can reach a model, durable result or audit.
func reviewedMCPResponse(result *mcp.CallToolResult, token string) (string, error) {
	texts := make([]string, 0)
	total := 0
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			total += len(text.Text)
			if total > MaxReviewedMCPReadBytes || !utf8.ValidString(text.Text) || strings.ContainsRune(text.Text, '\x00') ||
				(token != "" && strings.Contains(text.Text, token)) {
				return "", ErrUnavailable
			}
			texts = append(texts, text.Text)
		}
	}
	if len(texts) == 0 && result.StructuredContent == nil {
		return "", ErrUnavailable
	}
	body, err := json.Marshal(struct {
		Text       []string `json:"text"`
		Structured any      `json:"structured_content,omitempty"`
	}{texts, result.StructuredContent})
	if err != nil || len(body) > MaxReviewedMCPReadBytes {
		return "", ErrUnavailable
	}
	if token != "" {
		// Scan the serialized string too, including JSON-escaped credentials.
		encodedToken, err := json.Marshal(token)
		if err != nil || len(encodedToken) < 3 || strings.Contains(string(body), string(encodedToken[1:len(encodedToken)-1])) {
			return "", errors.New("MCP evidence was unavailable")
		}
	}
	return string(body), nil
}
