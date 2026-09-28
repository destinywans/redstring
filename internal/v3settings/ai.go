package v3settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
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

func (s *Service) models() []inference.Model {
	if s.Runtime == nil {
		return nil
	}
	return s.Runtime.Models()
}
func (s *Service) model(id string) (inference.Model, bool) {
	for _, m := range s.models() {
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
func (s *Service) getAI(ctx context.Context, id string) (aiPreferences, error) {
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
	if _, ok := s.model(p.Default.Model); !ok {
		models := s.models()
		if len(models) > 0 {
			p.Default = modelChoice{Model: models[0].ID}
		}
	}
	return p, nil
}
func (s *Service) putAI(ctx context.Context, id string, p aiPreferences) (aiPreferences, error) {
	m, ok := s.model(p.Default.Model)
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
	items, err := s.modelCatalogItems(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	p, err := s.getAI(r.Context(), httpapi.CurrentUser(r).ID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "default": p.Default})
}

func (s *Service) modelCatalogItems(r *http.Request) ([]map[string]any, error) {
	items := []map[string]any{}
	for _, m := range s.models() {
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
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &v)
	}
	if v.Subscriptions == nil {
		v.Subscriptions = []subscription{}
	}
	return v, err
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
	v, err := s.getNotifications(ctx, id)
	if err != nil {
		return v, err
	}
	if b, ok := raw["quiet"]; ok {
		if len(b) > 0 && b[0] == '{' {
			if err = json.Unmarshal(b, &v.Quiet); err != nil {
				return v, errInvalid
			}
		} else if err = json.Unmarshal(b, &v.Quiet.Enabled); err != nil {
			return v, errInvalid
		}
	}
	if b, ok := raw["digest"]; ok {
		if err = json.Unmarshal(b, &v.Digest); err != nil {
			return v, errInvalid
		}
	}
	if b, ok := raw["subscriptions"]; ok {
		if len(b) > 0 && b[0] == '[' {
			if err = json.Unmarshal(b, &v.Subscriptions); err != nil {
				return v, errInvalid
			}
		} else {
			var patch map[string]bool
			if err = json.Unmarshal(b, &patch); err != nil {
				return v, errInvalid
			}
			for key, on := range patch {
				found := false
				for i := range v.Subscriptions {
					if v.Subscriptions[i].ID == key {
						v.Subscriptions[i].On = on
						found = true
						break
					}
				}
				if !found {
					return v, errInvalid
				}
			}
		}
	}
	if !validClock(v.Quiet.From) || !validClock(v.Quiet.To) || (v.Digest != "off" && v.Digest != "daily" && v.Digest != "weekly") || len(v.Subscriptions) > 100 {
		return v, errInvalid
	}
	seen := map[string]bool{}
	for _, sub := range v.Subscriptions {
		if sub.ID == "" || seen[sub.ID] || len(sub.ID) > 80 || len(sub.Label) > 120 {
			return v, errInvalid
		}
		seen[sub.ID] = true
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO v3_user_settings(user_id,notification_settings) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET notification_settings=EXCLUDED.notification_settings,updated_at=now()`, id, mustJSON(v))
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
		v, e := s.getAI(r.Context(), httpapi.CurrentUser(r).ID)
		respond(w, v, e)
	})
	api.Register("PUT /api/v1/me/ai-preferences", "viewer", func(w http.ResponseWriter, r *http.Request) {
		var v aiPreferences
		if !httpapi.Decode(w, r, &v) {
			return
		}
		out, e := s.putAI(r.Context(), httpapi.CurrentUser(r).ID, v)
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
