package v3settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/v3models"
	"github.com/jackc/pgx/v5"
)

type modelChoice struct {
	Model     string `json:"model"`
	Reasoning string `json:"reasoning,omitempty"`
}
type aiPreferences struct {
	Default          modelChoice `json:"default"`
	CanvasInherit    bool        `json:"canvas_inherit"`
	ShowReasoning    string      `json:"show_reasoning"`
	AutoContext      bool        `json:"auto_context"`
	Memory           bool        `json:"memory"`
	BudgetWarnTokens int         `json:"budget_warn_tokens"`
}

func defaultAI() aiPreferences {
	return aiPreferences{CanvasInherit: true, ShowReasoning: "collapsed", AutoContext: true, Memory: true, BudgetWarnTokens: 8000}
}

func (s *Service) models(env string) []inference.Model {
	if s.Runtime == nil {
		return nil
	}
	if bound, ok := s.Runtime.(*v3models.BoundRunner); ok {
		return bound.Bindings().ModelsForEnvironment(env)
	}
	return s.Runtime.Models()
}
func (s *Service) model(env, id string) (inference.Model, bool) {
	for _, m := range s.models(env) {
		if m.ID == id {
			return m, true
		}
	}
	return inference.Model{}, false
}
func validAI(p aiPreferences, m inference.Model) bool {
	if p.ShowReasoning != "expanded" && p.ShowReasoning != "collapsed" && p.ShowReasoning != "hidden" {
		return false
	}
	if p.BudgetWarnTokens < 100 || p.BudgetWarnTokens > 1_000_000 {
		return false
	}
	if p.Default.Reasoning != "" {
		found := false
		for _, id := range m.ReasoningLevels {
			if id == p.Default.Reasoning {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (s *Service) getAI(ctx context.Context, id, env string) (aiPreferences, error) {
	p := defaultAI()
	var raw []byte
	err := s.Store.DB.QueryRow(ctx, `SELECT ai_preferences FROM v3_user_settings WHERE user_id=$1`, id).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &p); err != nil {
			return p, err
		}
	}
	if _, ok := s.model(env, p.Default.Model); !ok {
		if bound, ok := s.Runtime.(*v3models.BoundRunner); ok {
			p.Default = modelChoice{Model: bound.Bindings().DefaultModel(env)}
		} else if models := s.models(env); len(models) > 0 {
			p.Default = modelChoice{Model: models[0].ID}
		}
	}
	return p, nil
}
func (s *Service) putAI(ctx context.Context, id, env string, p aiPreferences) (aiPreferences, error) {
	m, ok := s.model(env, p.Default.Model)
	if !ok || !validAI(p, m) {
		return aiPreferences{}, errInvalid
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return aiPreferences{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,ai_preferences) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET ai_preferences=EXCLUDED.ai_preferences,updated_at=now()`, id, mustJSON(p))
	if err != nil {
		return aiPreferences{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.ai_preferences.updated',$1,'success',$2)`, id, mustJSON(map[string]any{"model": p.Default.Model, "reasoning": p.Default.Reasoning}))
	if err != nil {
		return aiPreferences{}, err
	}
	return p, tx.Commit(ctx)
}

// ModelsHandler describes only adapters actually configured in this process.
// Availability is "configured", not an invented successful remote probe.
func (s *Service) ModelsHandler(w http.ResponseWriter, r *http.Request) {
	env, ok := s.modelEnvironment(w, r)
	if !ok {
		return
	}
	items, err := s.modelCatalogItems(r, env)
	if err != nil {
		respond(w, nil, err)
		return
	}
	p, err := s.getAI(r.Context(), httpapi.CurrentUser(r).ID, env)
	if err != nil {
		respond(w, nil, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "default": p.Default})
}

func (s *Service) modelEnvironment(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, bound := s.Runtime.(*v3models.BoundRunner); !bound {
		return "", true
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 503, "environment_scope_unavailable", "Environment scope is unavailable")
		return "", false
	}
	if scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Choose one environment")
		return "", false
	}
	return scope.EnvironmentIDs[0], true
}

func (s *Service) modelCatalogItems(r *http.Request, env string) ([]map[string]any, error) {
	items := []map[string]any{}
	for _, m := range s.models(env) {
		if m.API == "ollama" {
			// The frontend currently accepts only responses, chat_completions
			// and messages. Reporting Ollama's native API as one of those would
			// misrepresent its wire protocol; fail until the DTO is extended.
			return nil, errModelCatalogUnavailable
		}
		note := localize(r, "已配置；连接状态未验证", "Configured; connection has not been verified")
		if m.API != "responses" && m.API != "chat_completions" && m.API != "messages" {
			return nil, errModelCatalogUnavailable
		}
		levels := []map[string]string{}
		for _, level := range m.ReasoningLevels {
			levels = append(levels, map[string]string{"id": level, "label": level})
		}
		kind := "none"
		if len(levels) > 0 {
			kind = "levels"
			if m.ReasoningKind == "ollama_think" {
				kind = "toggle"
			}
		}
		contextK := m.ContextWindow / 1024
		items = append(items, map[string]any{
			"id": m.ID, "name": m.Name, "provider": m.Provider, "brand": m.Provider, "connector": m.Provider, "api": m.API,
			"reasoning": map[string]any{"kind": kind, "levels": levels}, "tools": m.Tools && s.ToolModels[m.ID], "vision": false,
			"context_k": contextK, "cost": localize(r, "未提供", "Not provided"),
			"note": note, "available": true,
		})
	}
	return items, nil
}

