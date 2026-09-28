package v3settings

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/inference"
)

func TestModelsRejectsNativeOllamaUntilCatalogSupportsIt(t *testing.T) {
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{{
		ID: "ollama:test", Kind: "ollama", API: "ollama", Model: "test",
		BaseURL: "http://127.0.0.1:11434",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(nil, runtime)
	w := httptest.NewRecorder()
	service.ModelsHandler(w, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"model_catalog_unavailable"`) {
		t.Fatalf("expected explicit unsupported protocol response, got %d %s", w.Code, w.Body.String())
	}
}

func TestModelCatalogToolsFollowV3Allowlist(t *testing.T) {
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{
		{ID: "openai:enabled", Kind: "openai", Model: "enabled", BaseURL: "http://127.0.0.1:8080"},
		{ID: "openai:disabled", Kind: "openai", Model: "disabled", BaseURL: "http://127.0.0.1:8080"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service := New(nil, runtime)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	for _, test := range []struct {
		name      string
		allowlist map[string]bool
		expected  []bool
	}{
		{name: "no V3 tools enabled", expected: []bool{false, false}},
		{name: "one V3 model enabled", allowlist: map[string]bool{"openai:enabled": true}, expected: []bool{true, false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.ToolModels = test.allowlist
			items, err := service.modelCatalogItems(request)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != len(test.expected) {
				t.Fatalf("got %d models, want %d", len(items), len(test.expected))
			}
			for i, want := range test.expected {
				if got := items[i]["tools"]; got != want {
					t.Errorf("model %q tools = %v, want %t", items[i]["id"], got, want)
				}
			}
		})
	}
	if !runtime.Models()[1].Tools {
		t.Fatal("V3 catalog must not change legacy model capabilities")
	}
}
