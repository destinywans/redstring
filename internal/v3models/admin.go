package v3models

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// These DTOs mirror the existing V3 ModelAdminView. Their status fields remain
// unverified because a metadata lookup cannot attest generation or continuity.
type adminView struct {
	Providers []adminProvider `json:"providers"`
	Items     []adminItem     `json:"items"`
	Detail    adminDetail     `json:"detail"`
}

type adminProvider struct {
	Brand  string `json:"brand"`
	Name   string `json:"name"`
	API    string `json:"api"`
	Models int    `json:"models"`
	Status string `json:"status"`
}

type adminReasoningLevel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type adminReasoning struct {
	Kind   string                `json:"kind"`
	Levels []adminReasoningLevel `json:"levels"`
}

type adminItem struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	Brand     string         `json:"brand"`
	Connector string         `json:"connector"`
	API       string         `json:"api"`
	Reasoning adminReasoning `json:"reasoning"`
	Tools     bool           `json:"tools"`
	Vision    bool           `json:"vision"`
	ContextK  int            `json:"context_k"`
	Cost      string         `json:"cost"`
	Note      string         `json:"note"`
	Available bool           `json:"available"`
	UsedBy    string         `json:"used_by"`
	Scope     string         `json:"scope"`
	Status    string         `json:"status"`
	IsDefault bool           `json:"is_default"`
}

type adminAPIStep struct {
	API  string `json:"api"`
	Note string `json:"note"`
}

type adminMapping struct {
	Level string `json:"level"`
	Param string `json:"param"`
}

// CheckedAt is the metadata check time inside Checks, in RFC 3339 (PA-83);
// null when this process has no check.
type adminDetail struct {
	Model     string         `json:"model"`
	APIChain  []adminAPIStep `json:"api_chain"`
	Mapping   []adminMapping `json:"mapping"`
	Checks    []string       `json:"checks"`
	CheckedAt *string        `json:"checked_at"`
	Roles     string         `json:"roles"`
	Agents    string         `json:"agents"`
	Limits    string         `json:"limits"`
}

type adminUsage struct {
	Conversations int64
	Canvases      int64
	AgentSpans    int64
}

type adminObservation struct {
	InstanceID string
	Status     string
	CheckedAt  time.Time
	ErrorCode  string
}

func (s *Service) admin(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	env, ok := s.selectedEnvironment(w, r, false)
	if !ok {
		return
	}
	if s.bindings == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_binding_unavailable", "Environment model bindings are not configured")
		return
	}
	if s.ToolModels == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_tool_allowlist_unavailable", "Model tool allowlist is not connected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		adminUnavailable(w)
		return
	}
	defer rollback(tx)
	if err := lockModelAuthority(ctx, tx, r, env, "view"); err != nil {
		writeAuthorityError(w, err)
		return
	}
	if err := s.Access.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(r), rbac.V3Decision{Module: "models", Action: "view", EnvironmentID: env}); err != nil {
		writeAuthorityError(w, err)
		return
	}
	models := s.bindings.ModelsForEnvironment(env)
	defaultModel := s.bindings.DefaultModel(env)
	if len(models) == 0 || defaultModel == "" {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_binding_unavailable", "Environment model bindings are incomplete")
		return
	}
	ids := make([]string, 0, len(models))
	var detailModel inference.Model
	for _, model := range models {
		// ModelInfo.api has no native Ollama member. Refuse an incomplete or
		// misleading main DTO until the frontend contract supports it.
		if model.API != "responses" && model.API != "chat_completions" && model.API != "messages" {
			httpapi.Error(w, http.StatusServiceUnavailable, "model_admin_api_unsupported", "A configured model API is not representable by the V3 model contract")
			return
		}
		ids = append(ids, model.ID)
		if model.ID == defaultModel {
			detailModel = model
		}
	}
	if detailModel.ID == "" {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_binding_unavailable", "Environment default model is not bound")
		return
	}
	observations, err := readAdminObservations(ctx, tx, env, ids)
	if err != nil {
		adminUnavailable(w)
		return
	}
	usage, err := readAdminUsage(ctx, tx, env, ids)
	if err != nil {
		adminUnavailable(w)
		return
	}
	view := buildAdminView(env, models, defaultModel, observations, usage, s.instanceID, s.ToolModels)
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session is no longer active")
		return
	}
	if err = store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session is no longer active")
		} else {
			adminUnavailable(w)
		}
		return
	}
	if err := tx.Commit(ctx); err != nil {
		adminUnavailable(w)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, view)
}

