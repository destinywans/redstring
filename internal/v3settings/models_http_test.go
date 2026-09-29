package v3settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3models"
)

func TestModelsAuthenticatedHTTPUsesPersistedUserDefault(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{
		{ID: "openai:first", Kind: "openai", API: "chat_completions", Model: "first", BaseURL: "http://127.0.0.1:18080"},
		{ID: "openai:second", Kind: "openai", API: "responses", Model: "second", BaseURL: "http://127.0.0.1:18080"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	settings := New(s, runtime)
	api := httpapi.New(s, nil, true)
	settings.Register(api)
	api.V3ModelsHandler = settings.ModelsHandler
	api.MarkV3Route("GET /api/v1/models", "models", "view")
	api.MarkV3Route("GET /api/v1/me/ai-preferences", "", "")
	api.MarkV3Route("PUT /api/v1/me/ai-preferences", "", "")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, allowPreferencesEnvironment{}, s.Session, func(_ context.Context, _ core.User, module, action, _ string) error {
		if module != "models" || action != "view" {
			t.Fatalf("unexpected authorization request: %s/%s", module, action)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	password := "isolated-model-catalog-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id IN ('one','two')`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	_, oneToken, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, twoToken, err := s.Login(ctx, "two@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if got := request(""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d, body=%s", got.Code, got.Body.String())
	}
	assertCatalog := func(token, defaultModel string) {
		t.Helper()
		w := request(token)
		if w.Code != http.StatusOK {
			t.Fatalf("catalog status=%d, body=%s", w.Code, w.Body.String())
		}
		var catalog struct {
			Items []struct {
				ID        string `json:"id"`
				API       string `json:"api"`
				Available bool   `json:"available"`
				Tools     bool   `json:"tools"`
			} `json:"items"`
			Default modelChoice `json:"default"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
			t.Fatal(err)
		}
		if len(catalog.Items) != 2 || catalog.Items[0].ID != "openai:first" || catalog.Items[0].API != "chat_completions" || !catalog.Items[0].Available || catalog.Items[0].Tools || catalog.Items[1].ID != "openai:second" || catalog.Items[1].API != "responses" || catalog.Default.Model != defaultModel {
			t.Fatalf("unexpected catalog: %+v", catalog)
		}
	}
	assertCatalog(oneToken, "openai:first")
	assertCatalog(twoToken, "openai:first")
	preferred := defaultAI()
	preferred.Default.Model = "openai:second"
	payload, err := json.Marshal(preferred)
	if err != nil {
		t.Fatal(err)
	}
	put := func(token, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/me/ai-preferences", strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		r.Header.Set("X-AIOps-Environment", env)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := put(oneToken, "dev", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("save AI preferences: %d %s", w.Code, w.Body.String())
	}
	read := func(token string) aiPreferences {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me/ai-preferences", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		r.Header.Set("X-AIOps-Environment", "dev")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("read AI preferences: %d %s", w.Code, w.Body.String())
		}
		var got aiPreferences
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(oneToken); got != preferred {
		t.Fatalf("persisted AI preferences: %+v, want %+v", got, preferred)
	}
	if got := read(twoToken); got.Default.Model != "openai:first" {
		t.Fatalf("another user's AI default: %+v", got)
	}
	if w := put(oneToken, "dev", strings.Replace(string(payload), "openai:second", "missing:model", 1)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid model accepted: %d %s", w.Code, w.Body.String())
	}
	if w := put(oneToken, "all", string(payload)); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "env_view_only") {
		t.Fatalf("all environment write: %d %s", w.Code, w.Body.String())
	}
	if got := read(oneToken); got != preferred {
		t.Fatalf("invalid/all write changed persisted AI preference: %+v", got)
	}
	assertCatalog(oneToken, "openai:second")
	assertCatalog(twoToken, "openai:first")
}

func TestBoundModelsAndPreferencesUseSelectedEnvironment(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{
		{ID: "openai:dev", Kind: "openai", API: "responses", Model: "dev", BaseURL: "http://127.0.0.1:18080"},
		{ID: "openai:test", Kind: "openai", API: "responses", Model: "test", BaseURL: "http://127.0.0.1:18080"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bindings.json")
	if err := os.WriteFile(path, []byte(`{"tier":"nonprod","environments":[{"id":"dev","models":["openai:dev"],"default_model":"openai:dev"},{"id":"test","models":["openai:test"],"default_model":"openai:test"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	bindings, err := v3models.LoadBindings(path, "nonprod", []string{"dev", "test"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	settings := New(st, v3models.NewBoundRunner(runtime, bindings))
	if items, err := settings.modelCatalogItems(httptest.NewRequest("GET", "/api/v1/models", nil), "dev"); err != nil || len(items) != 1 || items[0]["id"] != "openai:dev" {
		t.Fatalf("dev catalog: %v %v", items, err)
	}
	if items, err := settings.modelCatalogItems(httptest.NewRequest("GET", "/api/v1/models", nil), "test"); err != nil || len(items) != 1 || items[0]["id"] != "openai:test" {
		t.Fatalf("test catalog: %v %v", items, err)
	}
	choice := defaultAI()
	choice.Default.Model = "openai:test"
	if _, err := settings.putAI(ctx, "one", "dev", choice); !errors.Is(err, errInvalid) {
		t.Fatalf("cross-environment preference: %v", err)
	}
	if _, err := settings.putAI(ctx, "one", "test", choice); err != nil {
		t.Fatal(err)
	}
	if got, err := settings.getAI(ctx, "one", "dev"); err != nil || got.Default.Model != "openai:dev" {
		t.Fatalf("dev fallback: %+v %v", got, err)
	}
	if got, err := settings.getAI(ctx, "one", "test"); err != nil || got.Default.Model != "openai:test" {
		t.Fatalf("test preference: %+v %v", got, err)
	}
	var stored string
	if err := st.DB.QueryRow(ctx, `SELECT ai_preferences->'default'->>'model' FROM v3_user_settings WHERE user_id='one'`).Scan(&stored); err != nil || stored != "openai:test" {
		t.Fatalf("fallback changed persistence: %q %v", stored, err)
	}
	api := httpapi.New(st, nil, true)
	settings.Register(api)
	api.V3ModelsHandler = settings.ModelsHandler
	api.MarkV3Route("GET /api/v1/models", "models", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, allowPreferencesEnvironment{}, st.Session, func(_ context.Context, _ core.User, _, _, _ string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	password := "isolated-bound-model-password"
	if _, err := st.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/v1/models", "dev", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"openai:dev"`) || strings.Contains(w.Body.String(), `"id":"openai:test"`) {
		t.Fatalf("dev HTTP catalog: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", "/api/v1/models", "all", ""); w.Code != 422 {
		t.Fatalf("multi-environment catalog: %d %s", w.Code, w.Body.String())
	}
	payload, err := json.Marshal(choice)
	if err != nil {
		t.Fatal(err)
	}
	if w := request("PUT", "/api/v1/me/ai-preferences", "dev", string(payload)); w.Code != 422 {
		t.Fatalf("cross-environment HTTP preference: %d %s", w.Code, w.Body.String())
	}
}
