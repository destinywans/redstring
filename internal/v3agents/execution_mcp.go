package v3agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/v3mcp"
	"github.com/jackc/pgx/v5"
)

var mcpExecutionName = regexp.MustCompile(`^mcp_[0-9a-f]{32}$`)
var errMCPReadUnavailable = errors.New("an approved MCP read is no longer available to this Agent")

func (s *Service) executionMCPTools(w http.ResponseWriter, r *http.Request) {
	tx, envs, ok := s.beginLifecycleRead(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	a, ok := s.readLifecycleObject(w, r, tx, envs)
	if !ok {
		return
	}
	if err := s.authorizeLifecycle(r.Context(), tx, r, a, "manage"); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	items := []v3mcp.AgentReadTool{}
	if s.MCP != nil {
		var err error
		items, err = s.MCP.AgentReadToolsTx(r.Context(), tx, httpapi.CurrentUser(r), a.App, a.Env)
		if err != nil {
			unavailable(w)
			return
		}
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (s *Service) resolveMCPToolsTx(ctx context.Context, tx pgx.Tx, r *http.Request, a lifecycleAgent, c ExecutionConfig) ([]v3mcp.AgentReadTool, error) {
	if !slices.ContainsFunc(c.ToolNames, mcpExecutionName.MatchString) {
		return nil, nil
	}
	if s.MCP == nil || s.MCPModelAllowed == nil || !s.MCPModelAllowed(c.ModelID) {
		return nil, fmt.Errorf("%w: %w", errExecutionDrift, errMCPReadUnavailable)
	}
	current, err := s.MCP.AgentReadToolsTx(ctx, tx, httpapi.CurrentUser(r), a.App, a.Env)
	if err != nil {
		return nil, err
	}
	out := make([]v3mcp.AgentReadTool, 0, len(c.ToolNames))
	for _, name := range c.ToolNames {
		if !mcpExecutionName.MatchString(name) {
			continue
		}
		index := slices.IndexFunc(current, func(tool v3mcp.AgentReadTool) bool { return tool.ID == name })
		if index < 0 {
			return nil, fmt.Errorf("%w: %w", errExecutionDrift, errMCPReadUnavailable)
		}
		out = append(out, current[index])
	}
	return out, nil
}