type quietHours struct {
	Enabled bool   `json:"enabled"`
	From    string `json:"from"`
	To      string `json:"to"`
}
type subscription struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Sub   string `json:"sub"`
	On    bool   `json:"on"`
}
type notificationSettings struct {
	Quiet         quietHours     `json:"quiet"`
	Digest        string         `json:"digest"`
	Subscriptions []subscription `json:"subscriptions"`
}

var errStoredNotificationSettings = errors.New("stored notification settings are invalid")

func defaultNotifications() notificationSettings {
	return notificationSettings{Quiet: quietHours{From: "22:00", To: "08:00"}, Digest: "daily", Subscriptions: []subscription{}}
}
func (s *Service) getNotifications(ctx context.Context, id string) (notificationSettings, error) {
	v := defaultNotifications()
	var raw []byte
	err := s.Store.DB.QueryRow(ctx, `SELECT notification_settings FROM v3_user_settings WHERE user_id=$1`, id).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil
	}
	return decodeStoredNotifications(raw)
}

func decodeStoredNotifications(raw []byte) (notificationSettings, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil ||
		len(fields["quiet"]) == 0 || len(fields["digest"]) == 0 || len(fields["subscriptions"]) == 0 ||
		string(fields["quiet"]) == "null" || string(fields["digest"]) == "null" || string(fields["subscriptions"]) == "null" {
		return notificationSettings{}, errStoredNotificationSettings
	}
	var quiet map[string]json.RawMessage
	if err := json.Unmarshal(fields["quiet"], &quiet); err != nil || quiet == nil ||
		len(quiet["enabled"]) == 0 || len(quiet["from"]) == 0 || len(quiet["to"]) == 0 ||
		string(quiet["enabled"]) == "null" || string(quiet["from"]) == "null" || string(quiet["to"]) == "null" {
		return notificationSettings{}, errStoredNotificationSettings
	}
	var v notificationSettings
	if err := json.Unmarshal(raw, &v); err != nil || v.Subscriptions == nil || !validNotificationSettings(v) {
		return notificationSettings{}, errStoredNotificationSettings
	}
	return v, nil
}