func adminUnavailable(w http.ResponseWriter) {
	httpapi.Error(w, http.StatusServiceUnavailable, "model_admin_unavailable", "Model management data is unavailable")
}

func readAdminObservations(ctx context.Context, tx pgx.Tx, env string, ids []string) (map[string]adminObservation, error) {
	rows, err := tx.Query(ctx, `SELECT model_id,process_instance_id,status,checked_at,error_code
 FROM v3_model_observations WHERE environment_id=$1 AND model_id=ANY($2::text[])`, env, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]adminObservation, len(ids))
	for rows.Next() {
		var model string
		var observation adminObservation
		if err := rows.Scan(&model, &observation.InstanceID, &observation.Status, &observation.CheckedAt, &observation.ErrorCode); err != nil {
			return nil, err
		}
		result[model] = observation
	}
	return result, rows.Err()
}

func readAdminUsage(ctx context.Context, tx pgx.Tx, env string, ids []string) (map[string]adminUsage, error) {
	result := make(map[string]adminUsage, len(ids))
	for _, source := range []struct {
		query string
		set   func(*adminUsage, int64)
	}{
		{`SELECT r.model_id,count(*) FROM v3_conversation_replies r
 JOIN v3_conversations c ON c.id=r.conversation_id
 WHERE c.environment_id=$1 AND r.state='completed' AND r.model_id=ANY($2::text[])
 GROUP BY r.model_id`, func(u *adminUsage, n int64) { u.Conversations = n }},
		{`SELECT x.model_id,count(*) FROM v3_canvas_node_exchanges x
 JOIN v3_canvases c ON c.id=x.canvas_id
 WHERE c.environment_id=$1 AND x.state='completed' AND x.model_id=ANY($2::text[])
 GROUP BY x.model_id`, func(u *adminUsage, n int64) { u.Canvases = n }},
		{`SELECT s.model_id,count(*) FROM v3_agent_run_spans s
 JOIN v3_agent_runs r ON r.id=s.run_id
 WHERE r.environment_id=$1 AND s.kind='llm' AND s.model_id=ANY($2::text[])
 GROUP BY s.model_id`, func(u *adminUsage, n int64) { u.AgentSpans = n }},
	} {
		rows, err := tx.Query(ctx, source.query, env, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var model string
			var count int64
			if err = rows.Scan(&model, &count); err != nil {
				break
			}
			entry := result[model]
			source.set(&entry, count)
			result[model] = entry
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func buildAdminView(env string, models []inference.Model, defaultModel string, observations map[string]adminObservation, usage map[string]adminUsage, instanceID string, toolModels map[string]bool) adminView {
	view := adminView{Providers: []adminProvider{}, Items: []adminItem{}}
	providerIndexes := map[string]int{}
	providerAPIs := map[string][]string{}
	for _, model := range models {
		provider := model.Provider
		if provider == "" {
			provider = "Unspecified provider"
		}
		index, exists := providerIndexes[provider]
		if !exists {
			index = len(view.Providers)
			providerIndexes[provider] = index
			view.Providers = append(view.Providers, adminProvider{Brand: provider, Name: provider, Status: "unverified"})
		}
		p := &view.Providers[index]
		p.Models++
		api := adminAPIName(model.API)
		if !containsString(providerAPIs[provider], api) {
			providerAPIs[provider] = append(providerAPIs[provider], api)
		}
		p.API = strings.Join(providerAPIs[provider], ", ")
		levels := make([]adminReasoningLevel, 0, len(model.ReasoningLevels))
		for _, level := range model.ReasoningLevels {
			levels = append(levels, adminReasoningLevel{ID: level, Label: level})
		}
		kind := "none"
		if len(levels) > 0 {
			kind = "levels"
			if model.ReasoningKind == "ollama_think" {
				kind = "toggle"
			}
		}
		view.Items = append(view.Items, adminItem{
			ID: model.ID, Name: model.Name, Provider: model.Provider, Brand: provider,
			Connector: "", API: model.API, Reasoning: adminReasoning{Kind: kind, Levels: levels},
			Tools: model.Tools && toolModels[model.ID], Vision: model.ImageInput, ContextK: model.ContextWindow / 1024,
			Cost: "Not configured", Note: "Bound for execution in this environment; enabled tool use is not a verified endpoint check.",
			Available: true, UsedBy: usage[model.ID].description(), Scope: "Environment: " + env + "; subject to conversation/canvas access",
			Status: "unverified", IsDefault: model.ID == defaultModel,
		})
	}
	for _, model := range models {
		if model.ID == defaultModel {
			view.Detail = adminModelDetail(model, observations[model.ID], usage[model.ID], instanceID)
			break
		}
	}
	return view
}

func (u adminUsage) description() string {
	if u.Conversations == 0 && u.Canvases == 0 && u.AgentSpans == 0 {
		return "No recorded use"
	}
	parts := []string{}
	if u.Conversations > 0 {
		parts = append(parts, fmt.Sprintf("%d completed conversation turns", u.Conversations))
	}
	if u.Canvases > 0 {
		parts = append(parts, fmt.Sprintf("%d completed canvas turns", u.Canvases))
	}
	if u.AgentSpans > 0 {
		parts = append(parts, fmt.Sprintf("%d recorded Agent LLM spans", u.AgentSpans))
	}
	return "Historical: " + strings.Join(parts, ", ")
}

func adminModelDetail(model inference.Model, observation adminObservation, usage adminUsage, instanceID string) adminDetail {
	checks := []string{"Generation, streaming, tools, and vision have not been checked."}
	var checkedAt *string
	if observation.Status == "" {
		checks = append(checks, "No model metadata check is recorded.")
	} else if observation.InstanceID != instanceID {
		checks = append(checks, "A metadata check belongs to another process instance; current process is unverified.")
	} else {
		stamp := observation.CheckedAt.UTC().Format(time.RFC3339)
		checkedAt = &stamp
		switch observation.Status {
		case "verified":
			checks = append(checks, "Local credential-free metadata lookup succeeded at "+stamp+".")
		case "error", "unsupported":
			checks = append(checks, "Metadata lookup "+observation.Status+" at "+stamp+" (code: "+observation.ErrorCode+").")
		}
	}
	mapping := make([]adminMapping, 0, len(model.ReasoningLevels))
	for _, level := range model.ReasoningLevels {
		param := ""
		switch model.ReasoningKind {
		case "openai_effort":
			if model.API == "responses" {
				param = fmt.Sprintf("reasoning.effort = %q", level)
			} else {
				param = fmt.Sprintf("reasoning_effort = %q", level)
			}
		case "claude_adaptive":
			param = fmt.Sprintf("thinking.type = adaptive; output_config.effort = %q", level)
		}
		mapping = append(mapping, adminMapping{Level: level, Param: param})
	}
	return adminDetail{
		Model:    model.ID,
		APIChain: []adminAPIStep{{API: adminAPIName(model.API), Note: "Only configured API; no automatic fallback."}},
		Mapping:  mapping,
		Checks:   checks, CheckedAt: checkedAt,
		Roles:  "No model-specific role binding; conversation and canvas route grants plus environment scope apply.",
		Agents: fmt.Sprintf("No active Agent-model binding; %d historical Agent LLM spans.", usage.AgentSpans),
		Limits: fmt.Sprintf("Configured context window: %d tokens; input budget: %d tokens.", model.ContextWindow, model.InputBudget),
	}
}

func adminAPIName(api string) string {
	switch api {
	case "responses":
		return "Responses API"
	case "chat_completions":
		return "Chat Completions API"
	case "messages":
		return "Messages API"
	default:
		return api
	}
}

func containsString(items []string, candidate string) bool {
	for _, item := range items {
		if item == candidate {
			return true
		}
	}
	return false
}
