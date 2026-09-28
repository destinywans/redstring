package v3conversation

import (
	"encoding/json"
	"strings"
)

// conversationDetail derives the side panel from saved conversation data. In
// particular, a connector is counted only after its source was recorded by a
// successful evidence read; an unconfigured connector is never invented here.
func (s *Service) conversationDetail(id, title, status, env, contextRef, defaultModel string, messages []Message) Conversation {
	scope := map[string]any{"env": env, "inferred": false}
	for _, ref := range strings.Split(contextRef, ",") {
		kind, service, ok := strings.Cut(strings.TrimSpace(ref), ":")
		if ok && kind == "service" && service != "" {
			scope["service"] = service
			break
		}
	}
	connectors := []map[string]any{}
	counts := map[string]int{}
	order := []string{}
	// Older replies may not have provider usage. Keep it explicitly unknown
	// instead of presenting a fabricated zero-token context.
	context := map[string]any{"used": nil, "budget": nil, "parts": []any{}}
	if s.runtime != nil {
		for _, model := range s.runtime.Models() {
			if model.ID == defaultModel && model.InputBudget > 0 {
				context["budget"] = model.InputBudget
				break
			}
		}
	}
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		// The side panel describes the most recent assistant turn. An older
		// reported input count must not survive a newer turn with unknown usage.
		context["used"] = nil
		context["parts"] = []any{}
		for _, raw := range m.Parts {
			var part struct {
				Type  string `json:"type"`
				Calls []struct {
					Connector string `json:"connector"`
					Status    string `json:"status"`
				} `json:"calls"`
			}
			if json.Unmarshal(raw, &part) != nil || part.Type != "tool_calls" {
				continue
			}
			for _, call := range part.Calls {
				if call.Connector == "" || call.Status != "ok" {
					continue
				}
				if counts[call.Connector] == 0 {
					order = append(order, call.Connector)
				}
				counts[call.Connector]++
			}
		}
		if len(m.Run) > 0 {
			var run struct {
				ContextInputTokens *int `json:"context_input_tokens"`
				ContextBudget      *int `json:"context_budget"`
			}
			if json.Unmarshal(m.Run, &run) == nil && run.ContextInputTokens != nil && run.ContextBudget != nil && *run.ContextBudget > 0 {
				context["used"] = *run.ContextInputTokens
				context["budget"] = *run.ContextBudget
				context["parts"] = []map[string]any{{"label": "模型输入", "tokens": *run.ContextInputTokens, "color": "var(--c1)"}}
			}
		}
	}
	if len(messages) == 0 {
		context["used"] = 0
	}
	for _, name := range order {
		connectors = append(connectors, map[string]any{"name": name, "calls": counts[name]})
	}
	// There is no chat-artifact or ranked-memory-recall writer yet. These are
	// genuinely empty for the current chat pipeline; do not copy mock records.
	return Conversation{
		ID: id, Title: title, Mode: "chat", Status: status, Scope: scope, Messages: messages,
		Artifacts: []any{}, Side: map[string]any{"connectors": connectors, "context": context, "recalled": []any{}},
	}
}