func validNotificationSettings(v notificationSettings) bool {
	// A stored settings document is configuration, not proof that a delivery
	// channel is connected. Reject malformed persisted values rather than
	// presenting zero values as a valid user preference.
	if !validClock(v.Quiet.From) || !validClock(v.Quiet.To) ||
		(v.Digest != "off" && v.Digest != "daily" && v.Digest != "weekly") || len(v.Subscriptions) > 100 {
		return false
	}
	seen := make(map[string]bool, len(v.Subscriptions))
	for _, sub := range v.Subscriptions {
		if sub.ID == "" || seen[sub.ID] || len(sub.ID) > 80 || len(sub.Label) > 120 {
			return false
		}
		seen[sub.ID] = true
	}
	return true
}
func validClock(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s[:2] <= "23" && s[3:] <= "59"
}
func (s *Service) putNotifications(ctx context.Context, id string, raw map[string]json.RawMessage) (notificationSettings, error) {
	if len(raw) == 0 {
		return notificationSettings{}, errInvalid
	}
	for key, b := range raw {
		if key != "quiet" && key != "digest" && key != "subscriptions" || len(b) == 0 || string(b) == "null" {
			return notificationSettings{}, errInvalid
		}
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return notificationSettings{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id) VALUES($1) ON CONFLICT(user_id) DO NOTHING`, id); err != nil {
		return notificationSettings{}, err
	}
	var stored []byte
	if err = tx.QueryRow(ctx, `SELECT notification_settings FROM v3_user_settings WHERE user_id=$1 FOR UPDATE`, id).Scan(&stored); err != nil {
		return notificationSettings{}, err
	}
	v, err := decodeStoredNotifications(stored)
	if err != nil {
		return notificationSettings{}, err
	}
	if b, ok := raw["quiet"]; ok {
		if len(b) > 0 && b[0] == '{' {
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(b, &fields); err != nil || len(fields) != 3 ||
				len(fields["enabled"]) == 0 || len(fields["from"]) == 0 || len(fields["to"]) == 0 ||
				string(fields["enabled"]) == "null" || string(fields["from"]) == "null" || string(fields["to"]) == "null" {
				return notificationSettings{}, errInvalid
			}
			for key := range fields {
				if key != "enabled" && key != "from" && key != "to" {
					return notificationSettings{}, errInvalid
				}
			}
			if err = json.Unmarshal(b, &v.Quiet); err != nil {
				return notificationSettings{}, errInvalid
			}
		} else if err = json.Unmarshal(b, &v.Quiet.Enabled); err != nil {
			return notificationSettings{}, errInvalid
		}
	}
	if b, ok := raw["digest"]; ok {
		if err = json.Unmarshal(b, &v.Digest); err != nil {
			return notificationSettings{}, errInvalid
		}
	}
	if b, ok := raw["subscriptions"]; ok {
		if len(b) > 0 && b[0] == '[' {
			var input []json.RawMessage
			if err = json.Unmarshal(b, &input); err != nil || len(input) != len(v.Subscriptions) {
				return notificationSettings{}, errInvalid
			}
			patch := make(map[string]bool, len(input))
			for _, entry := range input {
				var fields map[string]json.RawMessage
				if err = json.Unmarshal(entry, &fields); err != nil || len(fields) != 4 {
					return notificationSettings{}, errInvalid
				}
				for key := range fields {
					if key != "id" && key != "label" && key != "sub" && key != "on" {
						return notificationSettings{}, errInvalid
					}
				}
				var item subscription
				if err = json.Unmarshal(entry, &item); err != nil || item.ID == "" || len(fields["id"]) == 0 || len(fields["label"]) == 0 || len(fields["sub"]) == 0 || len(fields["on"]) == 0 ||
					string(fields["id"]) == "null" || string(fields["label"]) == "null" || string(fields["sub"]) == "null" || string(fields["on"]) == "null" {
					return notificationSettings{}, errInvalid
				}
				if _, exists := patch[item.ID]; exists {
					return notificationSettings{}, errInvalid
				}
				found := false
				for _, existing := range v.Subscriptions {
					if existing.ID == item.ID && existing.Label == item.Label && existing.Sub == item.Sub {
						found = true
						break
					}
				}
				if !found {
					return notificationSettings{}, errInvalid
				}
				patch[item.ID] = item.On
			}
			for i := range v.Subscriptions {
				v.Subscriptions[i].On = patch[v.Subscriptions[i].ID]
			}
		} else {
			var patch map[string]json.RawMessage
			if err = json.Unmarshal(b, &patch); err != nil || patch == nil {
				return notificationSettings{}, errInvalid
			}
			for key, value := range patch {
				var on bool
				if string(value) == "null" || json.Unmarshal(value, &on) != nil {
					return notificationSettings{}, errInvalid
				}
				found := false
				for i := range v.Subscriptions {
					if v.Subscriptions[i].ID == key {
						v.Subscriptions[i].On = on
						found = true
						break
					}
				}
				if !found {
					return notificationSettings{}, errInvalid
				}
			}
		}
	}
	if !validNotificationSettings(v) {
		return notificationSettings{}, errInvalid
	}
	_, err = tx.Exec(ctx, `UPDATE v3_user_settings SET notification_settings=$2,updated_at=now() WHERE user_id=$1`, id, mustJSON(v))
	if err != nil {
		return v, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.notification_settings.updated',$1,'success',$2)`, id, mustJSON(map[string]any{"quiet": v.Quiet.Enabled, "digest": v.Digest, "subscriptions": len(v.Subscriptions)}))
	if err != nil {
		return v, err
	}
	return v, tx.Commit(ctx)
}

func (s *Service) registerAI(api *httpapi.Server) {
	api.Register("GET /api/v1/me/ai-preferences", "viewer", func(w http.ResponseWriter, r *http.Request) {
		env, ok := s.modelEnvironment(w, r)
		if !ok {
			return
		}
		v, e := s.getAI(r.Context(), httpapi.CurrentUser(r).ID, env)
		respond(w, v, e)
	})
	api.Register("PUT /api/v1/me/ai-preferences", "viewer", func(w http.ResponseWriter, r *http.Request) {
		env, ok := s.modelEnvironment(w, r)
		if !ok {
			return
		}
		var v aiPreferences
		if !httpapi.Decode(w, r, &v) {
			return
		}
		out, e := s.putAI(r.Context(), httpapi.CurrentUser(r).ID, env, v)
		respond(w, out, e)
	})
	api.Register("GET /api/v1/me/notification-settings", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.getNotifications(r.Context(), httpapi.CurrentUser(r).ID)
		respond(w, v, e)
	})
	api.Register("PUT /api/v1/me/notification-settings", "viewer", func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		if !httpapi.Decode(w, r, &raw) {
			return
		}
		v, e := s.putNotifications(r.Context(), httpapi.CurrentUser(r).ID, raw)
		respond(w, v, e)
	})
}
